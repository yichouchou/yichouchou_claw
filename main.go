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
	"encoding/base64"
	"fmt"
	"io"
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
	"github.com/yichouchou/yichouchou_claw/internal/office"
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

	// 进程级别的会话存储(2026-07-30 回退到原始逻辑: 始终磁盘持久化)。
	//
	// 用户上传的图片/文件按 Claude Code 精神"用完即丢不存盘";但会话内容、
	// memory 长期记忆仍走原来的磁盘持久化逻辑。
	//
	// 设计原则:
	//   - 会话和 memory 落盘 → 保留原始逻辑(NewStoreWithPersist + Recorder)
	//   - 上传文件不落盘    → 硬编码,新行为
	//   - 不引入 PERSIST_DISK 开关
	//
	store := session.NewStoreWithPersist(appCfg.Session.MaxRounds, resolvePath(wd, "workdir/sessions"))
	if err := store.LoadFromDisk(); err != nil {
		log.Printf("[main] session.LoadFromDisk failed: %v (继续运行,内存会话不受影响)", err)
	}

	// =====================================================================
	// Memory Recorder 初始化(2026-07-30 回退: 始终启用)
	// =====================================================================
	memRecorder, err := memory.NewAsyncMarkdownRecorder(memoryRoot)
	if err != nil {
		log.Fatalf("[main] failed to init memory recorder: %v", err)
	}
	memory.SetRecorder(memRecorder)
	log.Printf("[main] memory recorder enabled at %s (async)", memRecorder.Root())

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
	//
	// 2026-07-29 修复:之前这里没真正挂 PersistMiddleware,所有对话只活在内存里,
	// 多模态附件(图片 base64)直接丢。修复后每条 assistant + tool 消息会被
	// Append 到 store,store 启用 PersistPath 时同步落盘。
	routerAgent := subagents.NewRouterAgent(store,
		session.NewPersistMiddleware(store),
		memory.NewMemoryMiddleware("RouterAgent"),
	)

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
		// === 2026-07-29: 强制不缓存,避免前端拿到旧 HTML 后 404 ===
		c.Response.Header.Set("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Response.Header.Set("Pragma", "no-cache")
		c.Response.Header.Set("Expires", "0")
		c.Data(consts.StatusOK, "text/html; charset=utf-8", data)
	})

	h.GET("/chat", handleChatWrapper(ctx, runner, store, true))
	// === 多模态修复(2026-07-29): 支持 POST + JSON body ===
	// 旧 GET 把多模态放在 header/base64 中不可靠,改成 POST + body。
	h.POST("/chat", handleChatWrapper(ctx, runner, store, false))

	// === 多模态文件上传端点（2026-07-29 新增）===
	// 前端 fetch('/upload', FormData) 走这里。
	// 文件存到 workdir/uploads/<random>-<sanitized-name>,返回 URL。
	// === === === === === === === === === === === === === === === === === === === ===
	// 上传路由(2026-07-30): hardcoded 不落盘
	// Claude Code 精神: 用完即丢。后端走 inline base64,前端再 POST 到 /chat。
	// === === === === === === === === === === === === === === === === === === === ===
	h.POST("/upload", func(ctx context.Context, c *app.RequestContext) {
		handleUpload(ctx, c)
	})
	// /uploads/*filepath 静态服务已删(永远走 inline base64,不存磁盘)

	// === 隐私: DELETE /api/sessions/:id(让用户主动擦除会话+memory) ===
	// 会话本身我们仍持久化(按你的要求),但是用户依然可以主动清除。
	h.DELETE("/api/sessions/*sessionId", func(ctx context.Context, c *app.RequestContext) {
		handleDeleteSession(ctx, c, store)
	})

	// === 多模态：静态文件服务已删除(2026-07-30) ===
	// 原因: Claude Code 精神,用户上传文件永远不存盘,所以 /uploads/<file> 没有意义。
	// 当前端收到 image_data 后只走 /chat POST body → LLM 上下文 → 丢弃。

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

