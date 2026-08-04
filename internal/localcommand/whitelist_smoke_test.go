package localcommand

import (
	"context"
	"strings"
	"testing"

	"github.com/yichouchou/yichouchou_claw/internal/config"
)

// TestSmoke_WhitelistCoverage 复现 14h 那 9 次沙箱失败, 验证简单加白后能放行。
//
// 这是 2026-08-04 用户提的"简单加白"方案的 smoke test。
// 跑 go test 即可快速验证 exec-approvals.json 白名单覆盖度。
//
// 用法:
//  1. 在 workdir/config/exec-approvals.json 加白需要测试的命令
//  2. go test -run TestSmoke_WhitelistCoverage -v ./internal/localcommand/
//  3. 所有 case wantBlocked=false 应该通过 → 沙箱不再拦截
func TestSmoke_WhitelistCoverage(t *testing.T) {
	// 加载 exec-approvals.json (与 main.go 启动时一致)
	if err := config.LoadApprovals("../../workdir/config/exec-approvals.json"); err != nil {
		t.Fatalf("load exec-approvals.json failed: %v", err)
	}
	SetAllowedCommands("LocalCommandAgent")
	SetDeniedCommands("LocalCommandAgent")

	ctx := context.Background()
	tests := []struct {
		cmd         string
		wantBlocked bool
		why         string
	}{
		// 14h session 真实失败命令 — 加白后应该全部放行
		{`which mermaid mmdc drawio dot plantuml`, false, "探活类命令应放行 (which 已在白名单)"},
		{`dot -V`, false, "dot 应在白名单"},
		{`mermaid --version`, false, "mermaid 应在白名单"},
		{`mmdc --version`, false, "mmdc 应在白名单"},
		{`plantuml -tpng -o /tmp/x.png x.puml`, false, "plantuml 应在白名单"},
		{`xvfb-run --help`, false, "xvfb-run 应在白名单 (drawio 渲染依赖)"},
		{`python3 -c "import PIL"`, false, "python3 -c 应放行 (description 已含)"},
		{`ls /opt/drawio*`, false, "ls 应放行 (path glob 不算 sensitive)"},
	}
	for _, tc := range tests {
		dangerous, reason := IsDangerousWithAgent(ctx, "LocalCommandAgent", tc.cmd)
		blocked := dangerous
		if blocked != tc.wantBlocked {
			t.Errorf("cmd=%q 期望 blocked=%v 实际 blocked=%v reason=%q (场景: %s)",
				tc.cmd, tc.wantBlocked, blocked, reason, tc.why)
		}
		// 失败时 reason 应含分类前缀 (白名单 / 敏感 / 硬禁止 / 软禁止),
		// 成功时 reason 为空, 不做检查。
		if blocked && reason != "" &&
			!strings.Contains(reason, "白名单") &&
			!strings.Contains(reason, "敏感") &&
			!strings.Contains(reason, "硬禁止") &&
			!strings.Contains(reason, "软禁止") {
			t.Logf("cmd=%q reason=%q (unexpected error format, may be OK)", tc.cmd, reason)
		}
	}
}
