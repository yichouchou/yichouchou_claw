package localcommand

import (
	"fmt"
	"testing"
)

func TestEmergeRegexHits(t *testing.T) {
	cases := []struct {
		cmd    string
		want   bool
		reason string
	}{
		// 应该拦截（需要授权）
		{"emerge vim", true, "包名安装"},
		{"emerge --unmerge vim", true, "卸载"},
		{"emerge --depclean", true, "卸载"},
		{"emerge --deep vim", true, "深度构建"},
		{"emerge --fetchall vim", true, "fetchall"},
		{"emerge --autounmask vim", true, "autounmask"},
		{"emerge --oneshot vim", true, "oneshot"},
		{"emerge -C vim", true, "uninstall -C"},
		{"emerge app-editors/vim", true, "category/pkg"},

		// 应该放行（纯查询）
		{"emerge --info vim", false, "info 查询"},
		{"emerge --pretend vim", false, "pretend"},
		{"emerge --search vim", false, "search"},
		{"emerge --sync", false, "sync"},
		{"emerge --version", false, "version"},
	}
	for _, c := range cases {
		var matched bool
		var hitIdx = -1
		for i, p := range SoftForbiddenPatterns {
			if p.MatchString(c.cmd) {
				matched = true
				hitIdx = i
				break
			}
		}
		status := "OK"
		if matched != c.want {
			status = "FAIL"
			t.Errorf("[%s] cmd=%q want=%v got=%v reason=%s", status, c.cmd, c.want, matched, c.reason)
		} else {
			t.Logf("[%s] cmd=%q hit_pattern_idx=%d", status, c.cmd, hitIdx)
		}
		_ = fmt.Sprintf
	}
}
