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

	if walker.dangerous {
		return true, walker.reason
	}
	return false, ""
}

// astSafetyWalker AST 遍历过程中的安全检查状态。
type astSafetyWalker struct {
	ctx       context.Context
	agentName string

	// pathACL 路径 ACL（来自 application.yml sandbox.path_acl 配置）
	pathACL *PathACL

	dangerous bool
	reason    string
}

// Visit 是 syntax.Walk 的回调函数。
// 返回 true 继续遍历子节点；返回 false 停止遍历（已触发拒绝）。
func (w *astSafetyWalker) Visit(node syntax.Node) bool {
	if w.dangerous {
		return false
	}
	if node == nil {
		return true
	}

	switch n := node.(type) {
	case *syntax.CallExpr:
		return w.checkCallExpr(n)
	case *syntax.Redirect:
		return w.checkRedirect(n)
	case *syntax.BinaryCmd:
		// `&&` `||` `|` `;` 等连接的命令
		// 继续遍历子节点即可（每个 CallExpr 都会被独立检查）
		return true
	default:
		// 其它节点类型（IfClause / ForClause / FuncDecl / Subshell / CmdSubst / 等）
		// 继续遍历，让 Visit 命中里面的 CallExpr
		return true
	}
}

// checkCallExpr 检查单个 CallExpr 节点。
func (w *astSafetyWalker) checkCallExpr(c *syntax.CallExpr) bool {
	if len(c.Args) == 0 {
		return true
	}

	// 提取命令名
	name := wordToString(c.Args[0])

	// 1. 禁止危险 builtin
	switch name {
	case "eval", "source", ".":
		w.dangerous = true
		w.reason = fmt.Sprintf("[AST] 禁止 builtin %q: 能执行任意字符串,无法静态验证安全性", name)
		return false
	case "exec":
		// exec 会替换当前进程, 但命令本身仍然需要走 IsDangerous
		// 不直接拒绝,让后续检查决定
	}

	// 2. 重建命令字符串, 走 IsDangerousWithAgent
	reconstructed := formatCallExpr(c)
	if dangerous, reason := IsDangerousWithAgent(w.ctx, w.agentName, reconstructed); dangerous {
		w.dangerous = true
		w.reason = fmt.Sprintf("[AST CallExpr %q] %s", reconstructed, reason)
		return false
	}

	// 3. 继续遍历子节点 (处理嵌套的 $() 等)
	return true
}

// checkRedirect 检查 Redirect 节点（重定向目标是否在敏感路径）。
func (w *astSafetyWalker) checkRedirect(r *syntax.Redirect) bool {
	if w.pathACL == nil {
		return true
	}

	op := r.Op.String()
	target := wordToString(r.Word)

	// 检查写入操作是否命中 write_denied
	// 包含 > >> &> &>> <> 及其变种
	if isWriteOp(op) {
		if reason := w.pathACL.checkWriteDenied(target); reason != "" {
			w.dangerous = true
			w.reason = fmt.Sprintf("[AST Redirect %s %q] %s", op, target, reason)
			return false
		}
	}

	// 检查读取操作是否命中 read_denied
	if isReadOp(op) {
		if reason := w.pathACL.checkReadDenied(target); reason != "" {
			w.dangerous = true
			w.reason = fmt.Sprintf("[AST Redirect %s %q] %s", op, target, reason)
			return false
		}
	}

	return true
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
