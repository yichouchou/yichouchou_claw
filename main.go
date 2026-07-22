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
	"log"
	"net/url"
	"os"
	"path/filepath"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/google/uuid"
	"github.com/hertz-contrib/sse"

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

	weatherAgent := subagents.NewWeatherAgent()
	chatAgent := subagents.NewChatAgent(context.Background(), skillsRoot)
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
		subagents.NewLocalCommandAgent(context.Background(), skillsRoot),
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
	routerAgent := subagents.NewRouterAgent(store)

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

	// 关键：通过 adk.WithSessionValues 把 session_id 注入 ctx。
	// 中间件 PersistMiddleware.AfterAgent 从 ctx 中读 session_id，把完整 messages 写入 store。
	opts := []adk.AgentRunOption{
		adk.WithSessionValues(map[string]any{
			session.KeySessionID: sessionID,
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

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		// 这里只做 SSE 转发，不再尝试从 chunk 反推 message 持久化历史：
		// 持久化由 PersistMiddleware.AfterAgent 在 SDK 自己维护的状态上完成。
		if err := message.ProcessAgentEvent(ctx, s, event); err != nil {
			log.Printf("[main] ProcessAgentEvent error session=%s err=%v", sessionID, err)
			break
		}
	}

	_ = message.SendSSEEvent(s, message.SSEEvent{Type: "end"})

	// AfterAgent middleware 已把本次完整 messages 写回 store；这里只做日志回顾。
	if final := store.Get(sessionID); len(final) > 0 {
		log.Printf("[main] Run done session=%s persisted_msgs=%d", sessionID, len(final))
	}
}
