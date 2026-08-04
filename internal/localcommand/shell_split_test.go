package localcommand

import (
	"strings"
	"testing"
)

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

// TestSplitByShellLogic_Heredoc 2026-08-04 新增: 验证 heredoc 整段被视作单 stage，
// 避免 sandbox 把 `html=1` / `whiteSpace=wrap` / `rounded=1` 等 XML 属性误识为"未授权命令名"。
//
// 用户真实场景 (line 82147 of inputs/2026-08-03/19h.md):
//
//	cat > /tmp/diagrams/codex-demo.drawio <<'DRAWIO_XML'
//	<mxCell ... style="text;html=1;strokeColor=none;..." />
//	DRAWIO_XML
//
// 修复前: sandbox 把 `html=1` 拆成新 stage → "命令不在白名单中: html=1"
// 修复后: heredoc body 全部归到 `cat > ... <<'DRAWIO_XML'` 整个 stage → cat 在白名单 → 放行
func TestSplitByShellLogic_Heredoc(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string // 期望的 stage 内容 (含 heredoc body 整体)
	}{
		// 单引号 heredoc + 含可疑关键字
		{
			"heredoc_with_html_keyword",
			"cat > /tmp/x <<'EOF'\nhtml=1\nEOF\n",
			[]string{"cat > /tmp/x <<'EOF'\nhtml=1\nEOF\n"},
		},
		// 自定义 TAG + 含多关键字
		{
			"heredoc_with_mxgraph_keys",
			"cat > /tmp/x.drawio <<'DRAWIO'\n<mxCell html=1 whiteSpace=wrap rounded=1>\nDRAWIO\n",
			[]string{"cat > /tmp/x.drawio <<'DRAWIO'\n<mxCell html=1 whiteSpace=wrap rounded=1>\nDRAWIO\n"},
		},
		// 无引号 heredoc (变量展开模式)
		{
			"heredoc_no_quote",
			"cat > /tmp/x <<EOF\nfoo=bar\nEOF\n",
			[]string{"cat > /tmp/x <<EOF\nfoo=bar\nEOF\n"},
		},
		// <<- (允许缩进的 heredoc)
		{
			"heredoc_with_dash",
			"cat > /tmp/x <<-EOF\nhtml=1\nEOF\n",
			[]string{"cat > /tmp/x <<-EOF\nhtml=1\nEOF\n"},
		},
		// heredoc 与 shell chain 混合: 前置命令用 &&
		{
			"heredoc_after_chain",
			"mkdir -p /tmp/x && cat > /tmp/x <<'EOF'\nhtml=1\nEOF\n",
			[]string{"mkdir -p /tmp/x ", " cat > /tmp/x <<'EOF'\nhtml=1\nEOF\n"},
		},
		// heredoc body 跨多行 + 含 ; 类似字符 (但被 heredoc body 包裹)
		{
			"heredoc_multiline",
			"cat > /tmp/x <<'EOF'\nline1=foo\nline2=bar;baz\nline3 html=1\nEOF\n",
			[]string{"cat > /tmp/x <<'EOF'\nline1=foo\nline2=bar;baz\nline3 html=1\nEOF\n"},
		},
		// << 后有空格 (LLM 常用, 2026-08-04 修复: 之前 << 'EOF' 中间空格导致 fix 不触发)
		{
			"heredoc_with_space_after_lt",
			"cat > /tmp/x << 'EOF'\nhtml=1\nEOF\n",
			[]string{"cat > /tmp/x << 'EOF'\nhtml=1\nEOF\n"},
		},
		// <<- (允许缩进的 heredoc) + 空格
		{
			"heredoc_with_dash_and_space",
			"cat > /tmp/x <<- EOF\nhtml=1\nEOF\n",
			[]string{"cat > /tmp/x <<- EOF\nhtml=1\nEOF\n"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitByShellLogic(c.cmd)
			if len(got) != len(c.want) {
				t.Errorf("splitByShellLogic(%q) got %d parts, want %d: %#v",
					c.cmd, len(got), len(c.want), got)
				return
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("splitByShellLogic(%q)[%d] = %q, want %q",
						c.cmd, i, got[i], c.want[i])
				}
			}
			// 关键断言: got 里不应该出现孤立的 "html=1" / "whiteSpace=wrap" 等
			// 关键字 (它们必须包含在 heredoc stage 里, 而不是独立 stage)。
			for i, p := range got {
				trimmed := strings.TrimSpace(p)
				if trimmed == "html=1" || trimmed == "whiteSpace=wrap" ||
					trimmed == "rounded=1" || trimmed == "endArrow=block" {
					t.Errorf("splitByShellLogic(%q)[%d] = %q, want heredoc body absorbed (not standalone stage)",
						c.cmd, i, p)
				}
			}
		})
	}
}
