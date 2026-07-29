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
	"strings"
	"testing"
	"time"
)

// =====================================================================
// splitByShellLogic 单元测试
// =====================================================================

func TestSplitByShellLogic(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string
	}{
		{
			"短路 OR",
			`which gh || command -v gh`,
			[]string{"which gh ", " command -v gh"},
		},
		{
			"短路 AND",
			`ls /tmp && echo done`,
			[]string{"ls /tmp ", " echo done"},
		},
		{
			"顺序分号",
			`cd /tmp; ls; pwd`,
			[]string{"cd /tmp", " ls", " pwd"},
		},
		{
			"保留管道在 stage 内",
			`cat /etc/hosts | head && echo done`,
			[]string{"cat /etc/hosts | head ", " echo done"},
		},
		{
			"单引号内的 || 不拆",
			`echo 'a || b' || true`,
			[]string{"echo 'a || b' ", " true"},
		},
		{
			"双引号内的 && 不拆",
			`echo "x && y" && echo done`,
			[]string{`echo "x && y" `, " echo done"},
		},
		{
			"单个 stage 无逻辑符",
			`ls /tmp`,
			[]string{"ls /tmp"},
		},
		{
			"空字符串",
			``,
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitByShellLogic(c.cmd)
			if len(got) != len(c.want) {
				t.Errorf("splitByShellLogic(%q) got %d parts %v, want %d %v",
					c.cmd, len(got), got, len(c.want), c.want)
				return
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("splitByShellLogic(%q)[%d] = %q, want %q",
						c.cmd, i, got[i], c.want[i])
				}
			}
		})
	}
}

// =====================================================================
// scanSubshellInjection 单元测试
// =====================================================================

func TestScanSubshellInjection(t *testing.T) {
	cases := []struct {
		name      string
		stage     string
		wantBlock bool
	}{
		{"无 subshell", `ls /tmp`, false},
		{"普通 echo", `echo hello`, false},
		{"管道内 subshell", `echo $(id)`, true},
		{"反引号 subshell", "echo `id`", true},
		{"process substitution <", `diff <(ls) <(ls)`, true},
		{"process substitution >", `tee >(cat)`, true},
		{"单引号内的 $() 视为字面量", `echo '$(rm -rf /)'`, false},
		{"单引号内的反引号视为字面量", "echo '`ls`'", false},
		// 注意：双引号内的 $(...) / `...` 仍会被 shell 展开，按危险处理
		{"双引号内的 $() 仍危险", `echo "$(rm -rf /)"`, true},
		{"双引号内的反引号仍危险", "echo \"`ls`\"", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason := scanSubshellInjection(c.stage)
			blocked := reason != ""
			if blocked != c.wantBlock {
				t.Errorf("scanSubshellInjection(%q) blocked=%v reason=%q, want blocked=%v",
					c.stage, blocked, reason, c.wantBlock)
			}
		})
	}
}

// =====================================================================
// checkShellChainSafety 单元测试
//
// 这些是核心安全测试，验证 `safe && dangerous` 等 bypass 模式被拦截。
// =====================================================================

