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

package localcommand

import (
	"context"
	"time"
)

// AuthorizationScope 描述用户授权沙箱放宽限制的范围。
//
// 授权分三类（可叠加）：
//   - Install:      允许"安装类"命令（apt/yum/dnf install、pip install、
//     npm install、go install、dpkg -i、rpm -i 等）。
//   - Bash:         允许"通用 bash"放宽；除硬禁止外任何命令都可执行。
//   - WhitelistAuth: 允许"白名单外的单个命令"放行；用于临时允许某个不常见
//     但用户明确要求的命令（例如 command -v 等 POSIX builtin）。
//
// 三类授权都受 ExpiresAt 控制，过期后自动失效；都要求 GrantedBy 非空
// （通常是用户的 user id / session id），便于审计。
//
// 注意：AuthorizationScope 只能放宽"软禁止"模式；
// HardForbiddenPatterns 中的模式（如 rm -rf /、dd of=/...、shutdown、反弹 shell 等）
// **永远不会被授权放行**，任何授权都不能突破。
type AuthorizationScope struct {
	// Install 授权"安装类"操作
	Install bool
	// Bash 授权"通用 bash"放宽
	Bash bool
	// WhitelistAuth 授权"白名单外命令"放行（针对单个不在白名单的命令）
	WhitelistAuth bool
	// WhitelistCmd 被授权放行的具体命令名（可选；如果为空则放行所有白名单外命令）
	WhitelistCmd string
	// ExpiresAt 授权过期时间，零值表示不过期（不推荐）
	ExpiresAt time.Time
	// GrantedBy 谁授权的（user id / session id / 备注）
	GrantedBy string
	// Reason 授权理由（用户原话摘要，便于审计）
	Reason string
}

// Valid 检查授权是否仍在有效期内。
func (a AuthorizationScope) Valid(now time.Time) bool {
	if a.ExpiresAt.IsZero() {
		return true
	}
	return now.Before(a.ExpiresAt)
}

// IsEmpty 判断授权是否完全为空（三个开关都未开启）。
func (a AuthorizationScope) IsEmpty() bool {
	return !a.Install && !a.Bash && !a.WhitelistAuth
}

// authContextKey 用于把 AuthorizationScope 存入 context.Value。
type authContextKey struct{}

// WithAuthorization 把授权写入 ctx，供 IsDangerous 等函数读取。
func WithAuthorization(ctx context.Context, a AuthorizationScope) context.Context {
	if a.IsEmpty() {
		return ctx
	}
	return context.WithValue(ctx, authContextKey{}, a)
}

// AuthorizationFromContext 从 ctx 读取授权；若不存在则返回零值（无授权）。
func AuthorizationFromContext(ctx context.Context) AuthorizationScope {
	v, _ := ctx.Value(authContextKey{}).(AuthorizationScope)
	return v
}

// Now 暴露给上层注入"测试时钟"，避免 time.Now 被硬编码到 IsDangerous。
var Now = time.Now
