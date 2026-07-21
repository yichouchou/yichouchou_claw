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
	// 英文授权
	englishAuthPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bi\s+authorize\s+install\b`),
		regexp.MustCompile(`\binstall\s+auth(orization)?\s+granted\b`),
		regexp.MustCompile(`\bgrant\s+install\s+permission\b`),
		regexp.MustCompile(`\bi\s+authorize\s+bash\b`),
		regexp.MustCompile(`\bbash\s+auth(orization)?\s+granted\b`),
		regexp.MustCompile(`\bgrant\s+bash\s+permission\b`),
		regexp.MustCompile(`\bauth\s*[:=]\s*(bash|install)\b`),
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
// 同一句话里同时表达 bash 和 install 授权时，两者都生效。
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
	for _, p := range englishAuthPatterns {
		if p.MatchString(lower) {
			// 进一步判断是 install 还是 bash
			src := p.String()
			if strings.Contains(src, `install`) {
				auth.Install = true
			} else if strings.Contains(src, `bash`) {
				auth.Bash = true
			}
		}
	}

	// Bash 授权
	for _, p := range bashAuthPatterns {
		if p.MatchString(userText) {
			auth.Bash = true
			break
		}
	}

	if auth.Install || auth.Bash {
		auth.GrantedBy = "user"
		auth.Reason = firstNonEmptyLine(userText)
		auth.ExpiresAt = time.Now().Add(10 * time.Minute)
	}
	return auth
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
	if len(parts) == 0 {
		return ""
	}
	exp := a.ExpiresAt.Format("15:04:05")
	scope := strings.Join(parts, "+")
	return "\n\n[系统提示] 用户已授权 " + scope + " 授权（至 " + exp + "）。本次会话内相关的安装/写操作可以自动放行；硬禁止命令仍然不能执行。"
}
