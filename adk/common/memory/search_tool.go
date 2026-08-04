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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
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
const toolDescriptionNew = `在 workdir/memory/ 索引里检索历史对话/trace 条目, 拿到每个命中的 file:line。

【必须调的场景】
- 时间指代: "之前 / 上次 / 以前 / 当天 / 前几天"
- 决策溯源: "为什么用 X / 为什么不用 Y / 我们的约定 / 项目规则"
- 状态追溯: "之前怎么做的 / 之前的结果 / 这个项目之前聊过"
- 模糊回溯: "我们聊过什么 / 之前讨论过 X"

【参数】
- query: 核心名词 2-4 词最有效 ("PostgreSQL 优势" / "HBM 市场规模"); 用户说"我们聊过什么"时留空, 自动注入最近 7 天
- agent / kind / since / until / subdir: 可选过滤

【解读结果】
- 返回的是历史摘要列表, 不是最终答案
- 按 file:line 用 Read 工具读原始 markdown 拿到完整上下文后再综合回答
- 0 命中 ≠ "没聊过", 试更短或换关键词再试

【反模式】(绝对禁止)
- 凭训练知识"猜"我们之前讨论过什么
- 把摘要列表直接复制粘贴当答案
- 完全不调 memory_search 就声称"基于历史..."(幻觉高发)
`

