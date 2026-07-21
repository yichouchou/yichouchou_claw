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

// DangerousPatterns 危险模式列表
var DangerousPatterns = []*regexp.Regexp{
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
	regexp.MustCompile(`reboot`),
	regexp.MustCompile(`halt`),
	regexp.MustCompile(`shutdown`),
	regexp.MustCompile(`init\s+0`),
	regexp.MustCompile(`init\s+6`),
	regexp.MustCompile(`nmap\s+--`),
	regexp.MustCompile(`hping`),
	regexp.MustCompile(`netcat`),
	regexp.MustCompile(`nc\s+-e`),
	regexp.MustCompile(`/dev/tcp`),
	regexp.MustCompile(`/dev/udp`),
	regexp.MustCompile(`/etc/passwd`),
	regexp.MustCompile(`/etc/shadow`),
	regexp.MustCompile(`chmod\s+777\s+/etc`),
	regexp.MustCompile(`chmod\s+777\s+/root`),
	regexp.MustCompile(`curl\s+.*\|\s*sh`),
	regexp.MustCompile(`wget\s+.*\|\s*sh`),
	regexp.MustCompile(`curl\s+-s\s+http://.*\.sh`),
	regexp.MustCompile(`wget\s+-O\s+-.*\.sh`),
	regexp.MustCompile(`chmod\s+4755`),
	regexp.MustCompile(`chown\s+root:root`),
	regexp.MustCompile(`>\s*/dev/sd`),
	regexp.MustCompile(`tee\s+/dev/`),
	regexp.MustCompile(`eval\s+`),
	regexp.MustCompile(`exec\s+`),
	regexp.MustCompile(`fork\s+`),
}

// SensitivePaths 敏感路径
var SensitivePaths = []string{
	"/root", "/etc/ssh", "/etc/pki", "/var/log/secure", "/var/log/auth",
	"/proc/1", "/proc/sys", "/sys/kernel", "/boot", "/dev/mapper",
	"/etc/shadow", ".ssh", ".bash_history", ".zsh_history",
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

// IsDangerous 检查命令是否危险
func IsDangerous(cmd string) (bool, string) {
	for _, pattern := range DangerousPatterns {
		if pattern.MatchString(cmd) {
			return true, fmt.Sprintf("检测到危险模式: %s", pattern.String())
		}
	}
	lowerCmd := strings.ToLower(cmd)
	for _, path := range SensitivePaths {
		if strings.Contains(lowerCmd, strings.ToLower(path)) {
			return true, fmt.Sprintf("禁止访问敏感路径: %s", path)
		}
	}
	return false, ""
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
func Execute(ctx context.Context, input *CommandInput) (*CommandOutput, error) {
	cmd := input.Command
	if cmd == "" {
		return nil, fmt.Errorf("命令不能为空")
	}

	log.Printf("[LocalCommand][%s] Executing: %s", PlatformName, cmd)

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

		// 检查白名单
		if !isCommandAllowed(part) && !strings.Contains(part, "/") {
			stderr := fmt.Sprintf(
				"命令不在白名单中: %s\n"+
					"当前平台: %s\n"+
					"提示：白名单是平台相关的，请使用 %s 平台等价的命令。"+
					"可调用 GetAllowedCommands() 查看本平台所有允许的命令。",
				argv[0], PlatformName, PlatformName)
			return &CommandOutput{
				Stdout:   "",
				Stderr:   stderr,
				ExitCode: -1,
				Duration: "0s",
			}, ErrNotInWhitelist
		}

		// 安全检查
		if dangerous, reason := IsDangerous(part); dangerous {
			return &CommandOutput{
				Stdout:   "",
				Stderr:   fmt.Sprintf("安全拦截: %s", reason),
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
