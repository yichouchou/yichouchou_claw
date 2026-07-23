//go:build windows

package localcommand

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// platformSysProcAttr 在 Windows 上让子进程成为新进程组的一员，
// 便于通过 Job 对象或 Process.Kill 统一清理。Windows syscall.SysProcAttr
// 没有 Setpgid 字段，使用 CreationFlags.CREATE_NEW_PROCESS_GROUP 达到近似效果。
func platformSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: 0x00000200, // CREATE_NEW_PROCESS_GROUP
	}
}

// killProcessGroup 在 Windows 上不能向负 PID 发信号，这里直接终止主进程。
// Windows 进程组语义与 Unix 不同，逐进程 Kill 已经能满足沙箱使用场景。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

// sandboxEnv 返回沙箱内子进程使用的环境变量。
//
// 透传策略：
//   - 完全透传宿主机环境，让子进程复用用户所有配置
//   - 仅追加/覆盖 PATH、TZ、LANG（避免子进程找不到系统命令、日志时间本地化）
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
		env = append(env, "PATH=%SystemRoot%\\System32;%SystemRoot%;%SystemRoot%\\System32\\WindowsPowerShell\\v1.0\\")
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
	hasLANG := false
	for _, e := range env {
		if strings.HasPrefix(e, "LANG=") {
			hasLANG = true
			break
		}
	}
	if !hasLANG {
		env = append(env, "LANG=zh_CN.UTF-8")
	}
	return env
}

// sandboxWorkDir 返回沙箱工作目录。Windows 上用 %TEMP%。
func sandboxWorkDir() string {
	return "%TEMP%"
}

// hintForCommand 在 Windows 上给出 Linux 命令的等价命令提示。
// 数据源：allowed_windows.go 中的 linuxEquivalent 表。
func hintForCommand(cmdName string) string {
	base := cmdName
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "\\"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	if eq, ok := linuxEquivalent[base]; ok {
		return "Windows 等价命令: " + eq +
			"\n提示：当前运行在 Windows 上，本白名单为 Windows 原生命令重新组织。" +
			"\n请使用 GetAllowedCommands() 查看 Windows 平台允许的命令。"
	}
	return ""
}
