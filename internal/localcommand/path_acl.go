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
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// pathACLLock 保护 globalPathACL 的读写。
var pathACLLock sync.RWMutex

// globalPathACL 是全局路径 ACL，AST walker 与 Redirect 检查都从这里读。
// 由 main.go 启动时从 application.yml 加载（SetGlobalPathACL）。
// nil = 未配置（astSafetyCheck 此时跳过 Redirect 路径检查）。
var globalPathACL *PathACL

// PathACL 是 read/write 分开的路径访问控制。
//
// 来源：参考 OpenAI Codex Windows 沙箱的"deny 路径 ACL"模式。
// 把原来 SensitivePaths 的字符串模糊匹配升级为显式前缀匹配 + read/write 分开。
type PathACL struct {
	// ReadDenied 禁止读取的路径前缀（如 /etc/shadow, /etc/sudoers, /root/.ssh）
	ReadDenied []string `json:"read_denied"`
	// WriteDenied 禁止写入的路径前缀（如 /etc, /usr, /boot, /proc）
	WriteDenied []string `json:"write_denied"`
	// ExecDenied 禁止执行的命令名（denybin PATH 注入使用）
	ExecDenied []string `json:"exec_denied"`
}

// SetGlobalPathACL 设置全局路径 ACL。
func SetGlobalPathACL(acl *PathACL) {
	pathACLLock.Lock()
	defer pathACLLock.Unlock()
	globalPathACL = acl
}

// loadPathACL 读取全局路径 ACL 的副本。
func loadPathACL() *PathACL {
	pathACLLock.RLock()
	defer pathACLLock.RUnlock()
	if globalPathACL == nil {
		return nil
	}
	// 返回深拷贝, 避免 walker 修改共享 ACL
	cp := &PathACL{
		ReadDenied:  append([]string(nil), globalPathACL.ReadDenied...),
		WriteDenied: append([]string(nil), globalPathACL.WriteDenied...),
		ExecDenied:  append([]string(nil), globalPathACL.ExecDenied...),
	}
	return cp
}

// checkReadDenied 检查目标路径是否被禁止读取。
// 命中时返回拒绝原因；未命中返回空字符串。
func (a *PathACL) checkReadDenied(target string) string {
	if a == nil || target == "" {
		return ""
	}
	if reason := matchDenied(target, a.ReadDenied); reason != "" {
		return fmt.Sprintf("禁止读取路径: %s", reason)
	}
	return ""
}

// checkWriteDenied 检查目标路径是否被禁止写入。
func (a *PathACL) checkWriteDenied(target string) string {
	if a == nil || target == "" {
		return ""
	}
	if reason := matchDenied(target, a.WriteDenied); reason != "" {
		return fmt.Sprintf("禁止写入路径: %s", reason)
	}
	return ""
}

// IsExecDenied 检查命令名是否在 deny 列表中。
func (a *PathACL) IsExecDenied(cmdName string) bool {
	if a == nil {
		return false
	}
	cmdLower := strings.ToLower(strings.TrimSpace(cmdName))
	for _, denied := range a.ExecDenied {
		if strings.ToLower(strings.TrimSpace(denied)) == cmdLower {
			return true
		}
	}
	return false
}

// matchDenied 把 target 与 denied 列表中的前缀做匹配。
// 返回首个匹配的 deny 规则（含其原始字符串）。
//
// 匹配规则（任一命中即视为命中）：
//  1. 完全相等：/etc/shadow == /etc/shadow
//  2. 前缀匹配：/etc/shadow 在 /etc 下 → /etc/shadow 命中 /etc 规则
//  3. 子串匹配：/etc/shadow.bak 包含 /etc/shadow 字符串 → 命中 /etc/shadow 规则
func matchDenied(target string, denied []string) string {
	cleanTarget := filepath.Clean(target)
	for _, pattern := range denied {
		cleanPattern := filepath.Clean(pattern)
		// 完全相等
		if cleanTarget == cleanPattern {
			return pattern
		}
		// 前缀匹配：target 在 denied 目录下
		// /etc 命中 /etc/shadow、/etc/passwd
		if strings.HasPrefix(cleanTarget, cleanPattern+string(filepath.Separator)) {
			return pattern
		}
		// 子串匹配：用于精确路径前缀防护
		// /etc/shadow 命中 /etc/shadow.bak 这种"同名前缀"的输入
		if cleanTarget != cleanPattern && strings.HasPrefix(cleanTarget, cleanPattern+".") {
			return pattern
		}
	}
	return ""
}
