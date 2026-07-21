package localcommand

import "testing"

func TestSplitByOperators_ShellLogic(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		// 短路 fallback（用户真实场景）
		{`which gh || command -v gh || echo "NOT_FOUND"`,
			[]string{`which gh `, ` command -v gh `, ` echo "NOT_FOUND"`}},

		// 短路 AND
		{`which gh && gh --version`,
			[]string{`which gh `, ` gh --version`}},

		// 顺序执行
		{`cd /tmp; ls; pwd`,
			[]string{`cd /tmp`, ` ls`, ` pwd`}},

		// 单 | 仍然按 pipe 拆分（向后兼容）
		{`which gh | grep -v foo`,
			[]string{`which gh `, ` grep -v foo`}},

		// 单个命令
		{`ls /tmp`,
			[]string{`ls /tmp`}},

		// || 和 | 混用（边界）
		{`cat /etc/hosts | head && echo done`,
			[]string{`cat /etc/hosts `, ` head `, ` echo done`}},
	}
	for _, c := range cases {
		got := splitByOperators(c.cmd)
		if len(got) != len(c.want) {
			t.Errorf("splitByOperators(%q) got %d parts, want %d: %v", c.cmd, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			// TrimSpace 后比较（splitByOperators 返回的 stage 可能带前后空白）
			g := trimAll(got[i])
			w := trimAll(c.want[i])
			if g != w {
				t.Errorf("splitByOperators(%q)[%d] = %q, want %q", c.cmd, i, g, w)
			}
		}
	}
}

func TestHasShellLogic(t *testing.T) {
	cases := map[string]bool{
		`which gh || echo not`:     true,
		`which gh && gh --version`: true,
		`cd /tmp; ls`:              true,
		`which gh`:                 false,
		`ls /tmp`:                  false,
		`which gh | grep foo`:      false, // 单 | 不是逻辑符
		`echo "a || b"`:            false, // 引号里的 ||
		`echo 'a && b'`:            false,
	}
	for cmd, want := range cases {
		got := hasShellLogic(cmd)
		if got != want {
			t.Errorf("hasShellLogic(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// trimAll 去除字符串中所有空格（用于测试简单比较）
func trimAll(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r != ' ' && r != '\t' {
			out = append(out, r)
		}
	}
	return string(out)
}