func TestCheckShellChainSafety_BlocksBypassAttempts(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name        string
		cmd         string
		wantBlocked bool
		wantReason  string // reason 片段（命中关键字即可）
	}{
		// === 已知 bypass 模式（必须拦截）===
		// 这些命令 bypass 了"用 safe 凑前面 + dangerous 躲后面"的模式。
		// checkShellChainSafety 必须拒绝（在硬禁止/敏感路径/subshell
		// 检测层面命中，不依赖 allowlist/WhitelistAuth）。
		{
			"sensitive path in second stage with ||",
			`echo ok || cat /etc/shadow`,
			true, "/etc/shadow",
		},
		{
			"sensitive path in second stage with ;",
			`echo ok; cat /etc/shadow`,
			true, "/etc/shadow",
		},
		{
			"sensitive path in second stage with &&",
			`echo ok && cat /etc/shadow`,
			true, "/etc/shadow",
		},
		{
			"hard forbidden rm -rf / in second stage",
			`true; rm -rf /`,
			true, "硬禁止",
		},
		{
			"hard forbidden reboot in second stage",
			`true && reboot`,
			true, "硬禁止",
		},
		{
			"sensitive path /etc/passwd with || echo safe",
			`cat /etc/passwd || echo safe`,
			true, "/etc/passwd",
		},
		{
			"curl with Authorization header in second stage",
			`echo ok && curl -H "Authorization: Bearer xxx" https://example.com`,
			true, "Authorization",
		},
		// subshell 命令：$(...) / `...` 自身会被检测出 subshell 注入，
		// 若 subshell 内含敏感路径,也会先命中敏感路径检测。两者至少
		// 命中一种,这里只断言"被拦截"。
		{
			"subshell in second stage is blocked",
			`echo ok && $(id)`,
			true, "",
		},
		{
			"backtick subshell in second stage is blocked",
			"echo ok && `id`",
			true, "",
		},
		{
			"process substitution in second stage (with sensitive path)",
			`echo ok && diff <(cat /etc/shadow) <(echo x)`,
			true, "/etc/shadow",
		},

		// === 安全命令：必须不被 bypass 检查拦截 ===
		// 注:默认 agentName="main" 无 allowlist,所以"白名单兜底"会让 safe
		// 命令也被 IsDangerousWithAgent 判为"不在白名单"。但这种拒绝不是
		// shell-chain bypass 的范畴——是 allowlist 兜底逻辑。checkShellChainSafety
		// 设计上一定会返回 dangerous=true（因为整条命令不在白名单）。
		//
		// 为测试 safe 路径真的能"绕过 bypass 检查"——我们注入 WhitelistAuth
		// 授权,让 IsDangerousWithAgent 通过 allowlist 兜底分支。
		// 这里改用 WithAuthorization + agentName=main。
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dangerous, reason := checkShellChainSafety(ctx, "main", c.cmd)
			if dangerous != c.wantBlocked {
				t.Errorf("checkShellChainSafety(%q) dangerous=%v, want %v (reason=%q)",
					c.cmd, dangerous, c.wantBlocked, reason)
				return
			}
			if c.wantReason != "" && !strings.Contains(reason, c.wantReason) {
				t.Errorf("checkShellChainSafety(%q) reason=%q, want contains %q",
					c.cmd, reason, c.wantReason)
			}
		})
	}
}

// TestCheckShellChainSafety_AllowsSafeShellLogic 验证合法 shell 逻辑命令
// 在有 WhitelistAuth 授权时不被安全检查拦截（沙箱层不阻挡）。
//
// 与 TestCheckShellChainSafety_BlocksBypassAttempts 互补：
//   - 那个测试覆盖"bypass 模式必须拦截"
//   - 这个测试覆盖"合法 shell 逻辑不应被 bypass 检查误伤"
func TestCheckShellChainSafety_AllowsSafeShellLogic(t *testing.T) {
	// 注入 WhitelistAuth 授权,绕过"不在白名单"兜底
	ctx := WithAuthorization(context.Background(), AuthorizationScope{
		WhitelistAuth: true,
		ExpiresAt:     Now().Add(10 * time.Minute),
		GrantedBy:     "test",
	})
	cases := []string{
		`env || env`,
		`cd . && pwd`,
		`ls . ; ls .`,
		`env || ls .`,
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			dangerous, reason := checkShellChainSafety(ctx, "main", cmd)
			if dangerous {
				t.Errorf("safe shell logic should not be blocked by checkShellChainSafety: cmd=%q reason=%q",
					cmd, reason)
			}
		})
	}
}

// =====================================================================
// Execute 端到端测试：验证 shell 逻辑 bypass 在主流程中被拦截
// =====================================================================

