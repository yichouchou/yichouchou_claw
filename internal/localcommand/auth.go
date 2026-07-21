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

// =====================================================================
// Session-backed AuthorizationScope —— 用于跨 ctx 边界的授权传递
// =====================================================================
//
// 问题背景：
//   eino ADK 的 ChatModelAgentMiddleware 只能修改 model call 节点作用域内的 ctx，
//   而 ToolsNode 调用 tool 时使用的 ctx 是 graph 的根 ctx（不含 AuthorizationScope）。
//   因此 AuthorizationMiddleware 通过 ctx.WithValue 注入的 AuthorizationScope
//   无法抵达 tool.endpoint(ctx)，导致沙箱始终拿不到授权。
//
// 解决方案：
//   eino 提供了 adk.AddSessionValue / adk.GetSessionValue 机制 —— session
//   values 跨 ctx 边界传递（包含 sub-agent 和 ToolsNode 调用）。
//   AuthorizationMiddleware 在写入 ctx 的同时也调用 adk.AddSessionValue 写入 session；
//   Execute() 在读取 ctx 之前先尝试从 session 读取（adk.GetSessionValue）。
//   当 ctx 中**已经存在** AuthorizationScope 时优先用 ctx（便于测试和注入）。

// SessionAuthorizationKey 是 session 中存储 AuthorizationScope 的 key。
const SessionAuthorizationKey = "localcommand.AuthorizationScope"

// sessionWriteOp 是 session value 写入函数签名（避免直接依赖 adk 包导致循环引用）。
type sessionWriteOp func(ctx context.Context, key string, value any)

// sessionReadOp 是 session value 读取函数签名。
type sessionReadOp func(ctx context.Context, key string) (any, bool)

var (
	writeSessionValueFn sessionWriteOp
	readSessionValueFn  sessionReadOp
)

// RegisterSessionOps 注入 adk session 操作函数。
// 由 adk/middlewares/messageHandler/auth.go 在 init() 中调用，避免循环依赖。
func RegisterSessionOps(write sessionWriteOp, read sessionReadOp) {
	writeSessionValueFn = write
	readSessionValueFn = read
}

// WriteAuthorizationToSession 把 AuthorizationScope 写入 eino session（如果可用）。
func WriteAuthorizationToSession(ctx context.Context, auth AuthorizationScope) {
	if auth.IsEmpty() {
		return
	}
	if writeSessionValueFn != nil {
		writeSessionValueFn(ctx, SessionAuthorizationKey, auth)
	}
}

// ReadAuthorizationFromSession 从 eino session 读取 AuthorizationScope（如果可用）。
// 返回 (auth, true) 表示 session 中存在授权；返回 (zero, false) 表示无授权。
func ReadAuthorizationFromSession(ctx context.Context) (AuthorizationScope, bool) {
	if readSessionValueFn == nil {
		return AuthorizationScope{}, false
	}
	v, ok := readSessionValueFn(ctx, SessionAuthorizationKey)
	if !ok {
		return AuthorizationScope{}, false
	}
	auth, ok := v.(AuthorizationScope)
	if !ok {
		return AuthorizationScope{}, false
	}
	return auth, true
}

// ResolveAuthorization 从 ctx 中按优先级解析 AuthorizationScope：
//  1. ctx 中已存在的 AuthorizationScope（优先级最高，便于测试/注入）
//  2. session 中的 AuthorizationScope（跨 ctx 边界的 fallback）
//  3. 零值（无授权）
//
// 解析后会检查有效期，过期则返回零值（视为无授权）。
func ResolveAuthorization(ctx context.Context) AuthorizationScope {
	auth := AuthorizationFromContext(ctx)
	if !auth.IsEmpty() {
		// ctx 中已有，直接用
		if !auth.Valid(Now()) {
			return AuthorizationScope{}
		}
		return auth
	}
	if sessAuth, ok := ReadAuthorizationFromSession(ctx); ok {
		if sessAuth.Valid(Now()) {
			return sessAuth
		}
	}
	return AuthorizationScope{}
}