// handleChatWrapper 返回一个 dispatch 函数,把 /chat 的 GET / POST 都包进来。
//
// isGET 用来区分:
//   - true  → 原 URL 路径 /chat (GET 模式)
//   - false → POST 模式从 body 读
//
// 兜底: 如果发现请求 URL 是 /chat&xxx(老前端误用),自动把 & 换成 ? 并
// 重写 path/hertz 的 Router 不支持 "&" 通配符,所以这里在 dispatcher 内部判断。
//
// 详细:
//
//	HERTZ panic 修复(2026-07-30):
//	panic: no / before wildcards in path /chat&*rest
//	hertz 路由解析器要求通配符( *xxx ) 必须前接 '/'。'&' 不是路径分隔符,
//	它在 URL 里是 query 分隔符,所以 hertz 拒绝注册。但浏览器收到的"旧前端
//	拼错的 URL"是 /chat&session_id=xxx,hertz 看到的 path 是 "chat&session_id=xxx"
//	没法在注册阶段处理,所以我们在 dispatcher 阶段做 rewrite。
func handleChatWrapper(ctx context.Context, runner *adk.Runner, store *session.Store, _ bool) app.HandlerFunc {
	return func(c context.Context, rc *app.RequestContext) {
		// === 兜底 rewrite: /chat&xxx → /chat?xxx ===
		// hertz 已经按 path 段匹配了 "/chat";path 里 & 后面的部分在 hertz
		// 看来是 "额外的 path" 而非 query。我们读 URI 来检测并重写。
		uri := string(rc.Request.RequestURI())
		if i := strings.Index(uri, "&"); i >= 0 && strings.HasPrefix(uri, "/chat") {
			// "/chat&session_id=xxx" → "/chat?session_id=xxx"
			rebuilt := "/chat?" + uri[i+1:]
			rc.Request.SetRequestURI(rebuilt)
			log.Printf("[main] rewrote malformed URL %s → %s", uri, rebuilt)
		}
		handleChat(ctx, rc, runner, store)
	}
}

// ChatRequest 是 /chat 的统一请求体。
//
// 协议(2026-07-29 修复): 从 GET + query/header 改成 POST + JSON body。
// 原因: HTTP Header 无法稳定传输 base64 / data URL。
//   - Header 单行,base64 可能含换行
//   - 字符 + / = 在 Header 中可能被框架特殊处理
//   - base64 长度常常 > 8KB,超过 Header 大小限制
//
// 请求字段:
//   - query           用户文本
//   - session_id      可选,客户端复用同一 session
//   - image_url       可选, []string, http(s) URL 或相对路径("/uploads/...")
//   - image_data      可选, []string, base64-encoded(不带 data: 前缀)
//   - image_mime      可选, []string, 与 image_data 一一对应的 MIME
//   - file_url/file_data/file_mime/file_name 类似,通用文件
type ChatRequest struct {
	Query     string   `json:"query"`
	SessionID string   `json:"session_id,omitempty"`
	ImageURL  []string `json:"image_url,omitempty"`  // http(s) URL
	ImageData []string `json:"image_data,omitempty"` // base64 (no data: prefix)
	ImageMime []string `json:"image_mime,omitempty"` // MIME for image_data
	FileURL   []string `json:"file_url,omitempty"`
	FileData  []string `json:"file_data,omitempty"`
	FileMime  []string `json:"file_mime,omitempty"`
	FileName  []string `json:"file_name,omitempty"`
}

