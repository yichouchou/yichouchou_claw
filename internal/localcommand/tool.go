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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// 注意：AllowedCommands / PlatformName 已按平台分离到以下文件：
//   - allowed_unix.go    （linux/darwin）
//   - allowed_windows.go （windows）
// 这里不再重复定义；运行时 PlatformName 用于在 stderr / LLM 提示中明确告知当前平台。
//
// 错误码约定（写入 CommandOutput.ExitCode），便于上层 / LLM 区分错误类型：
//   -1  安全拦截 / 白名单 / 平台不支持 / 参数错误 / 启动失败
//    0  正常退出码 0
//   >0  命令自身返回的非零退出码

// ErrPlatformNotSupported 在 Windows 上调用 Linux-only 命令时返回。
var ErrPlatformNotSupported = errors.New("command not supported on current platform")

// ErrNotInWhitelist 命令不在白名单时返回。
var ErrNotInWhitelist = errors.New("command not in whitelist")

// ErrHardForbidden 命令命中硬禁止模式，永远不会被授权放行。
var ErrHardForbidden = errors.New("command hard-forbidden by sandbox")

// ErrAuthMissing 命令命中软禁止模式，但当前没有授权。
var ErrAuthMissing = errors.New("command requires user authorization")

// HardForbiddenPatterns 硬禁止模式：无论用户是否授权都**永远拒绝执行**。
//
// 这些模式代表"不可逆 / 高危 / 越权"操作：
//   - 系统破坏：rm -rf /、dd of=/...、mkfs/fdisk/parted
//   - 进程不可中断：reboot/halt/shutdown/init 0/6
//   - 反弹 shell 与远控：nmap --、hping、netcat、nc -e、/dev/tcp、/dev/udp
//   - 敏感路径读写：/etc/passwd、/etc/shadow、chmod 4755、chown root:root、> /dev/sd*
//   - shell 元编程：eval / exec / fork（LLM 利用这些做语义劫持）
var HardForbiddenPatterns = []*regexp.Regexp{
	// ===== 系统破坏 =====
	regexp.MustCompile(`rm\s+-rf\s+/`),
	regexp.MustCompile(`rm\s+-rf\s+\*`),
	regexp.MustCompile(`rm\s+-rf\s+\.`),
	regexp.MustCompile(`rm\s+-rf\s+/[a-zA-Z]+`),
	regexp.MustCompile(`dd\s+.*of=/`),
	regexp.MustCompile(`dd\s+.*of=\.`),
	regexp.MustCompile(`mkfs`),
	regexp.MustCompile(`fdisk`),
	regexp.MustCompile(`parted`),
	regexp.MustCompile(`partprobe`),

	// ===== 进程不可中断（关机/重启） =====
	regexp.MustCompile(`reboot`),
	regexp.MustCompile(`halt`),
	regexp.MustCompile(`shutdown`),
	regexp.MustCompile(`init\s+0`),
	regexp.MustCompile(`init\s+6`),

	// ===== 反弹 shell / 远控 =====
	regexp.MustCompile(`nmap\s+--`),
	regexp.MustCompile(`hping`),
	regexp.MustCompile(`netcat`),
	regexp.MustCompile(`nc\s+-e`),
	regexp.MustCompile(`/dev/tcp`),
	regexp.MustCompile(`/dev/udp`),

	// ===== 敏感路径 =====
	regexp.MustCompile(`/etc/passwd`),
	regexp.MustCompile(`/etc/shadow`),
	regexp.MustCompile(`chmod\s+777\s+/etc`),
	regexp.MustCompile(`chmod\s+777\s+/root`),
	regexp.MustCompile(`chmod\s+4755`),
	regexp.MustCompile(`chown\s+root:root`),
	regexp.MustCompile(`>\s*/dev/sd`),
	regexp.MustCompile(`tee\s+/dev/`),

	// ===== shell 元编程 =====
	regexp.MustCompile(`eval\s+`),
	regexp.MustCompile(`exec\s+`),
	regexp.MustCompile(`fork\s+`),
}

