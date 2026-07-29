/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package memorytool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yichouchou/yichouchou_claw/internal/memory"
)

// setupIndexWithEntries 在临时目录构造一个 memory index 并注入若干 entry。
func setupIndexWithEntries(t *testing.T, entries []struct {
	SubDir, File, SessionID, Agent, Kind, Summary, Time string
	Line                                                int
}) *memory.Index {
	t.Helper()
	root := t.TempDir()
	// 创建标准的四象限子目录（NewIndex 只在索引缺失时全量扫描,这里需要预创建以便 rebuild 正常）
	for _, sub := range memory.SubDirs {
		_ = os.MkdirAll(filepath.Join(root, sub), 0o755)
	}
	idx, err := memory.NewIndex(root)
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	for _, e := range entries {
		entry := &memory.IndexEntry{
			SubDir:    e.SubDir,
			File:      e.File,
			Line:      e.Line,
			SessionID: e.SessionID,
			Agent:     e.Agent,
			Kind:      e.Kind,
			Time:      e.Time,
			Summary:   e.Summary,
		}
		if err := idx.Append(entry); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

// 注入 entries 到全局索引, 测试结束自动恢复。
func installGlobalIndex(t *testing.T, idx *memory.Index) {
	t.Helper()
	old := memory.GetGlobalIndex()
	memory.SetGlobalIndex(idx)
	t.Cleanup(func() { memory.SetGlobalIndex(old) })
}

// TestDoSearch_HintWhenTotalZero 验证 0 命中时的增强 Hint 提示。
func TestDoSearch_HintWhenTotalZero(t *testing.T) {
	idx := setupIndexWithEntries(t, []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{}) // 空索引
	installGlobalIndex(t, idx)

	// 1. 空 query + 无过滤 → 应自动注入 7 天窗口（Hint 提示"没最近记忆"）
	out, err := doSearchExposed(&SearchInput{})
	if err != nil {
		t.Fatalf("doSearch: %v", err)
	}
	if out.Total != 0 {
		t.Errorf("expected 0 results, got %d", out.Total)
	}
	if !strings.Contains(out.Hint, "no recent memory entries") {
		t.Errorf("Hint should indicate 'no recent', got: %q", out.Hint)
	}
}

// TestDoSearch_HintWhenQueryHasNoMatch 验证有 query 但无命中时的 Hint。
func TestDoSearch_HintWhenQueryHasNoMatch(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	idx := setupIndexWithEntries(t, []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"用户问 HBM 市场规模", now, 1},
	})
	installGlobalIndex(t, idx)

	// 关键词不命中
	out, err := doSearchExposed(&SearchInput{Query: "完全不相关的词xyz"})
	if err != nil {
		t.Fatalf("doSearch: %v", err)
	}
	if out.Total != 0 {
		t.Errorf("expected 0 results, got %d", out.Total)
	}
	if !strings.Contains(out.Hint, "完全不相关的词xyz") {
		t.Errorf("Hint should echo the failing query: %q", out.Hint)
	}
	if !strings.Contains(out.Hint, "try") {
		t.Errorf("Hint should suggest 'try' alternative: %q", out.Hint)
	}
}

// TestDoSearch_HintWhenHit 验证命中时给出"读全文"提示。
func TestDoSearch_HintWhenHit(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	idx := setupIndexWithEntries(t, []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"用户问 HBM 市场规模", now, 1},
	})
	installGlobalIndex(t, idx)

	out, err := doSearchExposed(&SearchInput{Query: "HBM"})
	if err != nil {
		t.Fatalf("doSearch: %v", err)
	}
	if out.Total != 1 {
		t.Errorf("expected 1 result, got %d", out.Total)
	}
	if !strings.Contains(out.Hint, "Read") {
		t.Errorf("Hint after hit should mention 'Read': %q", out.Hint)
	}
}

// TestDoSearch_TimeAgoField 验证 TimeAgo 字段正确格式化。
func TestDoSearch_TimeAgoField(t *testing.T) {
	// 构造不同时间段的 entry
	now := time.Now()
	entries := []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"刚刚的对话", now.Format(time.RFC3339), 1},
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_response",
			"2 小时前的对话", now.Add(-2 * time.Hour).Format(time.RFC3339), 12},
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_response",
			"3 天前的对话", now.Add(-3 * 24 * time.Hour).Format(time.RFC3339), 20},
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_response",
			"3 周前的对话", now.Add(-3 * 7 * 24 * time.Hour).Format(time.RFC3339), 30},
	}
	idx := setupIndexWithEntries(t, entries)
	installGlobalIndex(t, idx)

	out, err := doSearchExposed(&SearchInput{Query: "对话", Limit: 10})
	if err != nil {
		t.Fatalf("doSearch: %v", err)
	}
	if out.Total == 0 {
		t.Fatalf("expected matches")
	}

	// 验证 TimeAgo 字段填充
	for _, e := range out.Entries {
		if e.TimeAgo == "" {
			t.Errorf("TimeAgo should not be empty for entry %q", e.Summary)
		}
	}

	// 验证不同时间段格式
	timeAgoSet := make(map[string]bool)
	for _, e := range out.Entries {
		timeAgoSet[e.TimeAgo] = true
	}
	wantAny := []string{"刚刚", "分钟前", "小时前", "天前", "周前", "月前"}
	found := false
	for _, want := range wantAny {
		if timeAgoSet[want] {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one of %v in TimeAgo values, got %v",
			wantAny, timeAgoSet)
	}
}

