package localcommand

import (
	"os"
	"strings"
	"testing"
)

// TestSandboxEnv_PassesThroughHome 验证 HOME 被透传给子进程，让 gh / docker 等工具
// 能读到用户的 ~/.config/gh 等配置。沙箱不再做 HOME 隔离，由用户自行控制风险。
func TestSandboxEnv_PassesThroughHome(t *testing.T) {
	const fakeHome = "/home/test-user-fake-home"
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", fakeHome)
	defer func() {
		if originalHome == "" {
			os.Unsetenv("HOME")
		} else {
			os.Setenv("HOME", originalHome)
		}
	}()

	env := sandboxEnv()

	found := false
	for _, e := range env {
		if e == "HOME="+fakeHome {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("sandboxEnv must pass through HOME=%s; got env=%v", fakeHome, env)
	}
}

// TestSandboxEnv_PassesThroughAuthTokens 验证用户主动 export 的认证 token 会被透传。
// 解决 gh / docker / kubectl 等工具无法复用用户凭证的问题。
func TestSandboxEnv_PassesThroughAuthTokens(t *testing.T) {
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
	os.Unsetenv("GH_TOKEN")

	env := sandboxEnv()

	for _, e := range env {
		if strings.HasPrefix(e, "GH_TOKEN=") {
			t.Fatalf("sandboxEnv should not pass empty GH_TOKEN; got %s", e)
		}
	}
}
