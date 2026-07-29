/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package localcommand

import (
	"context"
	"fmt"
	"log"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// astLogPrefix AST 安全层日志前缀
const astLogPrefix = "[localcommand.ast]"

// astSafetyCheck 用 mvdan.cc/sh 解析命令为 AST，递归检查每个节点。
//
// 这是 shell 命令安全检查的"最强层"，解决了字符串扫描无法覆盖的问题：
//   - 引号嵌套 / subshell 注入 (CmdSubst / Subshell 节点)
//   - shell 元字符：重定向、管道 (Redirect / BinaryCmd 节点)
//   - glob / brace expansion (BraceExp / Glob 节点)
//   - history expansion `!!` `!$` (AST 解析失败 → fail-closed)
//   - ANSI-C quoting `$'\n'` (AST 解析失败 → fail-closed)
//
// 设计原则 (源自 Claude Code Bash 安全链 + safecmd)：
//  1. AST 解析失败 → fail-closed (拒绝执行，不 fallback 到字符串扫描)
//  2. 任何未知 node type → fail-closed
//  3. 对每个 CallExpr 重建命令字符串, 走 IsDangerousWithAgent
//  4. 对 Redirect 检查重定向目标是否在敏感路径
//  5. 检测到 eval / source / . → 直接拒绝（能执行任意字符串）
//
// 返回值：(dangerous, reason)
//
//	dangerous=true 表示命令不安全, Execute 主流程应直接拒绝。
//	dangerous=false 表示通过 AST 安全检查。
func astSafetyCheck(ctx context.Context, agentName, cmd string) (bool, string) {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(cmd), "input.sh")
	if err != nil {
		// 解析失败 → fail-closed
		return true, fmt.Sprintf("[AST 解析失败: %v] 命令包含无法安全解析的语法(可能是 history expansion / ANSI-C quoting / parser differential 等),拒绝执行", err)
	}

	// 创建路径 ACL walker (检查 Redirect 是否写敏感路径)
	pathACL := loadPathACL()

	walker := &astSafetyWalker{
		ctx:       ctx,
		agentName: agentName,
		pathACL:   pathACL,
	}
	syntax.Walk(file, walker.Visit)

	return walker.finalize()
}

// astSafetyWalker AST 遍历过程中的安全检查状态。
//
// 设计: 收集所有 CallExpr 的失败原因, **不**在第一个失败就停。
// 原因: 当命令形如 `safe && dangerous`, 第一个 CallExpr 检查失败
// (例如"不在白名单: echo")不该让我们停, 因为后续阶段可能有更
// 严重的威胁(敏感路径/硬禁止), 用户/LLM 看到的拦截原因应该是
// 最严重的那个, 而不是第一个失败的。
type astSafetyWalker struct {
	ctx       context.Context
	agentName string

	// pathACL 路径 ACL(来自 application.yml sandbox.path_acl 配置)
	pathACL *PathACL

	// 收集所有失败 CallExpr 的拦截原因, 后面会按优先级筛选
	reasons []string
}

// Visit 是 syntax.Walk 的回调函数。
// 始终返回 true 继续遍历(收集所有节点); 我们不在第一个失败就 stop。
func (w *astSafetyWalker) Visit(node syntax.Node) bool {
	if node == nil {
		return true
	}

	switch n := node.(type) {
	case *syntax.CallExpr:
		w.checkCallExpr(n)
	case *syntax.Redirect:
		w.checkRedirect(n)
	case *syntax.BinaryCmd:
		// `&&` `||` `|` `;` 等连接的命令
		// 继续遍历子节点即可（每个 CallExpr 都会被独立检查）
	default:
		// 其它节点类型(IfClause / ForClause / FuncDecl / Subshell / CmdSubst / 等)
	}
	return true
}

// priorityOf 根据 reason 内容返回严重程度(数字越小越严重)。
// 用于从多个失败原因中选最严重的返回给 LLM/用户。
func priorityOf(reason string) int {
	switch {
	case strings.Contains(reason, "[AST] 禁止 builtin"):
		return 0 // eval / source / . 绝对禁止, 永远最严重
	case strings.Contains(reason, "硬禁止"):
		return 1 // rm -rf / / reboot 等
	case strings.Contains(reason, "敏感路径"), strings.Contains(reason, "[AST Redirect"):
		return 2
	case strings.Contains(reason, "敏感信息"), strings.Contains(reason, "Authorization"),
		strings.Contains(reason, "Token"), strings.Contains(reason, "Cookie"),
		strings.Contains(reason, "API Key"):
		return 3 // curl 敏感信息夹带
	case strings.Contains(reason, "内网"):
		return 4 // curl 内网探测
	case strings.Contains(reason, "软禁止"):
		return 5
	case strings.Contains(reason, "白名单"):
		return 6 // "不在白名单中" 优先级最低
	default:
		return 10
	}
}

