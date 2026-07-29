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

func main() {
	// 解析 cwd：所有 workdir 相对路径都以 cwd 为基准。
	wd, _ := os.Getwd()

	// =====================================================================
	// 配置加载：先后 exec-approvals.json（沙箱）再 application.yml（应用）
	// =====================================================================
	approvalsPath := filepath.Join(wd, "workdir", "config", "exec-approvals.json")
	if err := config.LoadApprovals(approvalsPath); err != nil {
		log.Fatalf("[main] failed to parse %s: %v", approvalsPath, err)
	}

	appCfgPath := filepath.Join(wd, "workdir", "config", "application.yml")
	if err := config.LoadApplication(appCfgPath); err != nil {
		log.Fatalf("[main] failed to parse %s: %v", appCfgPath, err)
	}
	appCfg := config.GetApplication()

	// =====================================================================
	// Sandbox 安全初始化（2026-07-27 新增,集成四个安全阶段）
	// =====================================================================
	if appCfg.Sandbox.Enabled {
		// 阶段 3+4: 把 path_acl 注入 localcommand 全局 ACL
		if appCfg.Sandbox.PathACL != nil {
			acl := &localcommand.PathACL{
				ReadDenied:  appCfg.Sandbox.PathACL.ReadDenied,
				WriteDenied: appCfg.Sandbox.PathACL.WriteDenied,
				ExecDenied:  appCfg.Sandbox.PathACL.ExecDenied,
			}
			localcommand.SetGlobalPathACL(acl)
			log.Printf("[main] sandbox.path_acl enabled: read=%d write=%d exec=%d",
				len(acl.ReadDenied), len(acl.WriteDenied), len(acl.ExecDenied))
		}

		// 阶段 2: denybin PATH 注入（OS 层 defense-in-depth）
		if appCfg.Sandbox.Denybin != nil && appCfg.Sandbox.Denybin.Enabled {
			binDir, newPATH, err := localcommand.SetupDenyBin()
			if err != nil {
				log.Printf("[main] denybin setup failed: %v (PATH injection disabled)", err)
			} else {
				log.Printf("[main] denybin enabled: %s (injected into PATH)", binDir)
				// 把注入后的 PATH 存到 env,供子进程继承
				_ = os.Setenv("PATH", newPATH)
			}
		}
	} else {
		log.Printf("[main] WARNING: sandbox security DISABLED (extremely unsafe)")
	}

	// 解析关键路径（相对路径转绝对路径）。
	skillsRoot := resolvePath(wd, appCfg.Paths.Skills)
	memoryRoot := resolvePath(wd, appCfg.Paths.Memory)

	// 进程级别的会话存储。
	store := session.NewStore(appCfg.Session.MaxRounds)

	// =====================================================================
	// Memory Recorder 初始化
	// =====================================================================
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

	// === Memory Index（长期记忆快速查询）===
	// 索引文件位于 memRecorder.Root()/.index.jsonl；
	// 启动时如不存在,自动从 workdir/memory/**/*.md 全量重建。
	var memIndex *memory.Index
	if memRecorder != nil {
		memIndex, err = memory.NewIndex(memRecorder.Root())
		if err != nil {
			log.Printf("[main] failed to init memory index: %v (long-term search disabled)", err)
			memIndex = nil
		} else {
			memRecorder.SetIndex(memIndex)
			memory.SetGlobalIndex(memIndex)
			stats := memIndex.Stats()
			log.Printf("[main] memory index enabled: %d entries (kinds=%d agents=%d)",
				stats.TotalEntries, len(stats.ByKind), len(stats.ByAgent))
		}
	}

	// === Memory LLM Refine（可选）：用 LLM 重新生成精准摘要 ===
	refineCfg := memory.DefaultRefinerConfig()
	applyRefinerConfig(&refineCfg, appCfg.Memory.Refiner)

	var refineQueue *memory.RefineQueue
	if appCfg.Memory.LLMRefineEnabled && memRecorder != nil {
		einoCM := subagents.NewChatModelForRefiner()
		var cm memory.ChatModel
		if einoCM != nil {
			cm = memory.AdapterFunc(func(ctx context.Context, msgs []*schema.Message) (*schema.Message, error) {
				return einoCM.Generate(ctx, msgs)
			})
		}
		einoRefiner := memory.NewEinoLLMRefiner(cm, refineCfg)
		if einoRefiner != nil {
			updater := memory.NewFileUpdater()
			refineQueue = memory.NewRefineQueue(einoRefiner, refineCfg, updater)
			if refineQueue != nil {
				memRecorder.SetRefiner(refineQueue)
				// 让 refine 写回文件后同步更新索引
				if memIndex != nil {
					refineQueue.SetIndex(memIndex)
				}
				log.Printf("[main] LLM refine enabled (workers=%d)", refineCfg.Workers)
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

	// =====================================================================
	// 执行审批配置（白名单/黑名单从 JSON 喂给沙箱）
	// =====================================================================
	loaded := 0
	for _, agentName := range config.ListAgentNames() {
		localcommand.SetAllowedCommands(agentName)
		localcommand.SetDeniedCommands(agentName)
		loaded++
	}
	log.Printf("[main] exec-approvals.json applied to %d agent(s): %v", loaded, config.ListAgentNames())

	// =====================================================================
	// 构造各 Agent
	// =====================================================================
	weatherAgent := subagents.NewWeatherAgent(store, memory.NewMemoryMiddleware("WeatherAgent"))
	chatAgent := subagents.NewChatAgent(context.Background(), skillsRoot, store, memory.NewMemoryMiddleware("ChatAgent"))
	// 包装 ChatAgent / WeatherAgent / LocalCommandAgent：禁止它们转回 RouterAgent。
	localCmdAgent := adk.AgentWithOptions(
		context.Background(),
		subagents.NewLocalCommandAgent(context.Background(), skillsRoot, store, memory.NewMemoryMiddleware("LocalCommandAgent")),
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

	// =====================================================================
	// HTTP 服务（监听地址从配置读取）
	// =====================================================================
	h := server.Default(server.WithHostPorts(appCfg.Server.Host))

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

	log.Printf("Server starting on http://localhost%s", appCfg.Server.Host)
	// 示例 URL 中的 query 参数是 "北京天气怎样" 的 URL 编码。
	log.Println("Or try: curl -N \"http://localhost" + appCfg.Server.Host +
		"/chat?session_id=demo&query=" + url.QueryEscape("北京天气怎样") + "\"")
	h.Spin()
}

// resolvePath 把相对路径转换为绝对路径（基于 cwd）。若已是绝对路径则原样返回。
func resolvePath(cwd, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(cwd, p)
}

// applyRefinerConfig 把 application.yml 中的 refiner 配置覆盖到 memory.RefinerConfig。
// 仅覆盖 yaml 中显式给了非零值的字段（实现细节：DefaultRefinerConfig 已填默认值，
// 这里根据 RefinerConfig 各字段是否非零做条件覆盖）。
func applyRefinerConfig(dst *memory.RefinerConfig, src config.RefinerConfig) {
	if src.Workers > 0 {
		dst.Workers = src.Workers
	}
	if src.QueueSize > 0 {
		dst.QueueSize = src.QueueSize
	}
	if src.PerCallTimeoutSeconds > 0 {
		dst.PerCallTimeout = time.Duration(src.PerCallTimeoutSeconds) * time.Second
	}
	if src.ContentSnippetBytes > 0 {
		dst.ContentSnippetBytes = src.ContentSnippetBytes
	}
	if src.ModelTemperature > 0 {
		dst.ModelTemperature = src.ModelTemperature
	}
	if src.MaxSummaryLength > 0 {
		dst.MaxSummaryLength = src.MaxSummaryLength
	}
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
	requestGID := memory.NewRequestGroupID()
	ctx = memory.WithRequestGroupID(ctx, requestGID)
	log.Printf("[main] new request_group_id=%s session=%s", requestGID, sessionID)

	// === Memory: 记录浏览器/客户端用户请求 ===
	memory.SafeRecordUserRequest(ctx, requestGID, sessionID, "RouterAgent", []byte(query))

	// 关键：通过 adk.WithSessionValues 把 session_id 和 request_group_id 注入 ctx。
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

	// === Memory: 把 SSE event 流转发给客户端 ===
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if err := message.ProcessAgentEvent(ctx, s, event); err != nil {
			log.Printf("[main] ProcessAgentEvent error session=%s err=%v", sessionID, err)
			break
		}
	}

	_ = message.SendSSEEvent(s, message.SSEEvent{Type: "end"})

	// === Memory: 落盘 user_response ===
	final := store.Get(sessionID)
	log.Printf("[main] store.Get session=%s msgs=%d", sessionID, len(final))
	for i, m := range final {
		log.Printf("[main]   msg[%d] role=%s content_len=%d tool_calls=%d",
			i, m.Role, len(m.Content), len(m.ToolCalls))
	}

	var responseBuilder strings.Builder
	assistantCount := 0
	for _, m := range final {
		if m.Role == schema.Assistant && m.Content != "" {
			if responseBuilder.Len() > 0 {
				responseBuilder.WriteString("\n\n")
			}
			responseBuilder.WriteString(m.Content)
			assistantCount++
		}
	}

	if assistantCount > 0 {
		// 对拼接后的完整内容统一 stripThinkTags
		fullResponse := stripThinkTags(responseBuilder.String())
		if fullResponse != "" {
			memory.SafeRecordUserResponse(ctx, requestGID, sessionID, "RouterAgent", []byte(fullResponse))
			log.Printf("[main] recorded user_response session=%s group=%s from=%d_messages content_len=%d",
				sessionID, requestGID, assistantCount, len(fullResponse))
		} else {
			log.Printf("[main] WARNING: all assistant content stripped as think blocks session=%s", sessionID)
		}
	} else {
		log.Printf("[main] WARNING: no assistant message with content found session=%s", sessionID)
	}

	// AfterAgent middleware 已把本次完整 messages 写回 store；这里只做日志回顾。
	if final := store.Get(sessionID); len(final) > 0 {
		log.Printf("[main] Run done session=%s persisted_msgs=%d", sessionID, len(final))
	}
}

// eventToMemoryString 把 eino AgentEvent 序列化成可读字符串,供 sessions/user_response 落盘。
func eventToMemoryString(event *adk.AgentEvent) string {
	if event == nil {
		return "(nil event)"
	}
	if event.Output != nil && event.Output.MessageOutput != nil &&
		event.Output.MessageOutput.Message != nil {
		msg := event.Output.MessageOutput.Message
		return string(msg.Role) + ": " + memory.TruncateBytes(msg.Content, 500)
	}
	return fmt.Sprintf("[event agent=%s]", event.AgentName)
}

// stripThinkTags 剥离 <think> 复合标签及其内容。
func stripThinkTags(s string) string {
	re := regexp.MustCompile(`(?is)<think>.*?</think>`)
	return strings.TrimSpace(re.ReplaceAllString(s, ""))
}
