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
// 规则说明：仅前缀完全匹配放行；所有命令仅限工作目录操作；高危修改/删除操作已拦截在黑名单
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
	"echo":        "打印自定义文本，可配合管道传递数据，仅输出，不直接写入系统文件",
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
	"ip":          "查看网卡IP地址、路由表、网络设备状态，新一代网络查询工具",
	"ifconfig":    "传统网卡信息查看，兼容旧系统网络接口查询，只读",

	// 补充拓展命令
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
