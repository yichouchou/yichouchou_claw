/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempYAML 把 yaml 内容写到临时文件并返回路径。
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "application.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// resetGlobalConfig 清空全局 config 状态（避免测试间相互污染）。
// 用 appMu.Lock()，本身是 sync.RWMutex 上的写锁,所以函数体不能带 t 参数。
func resetGlobalConfig() {
	appMu.Lock()
	app = nil
	appAt = ""
	appMu.Unlock()
}

// TestLoadApplication_RecentMemoryDefaults 验证 LoadApplication 填充默认值。
func TestLoadApplication_RecentMemoryDefaults(t *testing.T) {
	t.Cleanup(resetGlobalConfig)

	yaml := `
memory:
  recent_memory:
    enabled: true
    # limit / max_tokens / max_age_days / kinds_filter 全部省略,期望默认填充
`
	path := writeTempYAML(t, yaml)
	if err := LoadApplication(path); err != nil {
		t.Fatalf("LoadApplication: %v", err)
	}
	t.Cleanup(resetGlobalConfig)

	cfg := GetRecentMemoryConfig()
	if !cfg.Enabled {
		t.Error("Enabled should be true (set in yml)")
	}
	if cfg.Limit != 10 {
		t.Errorf("default Limit should be 10, got %d", cfg.Limit)
	}
	if cfg.MaxTokens != 2000 {
		t.Errorf("default MaxTokens should be 2000, got %d", cfg.MaxTokens)
	}
	if cfg.MaxAgeDays != 30 {
		t.Errorf("default MaxAgeDays should be 30, got %d", cfg.MaxAgeDays)
	}
	if len(cfg.KindsFilter) != 1 || cfg.KindsFilter[0] != "user_request" {
		t.Errorf("default KindsFilter should be [user_request], got %v", cfg.KindsFilter)
	}
}

// TestLoadApplication_RecentMemoryCustom 验证自定义值不被默认值覆盖。
func TestLoadApplication_RecentMemoryCustom(t *testing.T) {
	t.Cleanup(resetGlobalConfig)

	yaml := `
memory:
  recent_memory:
    enabled: false
    limit: 25
    max_tokens: 4000
    max_age_days: 7
    kinds_filter:
      - user_request
      - user_response
`
	path := writeTempYAML(t, yaml)
	if err := LoadApplication(path); err != nil {
		t.Fatalf("LoadApplication: %v", err)
	}

	cfg := GetRecentMemoryConfig()
	if cfg.Enabled {
		t.Error("Enabled should be false (set in yml)")
	}
	if cfg.Limit != 25 {
		t.Errorf("Limit should be 25 (custom), got %d", cfg.Limit)
	}
	if cfg.MaxTokens != 4000 {
		t.Errorf("MaxTokens should be 4000 (custom), got %d", cfg.MaxTokens)
	}
	if cfg.MaxAgeDays != 7 {
		t.Errorf("MaxAgeDays should be 7 (custom), got %d", cfg.MaxAgeDays)
	}
	if len(cfg.KindsFilter) != 2 {
		t.Errorf("KindsFilter should have 2 items, got %d", len(cfg.KindsFilter))
	}
}

// TestLoadApplication_NoMemorySection 验证缺少 memory section 时的行为。
func TestLoadApplication_NoMemorySection(t *testing.T) {
	t.Cleanup(resetGlobalConfig)

	yaml := `
server:
  host: ":12345"
`
	path := writeTempYAML(t, yaml)
	if err := LoadApplication(path); err != nil {
		t.Fatalf("LoadApplication: %v", err)
	}

	// 没有 memory.recent_memory section → 取 ZeroRecentMemoryConfig(Enabled=false)
	cfg := GetRecentMemoryConfig()
	if cfg.Enabled {
		t.Error("Enabled should be false when no recent_memory section")
	}
}

// TestLoadApplication_FileMissing 验证文件不存在时的兜底。
func TestLoadApplication_FileMissing(t *testing.T) {
	t.Cleanup(resetGlobalConfig)

	err := LoadApplication("/non/existent/path.yml")
	if err != nil {
		t.Errorf("LoadApplication with missing file should not error: %v", err)
	}

	// File missing → app 还是 nil,但 GetRecentMemoryConfig 应该返回 Zero 兜底
	cfg := GetRecentMemoryConfig()
	if cfg.Enabled {
		t.Error("Enabled should be false when config not loaded")
	}
	// 其他字段应该有默认值
	if cfg.Limit != 10 || cfg.MaxTokens != 2000 || cfg.MaxAgeDays != 30 {
		t.Errorf("ZeroRecentMemoryConfig defaults wrong: %+v", cfg)
	}
}