func handleChat(ctx context.Context, c *app.RequestContext, runner *adk.Runner, store *session.Store) {
	// === 兼容 GET(旧)和 POST+JSON body(新) ===
	// 旧版:GPT query=xxx&session_id=xxx
	// 新版:POST body = {"query":"...", "session_id":"...", "image_data":["xxx"], ...}
	var req ChatRequest
	var query, sessionID string

	if string(c.Request.Method()) == "GET" {
		query = c.Query("query")
		sessionID = c.Query("session_id")
	} else {
		// POST + JSON body
		if err := c.BindAndValidate(&req); err != nil {
			c.JSON(consts.StatusBadRequest, map[string]string{
				"error": "invalid request body: " + err.Error(),
			})
			return
		}
		query = req.Query
		sessionID = req.SessionID
	}

	if query == "" {
		c.JSON(consts.StatusBadRequest, map[string]string{
			"error": "query is required",
		})
		return
	}

	// session_id: 客户端传入则复用；不传则由服务端生成。
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	log.Printf("[chat] method=%s session=%s query_len=%d image_data=%d image_url=%d file_data=%d file_url=%d",
		string(c.Request.Method()), sessionID, len(query),
		len(req.ImageData), len(req.ImageURL), len(req.FileData), len(req.FileURL))

	// === Memory: 本次浏览器请求的组 ID ===
	requestGID := memory.NewRequestGroupID()
	ctx = memory.WithRequestGroupID(ctx, requestGID)
	log.Printf("[main] new request_group_id=%s session=%s", requestGID, sessionID)

	// === Memory: 记录浏览器/客户端用户请求 ===
	// (由下方构造 messages 完成后补记,把 inline 标记 + 多模态标签都算进去)

	// 关键：通过 adk.WithSessionValues 把 session_id 和 request_group_id 注入 ctx。
	opts := []adk.AgentRunOption{
		adk.WithSessionValues(map[string]any{
			session.KeySessionID:  sessionID,
			session.KeyRequestGID: requestGID,
		}),
	}

	// 加载上一轮 SDK 维护的完整多轮 messages，作为本轮的输入。
	history := store.Get(sessionID)

	// === 多模态入参(2026-07-29 修复) ===
	//
	// 客户端入参方式(优先级 image_data > image_url > @image 标记):
	//   1. POST body.image_data[]   base64(无 data: 前缀)+ image_mime[]        ← 推荐
	//   2. POST body.image_url[]    http(s) URL 或相对路径("/uploads/...")      ← 兜底
	//   3. query 里 "@image:<URL>" / "@file:<URL>" 的 inline 标记            ← 调试用
	//
	// base64 走 Go 进程 memory → 不会污染 header/URL,Anthropic 100% 接受。
	parts := []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: query},
	}

	// === 方式 1: POST body 里的 base64(最稳定) ===
	// 严格按 base64 校验;长度超限(<1B 或 >8MB)跳过
	const maxBase64Size = 8 * 1024 * 1024
	for i, b64 := range req.ImageData {
		if b64 == "" {
			continue
		}
		// base64 字符校验(可选,无效字符会让 LLM 浪费 token)
		if !isLikelyBase64(b64) {
			log.Printf("[chat] image_data[%d] not base64 (len=%d), skip", i, len(b64))
			continue
		}
		if len(b64) < 100 || len(b64) > maxBase64Size {
			log.Printf("[chat] image_data[%d] size out of range (len=%d), skip", i, len(b64))
			continue
		}
		mime := "image/png"
		if i < len(req.ImageMime) && req.ImageMime[i] != "" {
			mime = req.ImageMime[i]
		}
		b := b64
		m := mime
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &b,
					MIMEType:   m,
				},
			},
		})
	}
	for i, b64 := range req.FileData {
		if b64 == "" {
			continue
		}
		if !isLikelyBase64(b64) {
			log.Printf("[chat] file_data[%d] not base64 (len=%d), skip", i, len(b64))
			continue
		}
		if len(b64) < 10 || len(b64) > maxBase64Size {
			log.Printf("[chat] file_data[%d] size out of range (len=%d), skip", i, len(b64))
			continue
		}
		mime := "application/octet-stream"
		if i < len(req.FileMime) && req.FileMime[i] != "" {
			mime = req.FileMime[i]
		}
		name := "attachment"
		if i < len(req.FileName) && req.FileName[i] != "" {
			name = req.FileName[i]
		}

		// === 2026-07-30: Ark 模型 bug 修复 ===
		//
		// eino Ark adapter (chat_completion_api.go:704) **不支持** ChatMessagePartTypeFileURL。
		// 用户模型实际为 ark( MiniMax-M3 ),之前传 .md/.pdf/.docx 会报:
		//   "unsupported chat message part type in user message: file_url"
		//
		// 修复:把文件 base64 解码 → 拼成 Markdown 风格文本 → 直接进 ChatMessagePartTypeText part。
		// 这样无论底层是 Ark / Anthropic / OpenAI 都接受(TEXT 所有 provider 都支持)。
		//
		// 限制:二进制 PDF 直接 base64 解码会乱码。我们按 mime 分流:
		//   - text/* (txt/md/html/log)       → 解 base64,得到 UTF-8 字符串,正常并入
		//   - application/pdf                 → 标注 "[PDF 附件,具体内容无法以文本传输,请用户口头描述]"
		//   - application/vnd.openxmlformats-* → 同样内容已经过 office.ExtractText 转 text/plain
		//                                       (handleUpload 在上传时已经做了抽取),所以这里
		//                                       mime 是 text/plain,会进 text 分支,正常显示
		//   - 其他二进制                       → 标注格式不支持
		attachmentText := buildAttachmentText(b64, mime, name)
		if attachmentText != "" {
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeText,
				Text: attachmentText,
			})
			log.Printf("[chat] file[%d] name=%s mime=%s as-text len=%d",
				i, name, mime, len(attachmentText))
		}
	}

	// === 方式 2: POST body.image_url / file_url(http(s) URL 或 /uploads/ 相对路径) ===
	for _, rawURL := range req.ImageURL {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			continue
		}
		// 相对路径(/uploads/xxx) → 后端从磁盘读 base64
		if strings.HasPrefix(rawURL, "/") {
			b64, mime, ok := loadDiskAsBase64(strings.TrimPrefix(rawURL, "/"))
			if !ok {
				log.Printf("[chat] image_url=%s disk read failed, skip", rawURL)
				continue
			}
			b := b64
			m := mime
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &b,
						MIMEType:   m,
					},
				},
			})
			continue
		}
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			log.Printf("[chat] image_url=%s not http(s), skip", rawURL)
			continue
		}
		url := rawURL
		parts = append(parts, schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}},
		})
	}
	for _, rawURL := range req.FileURL {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			continue
		}
		if strings.HasPrefix(rawURL, "/") {
			b64, mime, ok := loadDiskAsBase64(strings.TrimPrefix(rawURL, "/"))
			if !ok {
				log.Printf("[chat] file_url=%s disk read failed, skip", rawURL)
				continue
			}
			name := extractFileNameFromURL(rawURL)
			b := b64
			m := mime
			n := name
			parts = append(parts, schema.MessageInputPart{
				Type: schema.ChatMessagePartTypeFileURL,
				File: &schema.MessageInputFile{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &b,
						MIMEType:   m,
					},
					Name: n,
				},
			})
			continue
		}
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			log.Printf("[chat] file_url=%s not http(s), skip", rawURL)
			continue
		}
		url := rawURL
		name := extractFileNameFromURL(url)
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeFileURL,
			File: &schema.MessageInputFile{
				MessagePartCommon: schema.MessagePartCommon{URL: &url},
				Name:              name,
			},
		})
	}

	// 把 query 文本里残留的 "@image:xxx" inline 标记剥离（避免污染 LLM 看到的内容）
	cleanQuery := query
	for _, rawURL := range parseInlineAttachments(query, "@image") {
		url := rawURL
		parts = append(parts, schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}},
		})
		cleanQuery = strings.ReplaceAll(cleanQuery, "@image:"+rawURL, "")
	}
	for _, rawURL := range parseInlineAttachments(query, "@file") {
		url := rawURL
		name := extractFileNameFromURL(url)
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeFileURL,
			File: &schema.MessageInputFile{
				MessagePartCommon: schema.MessagePartCommon{URL: &url},
				Name:              name,
			},
		})
		cleanQuery = strings.ReplaceAll(cleanQuery, "@file:"+rawURL, "")
	}
	// 用清理后的 query 作为 text part（如果没解析到任何 part，parts 只有 text）
	if len(parts) == 1 {
		// 没解析到多模态 part → 退化为纯文本（保持行为兼容）
		parts[0].Text = cleanQuery
	} else {
		// 第一个 text part 用清理后的 query
		parts[0] = schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: strings.TrimSpace(cleanQuery),
		}
	}

	userMsg := &schema.Message{
		Role:                  schema.User,
		Content:               parts[0].Text,
		UserInputMultiContent: parts,
	}
	messages := append(history, userMsg)

	// === Memory: 记录本轮用户请求(2026-07-29 多模态改造) ===
	// 把 inline 标记 + 多模态附件都序列化到 memory,便于 memory 检索。
	memQuery := cleanQuery
	if len(parts) > 1 {
		imgCount := 0
		fileCount := 0
		for _, p := range parts {
			switch p.Type {
			case schema.ChatMessagePartTypeImageURL:
				imgCount++
			case schema.ChatMessagePartTypeFileURL:
				fileCount++
			}
		}
		extra := ""
		if imgCount > 0 {
			extra += fmt.Sprintf(" [%d image]", imgCount)
		}
		if fileCount > 0 {
			extra += fmt.Sprintf(" [%d file]", fileCount)
		}
		memQuery = cleanQuery + extra
	}
	memory.SafeRecordUserRequest(ctx, requestGID, sessionID, "RouterAgent", []byte(memQuery))

	log.Printf("[main] Run session=%s history_len=%d query=%q multimodal_parts=%d",
		sessionID, len(history), query, len(parts))

	// === 整个 run 的兜底超时(2026-07-29 新增) ===
	// 来源: application.yml → agent.total_run_timeout_seconds
	// 默认 600 秒。调到 0 则不设超时(不推荐)。
	// 落点: eino 内部的 ChatModel 调用 / 工具调用都会感知到 ctx.Done()。
	runCtx := ctx
	runCfg := config.GetApplication()
	if runCfg != nil && runCfg.Agent.TotalRunTimeoutSeconds > 0 {
		timeout := time.Duration(runCfg.Agent.TotalRunTimeoutSeconds) * time.Second
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		// 重要: cancel 必须在 handleChat 返回前调,否则上下文泄漏
		defer cancel()
		log.Printf("[main] run timeout=%s session=%s", timeout, sessionID)
	}

	iter := runner.Run(runCtx, messages, opts...)

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

