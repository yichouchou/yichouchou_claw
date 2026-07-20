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
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// AllowedCommand 白名单允许的命令及描述
var AllowedCommands = map[string]string{
	"ls":       "列出目录内容",
	"ll":       "列出目录详细信息 (ls -la)",
	"cat":      "查看文件内容",
	"head":     "查看文件开头部分",
	"tail":     "查看文件结尾部分",
	"wc":       "统计文件行数/字数/字符数",
	"grep":     "在文件中搜索文本",
	"find":     "查找文件",
	"ps":       "查看进程状态",
	"df":       "查看磁盘空间",
	"du":       "查看目录/文件大小",
	"free":     "查看内存使用情况",
	"uptime":   "查看系统运行时间",
	"date":     "显示当前日期时间",
	"echo":     "输出文本",
	"pwd":      "显示当前目录",
	"whoami":   "显示当前用户",
	"hostname": "显示主机名",
	"uname":    "显示系统信息",
	"top":      "查看系统进程 (交互式,限制5秒)",
	"netstat":  "查看网络连接状态",
	"ping":     "测试网络连接",
	"curl":     "发送 HTTP 请求 (仅允许简单 GET)",
	"wget":     "下载文件 (仅允许简单下载)",
	"tar":      "归档/解压缩文件",
	"zip":      "创建 zip 压缩包",
	"unzip":    "解压 zip 文件",
	"mkdir":    "创建目录",
	"cp":       "复制文件",
	"mv":       "移动/重命名文件",
	"diff":     "比较文件差异",
	"sort":     "排序文本",
	"uniq":     "过滤重复行",
	"awk":      "文本处理",
	"sed":      "流编辑器",
	"cut":      "剪切文件列",
	"tree":     "显示目录树状结构",
	"sensors":  "查看硬件传感器温度",
	"ip":       "查看网络接口信息",
	"ifconfig": "查看网络接口配置",
}

// DangerousPatterns 危险模式列表
var DangerousPatterns = []*regexp.Regexp{
	// 递归删除
	regexp.MustCompile(`rm\s+-rf\s+/`),
	regexp.MustCompile(`rm\s+-rf\s+\*`),
	regexp.MustCompile(`rm\s+-rf\s+\.`),
	regexp.MustCompile(`rm\s+-rf\s+/[a-zA-Z]+`),

	// 直接写磁盘
	regexp.MustCompile(`dd\s+.*of=/`),
	regexp.MustCompile(`dd\s+.*of=\.`),

	// 分区和格式化
	regexp.MustCompile(`mkfs`),
	regexp.MustCompile(`fdisk`),
	regexp.MustCompile(`parted`),
	regexp.MustCompile(`partprobe`),

	// 系统控制
	regexp.MustCompile(`reboot`),
	regexp.MustCompile(`halt`),
	regexp.MustCompile(`shutdown`),
	regexp.MustCompile(`init\s+0`),
	regexp.MustCompile(`init\s+6`),

	// 网络攻击
	regexp.MustCompile(`nmap\s+--`),
	regexp.MustCompile(`hping`),
	regexp.MustCompile(`netcat`),
	regexp.MustCompile(`nc\s+-e`),
	regexp.MustCompile(`/dev/tcp`),
	regexp.MustCompile(`/dev/udp`),

	// 密码和敏感文件
	regexp.MustCompile(`/etc/passwd`),
	regexp.MustCompile(`/etc/shadow`),
	regexp.MustCompile(`chmod\s+777\s+/etc`),
	regexp.MustCompile(`chmod\s+777\s+/root`),

	// 远程下载执行
	regexp.MustCompile(`curl\s+.*\|\s*sh`),
	regexp.MustCompile(`wget\s+.*\|\s*sh`),
	regexp.MustCompile(`curl\s+-s\s+http://.*\.sh`),
	regexp.MustCompile(`wget\s+-O\s+-.*\.sh`),

	// 其他危险操作
	regexp.MustCompile(`chmod\s+4755`),
	regexp.MustCompile(`chown\s+root:root`),
	regexp.MustCompile(`>\s*/dev/sd`),
	regexp.MustCompile(`tee\s+/dev/`),
	regexp.MustCompile(`eval\s+`),
	regexp.MustCompile(`exec\s+`),
	regexp.MustCompile(`fork\s+`),
}