// TestZeroRecentMemoryConfig 验证零值返回的是关闭+默认。
func TestZeroRecentMemoryConfig(t *testing.T) {
	cfg := ZeroRecentMemoryConfig()
	if cfg.Enabled {
		t.Error("ZeroRecentMemoryConfig should be Disabled")
	}
	if cfg.Limit <= 0 {
		t.Error("ZeroRecentMemoryConfig should have valid Limit default")
	}
}

// TestGetRecentMemoryConfig_NilApp 验证未加载时返回 Zero 兜底(不 panic)。
func TestGetRecentMemoryConfig_NilApp(t *testing.T) {
	t.Cleanup(resetGlobalConfig)

	cfg := GetRecentMemoryConfig()
	if cfg.Enabled {
		t.Error("should not be enabled when app is nil")
	}
}

// TestRecentMemoryConfig_FillDefaults 验证 fillDefaults 单独行为。
func TestRecentMemoryConfig_FillDefaults(t *testing.T) {
	cfg := RecentMemoryConfig{Enabled: true, Limit: 0, MaxTokens: 0, MaxAgeDays: 0}
	cfg.fillDefaults()
	if cfg.Limit != 10 {
		t.Errorf("Limit should be 10, got %d", cfg.Limit)
	}
	if cfg.MaxTokens != 2000 {
		t.Errorf("MaxTokens should be 2000, got %d", cfg.MaxTokens)
	}
	if cfg.MaxAgeDays != 30 {
		t.Errorf("MaxAgeDays should be 30, got %d", cfg.MaxAgeDays)
	}
	if len(cfg.KindsFilter) != 1 {
		t.Errorf("KindsFilter should have 1 item, got %d", len(cfg.KindsFilter))
	}
}

// TestRecentMemoryConfig_FillDefaults_PreservesUserValues 验证 fillDefaults
// 不会覆盖用户已设置的非零值。
func TestRecentMemoryConfig_FillDefaults_PreservesUserValues(t *testing.T) {
	cfg := RecentMemoryConfig{
		Enabled:     false,
		Limit:       100,
		MaxTokens:   5000,
		MaxAgeDays:  90,
		KindsFilter: []string{"llm_input", "llm_output"},
	}
	cfg.fillDefaults()
	if cfg.Limit != 100 {
		t.Errorf("user-set Limit should be preserved: got %d", cfg.Limit)
	}
	if cfg.MaxTokens != 5000 {
		t.Errorf("user-set MaxTokens should be preserved: got %d", cfg.MaxTokens)
	}
	if cfg.MaxAgeDays != 90 {
		t.Errorf("user-set MaxAgeDays should be preserved: got %d", cfg.MaxAgeDays)
	}
	if len(cfg.KindsFilter) != 2 {
		t.Errorf("user-set KindsFilter should be preserved: got %v", cfg.KindsFilter)
	}
}

// TestApplicationYML_HasRecentMemoryBlock 验证 workdir/config/application.yml
// 包含 recent_memory 块（这是给运维/部署的"必看字段"）。
//
// 这是文档级测试:防止 yml 字段被无意中删除。
func TestApplicationYML_HasRecentMemoryBlock(t *testing.T) {
	// 找到项目根的 application.yml（测试运行在项目根目录）
	ymlPath := filepath.Join("..", "..", "workdir", "config", "application.yml")
	data, err := os.ReadFile(ymlPath)
	if err != nil {
		t.Skipf("application.yml not readable (skipping): %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "recent_memory:") {
		t.Error("application.yml missing 'recent_memory:' section")
	}
	if !strings.Contains(content, "enabled:") {
		t.Error("recent_memory section should contain 'enabled:' field")
	}
	if !strings.Contains(content, "limit:") {
		t.Error("recent_memory section should contain 'limit:' field")
	}
	if !strings.Contains(content, "max_tokens:") {
		t.Error("recent_memory section should contain 'max_tokens:' field")
	}
	if !strings.Contains(content, "max_age_days:") {
		t.Error("recent_memory section should contain 'max_age_days:' field")
	}
	if !strings.Contains(content, "kinds_filter:") {
		t.Error("recent_memory section should contain 'kinds_filter:' field")
	}
}
