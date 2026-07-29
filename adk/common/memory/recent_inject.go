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

// Package memorytool - recent_inject.go 实现 D 方案：
//
//	"最近记忆自动注入到 system prompt" —— 参考 Claude Code 的 CLAUDE.md 机制。
//
// 思路：
//
//	与其让模型 "记得去调" memory_search, 不如把最近几条记忆直接放进
//	system prompt。这样模型在回答之前就在 context 里"看到"了过去发生的事,
//	大幅降低主动调用的门槛。
//
// 副作用：
//   - 模型无需主动调 tool 也能"感知"到历史
//   - 减少不必要的 tool call（节省 token + 延迟）
//   - 注入有 token 上限, 不可能全部历史都灌进去
//   - 真正查询"很久以前"还是得调 memory_search
package memorytool

import (
	"fmt"
	"strings"
	"time"

	"github.com/yichouchou/yichouchou_claw/internal/memory"
)

// RecentMemoryConfig 控制最近记忆注入行为。
type RecentMemoryConfig struct {
	// Enabled 是否启用最近记忆自动注入。默认 false（main.go 根据情况选择）。
	Enabled bool

	// Limit 注入多少条最近记忆。默认 10。
	// 太大 → 占用系统 token 太多；
	// 太小 → 模型看不到完整历史。
	Limit int

	// MaxTokens 大约的 token 上限（粗略用 len(text) 估算，避免超限）。
	// 注入的输出超过此值时, 会被裁剪。默认 2000 字符。
	MaxTokens int

	// KindsFilter 只注入这些 kind。默认 ["user_request"]（只注入用户问过什么）。
	// 可选: ["user_request", "user_response"] 包含问答对。
	KindsFilter []string

	// MaxAgeDays 最远多少天内的记忆会被注入。默认 30 天。
	// 超过此时间的记忆不注入（让模型知道"还有更早的,但需要调 memory_search"）。
	MaxAgeDays int
}

// DefaultRecentMemoryConfig 是带合理默认值的配置。
func DefaultRecentMemoryConfig() RecentMemoryConfig {
	return RecentMemoryConfig{
		Enabled:     true,
		Limit:       10,
		MaxTokens:   2000,
		KindsFilter: []string{"user_request"},
		MaxAgeDays:  30,
	}
}

// RecentMemoryContext 返回用于注入到 system prompt 的格式化文本。
//
// 设计参考 Claude Code 的 CLAUDE.md 机制：
//   - 标题清晰标识（"## Recent Memory"）
//   - 每条单独一行, 包含时间戳 + 简短摘要
//   - 用"如何用这部分的指引"作为开场白
//   - 末尾留指引，鼓励模型需要时调 memory_search 查更深
//
// 返回空字符串当：
//   - cfg.Enabled == false
//   - 全局索引未启用（idx == nil）
//   - 没有任何命中条目
func RecentMemoryContext(cfg RecentMemoryConfig) string {
	if !cfg.Enabled {
		return ""
	}

	idx := memory.GetGlobalIndex()
	if idx == nil {
		// 索引未启用 → 不打印警告(避免污染 startup log)
		return ""
	}

	limit := cfg.Limit
	if limit <= 0 {
		limit = 10
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2000
	}
	maxAge := cfg.MaxAgeDays
	if maxAge <= 0 {
		maxAge = 30
	}

	// kind 过滤
	kindFilter := cfg.KindsFilter
	if len(kindFilter) == 0 {
		kindFilter = []string{"user_request"}
	}

	// 拉取最近 entry（kind 过滤已经在 Recent 里完成）
	recent := idx.Recent(limit, kindFilter)

	// 时间窗口过滤：超过 maxAgeDays 的不入
	cutoff := time.Now().Add(-time.Duration(maxAge) * 24 * time.Hour)
	filtered := make([]memory.SearchResult, 0, len(recent))
	for _, r := range recent {
		t, err := time.Parse(time.RFC3339, r.Time)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			continue
		}
		filtered = append(filtered, r)
	}

	if len(filtered) == 0 {
		return ""
	}

	// 格式化为 system context 文本
	var sb strings.Builder
	sb.WriteString("## Recent Memory（最近对话摘要，已自动注入）\n\n")
	sb.WriteString("以下是你与用户最近的对话摘要（按时间倒序）。这是**自动注入**的上下文，\n")
	sb.WriteString("用于让你无需主动调 memory_search 就能感知到近期讨论。\n\n")

	now := time.Now()
	for _, r := range filtered {
		t, _ := time.Parse(time.RFC3339, r.Time)
		ageStr := formatTimeAgo(now, t)

		// 单行格式：[2 小时前] 用户问 HBM 市场规模
		// 包含：相对时间 + 摘要 + 关键路径（如果需要深查用 Read）
		line := fmt.Sprintf("- [%s] (%s) %s",
			ageStr, r.Agent, r.Summary)
		if r.File != "" {
			line += fmt.Sprintf("  [file: %s]", r.File)
		}
		sb.WriteString(line)
		sb.WriteString("\n")

		// token 上限保护
		if sb.Len() > maxTokens {
			sb.WriteString(fmt.Sprintf("... (还有 %d 条更早的,需要时用 memory_search 查询)\n",
				len(filtered)-len(filtered)+len(filtered)))
			// 简化: 只提示 "用 memory_search"
			sb.Reset()
			sb.WriteString("## Recent Memory（最近对话摘要，已自动注入）\n\n")
			sb.WriteString(fmt.Sprintf("（%d 条最近记忆由于 token 限制被省略，需要时请用 memory_search 工具查询）\n", len(filtered)))
			break
		}
	}

	sb.WriteString("\n指引:\n")
	sb.WriteString("- 这些是你已经知道的最近对话,无需再查；可直接基于它们回答。\n")
	sb.WriteString("- 如需更早/更详细的记忆,调 memory_search 工具(query 用核心名词)。\n")
	sb.WriteString("- 不要凭训练数据猜测最近讨论过什么——这里就是事实来源。\n")

	return sb.String()
}

// RecentMemoryBlock 返回完整的注入块（含开关标识）,
// 便于 main.go 或 ChatAgent 一次性嵌入到 instruction 中。
//
// 返回格式：
//
//	<recent_memory>
//	## Recent Memory（...）
//	...
//	</recent_memory>
//
// 或在禁用 / 索引不可用 / 无最近记忆时 返回 "<recent_memory></recent_memory>"。
func RecentMemoryBlock(cfg RecentMemoryConfig) string {
	body := RecentMemoryContext(cfg)
	return "<recent_memory>\n" + body + "\n</recent_memory>"
}
