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
package memorytool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/yichouchou/yichouchou_claw/internal/memory"
)

// SearchInput 是 memory_search 工具的入参。
type SearchInput struct {
	// Query 关键词；同时匹配 summary / session_id / request_group_id / llm_trace_id / agent。
	// 空字符串时只按过滤条件查（返回最近 N 条）。
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
	// Limit 返回上限(默认 20)
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
}

// NewSearchTool 构造 memory_search 工具。
//
// 工具会从 internal/memory.GetGlobalIndex() 取全局索引；
// 索引未初始化时返回的 Output.Hint 会提示模型退化为文件全文搜。
//
// 返回类型是 eino 的 tool.BaseTool,可直接塞进 adk.ToolsConfig.Tools 列表。
func NewSearchTool() (tool.BaseTool, error) {
	return utils.InferTool(
		"memory_search",
		"在 workdir/memory/ 索引中检索历史对话/trace 条目。\n"+
			"返回每条命中 entry 的 session/agent/kind/time/summary/file/line，\n"+
			"模型可基于 file:line 用 Read 类工具读取完整内容。\n"+
			"典型用法：用户提到「之前/上次/以前」或需要历史决策依据时调用。",
		func(ctx context.Context, input *SearchInput) (string, error) {
			return doSearch(input)
		},
	)
}

func doSearch(input *SearchInput) (string, error) {
	idx := memory.GetGlobalIndex()
	if idx == nil {
		out := SearchOutput{
			Total: 0,
			Hint:  "memory index not enabled; cannot search long-term memory",
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	}
	if input == nil {
		return "", fmt.Errorf("nil input")
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
		q.Limit = 20
	}

	results := idx.Search(q)
	out := SearchOutput{
		Total:   len(results),
		Entries: make([]Entry, 0, len(results)),
	}
	for _, r := range results {
		out.Entries = append(out.Entries, Entry{
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
		})
	}
	if out.Total == 0 {
		// 给出使用提示
		if strings.TrimSpace(input.Query) == "" {
			out.Hint = "no entries match the given filters; try widening since/until or removing filters"
		} else {
			out.Hint = fmt.Sprintf("no entries match query=%q; try a shorter or different keyword", input.Query)
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("marshal output: %w", err)
	}
	return string(b), nil
}

// _ 占用 context/time 引用,避免 unused 警告
var (
	_ = context.Background
	_ = time.Now
)
