//go:build linux || darwin

package localcommand

import (
	"os/exec"
	"syscall"
)

// PlatformName 当前平台标识
const PlatformName = "linux"

// allowedCommandsCache / deniedCommandsCache / SetAllowedCommands /
// SetDeniedCommands 在 cache.go 中定义（无 build tag，跨平台共享）。

// platformSysProcAttr 在 Linux/Darwin 上把子进程放到新的进程组，
// 这样可以一次性 kill 整组（包括 fork 出来的子进程），避免泄漏。
func platformSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 通过负 PID 把整个进程组一起 SIGKILL。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// sandboxEnv 返回沙箱内子进程使用的环境变量。
//
// 透传策略：
//   - 透传宿主机环境，让 gh / docker / kubectl 等工具能复用用户所有配置
//   - 仅追加/覆盖 PATH 和 TZ（PATH 防止子进程找不到系统命令；TZ 保证日志时间本地化）
//   - 关键：剥离"其他 LLM 提供方"前缀（ARK_ / OPENAI_ / VOLCENGINE_ / COZE_ /
//     MINIMAX_ 等），避免 Claude Code CLI 启动时被路由到 MiniMax/OpenAI 等
//     不可用 provider 导致 claude -p 输出空 stdout 或无响应。
//   - 保留 ANTHROPIC_* / CLAUDE_*，确保 Claude Code CLI 能拿到真实 API key。
//
// 实现委托给 sandboxBaseEnv（无 build tag，跨平台共享），本文件只保留
// Unix 特有的 platformSysProcAttr / killProcessGroup。
func sandboxEnv() []string {
	return sandboxBaseEnv()
}

// sandboxWorkDir 返回沙箱工作目录。
func sandboxWorkDir() string {
	return "/tmp"
}

// hintForCommand 在 Linux 上不需要"平台提示"——所有命令都是原生支持，
// 命令找不到一般是确实没装。这里返回空字符串即可。
func hintForCommand(cmdName string) string {
	return ""
}