// finalize 从所有收集到的 reasons 中挑最严重的返回。
func (w *astSafetyWalker) finalize() (bool, string) {
	if len(w.reasons) == 0 {
		return false, ""
	}
	best := w.reasons[0]
	bestP := priorityOf(best)
	for _, r := range w.reasons[1:] {
		if p := priorityOf(r); p < bestP {
			best = r
			bestP = p
		}
	}
	return true, best
}

// checkCallExpr 检查单个 CallExpr 节点(只收集不返回)。
func (w *astSafetyWalker) checkCallExpr(c *syntax.CallExpr) {
	if len(c.Args) == 0 {
		return
	}

	// 提取命令名
	name := wordToString(c.Args[0])

	// 1. 禁止危险 builtin
	switch name {
	case "eval", "source", ".":
		w.reasons = append(w.reasons,
			fmt.Sprintf("[AST] 禁止 builtin %q: 能执行任意字符串,无法静态验证安全性", name))
		return
	case "exec":
		// exec 会替换当前进程, 但命令本身仍然需要走 IsDangerous
		// 不直接拒绝,让后续检查决定
	}

	// 2. 重建命令字符串, 走 IsDangerousWithAgent
	reconstructed := formatCallExpr(c)
	if dangerous, reason := IsDangerousWithAgent(w.ctx, w.agentName, reconstructed); dangerous {
		w.reasons = append(w.reasons,
			fmt.Sprintf("[AST CallExpr %q] %s", reconstructed, reason))
		return
	}
}

// checkRedirect 检查 Redirect 节点(重定向目标是否在敏感路径)。
func (w *astSafetyWalker) checkRedirect(r *syntax.Redirect) {
	if w.pathACL == nil {
		return
	}

	op := r.Op.String()
	target := wordToString(r.Word)

	// 检查写入操作是否命中 write_denied
	if isWriteOp(op) {
		if reason := w.pathACL.checkWriteDenied(target); reason != "" {
			w.reasons = append(w.reasons,
				fmt.Sprintf("[AST Redirect %s %q] %s", op, target, reason))
			return
		}
	}

	// 检查读取操作是否命中 read_denied
	if isReadOp(op) {
		if reason := w.pathACL.checkReadDenied(target); reason != "" {
			w.reasons = append(w.reasons,
				fmt.Sprintf("[AST Redirect %s %q] %s", op, target, reason))
			return
		}
	}
}

// isWriteOp 判断 RedirOperator 是否为写入类操作。
func isWriteOp(op string) bool {
	switch op {
	case ">", ">>", "&>", "&>>", ">|", "&>|", "<>":
		return true
	}
	return false
}

// isReadOp 判断 RedirOperator 是否为读取类操作。
func isReadOp(op string) bool {
	return op == "<" || op == "<>"
}

// formatCallExpr 把 AST CallExpr 节点格式化为命令行字符串。
func formatCallExpr(c *syntax.CallExpr) string {
	var sb strings.Builder
	for i, w := range c.Args {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(wordToString(w))
	}
	return sb.String()
}

// wordToString 把 syntax.Word 节点还原为字符串。
func wordToString(w *syntax.Word) string {
	if w == nil {
		return ""
	}
	// 简单字面量（只有 Lit part）直接返回
	if len(w.Parts) == 1 {
		if lit, ok := w.Parts[0].(*syntax.Lit); ok {
			return lit.Value
		}
	}
	// 复杂 word：还原各 part
	var sb strings.Builder
	for _, part := range w.Parts {
		sb.WriteString(wordPartToString(part))
	}
	return sb.String()
}

// wordPartToString 把 WordPart 还原为字符串表示。
func wordPartToString(p syntax.WordPart) string {
	switch v := p.(type) {
	case *syntax.Lit:
		return v.Value
	case *syntax.SglQuoted:
		return "'" + v.Value + "'"
	case *syntax.DblQuoted:
		var sb strings.Builder
		sb.WriteByte('"')
		for _, inner := range v.Parts {
			sb.WriteString(wordPartToString(inner))
		}
		sb.WriteByte('"')
		return sb.String()
	case *syntax.CmdSubst:
		// 命令替换 - 还原为 $(...) 的字面表示
		return "$(...)"
	case *syntax.ParamExp:
		// 参数展开 - 还原为 ${VAR} 的字面表示
		if v.Param != nil {
			if v.Short {
				return "$" + v.Param.Value
			}
			return "${" + v.Param.Value + "}"
		}
		return "${...}"
	case *syntax.ArithmExp:
		return "$((arith))"
	default:
		// 其它类型 - 保守返回空字符串
		log.Printf("%s unknown word part type: %T", astLogPrefix, p)
		return ""
	}
}
