//go:build linux || darwin

package localcommand

import (
	"os"
	"os/exec"
	"strings"
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
//   - 完全透传宿主机环境，让 gh / docker / kubectl 等工具能复用用户所有配置
//   - 仅追加/覆盖 PATH 和 TZ（PATH 防止子进程找不到系统命令；TZ 保证日志时间本地化）
//   - 不再做任何变量过滤，由硬禁止模式 + 软禁止授权机制负责安全拦截
func sandboxEnv() []string {
	env := os.Environ() // 透传宿主机全部环境变量
	hasPath := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
			break
		}
	}
	if !hasPath {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	hasTZ := false
	for _, e := range env {
		if strings.HasPrefix(e, "TZ=") {
			hasTZ = true
			break
		}
	}
	if !hasTZ {
		env = append(env, "TZ=Asia/Shanghai")
	}
	return env
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