// handleUpload 处理 multipart/form-data 文件上传请求。
//
// 入参:标准 form 表单,字段名 "file"(可多个)
//
// 返回模式(由 query 参数 ?mode= 决定):
//   - mode=url(默认): 返回 URL 路径("/uploads/..."),前端用 location.origin + url
//     拼成绝对 URL;但 Anthropic/OpenAI 这种**外部**模型仍然抓不到 localhost
//     URL → 仅适用于 server-tool 链路；多模态 chat 路径强烈建议 mode=inline。
//   - mode=inline: 返回 data:[mime];base64,... 内联;Anthropic/OpenAI 都能直接
//     读取。代价:体积膨胀约 33%,单条 message 可能撑大上下文。
//
// 行为:
//   - 文件保存到 <workdir>/uploads/<random>-<safe-name>(不论 mode,都落盘,便于重发)
//   - 限制单文件 <=32MB
//   - 返回 JSON:
//     {
//     "url":  "<path or data: URL>",
//     "name": "...",
//     "size": N,
//     "mode": "url" | "inline"
//     }
//
// 设计取舍:
//   - 不做 mime 校验 / 病毒扫描:前端已限制 accept=image/*,.pdf,.txt,.md,.log
//   - 不做权限校验:多模态改造阶段暂不引入用户体系,后续多用户化时加
//
// 2026-07-29: image_url 错误"disallowed url: http://127.0.0.1:28080/uploads/..."
// 是因为 Anthropic / OpenAI 这类云端 LLM 从它们**自己的 server** 拉图片,
// 抓不到 localhost。这个修复就是默认走 inline(base64),云端模型能直接解析。
func handleUpload(_ context.Context, c *app.RequestContext) {
	const maxFileSize = 32 * 1024 * 1024 // 32MB
	// uploadDir 现在只用来"清理目标",不再写文件
	const uploadDir = "workdir/uploads"

	// === 2026-07-30: 硬编码不存盘, 对齐 Claude Code ===
	//
	// 不管前端传 ?mode=url 还是默认 mode=inline,后端都强制把 mode 改成 inline,
	// 且整个函数体内 **不调用 os.WriteFile**。字节只在内存里走一遍 base64 编码后
	// 通过响应回前端,前端再走 POST /chat 把它送进 LLM。
	//
	// 上传的字节流从进入到离开这个函数,从未接触过 workdir/uploads/ 磁盘。
	// 注意: 以前的 mode 变量删除了,响应里 mode 字段硬编码写 "inline"。
	log.Printf("[upload] 2026-07-30 Claude Code 模式: 不落盘, 强制 inline")
	// 这样无论客户端怎么走,字节都只在内存里 — Claude Code 精神。

	// 解析 multipart
	form, err := c.Request.MultipartForm()
	if err != nil {
		c.JSON(consts.StatusBadRequest, map[string]string{
			"error": "invalid multipart form: " + err.Error(),
		})
		return
	}
	files := form.File["file"]
	if len(files) == 0 {
		c.JSON(consts.StatusBadRequest, map[string]string{
			"error": "no file field in form",
		})
		return
	}
	// 单次上传只取第一个文件(简化);多文件走多次上传
	fh := files[0]
	if fh.Size > maxFileSize {
		c.JSON(consts.StatusRequestEntityTooLarge, map[string]string{
			"error": "file too large",
		})
		return
	}

	// === 2026-07-30: 字节只读一次到内存,从不落盘 ===
	//
	// 不再:
	//   - os.MkdirAll(uploadDir, ...)  创建目录(由 main 启动时一次性创建)
	//   - os.Create(target) / os.WriteFile(target, ...)  写盘
	//   - os.ReadFile(target)  回读(因为根本没写)
	// 只用 io.ReadAll + base64。
	src, err := fh.Open()
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{
			"error": "open upload failed: " + err.Error(),
		})
		return
	}
	data, err := io.ReadAll(io.LimitReader(src, maxFileSize+1))
	src.Close()
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]string{
			"error": "read upload failed: " + err.Error(),
		})
		return
	}
	if int64(len(data)) > maxFileSize {
		c.JSON(consts.StatusRequestEntityTooLarge, map[string]string{
			"error": "file too large",
		})
		return
	}

	// 安全验证:用 mime 做粗略 sanity check(让前端知道 base64 是 OK 的)
	mime := guessUploadMIME(fh.Filename)

	// === 2026-07-30: Office 文档在上传时即抽取纯文本 ===
	//
	// .docx / .xlsx / .pptx 不能直接发给 Anthropic(它不支持)。我们这里做一次
	// 服务器端的 format 转换:
	//   1. 解压 OOXML(zip + XML)
	//   2. 抽 <w:t> / shared string + cell / slide text frame
	//   3. 输出 markdown-like 文本
	//   4. 把 mime 重新标成 text/plain,继续走 PlainTextSource 路径
	//
	// 旧二进制格式 .doc/.xls/.ppt 不支持(失败告诉前端)。
	//
	// 全部在内存里完成,符合 Claude Code 精神不落盘。
	if office.IsOOXML(mime) {
		text, ok := office.ExtractText(data, mime)
		if !ok {
			c.JSON(consts.StatusUnsupportedMediaType, map[string]any{
				"error": "office document extraction failed",
				"hint": map[string]string{
					"old_binary_formats": ".doc/.xls/.ppt (旧 OLE 格式) 当前不支持,请另存为 .docx/.xlsx/.pptx 或用 LibreOffice 转 PDF 再上传",
					"alternative":        "PDF / txt / md 格式可直接被 Anthropic 接受",
				},
			})
			return
		}
		// 把 data 替换成纯文本字节,mime 改成 text/plain
		data = []byte(text)
		mime = "text/plain"
		log.Printf("[upload] office extracted: name=%s, extracted_len=%d, new mime=text/plain",
			fh.Filename, len(text))
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mime, encoded)

	log.Printf("[upload] ok name=%s size=%d mime=%s mode=inline (no-disk Claude Code 模式)",
		fh.Filename, len(data), mime)

	c.JSON(consts.StatusOK, map[string]any{
		"url":  dataURL, // 直接给 data URL,前端走 /chat body 用 image_data[]
		"name": fh.Filename,
		"size": len(data),
		"mode": "inline",
		// 注意: 没有 disk_url 字段,因为根本没存盘。
		// 防止前端缓存的旧代码做无效的 fetch('/uploads/...')。
	})
}

