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

// Package memory - index.go 实现 memory 长期记忆的"快速查询"索引。
//
// 设计目标：
//   - 把每条 entry 的关键字段（session_id/agent/kind/time/summary/file/line）抽出
//     到独立的 JSONL 索引文件，避免每次查询都全量扫 markdown。
//   - 写入路径（AsyncMarkdownRecorder worker 写盘成功后）自动 append 一行索引。
//   - LLM refine 异步覆盖 summary 时，同步更新索引中对应 entry 的 summary 字段。
//   - 启动时若索引文件丢失，全量扫描 workdir/memory/**/*.md 重建。
//
// 文件格式：<workdir>/memory/.index.jsonl
//
//	每行一个 JSON 对象（IndexEntry），append-only。
//	同一 file:line 在 refine 后会"原地更新"summary 字段；实现方式是：
//	在内存 map 中更新 + 标记 dirty；定期（或 Flush 时）把整个索引重写。
package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// indexLogPrefix 索引相关日志前缀。
const indexLogPrefix = "[memory.index]"

// IndexFileName 是 memory 索引文件名，位于 workdir/memory/ 下。
const IndexFileName = ".index.jsonl"

// IndexEntry 是单条 entry 在索引中的结构。
//
// 字段含义：
//   - ID           唯一 ID：file:line，例如 "sessions/2026-07-23/10h.md:42"
//   - SubDir       sessions/inputs/outputs/errors 之一
//   - File         相对 workdir/memory/ 的路径，例如 "sessions/2026-07-23/10h.md"
//   - Line         entry 在文件中的起始行号（front-matter 的 --- 行）
//   - SessionID    浏览器/客户端用户会话 ID
//   - LLMTraceID   LLM 调用 trace ID（sessions 类可能为空）
//   - RequestGroupID 本次浏览器请求组 ID（sessions 类有值，其他为空）
//   - Agent        agent 名称
//   - Kind         entry 类型（user_request / llm_input / ...）
//   - Time         RFC3339 时间戳
//   - Summary      摘要（规则版初始写入；refine 后覆盖）
//   - Refined      是否已被 LLM refine 过（true 后 summary 不再被规则版覆盖）
type IndexEntry struct {
	ID             string `json:"id"`
	SubDir         string `json:"subdir"`
	File           string `json:"file"`
	Line           int    `json:"line"`
	SessionID      string `json:"session_id"`
	LLMTraceID     string `json:"llm_trace_id,omitempty"`
	RequestGroupID string `json:"request_group_id,omitempty"`
	Agent          string `json:"agent"`
	Kind           string `json:"kind"`
	Time           string `json:"time"` // RFC3339
	Summary        string `json:"summary"`
	Refined        bool   `json:"refined"`
}

// ID 计算 entry 唯一 ID = File:Line。
func (e *IndexEntry) ComputeID() string {
	return fmt.Sprintf("%s:%d", e.File, e.Line)
}

// Index 是内存中的索引 + 索引文件持久化的封装。
//
// 并发模型：
//   - mu 保护内存 map
//   - Append 走 mu.Lock + 直接追加到 JSONL 文件（无读改写竞争）
//   - UpdateSummary 走 mu.Lock + 标记 dirty；周期性/Flush 时持久化
//
// 性能：
//   - 内存查询 O(1) map lookup 或 O(n log n) 排序遍历
//   - 持久化 append O(1)；重写 O(n) 但只 dirty 时触发
type Index struct {
	mu sync.RWMutex

	root string // workdir/memory/

	// entries 按 ID 索引；append 顺序由 order 维护（用于时间排序）
	entries map[string]*IndexEntry
	order   []string // entries 的插入顺序

	// dirty 标记是否有 in-memory 修改未持久化
	dirty bool

	// appender 是 append-only writer，带缓冲
	appender *bufio.Writer
	file     *os.File
}

