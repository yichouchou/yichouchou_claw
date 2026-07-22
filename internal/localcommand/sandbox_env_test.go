package localcommand

import (
	"os"
	"strings"
	"testing"
)

// TestSandboxEnv_AlwaysOverridesHome 验证 sandboxEnv 始终把 HOME 设为隔离目录。
// Linux 是 /tmp，Windows 是 %TEMP%；这是核心安全策略：
// 隔离 ~/.ssh / ~/.bash_history / ~/.aws 等敏感目录。
func TestSandboxEnv_AlwaysOverridesHome(t *testing.T) {
	env := sandboxEnv()
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			// 不能继承宿主机的真实 HOME（否则沙箱内命令能读到 ~/.ssh 等）
			if e != "HOME=/tmp" && e != "HOME=%TEMP%" {
				t.Fatalf("sandboxEnv HOME entry suspicious: %q (must be HOME=/tmp or HOME=%%TEMP%%)", e)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("sandboxEnv must contain a HOME=... entry; got env=%v", env)
	}
}

// TestSandboxEnv_PassesThroughAuthTokens 验证用户主动 export 的认证 token 会被透传。
// 修复前：沙箱把所有 env 重置为空，gh / docker / kubectl 等工具无法复用用户凭证。
// 修复后：GH_TOKEN / DOCKER_HOST / KUBECONFIG 等白名单变量会从父进程继承。
func TestSandboxEnv_PassesThroughAuthTokens(t *testing.T) {
	// 模拟用户在 shell 里 export 了 GH_TOKEN
	const fakeToken = "ghp_fakeTestTokenForUnitTest1234567890abcdef"
	const fakeConfigDir = "/home/user/.config/gh-fake"
	os.Setenv("GH_TOKEN", fakeToken)
	os.Setenv("GH_CONFIG_DIR", fakeConfigDir)
	defer os.Unsetenv("GH_TOKEN")
	defer os.Unsetenv("GH_CONFIG_DIR")

	env := sandboxEnv()

	assertContains := func(key, want string) {
		for _, e := range env {
			if strings.HasPrefix(e, key+"=") {
				if e != want {
					t.Fatalf("sandboxEnv %s mismatch: got %q, want %q", key, e, want)
				}
				return
			}
		}
		t.Fatalf("sandboxEnv missing %s (full env=%v)", key, env)
	}

	assertContains("GH_TOKEN", "GH_TOKEN="+fakeToken)
	assertContains("GH_CONFIG_DIR", "GH_CONFIG_DIR="+fakeConfigDir)
}

// TestSandboxEnv_DoesNotPassUnrelatedEnv 验证非白名单环境变量不会被透传。
// 防止沙箱进程意外继承宿主机上的随机敏感变量（如 AWS_SECRET_ACCESS_KEY）。
func TestSandboxEnv_DoesNotPassUnrelatedEnv(t *testing.T) {
	// 模拟父进程有一些与"工具认证"无关的环境变量
	os.Setenv("AWS_SECRET_ACCESS_KEY", "should-not-be-forwarded")
	os.Setenv("RANDOM_GARBAGE_VAR", "should-not-be-forwarded")
	defer os.Unsetenv("AWS_SECRET_ACCESS_KEY")
	defer os.Unsetenv("RANDOM_GARBAGE_VAR")

	env := sandboxEnv()

	for _, e := range env {
		if strings.HasPrefix(e, "AWS_SECRET_ACCESS_KEY=") {
			t.Fatalf("sandboxEnv leaked AWS_SECRET_ACCESS_KEY: %s", e)
		}
		if strings.HasPrefix(e, "RANDOM_GARBAGE_VAR=") {
			t.Fatalf("sandboxEnv leaked RANDOM_GARBAGE_VAR: %s", e)
		}
	}
}

// TestSandboxEnv_HandlesEmptyTokenGracefully 验证父进程未设置 token 时不会写入空值。
// 防止环境变量被设为空字符串后导致子进程行为异常。
func TestSandboxEnv_HandlesEmptyTokenGracefully(t *testing.T) {
	// 显式 unset 确保初始状态干净
	os.Unsetenv("GH_TOKEN")

	env := sandboxEnv()

	for _, e := range env {
		if strings.HasPrefix(e, "GH_TOKEN=") {
			t.Fatalf("sandboxEnv should not pass empty GH_TOKEN; got %s", e)
		}
	}
}