// SoftForbiddenPatterns 软禁止模式：在没有授权时被拦截，用户授权后可放行。
//
// 按授权类型分组：
//   - "Install 类"匹配 Install 授权
//   - "Bash 类"匹配 Bash 授权
//
// 注释里标 [Install]/[Bash] 表示需要哪类授权才能放行。
var SoftForbiddenPatterns = []*regexp.Regexp{
	// ===== 远程脚本执行（curl/wget | sh）：[Bash] =====
	// 即使用户授权"装东西"，也不允许把远程脚本直接 pipe 给 shell 解释器；
	// 必须先下载到 /tmp 再单独执行，且只能用 Bash 授权。
	regexp.MustCompile(`curl\s+.*\|\s*sh`),
	regexp.MustCompile(`wget\s+.*\|\s*sh`),
	regexp.MustCompile(`curl\s+-s\s+http://.*\.sh`),
	regexp.MustCompile(`wget\s+-O\s+-.*\.sh`),

	// ===== curl 落盘（任意文件）：[Bash] =====
	regexp.MustCompile(`curl\s+.*-\s*o\s+\S`),
	regexp.MustCompile(`curl\s+.*--output\s`),
	regexp.MustCompile(`curl\s+.*--remote-name(\s|\=|$)`),
	regexp.MustCompile(`curl\s+.*--output-dir\s`),
	regexp.MustCompile(`curl\s+.*>\s*\S`),
	regexp.MustCompile(`curl\s+.*\|\s*tee\s`),
	regexp.MustCompile(`curl\s+.*\|\s*dd\s`),
	regexp.MustCompile(`curl\s+.*&&\s*\S+\s*>.*\S`),

	// ===== curl 敏感信息夹带：[Bash] =====
	// 即使有 Install 授权也不能在 header/body 里塞凭据（高敏感）
	regexp.MustCompile(`curl\s+.*://[^\s/]*:[^\s/@]+@`),
	regexp.MustCompile(`curl\s+.*-H\s*['"]?\s*(Authorization|Cookie|Proxy-Authorization|X-Api-Key|X-Auth-Token)\b`),
	regexp.MustCompile(`curl\s+.*[?&](token|api[_-]?key|password|secret|access[_-]?token|auth|sid)=`),
	regexp.MustCompile(`curl\s+.*-(d|F|T)\s+['"]?[^'"\s]*\b(password|passwd|secret|token|api[_-]?key|private[_-]?key)\b`),

	// ===== curl 内网探测：[Bash] =====
	regexp.MustCompile(`curl\s+.*https?://127\.`),
	regexp.MustCompile(`curl\s+.*https?://10\.\d{1,3}\.\d{1,3}\.\d{1,3}`),
	regexp.MustCompile(`curl\s+.*https?://192\.168\.\d{1,3}\.\d{1,3}`),
	regexp.MustCompile(`curl\s+.*https?://169\.254\.`),
	regexp.MustCompile(`curl\s+.*https?://172\.(1[6-9]|2[0-9]|3[01])\.\d{1,3}\.\d{1,3}`),
	regexp.MustCompile(`curl\s+.*https?://0\.0\.0\.0`),
	regexp.MustCompile(`curl\s+.*https?://localhost\b`),

	// ===== wget 写入系统目录：[Bash] =====
	regexp.MustCompile(`wget\s+.*-O\s+/`),
	regexp.MustCompile(`wget\s+.*--output-document=/`),
	regexp.MustCompile(`wget\s+.*-P\s+/`),
	regexp.MustCompile(`wget\s+.*--directory-prefix=/`),

	// ===== 包管理器的 install/upgrade/remove：[Install] =====
	// 这是"安装"授权的主要使用场景。
	regexp.MustCompile(`(apt|apt-get|aptitude)\s+(install|remove|purge|upgrade|full-upgrade|dist-upgrade|autoremove)\b`),
	regexp.MustCompile(`(yum|dnf)\s+(install|remove|erase|upgrade|update|downgrade|autoremove)\b`),
	regexp.MustCompile(`pacman\s+-S`),
	regexp.MustCompile(`zypper\s+(install|remove|in|rm|up|update|patch)\b`),
	// emerge：Gentoo 包管理器。Go regexp（RE2）不支持 (?!...) 前瞻，
	// 且 Go 的 \b 在 space ↔ '-' 之间不触发（'-' 是 non-word），
	// 所以这里用显式"空格后跟 -"代替 \b。
	// 1) 带破坏性深度变更标志：--fetchall / --newuse / --newbin / --deep / --with-bdeps / --autounmask* / --backtrack=
	regexp.MustCompile(`\bemerge\s+(?:-{1,2}[a-zA-Z0-9-]+=?\s+)*(?:--fetchall|--newuse|--newbin|--deep|--with-bdeps|--autounmask|--autounmask-write|--backtrack=)\b`),
	// 2) 卸载相关：--unmerge / --prune / --depclean / --clean
	regexp.MustCompile(`\bemerge\s+(?:-{1,2}[a-zA-Z0-9-]+=?\s+)*(?:--unmerge|--prune|--depclean|--clean)\b`),
	// 3) 显式 install / 一次性安装 / noreplace / 重建：--noreplace / --with- / --oneshot / --emptytree
	regexp.MustCompile(`\bemerge\s+(?:-{1,2}[a-zA-Z0-9-]+=?\s+)*(?:--noreplace|--with-|--oneshot|--emptytree)\b`),
	// 4) -C / -c / -1 等短选项触发
	regexp.MustCompile(`\bemerge\s+-[a-zA-Z]*[1cC]`),
	// 5) "emerge <包名>" 安装形式（emerge 后面跟着非选项的标识符/原子名/包名）
	//    pkg 名不是 - 开头（避免命中 -pv 等查询选项）
	regexp.MustCompile(`\bemerge\s+([^-/][a-zA-Z0-9+_.@-]*)(?:\s|$)`),
	// 6) "emerge <category>/<pkg>" 完整原子名（Gentoo 分类形式）
	regexp.MustCompile(`\bemerge\s+([a-zA-Z0-9+_.-]+/[a-zA-Z0-9+_.-]+)(?:\s|$)`),
	regexp.MustCompile(`nix-env\s+-i`),
	regexp.MustCompile(`brew\s+(install|uninstall|upgrade|reinstall|link|untap|tap)\b`),
	regexp.MustCompile(`rpm\s+-i`),
	regexp.MustCompile(`rpm\s+--install`),
	regexp.MustCompile(`rpm\s+-U`),
	regexp.MustCompile(`rpm\s+--upgrade`),
	regexp.MustCompile(`rpm\s+-e`),
	regexp.MustCompile(`rpm\s+--erase`),
	regexp.MustCompile(`dpkg\s+-i`),
	regexp.MustCompile(`dpkg\s+--install`),
	regexp.MustCompile(`dpkg\s+-r`),
	regexp.MustCompile(`dpkg\s+--remove`),
	regexp.MustCompile(`dpkg\s+-P`),
	regexp.MustCompile(`dpkg\s+--purge`),

	// ===== 语言包管理器 install/upgrade：[Install] =====
	regexp.MustCompile(`pip(\d+)?\s+(install|uninstall|download)\b`),
	regexp.MustCompile(`pipx\s+(install|inject)\b`),
	regexp.MustCompile(`uv\s+(add|remove|install|sync|pip\s+install)\b`),
	regexp.MustCompile(`poetry\s+(add|install|remove|update|init|new)\b`),
	regexp.MustCompile(`pdm\s+(add|install|remove|update|init|use)\b`),
	regexp.MustCompile(`conda\s+(install|create|remove|update|env\s+create)\b`),
	regexp.MustCompile(`npm\s+(install|i|add|remove|rm|update|upgrade|run|exec|publish)\b`),
	regexp.MustCompile(`pnpm\s+(add|install|i|remove|rm|update|up|run|exec)\b`),
	regexp.MustCompile(`yarn\s+(add|install|remove|upgrade|run|global)\b`),
	regexp.MustCompile(`bun\s+(add|install|remove|rm|update|run)\b`),
	regexp.MustCompile(`deno\s+(install|add|remove|rm|uninstall)\b`),
	regexp.MustCompile(`go\s+(install|get)\b`),
	regexp.MustCompile(`cargo\s+(install|add|new|init|update|remove)\b`),
	regexp.MustCompile(`gem\s+(install|i|uninstall|update)\b`),
	regexp.MustCompile(`bundle\s+(install|update|add)\b`),
	regexp.MustCompile(`composer\s+(install|update|require|remove|global)\b`),
	regexp.MustCompile(`nuget\s+(install|update|add|remove)\b`),
	regexp.MustCompile(`dotnet\s+(tool\s+(install|update|uninstall)|add|remove|new)\b`),

	// ===== 任意 chmod / chown / chgrp：[Bash] =====
	// 即使是合法 chmod +x 也属于"修改文件权限"操作，需要 Bash 授权。
	regexp.MustCompile(`\bchmod\b`),
	regexp.MustCompile(`\bchown\b`),
	regexp.MustCompile(`\bchgrp\b`),
	regexp.MustCompile(`\bsetcap\b`),
	regexp.MustCompile(`\bsetfattr\b`),

	// ===== 任意 rm（非 -rf /... 已被硬禁止覆盖）：[Bash] =====
	// 比如 rm a.txt、rm -f file 都属于可授权范围；rm -rf / 等已经被硬禁止拦截
	regexp.MustCompile(`\brm\s+`),
}

