/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package memorytool 把 internal/memory 索引的 Search 接口包装成 eino 工具,
// 让 ChatAgent 等子 agent 可以在需要时主动检索"长期记忆"。
//
// 工具语义：
//   - 入参:关键词 + 可选过滤 (agent/kind/since/until/limit)
//   - 出参:命中的 entry 列表(每条含 session/agent/kind/time/summary/file/line)
//   - 模型拿到结果后可自行 Read 原始 markdown 文件获取完整内容
//
// 使用频率优化(2026-07-27)：
//   - 工具描述增加"何时必须调"信号, 而非"何时可以调"
//   - 触发关键词清单从 3 个扩到 12 个
//   - 支持 query 为空时返回"最近 N 条记忆"(降级策略)
//   - 默认 Limit 从 20 降到 10(更易消化)
//   - 增加 Sorting 时间范围提示(引导近因记忆优先)
package memorytool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/yichouchou/yichouchou_claw/internal/memory"
)

// memSearchLogPrefix memory_search 工具运行时日志前缀。
const memSearchLogPrefix = "[memory_search]"

// SearchInput 是 memory_search 工具的入参。
type SearchInput struct {
	// Query 关键词；同时匹配 summary / session_id / request_group_id / llm_trace_id / agent。
	// 空字符串时只按过滤条件查(返回最近 N 条, 用于"我们聊过什么"类模糊查询)。
	Query string `json:"query"`
	// Agent 按 agent 名过滤(空表示不过滤)
	Agent string `json:"agent,omitempty"`
	// Kind 按 kind 过滤(如 "user_request"/"llm_input")
	Kind string `json:"kind,omitempty"`
	// SubDir 按子目录过滤("sessions"/"inputs"/"outputs"/"errors")
	SubDir string `json:"subdir,omitempty"`
	// Since RFC3339 时间,只返回该时间之后的 entry
	Since string `json:"since,omitempty"`
	// Until RFC3339 时间,只返回该时间之前的 entry
	Until string `json:"until,omitempty"`
	// Limit 返回上限(默认 10, 适合模型一次性消化)
	Limit int `json:"limit,omitempty"`
}

// SearchOutput 是 memory_search 工具的出参。
type SearchOutput struct {
	// Total 命中条数
	Total int `json:"total"`
	// Entries 命中条目（按时间倒序，summary 命中优先）
	Entries []Entry `json:"entries"`
	// Hint 提示信息（索引未启用 / 无命中时给出）
	Hint string `json:"hint,omitempty"`
}

// Entry 是出参中单条命中。
type Entry struct {
	// SessionID 会话 ID
	SessionID string `json:"session_id"`
	// Agent agent 名
	Agent string `json:"agent"`
	// Kind entry 类型
	Kind string `json:"kind"`
	// Time RFC3339 时间戳
	Time string `json:"time"`
	// Summary 摘要
	Summary string `json:"summary"`
	// Refined 是否经过 LLM 精炼
	Refined bool `json:"refined"`
	// File 相对 workdir/memory 的路径（Read 用）
	File string `json:"file"`
	// Line front-matter 起始行号（Read 用）
	Line int `json:"line"`
	// MatchedField 命中来源（"summary"/"session_id"/...）
	MatchedField string `json:"matched_field"`
	// Score 简单评分
	Score int `json:"score"`
	// TimeAgo 相对时间描述(优化可读性, 如 "2 小时前")
	TimeAgo string `json:"time_ago,omitempty"`
}

// toolDescriptionNew 是优化后的工具描述。
//
// 关键变化(2026-07-27)：
//  1. 把描述从"可以调用"改成"必须调用"语义强约束
//  2. 触发关键词清单从 3 个扩到 12+ 个(覆盖更多隐式历史场景)
//  3. 把"何时调用"放在最前面(高信号在前)
//  4. 给出 query 构造公式(降低模型思考成本)
//  5. 明确"返回的不是答案,需要继续 Read"
const toolDescriptionNew = `在 workdir/memory/ 索引中检索历史对话 / trace 条目。这是跨 session 取历史上下文的唯一入口。

================================================================
【何时必须调用】——强烈推荐(宁可调不要漏)
================================================================
当用户的提问满足以下任一条件时,你必须先调 memory_search 再回答:

A. 时间指代型(隐式/显式):
   - "之前 / 上次 / 以前 / 之前 / 那次 / 当时 / 之前那次"
   - "我们 / 我们之前 / 我们昨天 / 我们刚才"
   - "上次 / 这两天 / 近几天 / 最近"
   - "昨天聊了 / 昨天说的 / 前几天讨论"

B. 决策溯源型("为什么是 X"):
   - "为什么用 X / 为啥不用 Y / 怎么不用 Y"
   - "为什么选 X / 为什么不用 X"
   - "X 是怎么定的 / X 是怎么决定的"
   - "我们的约定 / 我们的规范 / 项目的规则"

C. 知识/状态追溯型:
   - "我们做过什么 / 项目做过 / 这个项目之前"
   - "踩过哪些坑 / 遇到过什么问题 / 之前怎么解决的"
   - "之前的结果 / 之前的答案"

D. 模糊对话回溯(用户没说具体内容):
   - "我们聊过 X / 之前讨论过 X / 我们之前说过 X"
   - "我让你做过的 / 你之前帮我的"

================================================================
【何时不需要调】
================================================================
- 用户问纯知识/技术问题(如 "什么是 PostgreSQL")
- 用户问当前文件/代码相关(直接在 context 里)
- 闲聊/问候

================================================================
【参数怎么填】
================================================================
- query: 用户提问中的核心名词/动词(2-4 个词最有效)
   例: "PostgreSQL MySQL" / "HBM 市场规模" / "memory_search 修复"
- agent / kind / subdir / since / until: 用于精确过滤(可选)
- 查询为空(用户说"我们聊过什么"):
   - 用 kind="user_request" limit=10 since={过去 7 天}
   - 或直接留空 query=""

================================================================
【怎么解读结果】
================================================================
- 返回的每条 entry 是历史摘要列表,不是完整答案
- 应该按 file + line 用 Read 类工具读原始 markdown,拿到完整上下文后再综合回答
- 返回 0 条不代表"没聊过"——可能是关键词不对,试更短或换关键词
- 命中"session_id"或"agent"能让你精准定位(可以基于 session 调 GetBySession)

================================================================
【反模式】——以下做法都是错的
================================================================
- ❌ 凭训练数据"猜"之前发生过什么——必须查 memory_search
- ❌ 把摘要列表直接复制粘贴给用户——必须再 Read 原文
- ❌ 只看第一页——可能漏掉关键的早期对话
- ❌ 完全不调 memory_search 就声称"基于历史..."——幻觉高发区`