// isOfficeOOXML 已迁移到 internal/office/office.go 下的 office.IsOOXML()

// guessUploadMIME 按文件扩展名推断 MIME。
// 与 chatmodel.go / memory/search_tool.go 里的实现保持一致,便于排查。
func guessUploadMIME(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".log":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	// === 2026-07-30: Office 文档 MIME ===
	// .docx / .xlsx / .pptx 是 OOXML 规范文件 = ZIP 容器 + XML。
	// Anthropic 不直接接受,但我们可以用 zip+xml 抽纯文本进 PlainTextSource,
	// 或调用本地 LibreOffice 转 PDF(可选)。
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".doc":
		return "application/msword"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	default:
		// 兜底:用 http.DetectContentType 嗅探
		return "application/octet-stream"
	}
}

// buildAttachmentText 把上传的附件 base64 转成可拼到 message 里的文本片段。
//
// 解决 Ark / 多数 OpenAI 兼容 API **不支持 ChatMessagePartTypeFileURL** 的问题。
// 我们用 chat 协议"人人支持"的 TextPart,绕开所有 model-specific 的 part 类型支持差异。
//
// 入参:
//   - b64:  文件 base64 字符串(由前端 handleUpload 生成的 data URL 截取纯 base64 部分)
//   - mime: application/pdf / text/markdown / application/vnd.openxmlformats-… 等等
//   - name: 上传时的文件名(用于错误信息)
//
// 返回:
//   - string: 可直接喂给 TextPart 的文本片段;"" 表示无法表示
//
// 分流策略:
//   - text/* (text/plain, text/markdown, text/html): 解 base64 → UTF-8 字符串 → 拼"文档"块
//     真实的内容已经被 office_extract.go 抽取成 text/plain(对 .docx/.xlsx/.pptx),所以
//     走到这里 mime 已经是 text/plain;Office 也自然支持。
//   - application/pdf: PDF 是二进制,base64 解码是乱码,无法做"读"操作;
//     标注"[PDF 附件,模型无法直接读 PDF 内容,用户需提供文字摘要]"。
//   - 其它(application/octet-stream 或未知 mime): fallback,按纯文本尝试解码,失败则丢占位。
func buildAttachmentText(b64, mime, name string) string {
	// text/* 全部分支:实际可解码出 UTF-8 字符串
	if strings.HasPrefix(mime, "text/") {
		decoded, err := decodeBase64String(b64)
		if err != nil {
			log.Printf("[chat] text attachment base64 decode failed: %v", err)
			return fmt.Sprintf("\n[附件 %s (%s)] base64 解码失败,请重传\n", name, mime)
		}
		// 控制最大长度:防止单个附件爆 markdown
		const maxAttachmentText = 256 * 1024 // 256KB 文本片段
		if len(decoded) > maxAttachmentText {
			decoded = decoded[:maxAttachmentText] + "\n...(已截断,共 " +
				fmt.Sprintf("%d", len(decoded)) + " 字)..."
		}
		// 加格式提示:让 LLM 知道这是 markdown / html(2026-07-30 优化)
		prefix := textFormatHintForAttachment(mime)
		if prefix != "" {
			return "\n[附件 " + name + " (" + mime + ")]\n" + prefix + decoded + "\n[/附件]\n"
		}
		return "\n[附件 " + name + " (" + mime + ")]\n" + decoded + "\n[/附件]\n"
	}

	// application/pdf 是二进制,base64 解码是乱码
	if mime == "application/pdf" {
		return "\n[附件 " + name + " (application/pdf)] ⚠️ PDF 是二进制格式,当前后端模型不支持直接读取 PDF 内容。请把 PDF 转成 Markdown / 纯文本后再上传,或用文字描述你想问 PDF 的哪个部分。\n[/附件]\n"
	}

	// 其它未知 mime:尝试 base64 解码,失败则标占位
	decoded, err := decodeBase64String(b64)
	if err == nil && isPrintableUTF8(decoded) {
		const maxAttachmentText = 256 * 1024
		if len(decoded) > maxAttachmentText {
			decoded = decoded[:maxAttachmentText] + "\n...(已截断)..."
		}
		return "\n[附件 " + name + " (" + mime + ")]\n" + decoded + "\n[/附件]\n"
	}
	return "\n[附件 " + name + " (" + mime + ")] 二进制文件无法作为文本直接展示,当前 base64 size=" +
		fmt.Sprintf("%d", len(b64)) + "。请提供文字描述。\n[/附件]\n"
}