// DangerousPatterns 兼容旧接口：= HardForbiddenPatterns ∪ SoftForbiddenPatterns。
//
// 保留 DangerousPatterns 名称，避免破坏可能存在的外部引用。
func DangerousPatterns() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(HardForbiddenPatterns)+len(SoftForbiddenPatterns))
	out = append(out, HardForbiddenPatterns...)
	out = append(out, SoftForbiddenPatterns...)
	return out
}

// SensitivePaths 敏感路径（即便授权后也仍然检查；用于二次兜底）。
var SensitivePaths = []string{
	"/root", "/etc/ssh", "/etc/pki", "/var/log/secure", "/var/log/auth",
	"/proc/1", "/proc/sys", "/sys/kernel", "/boot", "/dev/mapper",
	"/etc/shadow", ".ssh", ".bash_history", ".zsh_history",
}

// IsHardForbidden 检查命令是否命中硬禁止模式（永远拒绝）。
func IsHardForbidden(cmd string) (bool, string) {
	for _, pattern := range HardForbiddenPatterns {
		if pattern.MatchString(cmd) {
			return true, fmt.Sprintf("硬禁止模式: %s", pattern.String())
		}
	}
	return false, ""
}

// IsDangerous 检查命令是否危险。
//
// 兼容旧签名（无 ctx）：等价于 IsDangerousWithContext(context.Background(), cmd)。
func IsDangerous(cmd string) (bool, string) {
	return IsDangerousWithContext(context.Background(), cmd)
}