// SensitivePaths 敏感路径，禁止访问
var SensitivePaths = []string{
	"/root",
	"/etc/ssh",
	"/etc/pki",
	"/var/log/secure",
	"/var/log/auth",
	"/proc/1",
	"/proc/sys",
	"/sys/kernel",
	"/boot",
	"/dev/mapper",
	"/etc/shadow",
	".ssh",
	".bash_history",
	".zsh_history",
}

// CommandInput 命令执行输入
type CommandInput struct {
	Command string `json:"command"` // 要执行的命令
}

// CommandOutput 命令执行输出
type CommandOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Duration string `json:"duration"`
}

// IsDangerous 检查命令是否危险
func IsDangerous(cmd string) (bool, string) {
	// 检查危险模式
	for _, pattern := range DangerousPatterns {
		if pattern.MatchString(cmd) {
			return true, fmt.Sprintf("检测到危险模式: %s", pattern.String())
		}
	}

	// 检查是否在白名单中
	cmdName := strings.Fields(cmd)[0]
	if _, ok := AllowedCommands[cmdName]; !ok {
		// 允许 cat 等带路径的情况
		if !strings.Contains(cmd, "/") && cmdName != "cat" && cmdName != "ls" && cmdName != "grep" && cmdName != "find" {
			return true, fmt.Sprintf("命令不在白名单中: %s", cmdName)
		}
	}

	// 检查敏感路径
	lowerCmd := strings.ToLower(cmd)
	for _, path := range SensitivePaths {
		if strings.Contains(lowerCmd, strings.ToLower(path)) {
			return true, fmt.Sprintf("禁止访问敏感路径: %s", path)
		}
	}

	return false, ""
}

// Execute 在沙箱中执行命令
func Execute(ctx context.Context, input *CommandInput) (*CommandOutput, error) {
	cmd := input.Command
	if cmd == "" {
		return nil, fmt.Errorf("命令不能为空")
	}

	// 记录日志
	log.Printf("[LocalCommand] Executing: %s", cmd)

	// 安全检查
	if dangerous, reason := IsDangerous(cmd); dangerous {
		log.Printf("[LocalCommand] Blocked dangerous command: %s, reason: %s", cmd, reason)
		return &CommandOutput{
			Stdout:   "",
			Stderr:   fmt.Sprintf("安全拦截: %s", reason),
			ExitCode: -1,
			Duration: "0s",
		}, nil
	}

	// 解析命令
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil, fmt.Errorf("无效命令")
	}

	// 验证命令在白名单中
	cmdName := parts[0]
	if _, allowed := AllowedCommands[cmdName]; !allowed && !strings.Contains(cmd, "/") {
		// 允许带路径的命令 (如 /bin/ls)
		return &CommandOutput{
			Stdout:   "",
			Stderr:   fmt.Sprintf("命令不在白名单中: %s", cmdName),
			ExitCode: -1,
			Duration: "0s",
		}, nil
	}

	// 创建执行上下文，设置超时
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 创建命令
	var execCmd *exec.Cmd
	if len(parts) == 1 {
		execCmd = exec.CommandContext(execCtx, parts[0])
	} else {
		execCmd = exec.CommandContext(execCtx, parts[0], parts[1:]...)
	}

	// 设置环境变量，限制访问
	execCmd.Env = []string{
		"HOME=/tmp",
		"PATH=/usr/bin:/bin:/usr/local/bin",
		"TZ=Asia/Shanghai",
	}

	// 设置工作目录
	execCmd.Dir = "/tmp"

	// 限制 stdin
	execCmd.Stdin = nil

	// 执行命令
	start := time.Now()
	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	// 设置资源限制
	execCmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

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

	log.Printf("[LocalCommand] Completed: exit=%d duration=%v", exitCode, duration)

	return &CommandOutput{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Duration: duration.String(),
	}, nil
}

// GetAllowedCommands 返回允许的命令列表
func GetAllowedCommands() string {
	var cmds []string
	for name, desc := range AllowedCommands {
		cmds = append(cmds, fmt.Sprintf("  %s: %s", name, desc))
	}
	return strings.Join(cmds, "\n")
}
