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

package messagehandler

import (
	"context"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/yichouchou/yichouchou_claw/internal/localcommand"
)

// AuthorizationMiddleware 解析用户输入中的授权意图，将 AuthorizationScope 写入 ctx，
// 供后续的 localcommand.Execute 使用。
//
// 支持的授权指令（中英文都可以）：
//
//   - "我授权使用 bash" / "授权 bash" / "i authorize bash" / "auth: bash"
//
//   - "我授权安装" / "我授权装" / "授权安装软件" / "i authorize install" / "auth: install"
//
//   - "我授权白名单放宽" / "授权白名单" / "授权运行 <cmd>" /
//     "whitelist auth" / "auth whitelist" / "i authorize whitelist"
//
// 授权默认有效期 10 分钟，过期后自动失效。可在构造时调整。
type AuthorizationMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	// TTL 授权有效期；零值 = 10 分钟
	TTL time.Duration

	// GrantedBy 授权审计用标识，默认为 "user"
	GrantedBy string
}

// NewAuthorizationMiddleware 创建授权检测 middleware。
func NewAuthorizationMiddleware() *AuthorizationMiddleware {
	return &AuthorizationMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		TTL:                          10 * time.Minute,
		GrantedBy:                    "user",
	}
}

// 匹配中文授权语句（兼容常见变体）
var (
	installAuthPatterns = []*regexp.Regexp{
		regexp.MustCompile(`授权(?:安装|安装软件|装包|装软件|install|installation)`),
		regexp.MustCompile(`(?:我|请)?授权装`),
		regexp.MustCompile(`可以安装`),
		regexp.MustCompile(`可以装(?:包|软件)?`),
		regexp.MustCompile(`允许安装`),
		regexp.MustCompile(`允许装`),
		regexp.MustCompile(`请(?:帮|进行)?安装`),
		regexp.MustCompile(`可以帮我装`),
	}
	bashAuthPatterns = []*regexp.Regexp{
		regexp.MustCompile(`授权(?:使用\s+)?bash`),
		regexp.MustCompile(`授权(?:执行|运行)?(?:bash|shell|脚本)`),
		regexp.MustCompile(`授权(?:通用)?bash`),
		regexp.MustCompile(`可以(?:用|执行|跑|运行)?bash`),
		regexp.MustCompile(`(?:你|我)?(?:可以|允许)?(?:执行|运行|跑)\s*bash`),
		regexp.MustCompile(`auth(?:orize)?\s*[=:]\s*bash`),
		regexp.MustCompile(`auth\s*:?\s*bash`),
	}
	// WhitelistAuth 关键词：放宽白名单外的命令
	whitelistAuthPatterns = []*regexp.Regexp{
		// 通用白名单放宽（任意白名单外命令都可执行）
		regexp.MustCompile(`授权(?:白名单|白名单放宽|whitelist|白名单外的命令)`),
		regexp.MustCompile(`(?:可以|允许)?放行(?:白名单外|白名单外的)?`),
		regexp.MustCompile(`(?:允许|授权)?执行(?:白名单外)?`),
		// 单命令白名单授权：识别 "授权运行 command" / "我授权 <command>"
		regexp.MustCompile(`授权(?:运行|执行)\s+([a-zA-Z][a-zA-Z0-9_.+@-]*)`),
		regexp.MustCompile(`(?:我|请)?授权\s+([a-zA-Z][a-zA-Z0-9_.+@-]+)\b`),
	}
	// 英文 WhitelistAuth
	whitelistEnglishPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bwhitelist\s*auth(orization)?\s+granted\b`),
		regexp.MustCompile(`\bi\s+authorize\s+whitelist\b`),
		regexp.MustCompile(`\bauth\s*:?\s*whitelist\b`),
		regexp.MustCompile(`\bauth(?:orize)?\s+([a-zA-Z][a-zA-Z0-9_.+@-]*)`),
	}
	// 英文授权
	englishAuthPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bi\s+authorize\s+install\b`),
		regexp.MustCompile(`\binstall\s+auth(orization)?\s+granted\b`),
		regexp.MustCompile(`\bgrant\s+install\s+permission\b`),
		regexp.MustCompile(`\bi\s+authorize\s+bash\b`),
		regexp.MustCompile(`\bbash\s+auth(orization)?\s+granted\b`),
		regexp.MustCompile(`\bgrant\s+bash\s+permission\b`),
		regexp.MustCompile(`\bauth\s*[:=]\s*(bash|install|whitelist)\b`),
	}
)

