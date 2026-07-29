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
	"strings"
	"testing"
	"time"

	"github.com/yichouchou/yichouchou_claw/internal/memory"
)

// helper: 注入若干 entry 到全局索引。
func setupRecentMemIndex(t *testing.T, entries []struct {
	SubDir, File, SessionID, Agent, Kind, Summary, Time string
	Line                                                int
}) *memory.Index {
	t.Helper()
	idx := setupIndexWithEntries(t, entries)
	installGlobalIndex(t, idx)
	return idx
}

// TestRecentMemoryContext_Disabled 当 Enabled=false 时应该返回空字符串。
func TestRecentMemoryContext_Disabled(t *testing.T) {
	cfg := RecentMemoryConfig{Enabled: false}
	out := RecentMemoryContext(cfg)
	if out != "" {
		t.Errorf("expected empty when disabled, got: %q", out)
	}
}

// TestRecentMemoryContext_NoIndex 当全局索引为 nil 时应该返回空字符串(无副作用)。
func TestRecentMemoryContext_NoIndex(t *testing.T) {
	// 清空全局索引
	old := memory.GetGlobalIndex()
	memory.SetGlobalIndex(nil)
	t.Cleanup(func() { memory.SetGlobalIndex(old) })

	out := RecentMemoryContext(RecentMemoryConfig{Enabled: true})
	if out != "" {
		t.Errorf("expected empty when no index, got: %q", out)
	}
}

// TestRecentMemoryContext_WithEntries 验证有效注入。
func TestRecentMemoryContext_WithEntries(t *testing.T) {
	now := time.Now()
	entries := []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"用户问 HBM 市场规模", now.Add(-1 * time.Hour).Format(time.RFC3339), 1},
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"用户问 PostgreSQL vs MySQL", now.Add(-2 * time.Hour).Format(time.RFC3339), 12},
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"讨论 mvdan.cc/sh", now.Add(-3 * 24 * time.Hour).Format(time.RFC3339), 24},
	}
	setupRecentMemIndex(t, entries)

	out := RecentMemoryContext(RecentMemoryConfig{
		Enabled:     true,
		Limit:       5,
		MaxTokens:   2000,
		KindsFilter: []string{"user_request"},
		MaxAgeDays:  30,
	})

	// 必须包含标题
	if !strings.Contains(out, "## Recent Memory") {
		t.Errorf("output should contain '## Recent Memory' header: %q", out)
	}

	// 必须包含所有 3 条摘要（按时间倒序：HBM 应在最前）
	if !strings.Contains(out, "HBM 市场规模") {
		t.Errorf("output should contain HBM summary")
	}
	if !strings.Contains(out, "PostgreSQL") {
		t.Errorf("output should contain PostgreSQL summary")
	}
	if !strings.Contains(out, "mvdan.cc/sh") {
		t.Errorf("output should contain mvdan.cc/sh summary")
	}

	// 检查顺序：HBM 应该排在 PostgreSQL 之前（因为 HBM 更新）
	idxHBM := strings.Index(out, "HBM 市场规模")
	idxPSQL := strings.Index(out, "PostgreSQL")
	if idxHBM < 0 || idxPSQL < 0 || idxHBM > idxPSQL {
		t.Errorf("HBM should appear before PostgreSQL (time desc): hbm=%d, psql=%d", idxHBM, idxPSQL)
	}
}

// TestRecentMemoryContext_RespectsMaxAgeDays 验证超过 MaxAgeDays 的记忆不注入。
func TestRecentMemoryContext_RespectsMaxAgeDays(t *testing.T) {
	now := time.Now()
	entries := []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		// 5 天前的 user_request（在 30 天窗口内）
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"5天前的对话", now.Add(-5 * 24 * time.Hour).Format(time.RFC3339), 1},
		// 60 天前的 user_request（超出 30 天窗口）
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"60天前的对话", now.Add(-60 * 24 * time.Hour).Format(time.RFC3339), 12},
	}
	setupRecentMemIndex(t, entries)

	out := RecentMemoryContext(RecentMemoryConfig{
		Enabled:     true,
		Limit:       10,
		KindsFilter: []string{"user_request"},
		MaxAgeDays:  30,
	})

	if !strings.Contains(out, "5天前的对话") {
		t.Error("5-day-old memory should be included")
	}
	if strings.Contains(out, "60天前的对话") {
		t.Error("60-day-old memory should be filtered out by MaxAgeDays")
	}
}

// TestRecentMemoryContext_RespectsKindFilter 验证 kind 过滤生效。
func TestRecentMemoryContext_RespectsKindFilter(t *testing.T) {
	entries := []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"USER request 摘要", time.Now().Format(time.RFC3339), 1},
		{"inputs", "inputs/2026-07-23/10h.md", "s1", "ChatAgent", "llm_input",
			"LLM input 摘要", time.Now().Format(time.RFC3339), 1},
	}
	setupRecentMemIndex(t, entries)

	// 只包含 user_request
	out := RecentMemoryContext(RecentMemoryConfig{
		Enabled:     true,
		KindsFilter: []string{"user_request"},
	})
	if !strings.Contains(out, "USER request") {
		t.Error("user_request should be included")
	}
	if strings.Contains(out, "LLM input") {
		t.Error("llm_input should be filtered out by kind filter")
	}
}

