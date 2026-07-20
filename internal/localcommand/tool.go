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
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// AllowedCommand 白名单允许的命令及描述
var AllowedCommands = map[string]string{
	"ls":          "列出当前或指定目录下文件与文件夹，只读查询",
	"ll":          "ls -la 别名，完整展示文件权限、大小、修改时间、所有者，只读查询",
	"cat":         "读取并打印完整文件内容，仅用于查看文本文件，禁止读取密钥/敏感配置",
	"head":        "读取文件前N行内容，默认10行，文件只读查看",
	"tail":        "读取文件末尾N行，支持实时追踪日志文件输出，常用于日志排查",
	"wc":          "统计文件行数、单词数、字符字节数，文本分析只读工具",
	"grep":        "文本检索，按关键词匹配文件内容、过滤命令输出，日志/配置检索核心工具",
	"find":        "按名称、大小、时间递归查找目录下文件，仅查询，不做删除操作",
	"ps":          "查看主机运行进程列表，查看进程PID、占用、启动命令，只读",
	"df":          "查看磁盘分区挂载、总容量、已用/剩余空间，磁盘资源查询",
	"du":          "统计目录/文件实际占用磁盘大小，排查大文件占用场景",
	"free":        "查看系统内存、Swap交换分区使用总量与剩余，资源监控",
	"uptime":      "展示系统开机时长、当前负载平均值，快速查看机器负载",
	"date":        "输出当前系统时间、时区，仅查看，不支持修改系统时间",
	"echo":        "打印自定义文本，可配合管道传递数据或重定向(>)写入文件，仅限工作目录内文件操作",
	"printf":      "格式化打印文本，支持格式化输出，可配合重定向写入文件",
	"pwd":         "打印当前所在工作绝对路径，目录定位工具",
	"whoami":      "输出当前执行命令的操作系统用户名，身份查询",
	"hostname":    "输出本机主机名称，节点标识查询",
	"uname":       "查看系统内核版本、操作系统架构，主机环境信息查询",
	"top":         "实时查看CPU/内存进程占用，交互式工具，执行超时自动终止",
	"netstat":     "查看本机TCP/UDP监听端口、网络连接、路由状态，网络排查只读",
	"ping":        "ICMP网络连通性探测，测试目标IP/域名延迟与丢包，网络诊断",
	"curl":        "发起HTTP/HTTPS网络请求，仅GET只读查询，禁止上传/修改接口、内网高危地址",
	"wget":        "远程下载网络文件到本地工作目录，仅允许公开静态资源下载",
	"tar":         "文件打包/解压工具，仅读写工作目录内压缩包，不操作系统目录",
	"zip":         "将目录/文件打包为zip压缩文件，仅限工作目录操作",
	"unzip":       "解压zip压缩包至当前目录，仅限工作目录操作",
	"mkdir":       "创建空文件夹，仅允许在沙箱工作目录内新建目录",
	"cp":          "复制文件/文件夹，仅支持工作目录内拷贝，禁止复制系统敏感文件",
	"mv":          "文件/目录移动、重命名，仅限工作目录内部操作，不可移动系统文件",
	"diff":        "对比两个文本文件内容差异，配置文件比对工具",
	"sort":        "对文本内容按行排序，配合管道做日志数据整理",
	"uniq":        "去除文本连续重复行，日志去重统计工具",
	"awk":         "高级文本列提取、数值统计、格式化输出，日志结构化分析核心工具",
	"sed":         "流式文本替换、过滤，仅允许工作目录内临时文件修改，禁止编辑系统配置",
	"cut":         "按分隔符截取文本指定列，解析日志、表格类文本",
	"tree":        "树形递归打印目录层级结构，直观查看文件夹结构，只读",
	"sensors":     "读取硬件CPU、硬盘温度传感器数据，服务器硬件状态监控",
	"ip":          "查看网卡IP地址、路由表、网络设备状态，新一代网络查询工具，支持重定向写入文件",
	"ifconfig":    "传统网卡信息查看，兼容旧系统网络接口查询，只读，支持重定向写入文件",
	"touch":       "创建空文件或更新文件时间戳，仅限沙箱工作目录内使用",
	"tee":         "从标准输入读取并同时写入标准输出和文件，配合管道使用，支持写入文件内容",
	"journalctl":  "查看系统服务日志，支持按服务、时间过滤，系统故障排查只读",
	"dmesg":       "查看内核启动、硬件报错日志，服务器异常排查工具",
	"ss":          "高性能替代netstat，查看系统套接字、端口连接状态",
	"dig":         "DNS解析查询，域名A记录/CNAME解析诊断",
	"nslookup":    "兼容式DNS域名解析工具，排查域名解析异常",
	"traceroute":  "路由追踪，排查网络链路延迟、断链节点",
	"gzip":        "单文件压缩解压，仅工作目录内使用",
	"gunzip":      "解压gzip格式文件",
	"md5sum":      "计算文件MD5哈希，校验文件完整性、防篡改",
	"sha256sum":   "计算文件sha256校验值，文件完整性校验",
	"watch":       "周期性重复执行查询命令，持续监控磁盘、进程、接口状态",
	"base64":      "Base64编码、解码文本，用于解析配置内加密字符串，只读转换",
	"jq":          "JSON格式化、过滤解析工具，解析接口返回、k8s json配置",
	"crontab":     "查看当前用户定时任务列表，仅-l查询，禁止编辑/删除定时任务",
}

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

	execCmd.Env = []string{"HOME=/tmp", "PATH=/usr/bin:/bin:/usr/local/bin", "TZ=Asia/Shanghai"}
	execCmd.Dir = "/tmp"
	execCmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	execCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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

	// 环境和工作目录
	env := []string{"HOME=/tmp", "PATH=/usr/bin:/bin:/usr/local/bin", "TZ=Asia/Shanghai"}
	workDir := "/tmp"

	// 最后一个命令的输出缓冲区
	finalStdout := &bytes.Buffer{}
	finalStderr := &bytes.Buffer{}

	// 启动所有命令
	for i, stage := range stages {
		cmd := exec.CommandContext(execCtx, stage.Argv[0], stage.Argv[1:]...)
		cmd.Env = env
		cmd.Dir = workDir
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
		if cmd == nil || cmd.Process == nil {
			continue
		}
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Execute 执行命令
func Execute(ctx context.Context, input *CommandInput) (*CommandOutput, error) {
	cmd := input.Command
	if cmd == "" {
		return nil, fmt.Errorf("命令不能为空")
	}

	log.Printf("[LocalCommand] Executing: %s", cmd)

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
			return &CommandOutput{
				Stdout:   "",
				Stderr:   fmt.Sprintf("命令不在白名单中: %s", argv[0]),
				ExitCode: -1,
				Duration: "0s",
			}, nil
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

	return ExecuteChain(ctx, stages)
}

// GetAllowedCommands 返回允许的命令列表
func GetAllowedCommands() string {
	var cmds []string
	for name, desc := range AllowedCommands {
		cmds = append(cmds, fmt.Sprintf("  %s: %s", name, desc))
	}
	return strings.Join(cmds, "\n")
}