// BeforeModelRewriteState 解析授权意图并写入 ctx；也把授权摘要回填到 system message，
// 让 LLM 在下一轮知道"我已经获得了 X 授权"，避免反复询问用户。
func (m *AuthorizationMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}

	var latestUserContent string
	for i := len(state.Messages) - 1; i >= 0; i-- {
		msg := state.Messages[i]
		if msg.Role == schema.User {
			latestUserContent = msg.Content
			break
		}
	}
	if strings.TrimSpace(latestUserContent) == "" {
		return ctx, state, nil
	}

	auth := parseAuthorization(latestUserContent)
	if auth.IsEmpty() {
		return ctx, state, nil
	}

	// 写入 ctx 供 localcommand.Execute 使用
	ctx = localcommand.WithAuthorization(ctx, auth)

	// 同时写入 eino session（跨 ctx 边界：ToolsNode 调用 tool 时也能拿到）
	// 这是关键修复 —— AuthorizationMiddleware 修改的 ctx 只在 model call 节点作用域内
	// 生效，而 tool 调用走的是 graph 根 ctx，必须通过 session values 才能传递
	localcommand.WriteAuthorizationToSession(ctx, auth)

	log.Printf("[AuthorizationMiddleware] Wrote AuthorizationScope to ctx+session: Install=%v Bash=%v WhitelistAuth=%v WhitelistCmd=%q ExpiresAt=%v GrantedBy=%s",
		auth.Install, auth.Bash, auth.WhitelistAuth, auth.WhitelistCmd, auth.ExpiresAt.Format("15:04:05"), auth.GrantedBy)

	// 把授权摘要回填到 system message，让 LLM 知道自己已获得授权
	notice := formatAuthNotice(auth)
	for _, msg := range state.Messages {
		if msg.Role == schema.System {
			if !strings.Contains(msg.Content, notice) {
				msg.Content += notice
			}
			break
		}
	}
	return ctx, state, nil
}

// parseAuthorization 从用户消息里解析授权意图；中文/英文都支持。
// 同一句话里同时表达多种授权时，全部生效。
func parseAuthorization(userText string) localcommand.AuthorizationScope {
	lower := strings.ToLower(userText)
	auth := localcommand.AuthorizationScope{}

	// Install 授权
	for _, p := range installAuthPatterns {
		if p.MatchString(userText) {
			auth.Install = true
			break
		}
	}
	// Bash 授权
	for _, p := range bashAuthPatterns {
		if p.MatchString(userText) {
			auth.Bash = true
			break
		}
	}
	// WhitelistAuth 授权
	var whitelistCmd string
	for _, p := range whitelistAuthPatterns {
		if loc := p.FindStringSubmatchIndex(userText); loc != nil {
			auth.WhitelistAuth = true
			// 提取命令名（捕获组 1）
			if len(loc) >= 4 && loc[2] >= 0 && loc[3] > loc[2] {
				candidate := userText[loc[2]:loc[3]]
				// 过滤掉 install / bash 等其他授权关键词
				if !isAuthKeyword(candidate) {
					whitelistCmd = candidate
				}
			}
			break
		}
	}
	// 英文 WhitelistAuth（覆盖 englishAuthPatterns 中 install/bash 之后的分支）
	for _, p := range whitelistEnglishPatterns {
		if loc := p.FindStringSubmatchIndex(lower); loc != nil {
			auth.WhitelistAuth = true
			if len(loc) >= 4 && loc[2] >= 0 && loc[3] > loc[2] {
				candidate := lower[loc[2]:loc[3]]
				if !isAuthKeyword(candidate) {
					whitelistCmd = candidate
				}
			}
			break
		}
	}
	// 英文通用 auth: install / bash（保持原逻辑）
	for _, p := range englishAuthPatterns {
		if p.MatchString(lower) {
			src := p.String()
			if strings.Contains(src, `install`) {
				auth.Install = true
			} else if strings.Contains(src, `bash`) {
				auth.Bash = true
			}
		}
	}

	if auth.Install || auth.Bash || auth.WhitelistAuth {
		auth.GrantedBy = "user"
		auth.Reason = firstNonEmptyLine(userText)
		auth.ExpiresAt = time.Now().Add(10 * time.Minute)
		auth.WhitelistCmd = whitelistCmd
	}
	return auth
}

// isAuthKeyword 判断某个候选命令名是否是授权关键词（用于过滤误识别）
func isAuthKeyword(s string) bool {
	keywords := map[string]bool{
		"install": true, "bash": true, "whitelist": true,
		"authorization": true, "auth": true, "permission": true,
		"shell": true, "script": true,
	}
	return keywords[strings.ToLower(s)]
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 80 {
				return line[:80] + "..."
			}
			return line
		}
	}
	return ""
}

func formatAuthNotice(a localcommand.AuthorizationScope) string {
	var parts []string
	if a.Install {
		parts = append(parts, "Install")
	}
	if a.Bash {
		parts = append(parts, "Bash")
	}
	if a.WhitelistAuth {
		if a.WhitelistCmd != "" {
			parts = append(parts, "Whitelist("+a.WhitelistCmd+")")
		} else {
			parts = append(parts, "Whitelist(*)")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	exp := a.ExpiresAt.Format("15:04:05")
	scope := strings.Join(parts, "+")
	return "\n\n[系统提示] 用户已授权 " + scope + " 授权（至 " + exp + "）。本次会话内相关的安装/写操作/白名单外命令可以自动放行；硬禁止命令仍然不能执行。"
}

// init 把 eino adk session 操作函数注入到 localcommand 包。
//
// 这是 AuthorizationScope 跨 ctx 边界传递的关键：
//   - eino ADK 的 ToolsNode 在调用 tool.endpoint 时使用的是 graph 的根 ctx，
//     不包含 ChatModelAgentMiddleware 修改的 ctx 值。
//   - 但是 eino session 的 values 是 graph 范围共享的，可以跨节点访问。
//   - 通过 adk.AddSessionValue / adk.GetSessionValue 注册到 localcommand，
//     让 ResolveAuthorization 能从 session 中 fallback 拿到授权。
func init() {
	localcommand.RegisterSessionOps(
		func(ctx context.Context, key string, value any) {
			adk.AddSessionValue(ctx, key, value)
		},
		func(ctx context.Context, key string) (any, bool) {
			return adk.GetSessionValue(ctx, key)
		},
	)
}