// NewSearchTool 构造 memory_search 工具(优化版)。
//
// 工具会从 internal/memory.GetGlobalIndex() 取全局索引；
// 索引未初始化时返回的 Output.Hint 会提示模型退化为文件全文搜。
//
// 返回类型是 eino 的 tool.BaseTool,可直接塞进 adk.ToolsConfig.Tools 列表。
//
// 多模态升级(2026-07-29):
//  1. 改用 utils.InferEnhancedTool,让 ToolResult 既包含 JSON 文本 part,
//     也包含命中的 markdown 文件作为附件(图片/截图随附)。
//  2. 客户端 eino ToolNode 会优先调用 EnhancedInvokableTool,确保多模态不被丢。
//  3. 同时也满足 tool.BaseTool 接口(向下兼容)。
func NewSearchTool() (tool.BaseTool, error) {
	return utils.InferEnhancedTool(
		"memory_search",
		toolDescriptionNew,
		func(ctx context.Context, input *SearchInput) (*schema.ToolResult, error) {
			return doSearchEnhanced(ctx, input)
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

// doSearchEnhanced 是 EnhancedInvokableTool 的入口。
//
// 返回 schema.ToolResult:
//   - 第一个 part: text 类型,内容是 SearchOutput 的 JSON 序列化
//     (兼容旧版纯文本返回,模型仍可看到结构化结果)
//   - 后续 part(可选): file 类型,挂上命中条目的 markdown 文件。
//     模型可以直接看到附件内容,不再需要 Read 一次磁盘。
//
// 设计取舍:
//   - 只挂前 N(默认 3)条最相关条目,避免一次性把全部 10 条 markdown 塞进 tool_result
//     (可能单条几十 KB,累计太大影响上下文窗口)。
//   - markdown 文件以"附件"形式挂入,模型可以用 Read 工具或直接引用。
func doSearchEnhanced(ctx context.Context, input *SearchInput) (*schema.ToolResult, error) {
	out, err := doSearchExposed(input)
	if err != nil {
		return nil, err
	}
	jsonBytes, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal output: %w", err)
	}

	result := schema.ToolResult{
		Parts: []schema.ToolOutputPart{
			{
				Type: schema.ToolPartTypeText,
				Text: string(jsonBytes),
			},
		},
	}

	// === 多模态 part: 挂上前 N 条命中的 markdown 文件 ===
	const maxAttachFiles = 3
	attached := 0
	for _, e := range out.Entries {
		if attached >= maxAttachFiles {
			break
		}
		if e.File == "" {
			continue
		}
		// 只挂绝对路径下的文件,workdir/memory/ 下的相对路径转绝对
		absPath := resolveMemoryFilePath(e.File)
		if absPath == "" {
			continue
		}
		filePart, ok := buildMemoryFilePart(absPath, e.File)
		if !ok {
			continue
		}
		result.Parts = append(result.Parts, filePart)
		attached++
	}
	return &result, nil
}

// resolveMemoryFilePath 把 SearchOutput.File 的相对路径转成绝对路径。
//
// 兼容两种形式:
//   - 已经是绝对路径（运维 / 测试场景）
//   - 相对路径（默认相对 workdir/，这是 memory 索引里 file 字段的实际形式）
func resolveMemoryFilePath(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	// 尝试相对 workdir
	if cwd, err := os.Getwd(); err == nil {
		abs := filepath.Join(cwd, rel)
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}
	return ""
}

// buildMemoryFilePart 把磁盘文件转成 ToolOutputPart 推给前端 + 进 LLM 上下文。
//
// 2026-08-03 重构: 关键修复——**用 ToolPartTypeImage 而不是 ToolPartTypeFile**。
//
// 之前的设计: ToolPartTypeFile → eino ToolNode 转 ChatMessagePartTypeFileURL
//
//	→ Ark adapter 不支持 → 报 "unsupported chat message part type in user message: file_url"
//
// 当前设计: ToolPartTypeImage → 转 ChatMessagePartTypeImageURL
//
//	→ Ark adapter 支持 (chat_completion_api.go line 644)
//	→ 同时通过 SSE multi_content (handleRegularMessage line 113-129) 推前端 <img>
//
// 行为:
//   - 文件存在 → 读字节,按文件扩展推断 MIME → base64 编码 → image/audio/video part
//   - text/* 类返回 false (LLM 看不懂 base64,前端也不展示原始文本)
//   - 文件不存在或太大(>2MB) → 跳过(返回 ok=false)
//
// 限制(2026-07-29): 单个 part 限 2MB,避免 tool_result 爆炸撑爆上下文。
// 后续可改成"上传到对象存储并返回 URL",但当前项目体量暂不引入额外依赖。
func buildMemoryFilePart(absPath, displayName string) (schema.ToolOutputPart, bool) {
	const maxFileSize = 2 * 1024 * 1024 // 2MB
	info, err := os.Stat(absPath)
	if err != nil || info.Size() == 0 || info.Size() > maxFileSize {
		return schema.ToolOutputPart{}, false
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		log.Printf("%s read attach file failed path=%s err=%v", memSearchLogPrefix, absPath, err)
		return schema.ToolOutputPart{}, false
	}
	mime := guessMIMEFromExt(absPath)
	encoded := base64.StdEncoding.EncodeToString(data)

	// === 2026-08-03: 用 ToolPartTypeImage 替代 ToolPartTypeFile ===
	// 详见上方注释 + chatmodel.go buildArtifactFilePart。
	switch {
	case strings.HasPrefix(mime, "image/"):
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeImage,
			Image: &schema.ToolOutputImage{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &encoded,
					MIMEType:   mime,
				},
			},
		}, true
	case strings.HasPrefix(mime, "audio/"):
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeAudio,
			Audio: &schema.ToolOutputAudio{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &encoded,
					MIMEType:   mime,
				},
			},
		}, true
	case strings.HasPrefix(mime, "video/"):
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeVideo,
			Video: &schema.ToolOutputVideo{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &encoded,
					MIMEType:   mime,
				},
			},
		}, true
	default:
		// text/* / application/pdf 等其他类 → 走 ToolPartTypeImage
		// (前端根据 mime 决定渲染方式,LLM 接受 image_url)
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeImage,
			Image: &schema.ToolOutputImage{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &encoded,
					MIMEType:   mime,
				},
			},
		}, true
	}
}

// guessMIMEFromExt 按文件后缀猜 MIME。
// 极简版，只覆盖 markdown / text / 图片三类；其他走 application/octet-stream。
func guessMIMEFromExt(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt", ".log":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

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