// IsDangerousWithContext 在知道授权上下文的情况下判断命令是否危险。
//
// 判定顺序：
//  1. 命中 HardForbiddenPatterns → 硬禁止（永远拒绝）
//  2. 命中 SensitivePaths → 拒绝（兜底；与 AuthorizationScope 无关）
//  3. 命中 SoftForbiddenPatterns → 检查 ctx 中的 AuthorizationScope
//     - Bash=true → 放行
//     - Install=true 且是 install 类规则 → 放行
//     - 否则 → 拒绝（ErrAuthMissing）
//  4. 未命中白名单 → 检查 ctx 中的 AuthorizationScope.WhitelistAuth
//     - WhitelistAuth=true 且 (WhitelistCmd 为空或等于命令名) → 放行
//     - 否则 → 拒绝（ErrNotInWhitelist）
//  5. 未命中 → 放行
func IsDangerousWithContext(ctx context.Context, cmd string) (bool, string) {
	// 1) 硬禁止：永远拒绝
	if dangerous, reason := IsHardForbidden(cmd); dangerous {
		return true, reason
	}

	// 2) 敏感路径兜底
	lowerCmd := strings.ToLower(cmd)
	for _, path := range SensitivePaths {
		if strings.Contains(lowerCmd, strings.ToLower(path)) {
			return true, fmt.Sprintf("禁止访问敏感路径: %s", path)
		}
	}

	// 3) 软禁止：依赖授权
	auth := AuthorizationFromContext(ctx)
	if !auth.Valid(Now()) {
		auth = AuthorizationScope{} // 过期视为无授权
	}

	for _, pattern := range SoftForbiddenPatterns {
		if !pattern.MatchString(cmd) {
			continue
		}
		// 检查授权是否覆盖
		if auth.Bash {
			// Bash 授权覆盖所有软禁止
			continue
		}
		// Install 授权仅覆盖"包管理器 install 类"规则；
		// 其他规则（curl 落盘、wget 写系统、chmod 等）需要 Bash 授权
		if auth.Install && isInstallClassPattern(pattern) {
			continue
		}
		return true, fmt.Sprintf(
			"软禁止模式 %s 需要用户授权（需要 Bash 授权或 Install 授权）",
			pattern.String())
	}

	// 4) 白名单检查：命令不在白名单时，需要 WhitelistAuth 授权才能放行
	if !isCommandAllowed(cmd) && !strings.Contains(cmd, "/") {
		cmdName := strings.Fields(cmd)[0]
		// WhitelistAuth=true 且（未指定具体命令 OR 指定的就是这条命令）→ 放行
		if auth.WhitelistAuth && (auth.WhitelistCmd == "" || auth.WhitelistCmd == cmdName) {
			return false, ""
		}
		return true, fmt.Sprintf("命令不在白名单中: %s", cmdName)
	}

	return false, ""
}

