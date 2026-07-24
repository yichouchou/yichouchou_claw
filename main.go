/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
	"github.com/hertz-contrib/sse"

	"github.com/yichouchou/yichouchou_claw/internal/config"
	"github.com/yichouchou/yichouchou_claw/internal/localcommand"
	"github.com/yichouchou/yichouchou_claw/internal/memory"
	"github.com/yichouchou/yichouchou_claw/internal/message"
	"github.com/yichouchou/yichouchou_claw/internal/session"
	"github.com/yichouchou/yichouchou_claw/subagents"
)

//go:embed index.html
var staticFiles embed.FS

// maxConversationRounds 设定每个会话最多保留多少轮上下文（修改可调整窗口大小）。
const maxConversationRounds = 12

func main() {
	// 进程级别的会话存储。中间件 session.PersistMiddleware 会用 AfterAgent 钩子
	// 把 SDK 内部维护的完整多轮 messages 写回这里 —— 这是一个 eino 原生方案。
	store := session.NewStore(maxConversationRounds)

	// 解析 skills 根目录。约定：workdir/skills/chat/<skill>/SKILL.md
	// 和 workdir/skills/localcommand/<skill>/SKILL.md。
	//
	// 用 exe 所在目录（server 启动目录）的"workdir/skills"——保证从任何 cwd 调用都能找到 skills。
	// 找不到目录时不报错——subagents.NewXxx 会 fallback 到"无 skill"模式。
	wd, _ := os.Getwd()
	skillsRoot := filepath.Join(wd, "workdir", "skills")
	memoryRoot := filepath.Join(wd, "workdir")

	// 初始化 trace 记忆 recorder。默认写到 <workdir>/memory/；
	// 通过 YICHOUCHOU_MEMORY=off 环境变量可整体关闭。
	memRecorder, err := memory.NewAsyncMarkdownRecorder(memoryRoot)
	if err != nil {
		log.Fatalf("[main] failed to init memory recorder: %v", err)
	}
	if memRecorder != nil {
		memory.SetRecorder(memRecorder)
		log.Printf("[main] memory recorder enabled at %s (async)", memRecorder.Root())
	} else {
		log.Printf("[main] memory recorder disabled")
	}

	// === Memory LLM Refine(可选):用 LLM 重新生成精准摘要 ===
	// 默认开启(YICHOUCHOU_LLM_REFINE=off 可关闭)。
	// 用与 ChatModelAgent 相同的 model.NewChatModel() 实例(可并行调 LLM)。
	refineEnabled := true
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("YICHOUCHOU_LLM_REFINE"))); v != "" {
		switch v {
		case "off", "false", "disable", "disabled":
			refineEnabled = false
		}
	}
	var refineQueue *memory.RefineQueue
	if refineEnabled && memRecorder != nil {
		refinerCfg := memory.DefaultRefinerConfig()
		refinerCfg.Workers = 2
		// eino ChatModel 适配成 memory.ChatModel 接口。
		// internal/memory 不引 eino components/model,用 AdapterFunc 转接。
		einoCM := subagents.NewChatModelForRefiner()
		var cm memory.ChatModel
		if einoCM != nil {
			cm = memory.AdapterFunc(func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
				return einoCM.Generate(ctx, msgs)
			})
		}
		einoRefiner := memory.NewEinoLLMRefiner(cm, refinerCfg)
		if einoRefiner != nil {
			updater := memory.NewFileUpdater()
			refineQueue = memory.NewRefineQueue(einoRefiner, refinerCfg, updater)
			if refineQueue != nil {
				memRecorder.SetRefiner(refineQueue)
				log.Printf("[main] LLM refine enabled (workers=%d)", refinerCfg.Workers)
			}
		} else {
			log.Printf("[main] LLM refine disabled (no ChatModel)")
		}
	}

	// SIGTERM / SIGINT 时优雅 Flush+Close,确保内存 channel 里的
	// 待写入条目都落盘后再退出。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Printf("[main] got signal=%v, flushing memory recorder...", sig)
		flushStart := time.Now()
		if err := memRecorder.Flush(); err != nil {
			log.Printf("[main] memory Flush err=%v", err)
		}
		log.Printf("[main] memory flushed in %v, draining refine...", time.Since(flushStart))
		if refineQueue != nil {
			refineQueue.Flush()
			refineQueue.Close()
		}
		log.Printf("[main] memory recorder + refiner closed, exiting")
		os.Exit(0)
	}()
	defer func() {
		// 程序 panic / 异常退出路径
		_ = memRecorder.Flush()
		_ = memRecorder.Close()
		if refineQueue != nil {
			refineQueue.Flush()
			refineQueue.Close()
		}
	}()

	// 加载执行审批配置（白名单/黑名单从 JSON 喂给沙箱）。
	// 文件不存在 / 解析失败时不阻塞启动：沙箱会退回到"全部走 WhitelistAuth 授权"。
	approvalsPath := filepath.Join(wd, "workdir", "config", "exec-approvals.json")
	if err := config.Load(approvalsPath); err != nil {
		log.Fatalf("[main] failed to parse %s: %v", approvalsPath, err)
	}
	// 把白名单/黑名单从内存 config 注入到沙箱缓存。
	// 遍历 exec-approvals.json 里所有 agent 配置（RouterAgent / ChatAgent /
	// LocalCommandAgent / WeatherAgent …），每个 agent 各取自己那段策略。
	// 配置里没列的 agent 不会被遍历到，沙箱里"无黑白名单"——命令一律落到
	// WhitelistAuth 授权分支，由用户授权后才能放行。
	loaded := 0
	for _, agentName := range config.ListAgentNames() {
		localcommand.SetAllowedCommands(agentName)
		localcommand.SetDeniedCommands(agentName)
		loaded++
	}
	log.Printf("[main] exec-approvals.json applied to %d agent(s): %v", loaded, config.ListAgentNames())

	weatherAgent := subagents.NewWeatherAgent(memory.NewMemoryMiddleware("WeatherAgent"))
	chatAgent := subagents.NewChatAgent(context.Background(), skillsRoot, memory.NewMemoryMiddleware("ChatAgent"))
	// 包装 ChatAgent / WeatherAgent / LocalCommandAgent：禁止它们转回 RouterAgent。
	// 原因：这三个 sub-agent 都没有子 agent，但 eino 框架会自动给所有 sub-agent
	// 添加 transfer_to_agent 工具，且默认目标包含父 agent（RouterAgent）。
	// LLM 偶尔会误调这个工具（如 ChatAgent 调 transfer_to_agent(LocalCommandAgent)，
	// 或者甚至误以为可以 transfer_to_agent(自己)），导致
	// "agent 'X' not found when transferring from 'Y'" 错误。
	// 用 adk.WithDisallowTransferToParent() 关闭这条路径，从工具列表里彻底移除
	// transfer_to_agent 工具，让 LLM 看不到就不会调。
	localCmdAgent := adk.AgentWithOptions(
		context.Background(),
		subagents.NewLocalCommandAgent(context.Background(), skillsRoot, memory.NewMemoryMiddleware("LocalCommandAgent")),
		adk.WithDisallowTransferToParent(),
	)
	chatAgent = adk.AgentWithOptions(
		context.Background(),
		chatAgent,
		adk.WithDisallowTransferToParent(),
	)
	weatherAgent = adk.AgentWithOptions(
		context.Background(),
		weatherAgent,
		adk.WithDisallowTransferToParent(),
	)
	// RouterAgent 挂上 PersistMiddleware，让最外层 ChatModelAgent 维护 messages。
	routerAgent := subagents.NewRouterAgent(store, memory.NewMemoryMiddleware("RouterAgent"))

	ctx := context.Background()
	// 三个子 agent：纯对话 / 查天气 / 本机命令执行（沙箱+授权）
	a, err := adk.SetSubAgents(ctx, routerAgent, []adk.Agent{
		chatAgent,
		weatherAgent,
		localCmdAgent,
	})
	if err != nil {
		log.Fatal(err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		EnableStreaming: true,
		Agent:           a,
	})

	h := server.Default(server.WithHostPorts(":28080"))

	h.GET("/index.html", func(ctx context.Context, c *app.RequestContext) {
		data, err := staticFiles.ReadFile("index.html")
		if err != nil {
			c.String(consts.StatusInternalServerError, "Failed to load index.html")
			return
		}
		c.Data(consts.StatusOK, "text/html; charset=utf-8", data)
	})

	h.GET("/", func(ctx context.Context, c *app.RequestContext) {
		data, err := staticFiles.ReadFile("index.html")
		if err != nil {
			c.String(consts.StatusInternalServerError, "Failed to load index.html")
			return
		}
		c.Data(consts.StatusOK, "text/html; charset=utf-8", data)
	})

	h.GET("/chat", func(ctx context.Context, c *app.RequestContext) {
		handleChat(ctx, c, runner, store)
	})

	log.Println("Server starting on http://localhost:28080")
	// 示例 URL 中的 query 参数是 "北京天气怎样" 的 URL 编码。
	// 用普通字符串（不是 Printf 格式串）避免 go vet 警告 %E 等占位符。
	log.Println("Or try: curl -N \"http://localhost:28080/chat?session_id=demo&query=" +
		url.QueryEscape("北京天气怎样") + "\"")
	h.Spin()
}