// NewIndex 从 workdir/memory 加载（不存在则全量重建）索引。
//
// root 是 workdir/memory 目录（已存在的四象限 sessions/inputs/outputs/errors 上级）。
func NewIndex(root string) (*Index, error) {
	idx := &Index{
		root:    root,
		entries: make(map[string]*IndexEntry),
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", root, err)
	}

	idxPath := filepath.Join(root, IndexFileName)
	if _, err := os.Stat(idxPath); err == nil {
		// 索引文件存在 → 加载
		if err := idx.load(idxPath); err != nil {
			log.Printf("%s load failed: %v, falling back to rebuild", indexLogPrefix, err)
			idx.entries = make(map[string]*IndexEntry)
			idx.order = nil
		}
	} else {
		// 索引文件不存在 → 全量重建
		log.Printf("%s no index file found at %s, rebuilding from markdown", indexLogPrefix, idxPath)
		if err := idx.rebuildFromMarkdown(); err != nil {
			return nil, fmt.Errorf("rebuild index: %w", err)
		}
		// 重建后立刻把内存里的索引写到 idxPath（此时 appender 还没开，避免 Windows 上文件锁冲突）
		if err := idx.writeEntireIndex(idxPath); err != nil {
			return nil, fmt.Errorf("write rebuilt index: %w", err)
		}
		idx.dirty = false
	}

	// 打开 appender（append-only,O_APPEND）
	if err := idx.openAppender(); err != nil {
		return nil, fmt.Errorf("open appender: %w", err)
	}

	return idx, nil
}

// writeEntireIndex 把整个内存索引写到 idxPath（全量写，不经 appender）。
//
// 用在 rebuildFromMarkdown 之后：此时 dirty=true 但 appender 尚未打开。
// 之后 Append 增量通过 appender 追加,UpdateSummary 走 Flush() 重写。
func (i *Index) writeEntireIndex(idxPath string) error {
	tmp := idxPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	w := bufio.NewWriterSize(f, 32*1024)
	for _, id := range i.order {
		e := i.entries[id]
		if e == nil {
			continue
		}
		data, err := json.Marshal(e)
		if err != nil {
			_ = f.Close()
			os.Remove(tmp)
			return fmt.Errorf("marshal %s: %w", id, err)
		}
		if _, err := w.WriteString(string(data) + "\n"); err != nil {
			_ = f.Close()
			os.Remove(tmp)
			return fmt.Errorf("write %s: %w", id, err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, idxPath); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, idxPath, err)
	}
	return nil
}

// load 从 JSONL 文件加载索引到内存。
func (i *Index) load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 索引行不会太长（< 4KB），默认 64KB 缓冲够用
	scanner.Buffer(make([]byte, 0, 1024), 64*1024)

	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var e IndexEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			log.Printf("%s skip malformed line %d: %v", indexLogPrefix, line, err)
			continue
		}
		if e.ID == "" {
			e.ID = e.ComputeID()
		}
		i.entries[e.ID] = &e
		i.order = append(i.order, e.ID)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	log.Printf("%s loaded %d entries", indexLogPrefix, len(i.entries))
	return nil
}