// isInstallClassPattern 判断某个软禁止正则是否属于"安装类"。
// 这些规则在有了 Install 授权后即可放行。
func isInstallClassPattern(p *regexp.Regexp) bool {
	src := p.String()
	installKeywords := []string{
		`install`, `uninstall`, `purge`, `upgrade`, `update`, `remove`,
		`-S`, `-i`, `-U`, `-e`, `-r`, `-P`, `-I`, `inject`, `add`,
		`require`, `global`, `sync`, `downgrade`, `autoremove`,
		`dist-upgrade`, `full-upgrade`, `tap`, `untap`, `link`, `reinstall`,
		`create`, `use`, `init`, `new`, `get`, `tool install`, `tool update`,
		`tool uninstall`,
	}
	for _, kw := range installKeywords {
		if strings.Contains(src, kw) {
			return true
		}
	}
	return false
}

// CommandInput 命令输入
type CommandInput struct {
	Command string `json:"command"`
}

// CommandOutput 命令输出
type CommandOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Duration string `json:"duration"`
}

// isCommandAllowed 检查命令是否在白名单
func isCommandAllowed(cmd string) bool {
	cmdName := strings.Fields(cmd)[0]
	if strings.Contains(cmd, "/") {
		baseName := filepath.Base(cmdName)
		_, ok := AllowedCommands[baseName]
		return ok
	}
	_, ok := AllowedCommands[cmdName]
	return ok
}