// NewSearchTool 构造 memory_search 工具(优化版)。
//
// 工具会从 internal/memory.GetGlobalIndex() 取全局索引；
// 索引未初始化时返回的 Output.Hint 会提示模型退化为文件全文搜。
//
// 返回类型是 eino 的 tool.BaseTool,可直接塞进 adk.ToolsConfig.Tools 列表。
func NewSearchTool() (tool.BaseTool, error) {
	return utils.InferTool(
		"memory_search",
		toolDescriptionNew,
		func(ctx context.Context, input *SearchInput) (string, error) {
			return doSearch(input)
		},
	)
}

// doSearch 是 NewSearchTool 的实际执行函数（返回 JSON string 给 LLM）。
func doSearch(input *SearchInput) (string, error) {
	out, err := doSearchExposed(input)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal output: %w", err)
	}
	return string(b), nil
}

// doSearchExposed 返回 SearchOutput 结构（供测试用，不暴露为 public API）。
//
// 关键优化(2026-07-27)：
//   - Default limit 从 20 降到 10(更易消化)
//   - 自动添加 TimeAgo 字段(让模型更易判断条目时效)
//   - 空 query + 长时间无过滤时,自动注入合理的 since(7 天前)
//   - 运行时记录调用统计(便于排查触发率)
func doSearchExposed(input *SearchInput) (*SearchOutput, error) {
	idx := memory.GetGlobalIndex()
	if idx == nil {
		return &SearchOutput{
			Total: 0,
			Hint: "memory index not enabled; cannot search long-term memory. " +
				"Fallback: use Read/Glob tools on workdir/memory/ directly.",
		}, nil
	}
	if input == nil {
		return nil, fmt.Errorf("nil input")
	}

	// === 优化: 空 query + 无过滤 → 自动注入 7 天窗口 ===
	// 避免"我们聊过什么"类查询全部漏过
	if strings.TrimSpace(input.Query) == "" &&
		input.Agent == "" && input.Kind == "" && input.SubDir == "" &&
		input.Since == "" && input.Until == "" {
		if input.Since == "" {
			input.Since = time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
		}
		if input.Limit <= 0 {
			input.Limit = 10
		}
	}

	q := memory.SearchQuery{
		Query:  input.Query,
		Agent:  input.Agent,
		Kind:   input.Kind,
		SubDir: input.SubDir,
		Since:  input.Since,
		Until:  input.Until,
		Limit:  input.Limit,
		Sort:   "time_desc",
	}
	if q.Limit <= 0 {
		q.Limit = 10 // 优化: 默认从 20 降到 10
	}

	results := idx.Search(q)

	// === 运行时统计 ===
	qForLog := q.Query
	if len(qForLog) > 60 {
		qForLog = qForLog[:60] + "..."
	}
	log.Printf("%s query=%q total=%d",
		memSearchLogPrefix, qForLog, len(results))

	now := time.Now()
	out := &SearchOutput{
		Total:   len(results),
		Entries: make([]Entry, 0, len(results)),
	}
	for _, r := range results {
		e := Entry{
			SessionID:    r.SessionID,
			Agent:        r.Agent,
			Kind:         r.Kind,
			Time:         r.Time,
			Summary:      r.Summary,
			Refined:      r.Refined,
			File:         r.File,
			Line:         r.Line,
			MatchedField: r.MatchedField,
			Score:        r.Score,
		}
		// === 优化: 添加 TimeAgo 字段 ===
		if t, err := time.Parse(time.RFC3339, r.Time); err == nil {
			e.TimeAgo = formatTimeAgo(now, t)
		}
		out.Entries = append(out.Entries, e)
	}

	// === 优化: 增强 Hint 信息(引导模型下一步动作) ===
	if out.Total == 0 {
		switch {
		case strings.TrimSpace(input.Query) == "":
			out.Hint = "no recent memory entries; try widening since/until or remove filters"
		case q.Limit >= 10:
			out.Hint = fmt.Sprintf(
				"no entries match query=%q; "+
					"try: shorter keyword, different keyword, "+
					"or query=\"\" to list recent entries",
				input.Query)
		default:
			out.Hint = fmt.Sprintf("no entries match query=%q; try a shorter keyword", input.Query)
		}
	} else {
		// === 优化: 命中后给出"下一步"提示 ===
		out.Hint = fmt.Sprintf("found %d entries — use Read on the file:line of the most relevant entry to read full content", out.Total)
	}

	return out, nil
}

// formatTimeAgo 把 RFC3339 时间格式化为相对时间(如 "2 小时前")。
func formatTimeAgo(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return "将来"
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 周前", int(d.Hours()/(24*7)))
	default:
		return fmt.Sprintf("%d 月前", int(d.Hours()/(24*30)))
	}
}