// TestDoSearch_AutoInjectSinceForEmptyQuery 验证空 query + 无过滤时自动注入 7 天窗口。
func TestDoSearch_AutoInjectSinceForEmptyQuery(t *testing.T) {
	// 一条非常旧的 entry（8 天前）
	old := time.Now().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	idx := setupIndexWithEntries(t, []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"很旧的对话", old, 1},
	})
	installGlobalIndex(t, idx)

	// 不传任何参数 → 应该只返回最近 7 天的（这里 0 条）
	out, err := doSearchExposed(&SearchInput{})
	if err != nil {
		t.Fatalf("doSearch: %v", err)
	}
	if out.Total != 0 {
		t.Errorf("old entry should be filtered by auto-injected since, got %d hits", out.Total)
	}
}

// TestDoSearch_NoIndexHint 验证索引未启用时的 Hint。
func TestDoSearch_NoIndexHint(t *testing.T) {
	old := memory.GetGlobalIndex()
	memory.SetGlobalIndex(nil)
	t.Cleanup(func() { memory.SetGlobalIndex(old) })

	out, err := doSearchExposed(&SearchInput{Query: "test"})
	if err != nil {
		t.Fatalf("doSearch: %v", err)
	}
	if out.Total != 0 {
		t.Errorf("expected 0 results when no index, got %d", out.Total)
	}
	if !strings.Contains(out.Hint, "not enabled") {
		t.Errorf("Hint should mention 'not enabled': %q", out.Hint)
	}
	if !strings.Contains(out.Hint, "Fallback") {
		t.Errorf("Hint should suggest fallback: %q", out.Hint)
	}
}

// TestToolDescription_HasRequiredTriggerKeywords 验证工具描述含必须触发关键词。
func TestToolDescription_HasRequiredTriggerKeywords(t *testing.T) {
	desc := toolDescriptionNew

	// 必须包含的信号词
	requiredSignals := []string{
		"必须",            // 强约束
		"memory_search", // 自指工具名
		"Read",          // 引导模型继续 Read
	}

	// 4 类触发关键词都应覆盖
	requiredCategories := map[string][]string{
		"时间指代": {"之前", "上次", "上次", "昨天"},
		"决策溯源": {"为什么", "怎么定"},
		"知识追溯": {"我们做过什么", "项目之前"},
		"模糊回溯": {"我们聊过什么", "之前讨论过"},
	}

	for _, sig := range requiredSignals {
		if !strings.Contains(desc, sig) {
			t.Errorf("toolDescriptionNew missing required signal: %q", sig)
		}
	}

	// 至少覆盖 4 类触发关键词中的 3 类
	hitCount := 0
	for _, keywords := range requiredCategories {
		allHit := true
		for _, kw := range keywords {
			if !strings.Contains(desc, kw) {
				allHit = false
				break
			}
		}
		if allHit {
			hitCount++
		}
	}
	if hitCount < 3 {
		t.Errorf("tool description should cover at least 3 of 4 trigger categories, got %d", hitCount)
	}

	// 必须有"反模式"或"不要"（强约束的标志）
	if !strings.Contains(desc, "❌") && !strings.Contains(desc, "禁止") {
		t.Errorf("tool description should include anti-patterns")
	}
}

// TestNewSearchTool_GetInfo 验证工具元数据正确暴露。
func TestNewSearchTool_GetInfo(t *testing.T) {
	tool, err := NewSearchTool()
	if err != nil {
		t.Fatalf("NewSearchTool: %v", err)
	}
	if tool == nil {
		t.Fatal("NewSearchTool returned nil")
	}

	// 验证 schema 含 SearchInput 的字段
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}

	if info.Name != "memory_search" {
		t.Errorf("expected tool name 'memory_search', got %q", info.Name)
	}
	if !strings.Contains(info.Desc, "memory_search") {
		t.Errorf("description should mention tool name: %q", info.Desc)
	}
	if !strings.Contains(info.Desc, "必须") {
		t.Errorf("description should have strong constraint signal: %q", info.Desc)
	}
}

// TestDoSearch_Serializable 验证输出 JSON 可被解析为预期 schema。
func TestDoSearch_Serializable(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	idx := setupIndexWithEntries(t, []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"测试对话", now, 1},
	})
	installGlobalIndex(t, idx)

	out, err := doSearch(&SearchInput{Query: "测试"})
	if err != nil {
		t.Fatalf("doSearch: %v", err)
	}

	// 必须能 JSON 序列化（eino 工具结果需要返回 string）
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// 验证字段名
	dataStr := string(data)
	for _, field := range []string{"total", "entries", "hint", "session_id", "agent",
		"kind", "time", "summary", "file", "line", "matched_field", "score", "time_ago"} {
		if !strings.Contains(dataStr, field) {
			t.Errorf("JSON should contain field %q, got: %s", field, dataStr)
		}
	}
}