// splitByOperators 按 | 分割命令（不支持 && ||，只支持管道）
func splitByOperators(cmd string) []string {
	var result []string
	var current bytes.Buffer
	var i int

	for i < len(cmd) {
		if i+1 < len(cmd) && cmd[i] == '|' && cmd[i+1] != '&' {
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
			i += 2
			continue
		}
		current.WriteByte(cmd[i])
		i++
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result
}

// CommandStage 命令阶段
type CommandStage struct {
	Cmd  string
	Argv []string
}

// ExecuteChain 执行命令链
func ExecuteChain(ctx context.Context, stages []CommandStage) (*CommandOutput, error) {
	if len(stages) == 0 {
		return nil, fmt.Errorf("空命令")
	}
	if len(stages) == 1 {
		return executeSingle(ctx, stages[0].Cmd, stages[0].Argv)
	}
	return executeWithPipes(ctx, stages)
}

// executeSingle 执行单个命令
func executeSingle(ctx context.Context, cmd string, argv []string) (*CommandOutput, error) {
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var execCmd *exec.Cmd
	if len(argv) == 1 {
		execCmd = exec.CommandContext(execCtx, argv[0])
	} else {
		execCmd = exec.CommandContext(execCtx, argv[0], argv[1:]...)
	}

	// 平台特定的环境变量与工作目录，由平台分支文件在 build 时提供
	execCmd.Env = sandboxEnv()
	execCmd.Dir = sandboxWorkDir()
	execCmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	execCmd.SysProcAttr = platformSysProcAttr()

	start := time.Now()
	err := execCmd.Run()
	duration := time.Since(start)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return &CommandOutput{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Duration: duration.String(),
	}, nil
}

// executeWithPipes 使用管道连接多个命令
func executeWithPipes(ctx context.Context, stages []CommandStage) (*CommandOutput, error) {
	execCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	numStages := len(stages)
	processes := make([]*exec.Cmd, numStages)
	pipes := make([]*io.PipeReader, numStages-1)
	pipeWriters := make([]*io.PipeWriter, numStages-1)

	// 创建管道
	for i := 0; i < numStages-1; i++ {
		r, w := io.Pipe()
		pipes[i] = r
		pipeWriters[i] = w
	}

	env := sandboxEnv()
	workDir := sandboxWorkDir()

	// 最后一个命令的输出缓冲区
	finalStdout := &bytes.Buffer{}
	finalStderr := &bytes.Buffer{}

	// 启动所有命令
	for i, stage := range stages {
		cmd := exec.CommandContext(execCtx, stage.Argv[0], stage.Argv[1:]...)
		cmd.Env = env
		cmd.Dir = workDir
		cmd.SysProcAttr = platformSysProcAttr()

		if i == 0 {
			cmd.Stdin = nil
		} else {
			cmd.Stdin = pipes[i-1]
		}

		if i == numStages-1 {
			cmd.Stdout = finalStdout
			cmd.Stderr = finalStderr
		} else {
			cmd.Stdout = pipeWriters[i]
			cmd.Stderr = &bytes.Buffer{}
		}

		if err := cmd.Start(); err != nil {
			// 清理
			for j := 0; j < i; j++ {
				if pipeWriters[j] != nil {
					pipeWriters[j].Close()
				}
			}
			cleanupProcessGroup(processes[:i])
			return nil, fmt.Errorf("启动命令失败: %s: %w", stage.Argv[0], err)
		}
		processes[i] = cmd

		// 关闭写端（在 goroutine 中延迟关闭，确保数据被读取后再关闭）
		if i < numStages-1 {
			go func(w *io.PipeWriter, c *exec.Cmd) {
				c.Wait()
				w.Close()
			}(pipeWriters[i], cmd)
		}
	}

	// 等待最后一个命令完成
	err := processes[numStages-1].Wait()
	cleanupProcessGroup(processes)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return &CommandOutput{
		Stdout:   finalStdout.String(),
		Stderr:   finalStderr.String(),
		ExitCode: exitCode,
		Duration: "pipe_chain",
	}, nil
}

// cleanupProcessGroup 清理进程组
func cleanupProcessGroup(cmds []*exec.Cmd) {
	for _, cmd := range cmds {
		killProcessGroup(cmd)
	}
}

// Execute 执行命令
//
// 从 ctx 中读取 AuthorizationScope；硬禁止永远拦截，
// 软禁止根据授权决定是否放行。
func Execute(ctx context.Context, input *CommandInput) (*CommandOutput, error) {
	cmd := input.Command
	if cmd == "" {
		return nil, fmt.Errorf("命令不能为空")
	}

	auth := AuthorizationFromContext(ctx)
	authDesc := "none"
	if !auth.IsEmpty() {
		authDesc = fmt.Sprintf("Install=%v,Bash=%v,By=%s", auth.Install, auth.Bash, auth.GrantedBy)
	}
	log.Printf("[LocalCommand][%s][auth=%s] Executing: %s", PlatformName, authDesc, cmd)

	// 解析命令链
	parts := splitByOperators(cmd)
	if len(parts) == 0 {
		return nil, fmt.Errorf("无效命令")
	}

	var stages []CommandStage
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		argv := strings.Fields(part)
		if len(argv) == 0 {
			continue
		}

		// 安全检查（带授权感知）
		// 白名单未命中会被 IsDangerousWithContext 当作软禁止处理，
		// 由 AuthorizationScope.WhitelistAuth 授权后即可放行。
		if dangerous, reason := IsDangerousWithContext(ctx, part); dangerous {
			hint := authorizationHint(reason, AuthorizationFromContext(ctx), part)
			return &CommandOutput{
				Stdout:   "",
				Stderr:   fmt.Sprintf("安全拦截: %s\n%s", reason, hint),
				ExitCode: -1,
				Duration: "0s",
			}, nil
		}

		stages = append(stages, CommandStage{Cmd: part, Argv: argv})
	}

	if len(stages) == 0 {
		return nil, fmt.Errorf("没有有效的命令阶段")
	}

	out, err := ExecuteChain(ctx, stages)
	if err != nil {
		return out, err
	}

	// 平台提示：如果命令找不到（Windows 上常见的 Linux 工具），附加解释。
	if out != nil && out.ExitCode != 0 && looksLikeMissingExecutable(out.Stderr) && len(stages) > 0 && len(stages[0].Argv) > 0 {
		hint := platformHintFor(stages[0].Argv[0])
		if hint != "" {
			out.Stderr += "\n\n[平台提示]\n" + hint
		}
	}
	return out, nil
}