// textFormatHintForAttachment 是与 anthropic_adapter.textFormatHint 同语义的 helper,
// 但放在 main.go(独立于 adapter 实现)用,所以不复用。
func textFormatHintForAttachment(mime string) string {
	switch mime {
	case "text/markdown":
		return "以下内容是 Markdown 格式,请按 Markdown 语法识别:\n\n"
	case "text/html":
		return "以下内容是 HTML 源代码,请按 HTML 结构识别:\n\n"
	default:
		return ""
	}
}

// decodeBase64String 解码标准或 URL-safe base64,容错无 padding 的情况。
func decodeBase64String(s string) (string, error) {
	padded := s
	if mod := len(padded) % 4; mod != 0 {
		padded += strings.Repeat("=", 4-mod)
	}
	raw, err := base64.StdEncoding.DecodeString(padded)
	if err != nil {
		// 尝试 url-safe 编码
		raw2, err2 := base64.URLEncoding.DecodeString(padded)
		if err2 != nil {
			return "", err
		}
		return string(raw2), nil
	}
	return string(raw), nil
}

// isPrintableUTF8 粗略判断 string 是否像可读文本(全部字符可打印 + 主要是 ASCII / UTF-8 多字节)。
func isPrintableUTF8(s string) bool {
	if len(s) == 0 {
		return false
	}
	nonPrintable := 0
	for _, r := range s {
		// 控制字符(除 \t \n \r 外)判失败
		if r < 32 && r != '\t' && r != '\n' && r != '\r' {
			nonPrintable++
		}
	}
	// 超过 5% 不可打印字符 → 判为二进制
	return nonPrintable*20 < len(s)
}