func handleChat(ctx context.Context, c *app.RequestContext, runner *adk.Runner, store *session.Store) {
	query := c.Query("query")
	if query == "" {
		c.JSON(consts.StatusBadRequest, map[string]string{
			"error": "query parameter is required",
		})
		return
	}

	// session_id: 客户端传入则复用；不传则由服务端生成。
	sessionID := c.Query("session_id")
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	// === Memory: 本次浏览器请求的组 ID ===
	// 一次浏览器请求可能触发 N 次 LLM 调用,user_request / llm_input / llm_output
	// × N / user_response 都用同一个 request_group_id 串起来,便于 sessions 回溯
	// 与精修定位。
	requestGID := memory.NewRequestGroupID()
	ctx = memory.WithRequestGroupID(ctx, requestGID)
	log.Printf("[main] new request_group_id=%s session=%s", requestGID, sessionID)

	// === Memory: 记录浏览器/客户端用户请求 ===
	// 落到 sessions/<date>/<hour>.md 的 user_request entry,front-matter 带 request_group_id。
	// 异步 fire-and-forget,主流程不阻塞。
	memory.SafeRecordUserRequest(ctx, requestGID, sessionID, "RouterAgent", []byte(query))

	// 关键：通过 adk.WithSessionValues 把 session_id 和 request_group_id 注入 ctx。
	//   - session_id → PersistMiddleware.AfterAgent 读它,写多轮 messages 到 store
	//   - request_group_id → MemoryMiddleware.BeforeModelRewriteState 读它,
	//                         让所有 LLM 调用 entry 关联到本次浏览器请求
	opts := []adk.AgentRunOption{
		adk.WithSessionValues(map[string]any{
			session.KeySessionID:  sessionID,
			session.KeyRequestGID: requestGID,
		}),
	}

	// 加载上一轮 SDK 维护的完整多轮 messages，作为本轮的输入。
	history := store.Get(sessionID)
	messages := append(history, schema.UserMessage(query))

	log.Printf("[main] Run session=%s history_len=%d query=%q",
		sessionID, len(history), query)

	iter := runner.Run(ctx, messages, opts...)

	// 把 SSE 流挂到 hertz 响应上。
	s := sse.NewStream(c)
	_ = message.SendSSEEvent(s, message.SSEEvent{
		Type:    "session",
		Content: sessionID,
	})

	// === Memory: 把 SSE event 流转发给客户端,同时收集 assistant 消息内容 ===
	// 从事件流中收集 assistant 消息,而不是事后读 store,避免时机问题。
	var responseBuilder strings.Builder
	assistantCount := 0
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		// 这里做 SSE 转发，同时从 event 中提取 assistant 消息内容
		if err := message.ProcessAgentEvent(ctx, s, event); err != nil {
			log.Printf("[main] ProcessAgentEvent error session=%s err=%v", sessionID, err)
			break
		}
		// 从 event 中提取 assistant 消息内容
		if event != nil && event.Output != nil && event.Output.MessageOutput != nil &&
			event.Output.MessageOutput.Message != nil {
			msg := event.Output.MessageOutput.Message
			if msg.Role == schema.Assistant && msg.Content != "" {
				// 剥离 think 块,只保留用户可见内容
				cleaned := stripThinkTags(msg.Content)
				if cleaned != "" {
					if responseBuilder.Len() > 0 {
						responseBuilder.WriteString("\n\n")
					}
					responseBuilder.WriteString(cleaned)
					assistantCount++
				}
			}
		}
	}

	_ = message.SendSSEEvent(s, message.SSEEvent{Type: "end"})

	// === Memory: 落盘 user_response ===
	if assistantCount > 0 {
		fullResponse := responseBuilder.String()
		memory.SafeRecordUserResponse(ctx, requestGID, sessionID, "RouterAgent", []byte(fullResponse))
		log.Printf("[main] recorded user_response session=%s group=%s merged_from=%d_events content_len=%d",
			sessionID, requestGID, assistantCount, len(fullResponse))
	} else {
		log.Printf("[main] WARNING: no assistant message with content found in events session=%s", sessionID)
	}

	// AfterAgent middleware 已把本次完整 messages 写回 store；这里只做日志回顾。
	if final := store.Get(sessionID); len(final) > 0 {
		log.Printf("[main] Run done session=%s persisted_msgs=%d", sessionID, len(final))
	}
}

// eventToMemoryString 把 eino AgentEvent 序列化成可读字符串,供 sessions/user_response 落盘。
// 用 JSON 序列化,字段稳定;同时用 eino schema 自带的 String() 方法作为 fallback。
func eventToMemoryString(event *adk.AgentEvent) string {
	if event == nil {
		return "(nil event)"
	}
	// 优先用 Output 的 Action / AgentName / Role 等关键字段拼一行人类可读字符串。
	if event.Output != nil && event.Output.MessageOutput != nil &&
		event.Output.MessageOutput.Message != nil {
		msg := event.Output.MessageOutput.Message
		return string(msg.Role) + ": " + memory.TruncateBytes(msg.Content, 500)
	}
	// fallback:输出 event 类型 + agent name
	return fmt.Sprintf("[event agent=%s]", event.AgentName)
}

// stripThinkTags 剥离  复合标签及其内容。
// 用于从 assistant 消息中提取用户可见的真实回复(RouterAgent 的路由推理不应暴露给用户)。
func stripThinkTags(s string) string {
	re := regexp.MustCompile(`(?is)<think>.*?</think>`)
	return strings.TrimSpace(re.ReplaceAllString(s, ""))
}