// authorizationHint 根据当前授权情况，给出"如何让该命令通过"的用户提示。
func authorizationHint(reason string, auth AuthorizationScope, cmd string) string {
	// 硬禁止不给提示（无论如何都无法放行）
	if strings.HasPrefix(reason, "硬禁止") {
		return fmt.Sprintf("[授权提示] 该命令命中硬禁止规则，任何授权都不能放行。请改用其他方式完成任务。")
	}
	// 敏感路径直接拒绝
	if strings.HasPrefix(reason, "禁止访问敏感路径") {
		return fmt.Sprintf("[授权提示] 该命令访问了敏感路径，即便授权也不会放行。")
	}
	// 白名单未命中：建议 WhitelistAuth 授权
	if strings.HasPrefix(reason, "命令不在白名单中") {
		cmdName := strings.Fields(cmd)[0]
		if auth.IsEmpty() {
			return fmt.Sprintf(
				`[授权提示] 命令 %q 不在本平台白名单中。`+
					`请用户在对话中明确授权，例如："授权运行 %s" / "我授权 %s" / "whitelist auth for %s" / "auth whitelist"。`+
					`授权后我会自动重试执行。`,
				cmdName, cmdName, cmdName, cmdName)
		}
		if !auth.WhitelistAuth {
			return fmt.Sprintf(
				`[授权提示] 当前仅有 Install / Bash 授权，不含 WhitelistAuth。请用户授权白名单：`+
					`"我授权运行 %s" / "whitelist auth for %s"。`,
				cmdName, cmdName)
		}
		if auth.WhitelistAuth && auth.WhitelistCmd != "" && auth.WhitelistCmd != cmdName {
			return fmt.Sprintf(
				`[授权提示] 当前 WhitelistAuth 仅授权 %q，不含 %q。请用户追加授权或改为通用白名单放宽。`,
				auth.WhitelistCmd, cmdName)
		}
	}
	// 软禁止：按需建议授权类型
	if auth.IsEmpty() {
		if isInstallClassCmd(cmd) {
			return fmt.Sprintf("[授权提示] 这是安装类操作，需要先在对话中告知我：" +
				`"我授权安装" 或 "Install auth granted"，授权有效期内我会自动放行。`)
		}
		return fmt.Sprintf("[授权提示] 这条命令被沙箱软禁止，需要先在对话中授权：" +
			`例如 "我授权使用 bash"（通用授权）或 "我授权安装"（仅安装类）。授权是一次性的，过期后需重新申请。`)
	}
	// 已经授权但仍被拦截 → 授权范围不够
	if auth.Install && !auth.Bash && !isInstallClassCmd(cmd) {
		return fmt.Sprintf("[授权提示] 当前仅有 Install 授权，本命令不在安装范围内。需要 Bash 授权才能放行：" +
			`请告诉我 "我授权使用 bash"。`)
	}
	return ""
}

// isInstallClassCmd 粗略判断命令是否为安装类（用于给用户更精准的提示）。
func isInstallClassCmd(cmd string) bool {
	installTokens := []string{
		"install", "uninstall", "purge", "upgrade", "dist-upgrade", "full-upgrade",
		"remove", "autoremove", "add ", " require", "global add",
	}
	lower := strings.ToLower(cmd)
	for _, t := range installTokens {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// looksLikeMissingExecutable 判断 stderr 是否像"找不到可执行文件"的错误。
// 这是一个简单的启发式判断，用于在 Windows 上跑 Linux 命令时附加平台提示。
func looksLikeMissingExecutable(stderr string) bool {
	s := strings.ToLower(stderr)
	keywords := []string{
		"not found",
		"is not recognized",
		"is not recognized as an internal or external command",
		"no such file or directory",
		"cannot find the path",
		"'xxx' 不是内部或外部命令",
	}
	for _, k := range keywords {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// platformHintFor 在 Windows 上执行 Linux-only 命令时给出等价命令提示。
// 由 platform_hint.go 在 build 时提供；Linux 上始终返回空（不需要提示）。
func platformHintFor(cmdName string) string {
	return hintForCommand(cmdName)
}

// GetAllowedCommands 返回允许的命令列表（带当前平台标识，方便 LLM 区分）
func GetAllowedCommands() string {
	header := fmt.Sprintf("# 当前平台: %s\n# 以下命令为 %s 平台允许的白名单命令：\n\n",
		PlatformName, PlatformName)
	var cmds []string
	for name, desc := range AllowedCommands {
		cmds = append(cmds, fmt.Sprintf("  %s: %s", name, desc))
	}
	return header + strings.Join(cmds, "\n")
}