// rebuildFromMarkdown 扫描 root 下所有 .md 文件，解析 front-matter，重建索引。
//
// 用于：
//   - 首次启动无索引文件
//   - 索引文件损坏时的恢复
func (i *Index) rebuildFromMarkdown() error {
	count := 0
	for _, sub := range SubDirs {
		subDir := filepath.Join(i.root, sub)
		if _, err := os.Stat(subDir); err != nil {
			continue // 子目录不存在则跳过
		}
		err := filepath.Walk(subDir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".md") {
				return nil
			}
			// 解析该文件中的所有 front-matter
			entries, err := parseFrontMatters(path, sub, i.root)
			if err != nil {
				log.Printf("%s parse %s failed: %v", indexLogPrefix, path, err)
				return nil
			}
			for _, e := range entries {
				i.entries[e.ID] = e
				i.order = append(i.order, e.ID)
				count++
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	i.dirty = true // 重建后需落盘一次
	log.Printf("%s rebuilt %d entries from markdown", indexLogPrefix, count)
	return nil
}

// openAppender 打开 append-only writer。
func (i *Index) openAppender() error {
	idxPath := filepath.Join(i.root, IndexFileName)
	f, err := os.OpenFile(idxPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	i.file = f
	i.appender = bufio.NewWriterSize(f, 16*1024)
	return nil
}

// Append 添加一条新 entry 到索引。
//
// 调用方保证：file/line 是新 entry 的写入位置（未与已有索引冲突）。
// 若同一 file:line 已存在（极小概率，例如重放），会覆盖式更新。
func (i *Index) Append(e *IndexEntry) error {
	if i == nil {
		return fmt.Errorf("nil index")
	}
	if e == nil {
		return fmt.Errorf("nil entry")
	}
	if e.ID == "" {
		e.ID = e.ComputeID()
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	_, exists := i.entries[e.ID]
	i.entries[e.ID] = e
	if !exists {
		i.order = append(i.order, e.ID)
	}

	if i.appender == nil {
		return fmt.Errorf("index appender not initialized")
	}

	// 立即 append 到 JSONL 文件
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}
	if _, err := i.appender.WriteString(string(data) + "\n"); err != nil {
		return fmt.Errorf("write index: %w", err)
	}
	// appender 缓冲 16KB，强制 flush 保证立即可见
	if err := i.appender.Flush(); err != nil {
		return fmt.Errorf("flush index: %w", err)
	}
	return nil
}

// UpdateSummary 更新 entry 的 summary（refiner 调用）。
//
// 定位策略：
//   - 优先按 file + groupID + side 定位（sessions 类 refine 用）
//   - 否则按 file + llm_trace_id 定位（普通 refine 用）
//   - 都没匹配到时静默返回 nil（refine 与索引解耦，refine 失败不影响主流程）
//
// file 可以传绝对路径或相对路径；内部统一用 forward-slash 相对路径比较。
func (i *Index) UpdateSummary(file, llmTraceID, groupID, side, newSummary string) error {
	if newSummary == "" {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	normFile := normalizeRelPath(file, i.root)

	for _, id := range i.order {
		e := i.entries[id]
		if e == nil || e.File != normFile {
			continue
		}
		matched := false
		if groupID != "" && e.RequestGroupID == groupID {
			// sessions 类：按 group_id + side 精确匹配
			if side == "" || e.Kind == "user_request" && side == "request" ||
				e.Kind == "user_response" && side == "response" {
				matched = true
			}
		} else if llmTraceID != "" && e.LLMTraceID == llmTraceID {
			matched = true
		}
		if matched {
			e.Summary = newSummary
			e.Refined = true
			i.dirty = true
			return nil
		}
	}
	return nil
}

// normalizeRelPath 把传入的 file 路径统一转换为相对 root 的 forward-slash 形式。
// 不存在的相对路径则原样 forward-slash 化。
func normalizeRelPath(file, root string) string {
	if file == "" {
		return ""
	}
	// 先 forward-slash 化
	norm := filepath.ToSlash(file)
	// 若 root 非空且 file 是绝对路径,尝试 Rel
	if root != "" && filepath.IsAbs(file) {
		absRoot, _ := filepath.Abs(root)
		absFile, _ := filepath.Abs(file)
		if rel, err := filepath.Rel(absRoot, absFile); err == nil {
			norm = filepath.ToSlash(rel)
		}
	}
	return norm
}

// Flush 把 in-memory 的 dirty 修改落盘（重写整个 JSONL）。
//
// 调用方：Close 流程；定期（每 N 次 update）由 caller 决定。
// 普通 append 路径已经实时 flush，本方法仅在 UpdateSummary 后调用。
func (i *Index) Flush() error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.dirty {
		return nil
	}

	idxPath := filepath.Join(i.root, IndexFileName)

	// 临时关闭 appender（Windows 上 rename 一个打开的文件会失败）。
	// 这里在持锁状态下操作,无并发问题。
	if i.appender != nil {
		_ = i.appender.Flush()
		i.appender = nil
	}
	if i.file != nil {
		_ = i.file.Close()
		i.file = nil
	}

	tmp := idxPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		// 恢复 appender（让后续 Append 还能工作）
		_ = i.openAppenderLocked()
		return fmt.Errorf("open tmp: %w", err)
	}
	w := bufio.NewWriterSize(f, 32*1024)
	for _, id := range i.order {
		e := i.entries[id]
		if e == nil {
			continue
		}
		data, err := json.Marshal(e)
		if err != nil {
			_ = f.Close()
			os.Remove(tmp)
			_ = i.openAppenderLocked()
			return fmt.Errorf("marshal %s: %w", id, err)
		}
		if _, err := w.WriteString(string(data) + "\n"); err != nil {
			_ = f.Close()
			os.Remove(tmp)
			_ = i.openAppenderLocked()
			return fmt.Errorf("write %s: %w", id, err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		os.Remove(tmp)
		_ = i.openAppenderLocked()
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		_ = i.openAppenderLocked()
		return err
	}
	if err := os.Rename(tmp, idxPath); err != nil {
		os.Remove(tmp)
		_ = i.openAppenderLocked()
		return fmt.Errorf("rename %s -> %s: %w", tmp, idxPath, err)
	}

	// 重新打开 appender（让后续 Append 仍能工作）
	if err := i.openAppenderLocked(); err != nil {
		return fmt.Errorf("reopen appender: %w", err)
	}

	i.dirty = false
	log.Printf("%s flushed %d entries", indexLogPrefix, len(i.entries))
	return nil
}

// openAppenderLocked 是 openAppender 的"持锁版本"，调用方必须已持有 i.mu。
func (i *Index) openAppenderLocked() error {
	idxPath := filepath.Join(i.root, IndexFileName)
	f, err := os.OpenFile(idxPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	i.file = f
	i.appender = bufio.NewWriterSize(f, 16*1024)
	return nil
}

// Close 关闭索引文件，必要时落盘 dirty 数据。
func (i *Index) Close() error {
	if i == nil {
		return nil
	}
	if err := i.Flush(); err != nil {
		log.Printf("%s Flush failed during Close: %v", indexLogPrefix, err)
	}
	if i.appender != nil {
		_ = i.appender.Flush()
		i.appender = nil
	}
	if i.file != nil {
		err := i.file.Close()
		i.file = nil
		return err
	}
	return nil
}

// =====================================================================
// 查询接口
// =====================================================================

// SearchQuery 描述一次索引查询。
//
// 字段语义：
//   - Query     关键词；同时匹配 summary / session_id / request_group_id / llm_trace_id。
//     空字符串时只按过滤条件查。
//   - Agent     按 agent 名过滤，空表示不过滤。
//   - Kind      按 kind 过滤（如 "user_request"），空表示不过滤。
//   - SubDir    按子目录过滤（"sessions"/"inputs"/...），空表示不过滤。
//   - Since/Until 按 RFC3339 时间区间过滤，空表示无界。
//   - Limit     返回上限，默认 20。
//   - Sort      排序方式："time_desc"（默认）/ "time_asc" / "refined"。
type SearchQuery struct {
	Query  string `json:"query"`
	Agent  string `json:"agent,omitempty"`
	Kind   string `json:"kind,omitempty"`
	SubDir string `json:"subdir,omitempty"`
	Since  string `json:"since,omitempty"`
	Until  string `json:"until,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Sort   string `json:"sort,omitempty"`
}

// SearchResult 是单条命中结果。
type SearchResult struct {
	IndexEntry
	// MatchedField 标记命中来源（"summary" / "session_id" / "request_group_id" / "llm_trace_id"）
	MatchedField string `json:"matched_field"`
	// Score 简单评分：summary 命中=3，其他字段=1；用于排序时同时间戳 tie-break
	Score int `json:"score"`
}

// Search 执行查询并返回按 Sort 排序后的结果。
func (i *Index) Search(q SearchQuery) []SearchResult {
	i.mu.RLock()
	defer i.mu.RUnlock()

	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}

	queryLower := strings.ToLower(strings.TrimSpace(q.Query))
	var sinceT, untilT time.Time
	if q.Since != "" {
		sinceT, _ = time.Parse(time.RFC3339, q.Since)
	}
	if q.Until != "" {
		untilT, _ = time.Parse(time.RFC3339, q.Until)
	}

	results := make([]SearchResult, 0, limit)
	for _, id := range i.order {
		e := i.entries[id]
		if e == nil {
			continue
		}
		// 过滤
		if q.Agent != "" && e.Agent != q.Agent {
			continue
		}
		if q.Kind != "" && e.Kind != q.Kind {
			continue
		}
		if q.SubDir != "" && e.SubDir != q.SubDir {
			continue
		}
		// 时间过滤
		if !sinceT.IsZero() || !untilT.IsZero() {
			t, err := time.Parse(time.RFC3339, e.Time)
			if err == nil {
				if !sinceT.IsZero() && t.Before(sinceT) {
					continue
				}
				if !untilT.IsZero() && t.After(untilT) {
					continue
				}
			}
		}

		// 关键词匹配
		score := 0
		matchedField := ""
		if queryLower == "" {
			// 无关键词时全部返回
			results = append(results, SearchResult{
				IndexEntry:   *e,
				MatchedField: "",
				Score:        0,
			})
		} else {
			summaryLower := strings.ToLower(e.Summary)
			if strings.Contains(summaryLower, queryLower) {
				score += 3
				matchedField = "summary"
			} else if strings.Contains(strings.ToLower(e.SessionID), queryLower) {
				score += 1
				matchedField = "session_id"
			} else if strings.Contains(strings.ToLower(e.RequestGroupID), queryLower) {
				score += 1
				matchedField = "request_group_id"
			} else if strings.Contains(strings.ToLower(e.LLMTraceID), queryLower) {
				score += 1
				matchedField = "llm_trace_id"
			} else if strings.Contains(strings.ToLower(e.Agent), queryLower) {
				score += 1
				matchedField = "agent"
			}
			if score > 0 {
				results = append(results, SearchResult{
					IndexEntry:   *e,
					MatchedField: matchedField,
					Score:        score,
				})
			}
		}

		if len(results) >= limit*4 { // 收集超额，后续截断
			break
		}
	}

	// 排序
	switch strings.ToLower(q.Sort) {
	case "time_asc":
		sort.SliceStable(results, func(a, b int) bool {
			return results[a].Time < results[b].Time
		})
	case "refined":
		sort.SliceStable(results, func(a, b int) bool {
			if results[a].Refined != results[b].Refined {
				return results[a].Refined
			}
			return results[a].Time > results[b].Time
		})
	default: // time_desc
		sort.SliceStable(results, func(a, b int) bool {
			if results[a].Score != results[b].Score {
				return results[a].Score > results[b].Score
			}
			return results[a].Time > results[b].Time
		})
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// GetBySession 返回某个 session 的所有 entry（按时间排序）。
func (i *Index) GetBySession(sessionID string, limit int) []SearchResult {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]SearchResult, 0)
	for _, id := range i.order {
		e := i.entries[id]
		if e == nil || e.SessionID != sessionID {
			continue
		}
		out = append(out, SearchResult{IndexEntry: *e})
	}
	sort.SliceStable(out, func(a, b int) bool {
		return out[a].Time < out[b].Time
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Stats 返回索引统计信息。
func (i *Index) Stats() IndexStats {
	i.mu.RLock()
	defer i.mu.RUnlock()
	stats := IndexStats{
		TotalEntries: len(i.entries),
		Dirty:        i.dirty,
		ByKind:       make(map[string]int),
		ByAgent:      make(map[string]int),
	}
	for _, e := range i.entries {
		if e == nil {
			continue
		}
		stats.ByKind[e.Kind]++
		stats.ByAgent[e.Agent]++
	}
	return stats
}

// IndexStats 索引统计。
type IndexStats struct {
	TotalEntries int            `json:"total_entries"`
	Dirty        bool           `json:"dirty"`
	ByKind       map[string]int `json:"by_kind"`
	ByAgent      map[string]int `json:"by_agent"`
}

// =====================================================================
// markdown front-matter 解析（用于 rebuildFromMarkdown）
// =====================================================================

// parseFrontMatters 解析单个 markdown 文件中的所有 front-matter 块。
//
// 返回每个块对应的 IndexEntry。每个 front-matter 之间用 --- 分隔，body 是 ```text...``` 块。
//
// 简化策略：仅提取 front-matter 头部；line 记录 --- 起始行（1-based）。
func parseFrontMatters(filePath, subDir, root string) ([]*IndexEntry, error) {
	absRoot, _ := filepath.Abs(root)
	absFile, _ := filepath.Abs(filePath)
	rel, err := filepath.Rel(absRoot, absFile)
	if err != nil {
		rel = filePath
	}
	// 统一用正斜杠
	rel = filepath.ToSlash(rel)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var entries []*IndexEntry
	lines := strings.Split(string(data), "\n")
	cur := 0
	for cur < len(lines) {
		// 找 --- 起始
		if strings.TrimSpace(lines[cur]) != "---" {
			cur++
			continue
		}
		startLine := cur + 1 // 1-based
		// 找 --- 结束
		endLine := -1
		for j := cur + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "---" {
				endLine = j
				break
			}
		}
		if endLine < 0 {
			break
		}
		// 解析 front-matter
		var fm map[string]string
		raw := strings.Join(lines[startLine:endLine], "\n")
		fm = parseSimpleYAML(raw)
		if len(fm) == 0 {
			cur = endLine + 1
			continue
		}
		e := &IndexEntry{
			SubDir:         subDir,
			File:           rel,
			Line:           startLine, // --- 起始行（front-matter 的第一行）
			SessionID:      fm["session_id"],
			LLMTraceID:     fm["llm_trace_id"],
			Agent:          fm["agent"],
			Kind:           fm["kind"],
			Time:           fm["time"],
			Summary:        fm["summary"],
			RequestGroupID: fm["request_group_id"],
		}
		e.ID = e.ComputeID()
		entries = append(entries, e)
		cur = endLine + 1
	}
	return entries, nil
}

// parseSimpleYAML 解析 front-matter 中简单的 key: "value" / key: value 行。
//
// 不依赖 yaml.v3（避免额外依赖），仅支持当前 front-matter 用到的几种格式。
func parseSimpleYAML(raw string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// 去掉首尾引号
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		out[key] = val
	}
	return out
}

// =====================================================================
// 全局单例 + package-level helpers
// =====================================================================

var (
	indexMu sync.RWMutex
	indexR  *Index
)

// SetGlobalIndex 注入全局索引（main.go 启动时调用）。
func SetGlobalIndex(idx *Index) {
	indexMu.Lock()
	defer indexMu.Unlock()
	indexR = idx
}

// GetGlobalIndex 取出全局索引（可能为 nil）。
func GetGlobalIndex() *Index {
	indexMu.RLock()
	defer indexMu.RUnlock()
	return indexR
}

// SearchGlobalIndex 便捷函数：全局索引搜索。
// nil 索引返回 nil，不报错（保持向后兼容）。
func SearchGlobalIndex(q SearchQuery) []SearchResult {
	idx := GetGlobalIndex()
	if idx == nil {
		return nil
	}
	return idx.Search(q)
}