// isLikelyBase64 用字符频次粗略判断字符串是否像 base64。
//
// 返回 true 的条件: 字符全部 ∈ [A-Za-z0-9+/=] (允许末尾 0-2 个 = padding)。
// 不做严格解码测试(开销大);只快速过滤明显非 base64 的输入。
//
// 设计取舍:
//   - 严格解码会扫描 4 字符一组,对 5MB 的 base64 会引入额外 1MB+ 内存压力
//   - 这里用简单字符校验已经足够;LLM 接收到非 base64 时仍会报错,但
//     至少能在日志里看到 "skip" 提示
func isLikelyBase64(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '+' || c == '/':
		case c == '=' && (i == len(s)-1 || i == len(s)-2):
		default:
			return false
		}
	}
	return true
}

// loadDiskAsBase64 从磁盘读取文件,返回 base64 + MIME + ok。
//
// 输入: 相对路径(不带前导 /),例如 "uploads/xxx.png"
// 输出: base64 字符串 + MIME + 是否成功
//
// 安全:
//   - 拒绝 ..
//   - 拒绝绝对路径
//   - 限定根目录: workdir/
func loadDiskAsBase64(relPath string) (string, string, bool) {
	if strings.Contains(relPath, "..") {
		return "", "", false
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	candidates := []string{
		filepath.Join(wd, relPath),
		filepath.Join(wd, "workdir", relPath),
	}
	if !strings.HasPrefix(relPath, "uploads/") {
		candidates = append(candidates, filepath.Join(wd, "workdir", "uploads", relPath))
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Size() < 8*1024*1024 {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			return base64.StdEncoding.EncodeToString(data), guessUploadMIME(path), true
		}
	}
	return "", "", false
}

// extractFileNameFromURL 从 URL 路径末尾取文件名,去掉 query string。
//
// 例:
//
//	"http://x.com/uploads/abc.png"  → "abc.png"
//	"http://x.com/path/foo.pdf?x=1" → "foo.pdf"
//	"https://x.com"                  → "download"
func extractFileNameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Path == "" {
		return "download"
	}
	base := filepath.Base(u.Path)
	if base == "." || base == "/" || base == "" {
		return "download"
	}
	return base
}