// TestRecentMemoryContext_RespectsLimit 验证数量上限生效。
func TestRecentMemoryContext_RespectsLimit(t *testing.T) {
	now := time.Now()
	entries := make([]struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}, 0)
	for i := 0; i < 20; i++ {
		entries = append(entries, struct {
			SubDir, File, SessionID, Agent, Kind, Summary, Time string
			Line                                                int
		}{
			"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"对话" + string(rune('A'+i)), now.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339), i + 1,
		})
	}
	setupRecentMemIndex(t, entries)

	out := RecentMemoryContext(RecentMemoryConfig{
		Enabled:     true,
		Limit:       3, // 只取 3 条
		KindsFilter: []string{"user_request"},
	})

	// 应只含 3 条对话
	count := 0
	for _, ch := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		if strings.Contains(out, string(rune(ch))) &&
			strings.Contains(out, "对话"+string(rune(ch))) {
			count++
		}
	}
	if count > 3 {
		t.Errorf("expected at most 3 entries, got ~%d in: %q", count, out)
	}
}

// TestRecentMemoryBlock_EmptyWhenNoEntries 验证无记忆时返回格式良好的空 block。
func TestRecentMemoryBlock_EmptyWhenNoEntries(t *testing.T) {
	// 空索引
	setupRecentMemIndex(t, []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{})

	out := RecentMemoryBlock(RecentMemoryConfig{Enabled: true})
	// 空 block 仍应包 <recent_memory> 标记
	if !strings.Contains(out, "<recent_memory>") {
		t.Errorf("empty block should still have <recent_memory> wrapper: %q", out)
	}
}

// TestRecentMemoryBlock_WithEntries 验证正常注入格式。
func TestRecentMemoryBlock_WithEntries(t *testing.T) {
	entries := []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"测试对话", time.Now().Format(time.RFC3339), 1},
	}
	setupRecentMemIndex(t, entries)

	out := RecentMemoryBlock(RecentMemoryConfig{Enabled: true})

	if !strings.HasPrefix(out, "<recent_memory>") {
		t.Errorf("block should start with <recent_memory>: %q", out)
	}
	if !strings.HasSuffix(out, "</recent_memory>") {
		t.Errorf("block should end with </recent_memory>: %q", out)
	}
	if !strings.Contains(out, "测试对话") {
		t.Errorf("block should contain test entry: %q", out)
	}
	if !strings.Contains(out, "## Recent Memory") {
		t.Errorf("block should contain header inside: %q", out)
	}
}

// TestRecentMemoryContext_TimeAgoFormat 验证每条记忆带相对时间。
func TestRecentMemoryContext_TimeAgoFormat(t *testing.T) {
	now := time.Now()
	entries := []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"最近对话", now.Add(-30 * time.Minute).Format(time.RFC3339), 1},
	}
	setupRecentMemIndex(t, entries)

	out := RecentMemoryContext(RecentMemoryConfig{Enabled: true})

	if !strings.Contains(out, "分钟前") {
		t.Errorf("time-ago format should contain '分钟前': %q", out)
	}
}

// TestRecentMemoryContext_ContainsGuidance 验证输出含"如何使用"的指引。
func TestRecentMemoryContext_ContainsGuidance(t *testing.T) {
	entries := []struct {
		SubDir, File, SessionID, Agent, Kind, Summary, Time string
		Line                                                int
	}{
		{"sessions", "sessions/2026-07-23/10h.md", "s1", "ChatAgent", "user_request",
			"对话", time.Now().Format(time.RFC3339), 1},
	}
	setupRecentMemIndex(t, entries)

	out := RecentMemoryContext(RecentMemoryConfig{Enabled: true})

	// 指引部分（让模型知道这只是"摘要"，需要时去查更深）
	if !strings.Contains(out, "指引") {
		t.Errorf("output should contain '指引' guidance section: %q", out)
	}
	if !strings.Contains(out, "memory_search") {
		t.Errorf("output should hint to use memory_search: %q", out)
	}
}

// TestDefaultRecentMemoryConfig 验证默认配置的合理性。
func TestDefaultRecentMemoryConfig(t *testing.T) {
	cfg := DefaultRecentMemoryConfig()
	if !cfg.Enabled {
		t.Error("default should be Enabled=true")
	}
	if cfg.Limit <= 0 {
		t.Errorf("default Limit should be positive: %d", cfg.Limit)
	}
	if cfg.MaxTokens <= 0 {
		t.Errorf("default MaxTokens should be positive: %d", cfg.MaxTokens)
	}
	if cfg.MaxAgeDays <= 0 {
		t.Errorf("default MaxAgeDays should be positive: %d", cfg.MaxAgeDays)
	}
	if len(cfg.KindsFilter) == 0 {
		t.Errorf("default KindsFilter should not be empty")
	}
}
