package localcommand

import (
	"os"
	"strings"
	"testing"
)

// TestSandboxEnv_PassesThroughHostEnv 验证 sandboxEnv 透传宿主机所有环境变量。
// 这是当前策略：放弃 env 隔离，让 gh / docker / kubectl 等工具直接复用用户配置。
// 安全由硬禁止模式 + 软禁止授权机制负责。
func TestSandboxEnv_PassesThroughHostEnv(t *testing.T) {
	const customVar = "CUSTOM_TEST_VAR_FOR_SANDBOX_ENV"
	const customVal = "should-be-forwarded-12345"

	// 父进程设置一个具有唯一值的测试变量（避免和宿主机上已有变量冲突）
	os.Setenv(customVar, customVal)
	defer os.Unsetenv(customVar)

	env := sandboxEnv()

	hasCustom := false
	for _, e := range env {
		if e == customVar+"="+customVal {
			hasCustom = true
			break
		}
	}
	if !hasCustom {
		t.Fatalf("sandboxEnv must pass through arbitrary env var %s=%s; got env (first 5)=%v",
			customVar, customVal, env[:min(5, len(env))])
	}
}

// TestSandboxEnv_EnsuresPathAndTZ 验证 sandboxEnv 始终包含 PATH 和 TZ（如果宿主机没有）。
// 防止子进程因为 PATH 为空而找不到系统命令、TZ 未设置而日志时间是 UTC。
func TestSandboxEnv_EnsuresPathAndTZ(t *testing.T) {
	// 临时 unset 父进程的 PATH 和 TZ
	originalPath := os.Getenv("PATH")
	originalTZ := os.Getenv("TZ")
	os.Unsetenv("PATH")
	os.Unsetenv("TZ")
	defer func() {
		if originalPath != "" {
			os.Setenv("PATH", originalPath)
		}
		if originalTZ != "" {
			os.Setenv("TZ", originalTZ)
		}
	}()

	env := sandboxEnv()

	hasPath := false
	hasTZ := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
		}
		if strings.HasPrefix(e, "TZ=") {
			hasTZ = true
		}
	}
	if !hasPath {
		t.Fatalf("sandboxEnv must ensure PATH=... when missing; got env=%v", env)
	}
	if !hasTZ {
		t.Fatalf("sandboxEnv must ensure TZ=... when missing; got env=%v", env)
	}
}

// TestSandboxEnv_PreservesExistingPathAndTZ 验证宿主机已经设置了 PATH / TZ 时，
// sandboxEnv 不会重复追加（避免冲突）。
func TestSandboxEnv_PreservesExistingPathAndTZ(t *testing.T) {
	const customPATH = "/usr/local/bin:/custom/path"
	const customTZ = "Europe/Paris"
	originalPATH := os.Getenv("PATH")
	originalTZ := os.Getenv("TZ")
	os.Setenv("PATH", customPATH)
	os.Setenv("TZ", customTZ)
	defer func() {
		if originalPATH == "" {
			os.Unsetenv("PATH")
		} else {
			os.Setenv("PATH", originalPATH)
		}
		if originalTZ == "" {
			os.Unsetenv("TZ")
		} else {
			os.Setenv("TZ", originalTZ)
		}
	}()

	env := sandboxEnv()

	pathCount := 0
	tzCount := 0
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			pathCount++
			if e != "PATH="+customPATH {
				t.Fatalf("sandboxEnv mutated PATH: got %q, want %q", e, "PATH="+customPATH)
			}
		}
		if strings.HasPrefix(e, "TZ=") {
			tzCount++
			if e != "TZ="+customTZ {
				t.Fatalf("sandboxEnv mutated TZ: got %q, want %q", e, "TZ="+customTZ)
			}
		}
	}
	if pathCount != 1 {
		t.Fatalf("sandboxEnv should have exactly 1 PATH entry; got %d", pathCount)
	}
	if tzCount != 1 {
		t.Fatalf("sandboxEnv should have exactly 1 TZ entry; got %d", tzCount)
	}
}

// min 是 Go 1.21+ 内置，但为了测试文件可独立编译显式定义
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