// sanitizeUploadName 把上传文件名里的不安全字符替换成下划线。
//
// 不依赖 session 包里的 sanitizeFileName,避免循环依赖。
func sanitizeUploadName(name string) string {
	r := strings.NewReplacer(
		"/", "_", "\\", "_",
		":", "_", "*", "_",
		"?", "_", "\"", "_",
		"<", "_", ">", "_",
		"|", "_",
	)
	return r.Replace(name)
}

// handleDeleteSession 处理 DELETE /api/sessions/:id。
//
// 行为:
//   - 从内存 store 清掉该 session
//   - 磁盘的 <PersistPath>/<id>.json 由 store.Reset 内部触发持久层清理
//   - 不清理上传文件(因为 2026-07-30 后已经不再落盘了)
//
// 返回: 200 / 400 / 404
func handleDeleteSession(_ context.Context, c *app.RequestContext, store *session.Store) {
	sessionID := c.Param("sessionId")
	// hertz 的 *sessionId 通配符带前导 "/" → 去掉
	if strings.HasPrefix(sessionID, "/") {
		sessionID = strings.TrimPrefix(sessionID, "/")
	}
	if sessionID == "" {
		c.JSON(consts.StatusBadRequest, map[string]string{
			"error": "session_id is required",
		})
		return
	}

	store.Reset(sessionID)
	log.Printf("[delete-session] cleared session=%s", sessionID)

	c.JSON(consts.StatusOK, map[string]any{
		"deleted": sessionID,
		"mode":    "memory+disk",
	})
}

// === handleUploadServe 已在 2026-07-30 删除 ===
//
// Claude Code 精神: 用户上传文件永远不存盘,所以 /uploads/<file> 静态服务
// 没有意义。未来如需重新启用 URL 模式(接对象存储),请:
//   1. 重新注册路由 h.GET("/uploads/*filepath", ...)
//   2. 重新实现该函数(从旧版可查 git log)

// parseInlineAttachments 从 query 文本里提取 @<tag>:<URL> 形式的附件引用。
//
// 行为：
//   - @<tag>:<URL>：整段匹配，URL 段允许字母数字 / : / . / / / - / _ / ? / = / &
//   - 返回所有匹配到的 URL（不重复；顺序保持）
//   - 找不到匹配 → 返回 nil
//
// 用法：parseInlineAttachments(query, "@image") / parseInlineAttachments(query, "@file")
// 设计取舍：保留 inline 形式是为了方便调试和 CLI/curl 测试；生产环境
// 强烈推荐用 Header X-Image-URL / X-File-URL 显式传，文本不会被污染。
func parseInlineAttachments(query, tag string) []string {
	if !strings.HasPrefix(tag, "@") {
		tag = "@" + tag
	}
	// 用正则匹配 @<tag>:<URL>；URL 部分尽量宽松但避免吞掉空白
	pattern := regexp.QuoteMeta(tag) + `:([^\s]+)`
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(query, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		url := strings.TrimRight(m[1], ".,;:!?)]}'\"")
		if _, dup := seen[url]; dup {
			continue
		}
		seen[url] = struct{}{}
		out = append(out, url)
	}
	return out
}
