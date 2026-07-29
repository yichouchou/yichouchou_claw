/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helper: 在临时目录构造一个 memDir（含四象限子目录）
func setupMemDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	memDir := filepath.Join(root, "memory")
	for _, sub := range SubDirs {
		if err := os.MkdirAll(filepath.Join(memDir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	return memDir
}

// TestIndex_AppendAndSearch 验证 Append 后 Search 能命中。
func TestIndex_AppendAndSearch(t *testing.T) {
	memDir := setupMemDir(t)
	idx, err := NewIndex(memDir)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Format(time.RFC3339)
	entries := []*IndexEntry{
		{SubDir: "sessions", File: "sessions/2026-07-23/10h.md", Line: 1,
			SessionID: "s1", Agent: "ChatAgent", Kind: "user_request",
			Time: now, Summary: "用户问 HBM 市场规模", Refined: false},
		{SubDir: "sessions", File: "sessions/2026-07-23/10h.md", Line: 12,
			SessionID: "s1", Agent: "ChatAgent", Kind: "user_response",
			Time: now, Summary: "回答 HBM 市场约 500 亿美元", Refined: false},
		{SubDir: "inputs", File: "inputs/2026-07-23/10h.md", Line: 1,
			SessionID: "s1", Agent: "ChatAgent", Kind: "llm_input",
			Time: now, Summary: "给 LLM 输入 HBM 趋势数据", Refined: false},
		{SubDir: "sessions", File: "sessions/2026-07-24/11h.md", Line: 1,
			SessionID: "s2", Agent: "ChatAgent", Kind: "user_request",
			Time: now, Summary: "用户询问 PostgreSQL 与 MySQL", Refined: false},
	}
	for _, e := range entries {
		e.ID = e.ComputeID()
		if err := idx.Append(e); err != nil {
			t.Fatalf("Append %s: %v", e.ID, err)
		}
	}

	// 按 summary 关键词搜：4 条 entry 中 3 条包含"HBM"（问 HBM / 答 HBM / LLM 输入 HBM）
	results := idx.Search(SearchQuery{Query: "HBM"})
	if len(results) != 3 {
		t.Errorf("query=HBM want 3 results, got %d", len(results))
	}
	for _, r := range results {
		if !strings.Contains(r.Summary, "HBM") {
			t.Errorf("result summary should contain HBM: %q", r.Summary)
		}
	}

	// 按 agent 过滤
	results = idx.Search(SearchQuery{Agent: "ChatAgent"})
	if len(results) != 4 {
		t.Errorf("agent=ChatAgent want 4 results, got %d", len(results))
	}

	// 按 kind 过滤
	results = idx.Search(SearchQuery{Kind: "user_request"})
	if len(results) != 2 {
		t.Errorf("kind=user_request want 2 results, got %d", len(results))
	}

	// 按 subdir 过滤
	results = idx.Search(SearchQuery{SubDir: "sessions"})
	if len(results) != 3 {
		t.Errorf("subdir=sessions want 3 results, got %d", len(results))
	}

	// 不命中
	results = idx.Search(SearchQuery{Query: "不存在的关键词xyz"})
	if len(results) != 0 {
		t.Errorf("query 不存在 want 0 results, got %d", len(results))
	}
}

// TestIndex_UpdateSummary 验证 refine 后 summary 同步更新到索引。
func TestIndex_UpdateSummary(t *testing.T) {
	memDir := setupMemDir(t)
	idx, err := NewIndex(memDir)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Format(time.RFC3339)
	e := &IndexEntry{
		SubDir: "inputs", File: "inputs/2026-07-23/10h.md", Line: 1,
		SessionID: "s1", LLMTraceID: "llm-abc-123", Agent: "ChatAgent",
		Kind: "llm_input", Time: now, Summary: "原 summary", Refined: false,
	}
	e.ID = e.ComputeID()
	if err := idx.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// 用绝对路径模拟 refiner 调用（refiner 拿到的是绝对路径）
	absPath := filepath.Join(memDir, e.File)
	err = idx.UpdateSummary(absPath, e.LLMTraceID, "", "", "LLM 精炼后的精准摘要")
	if err != nil {
		t.Fatalf("UpdateSummary: %v", err)
	}

	// 验证内存索引已更新
	if e2 := idx.entries[e.ID]; e2 == nil || e2.Summary != "LLM 精炼后的精准摘要" || !e2.Refined {
		t.Errorf("in-memory update failed: %+v", e2)
	}

	// 验证 Flush 后能从 JSONL 重新加载
	if err := idx.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	idx2, err := NewIndex(memDir)
	if err != nil {
		t.Fatalf("NewIndex (reload): %v", err)
	}
	defer idx2.Close()
	if e3 := idx2.entries[e.ID]; e3 == nil || e3.Summary != "LLM 精炼后的精准摘要" {
		t.Errorf("reloaded index should have refined summary: %+v", e3)
	}
}

// TestIndex_UpdateSummaryByGroupID 验证 sessions 类 refine 走 group_id + side 路径。
func TestIndex_UpdateSummaryByGroupID(t *testing.T) {
	memDir := setupMemDir(t)
	idx, err := NewIndex(memDir)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Format(time.RFC3339)
	entries := []*IndexEntry{
		{SubDir: "sessions", File: "sessions/2026-07-23/10h.md", Line: 1,
			SessionID: "s1", RequestGroupID: "req-1111", Agent: "RouterAgent",
			Kind: "user_request", Time: now, Summary: "原 request summary", Refined: false},
		{SubDir: "sessions", File: "sessions/2026-07-23/10h.md", Line: 12,
			SessionID: "s1", RequestGroupID: "req-1111", Agent: "RouterAgent",
			Kind: "user_response", Time: now, Summary: "原 response summary", Refined: false},
	}
	for _, e := range entries {
		e.ID = e.ComputeID()
		if err := idx.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// 只更新 request 那条
	absPath := filepath.Join(memDir, entries[0].File)
	err = idx.UpdateSummary(absPath, "", "req-1111", "request", "精炼 request")
	if err != nil {
		t.Fatalf("UpdateSummary: %v", err)
	}

	// request 已更新，response 未变
	if e := idx.entries[entries[0].ID]; e == nil || e.Summary != "精炼 request" || !e.Refined {
		t.Errorf("request should be updated: %+v", e)
	}
	if e := idx.entries[entries[1].ID]; e == nil || e.Summary != "原 response summary" || e.Refined {
		t.Errorf("response should NOT be updated: %+v", e)
	}
}

// TestIndex_RebuildFromMarkdown 验证启动时从 markdown 重建索引。
func TestIndex_RebuildFromMarkdown(t *testing.T) {
	memDir := setupMemDir(t)

	// 预先在 sessions/ 写一个真实的 markdown 文件（含 front-matter）
	sessDir := filepath.Join(memDir, "sessions", "2026-07-23")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mdContent := `---
request_group_id: "req-rebuild-1"
session_id: "sess-rebuild-1"
llm_trace_id: "llm-1"
agent: "ChatAgent"
kind: "user_request"
time: "2026-07-23T10:00:00+08:00"
summary: "用户询问天气情况"
---

` + "```text\n今天天气如何\n```\n" + `

---
session_id: "sess-rebuild-1"
llm_trace_id: "llm-2"
agent: "ChatAgent"
kind: "llm_input"
time: "2026-07-23T10:00:01+08:00"
summary: "天气查询 LLM 输入"
---

` + "```text\nsystem: ...\n```\n"
	mdPath := filepath.Join(sessDir, "10h.md")
	if err := os.WriteFile(mdPath, []byte(mdContent), 0o644); err != nil {
		t.Fatalf("write md: %v", err)
	}

	// 第一次启动 → 索引不存在 → 全量重建
	idx, err := NewIndex(memDir)
	if err != nil {
		t.Fatalf("NewIndex (rebuild): %v", err)
	}
	defer idx.Close()

	stats := idx.Stats()
	if stats.TotalEntries != 2 {
		t.Errorf("rebuilt total = %d, want 2", stats.TotalEntries)
	}

	// 验证能从索引里搜到
	results := idx.Search(SearchQuery{Query: "天气"})
	if len(results) < 1 {
		t.Errorf("query=天气 should hit rebuilt entries")
	}

	// 第二次启动 → 索引文件存在 → 直接加载
	idx2, err := NewIndex(memDir)
	if err != nil {
		t.Fatalf("NewIndex (reload): %v", err)
	}
	defer idx2.Close()
	if stats := idx2.Stats(); stats.TotalEntries != 2 {
		t.Errorf("reload total = %d, want 2", stats.TotalEntries)
	}
}

// TestIndex_GetBySession 验证按 session 查所有 entry。
func TestIndex_GetBySession(t *testing.T) {
	memDir := setupMemDir(t)
	idx, err := NewIndex(memDir)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	defer idx.Close()

	now := time.Now().Format(time.RFC3339)
	for i := 0; i < 5; i++ {
		e := &IndexEntry{
			SubDir: "sessions", File: fmt.Sprintf("sessions/2026-07-23/10h.md"),
			Line: i + 1, SessionID: "sess-A",
			Agent: "ChatAgent", Kind: "user_request",
			Time: now, Summary: fmt.Sprintf("entry %d", i),
		}
		e.ID = e.ComputeID()
		if err := idx.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// 不同 session 不应被返回
	e2 := &IndexEntry{
		SubDir: "sessions", File: "sessions/2026-07-23/10h.md",
		Line: 99, SessionID: "sess-B",
		Agent: "ChatAgent", Kind: "user_request",
		Time: now, Summary: "sess-B entry",
	}
	e2.ID = e2.ComputeID()
	if err := idx.Append(e2); err != nil {
		t.Fatalf("Append: %v", err)
	}

	results := idx.GetBySession("sess-A", 0)
	if len(results) != 5 {
		t.Errorf("sess-A want 5, got %d", len(results))
	}
	for _, r := range results {
		if r.SessionID != "sess-A" {
			t.Errorf("unexpected session in result: %s", r.SessionID)
		}
	}
}

// TestIndex_NilSafe 验证 nil receiver 不 panic。
func TestIndex_NilSafe(t *testing.T) {
	var idx *Index
	if err := idx.Close(); err != nil {
		t.Errorf("nil Close should be safe: %v", err)
	}
	if err := idx.Flush(); err != nil {
		t.Errorf("nil Flush should be safe: %v", err)
	}
	if err := idx.Append(&IndexEntry{}); err == nil {
		t.Errorf("nil Append should error")
	}
}

// TestSearchGlobalIndex_NilIndex 验证全局索引为 nil 时 Search 安全返回。
func TestSearchGlobalIndex_NilIndex(t *testing.T) {
	// 先确保全局索引为 nil
	old := indexR
	SetGlobalIndex(nil)
	defer SetGlobalIndex(old)

	results := SearchGlobalIndex(SearchQuery{Query: "anything"})
	if results != nil {
		t.Errorf("nil index should return nil, got %v", results)
	}
}