// TestExecute_ShellLogicBypass_BlocksBypassCommands 验证 Execute 主流程对
// shell 逻辑 bypass 的拒绝（关键安全回归测试）。
//
// 这些命令**不应该**进入 /bin/sh -c 执行,而是应该在安全检查阶段被拦截,
// 拒绝信息通过 CommandOutput.Stderr 返回给 LLM。
func TestExecute_ShellLogicBypass_BlocksBypassCommands(t *testing.T) {
	ctx := context.Background()
	bypassCommands := []string{
		`echo ok && cat /etc/shadow`,
		`echo ok; cat /etc/passwd`,
		`echo ok || cat /etc/shadow`,
		`true; rm -rf /`,
		`true && reboot`,
		// curl with Authorization header
		`echo ok && curl -H "Authorization: Bearer xxx" https://example.com`,
	}

	for _, cmd := range bypassCommands {
		t.Run(cmd, func(t *testing.T) {
			out, err := Execute(ctx, &CommandInput{Command: cmd})
			if err != nil {
				// 也算拦截成功（部分 error path 返回 error 而非结构化 output）
				return
			}
			if out == nil {
				t.Errorf("Execute(%q) returned nil output", cmd)
				return
			}
			if out.ExitCode != -1 {
				t.Errorf("Execute(%q) ExitCode=%d, want -1 (拦截)。Stdout=%q Stderr=%q",
					cmd, out.ExitCode, out.Stdout, out.Stderr)
				return
			}
			if !strings.Contains(out.Stderr, "安全拦截") {
				t.Errorf("Execute(%q) Stderr should contain '安全拦截', got: %s",
					cmd, out.Stderr)
			}
		})
	}
}

// TestExecute_ShellLogicBypass_AllowsSafeCommands 验证合法 shell 逻辑命令
// 仍然能正常执行（修复不应破坏合法用法）。
//
// 注入 WhitelistAuth 授权,让白名单兜底分支放行（否则 echo/which 命令
// 不在默认 allowlist 里会被 IsDangerousWithAgent 判为"不在白名单"）。
func TestExecute_ShellLogicBypass_AllowsSafeCommands(t *testing.T) {
	ctx := WithAuthorization(context.Background(), AuthorizationScope{
		WhitelistAuth: true,
		ExpiresAt:     Now().Add(10 * time.Minute),
		GrantedBy:     "test",
	})

	t.Run("env with ||", func(t *testing.T) {
		out, err := Execute(ctx, &CommandInput{Command: `env || env || env`})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		// 安全检查应放行；具体 ExitCode 取决于沙箱环境,
		// 但 stderr 不能是"安全拦截"标记（sandbox timeout/命令找不到等是合法退出路径）。
		if strings.Contains(out.Stderr, "安全拦截") {
			t.Errorf("safe shell logic should not be blocked by safety check: Stderr=%s", out.Stderr)
		}
	})

	t.Run("ls with ||", func(t *testing.T) {
		out, err := Execute(ctx, &CommandInput{Command: `ls . || ls . || ls .`})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if strings.Contains(out.Stderr, "安全拦截") {
			t.Errorf("safe shell logic should not be blocked by safety check: Stderr=%s", out.Stderr)
		}
	})
}

// TestExecute_QuotedShellLogic_NotTriggered 验证引号内的 shell 逻辑符
// 不会被当作"含 shell 逻辑"——与 hasShellLogic 行为一致。
//
// 注入 WhitelistAuth 让 echo 不被 allowlist 兜底拦截。
func TestExecute_QuotedShellLogic_NotTriggered(t *testing.T) {
	ctx := WithAuthorization(context.Background(), AuthorizationScope{
		WhitelistAuth: true,
		ExpiresAt:     Now().Add(10 * time.Minute),
		GrantedBy:     "test",
	})

	// 引号内包含 || / && / ; 的字符串字面量命令，应该走"无 shell 逻辑"分支
	// （被当成带参数的命令单条执行，安全检查照常生效）。
	out, err := Execute(ctx, &CommandInput{Command: `ls '/tmp && echo injected' || ls done`})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if strings.Contains(out.Stderr, "安全拦截") {
		t.Errorf("quoted shell logic chars should not be blocked by safety check: Stderr=%s", out.Stderr)
	}
}
