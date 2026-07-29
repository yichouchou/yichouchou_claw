/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package localcommand

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// evaluationStage 标识 deny → ask → allow 三段评估的当前阶段。
//
// 来源：Claude Code Bash 工具的安全链。
//   - deny:   永远拒绝,不依赖授权
//   - ask:    需要授权 / 用户确认
//   - allow:  自动放行
type evaluationStage string

const (
	stageDeny   evaluationStage = "deny"   // 永拒
	stageAsk    evaluationStage = "ask"    // 需要授权
	stageAllow  evaluationStage = "allow"  // 自动放行
	stageDenied evaluationStage = "denied" // ask 检查后仍然拒绝
)

// EvaluationResult 是单次评估的详细结果。
type EvaluationResult struct {
	Stage  evaluationStage // 命中的评估阶段
	Reason string          // 拒绝/放行的具体原因
}

// IsDangerousWithAgentV2 是 deny → ask → allow 三段评估的入口。
//
// 与原 IsDangerousWithAgent 的差异：
//  1. 把现有逻辑显式拆为三个评估阶段(更清晰的拒绝链路)
//  2. 把 PathACL (read/write 分开) 接入 deny 阶段
//  3. 把 denybin 命令名(exec_denied) 接入 deny 阶段(命令名层面的硬禁止)
//  4. 保留现有 IsDangerousWithAgent 的所有行为作为 deny/ask/allow 子步骤
//
// 评估流程：
//
//	Stage 1 (deny, 永拒):
//	  - 硬禁止正则 (HardForbiddenPatterns)
//	  - 敏感路径模糊匹配 (SensitivePaths)
//	  - per-agent denylist 路径规则
//	  - 自写脚本自执行检测
//	  - PathACL: 命令参数中的敏感路径(只读取全局 PathACL)
//
//	Stage 2 (ask, 依赖授权):
//	  - 软禁止正则 (SoftForbiddenPatterns)  ← 需要 Bash/Install 授权
//	  - per-agent denylist 命令名(覆盖 allowlist 但 WhitelistAuth 不覆盖)
//
//	Stage 3 (allow, 自动放行):
//	  - per-agent allowlist 命中
//
//	Default (ask, 不在白名单 → 需要 WhitelistAuth)
//
// 返回值 (dangerous, reason):
//
//	dangerous=true → 拒绝;  reason 包含命中的 stage + 详细原因
//	dangerous=false → 放行
func IsDangerousWithAgentV2(ctx context.Context, agentName, cmd string) (bool, string) {
	// 解析 agentName (兼容 ctx 注入与默认 "main")
	if agentName == "" {
		agentName = AgentNameFromContext(ctx)
	}
	if agentName == "" {
		agentName = "main"
	}

	lowerCmd := strings.ToLower(cmd)
	auth := ResolveAuthorization(ctx)

	// =================================================================
	// Stage 1: deny (永远拒绝,不依赖任何授权)
	// =================================================================

	// 1.1 硬禁止正则
	if dangerous, reason := IsHardForbidden(cmd); dangerous {
		return true, fmt.Sprintf("[%s] %s", stageDeny, reason)
	}

	// 1.2 敏感路径模糊匹配 (代码内嵌兜底)
	for _, path := range SensitivePaths {
		if strings.Contains(lowerCmd, strings.ToLower(path)) {
			return true, fmt.Sprintf("[%s] 禁止访问敏感路径: %s", stageDeny, path)
		}
	}

	// 1.3 per-agent denylist 路径规则 (配置层加固)
	if path, hit := isPathDenied(agentName, lowerCmd); hit {
		return true, fmt.Sprintf("[%s] denylist 路径规则拒绝(任何授权都不能放行): %s", stageDeny, path)
	}

	// 1.4 自写脚本自执行检测
	if argv := strings.Fields(cmd); len(argv) > 0 {
		if reason := checkTmpScriptExecution(argv); reason != "" {
			return true, fmt.Sprintf("[%s] %s", stageDeny, reason)
		}
	}

	// 1.5 PathACL: 命令参数中的 read_denied 路径 (AST 层会更严格,这里是字符串兜底)
	if acl := loadPathACL(); acl != nil {
		for _, deniedPath := range acl.ReadDenied {
			if strings.Contains(lowerCmd, strings.ToLower(deniedPath)) {
				return true, fmt.Sprintf("[%s] PathACL 禁止读取: %s", stageDeny, deniedPath)
			}
		}
	}

	// 1.6 PathACL: denybin exec_denied 命令名 (命令本身就在 deny 列表)
	if acl := loadPathACL(); acl != nil {
		argv := strings.Fields(cmd)
		if len(argv) > 0 {
			cmdName := filepathBase(argv[0])
			if acl.IsExecDenied(cmdName) {
				return true, fmt.Sprintf("[%s] denybin 拒绝执行命令: %s", stageDeny, cmdName)
			}
		}
	}

	// =================================================================
	// Stage 2: ask (依赖授权才能放行)
	// =================================================================

	// 2.1 软禁止正则 (需要 Bash/Install 授权)
	for _, pattern := range SoftForbiddenPatterns {
		if !pattern.MatchString(cmd) {
			continue
		}
		if auth.Bash {
			continue
		}
		if auth.Install && isInstallClassPattern(pattern) {
			continue
		}
		return true, fmt.Sprintf(
			"[%s] 软禁止模式 %s 需要用户授权(需要 Bash 授权或 Install 授权)",
			stageAsk, pattern.String())
	}

	// 2.2 per-agent denylist 命令名 (WhitelistAuth 不覆盖)
	if isCommandDenied(agentName, cmd) {
		cmdName := strings.Fields(cmd)[0]
		return true, fmt.Sprintf("[%s] denylist 拒绝(任何授权都不能放行): %s", stageAsk, cmdName)
	}

	// =================================================================
	// Stage 3: allow (自动放行)
	// =================================================================

	if isCommandAllowed(agentName, cmd) && !strings.Contains(cmd, "/") {
		// 命令在白名单 → 放行
		return false, ""
	}

	// =================================================================
	// Default: ask (不在白名单 → 需要 WhitelistAuth)
	// =================================================================

	cmdName := strings.Fields(cmd)[0]
	if auth.WhitelistAuth && (auth.WhitelistCmd == "" || auth.WhitelistCmd == cmdName) {
		return false, ""
	}
	cmdSnippet := cmd
	if len(cmdSnippet) > 120 {
		cmdSnippet = cmdSnippet[:120] + "..."
	}
	return true, fmt.Sprintf("[default-ask] 命令不在白名单中: %s (整条命令: %q)", cmdName, cmdSnippet)
}

// filepathBase 提取路径中的文件名部分(跨平台 / 和 \)。
func filepathBase(p string) string {
	return filepath.Base(p)
}

// 防止 import unused 警告
var _ = regexp.MustCompile
