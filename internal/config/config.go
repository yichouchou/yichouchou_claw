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

// Package config 提供应用配置的加载与内存缓存。
//
// 两类配置：
//  1. 执行审批配置（白名单/黑名单）：从 exec-approvals.json 加载，喂给沙箱。
//  2. 应用配置（端口、LLM、路径、超时等）：从 application.yml 加载，供各模块读取。
//
// 设计目标：
//   - 把配置从代码字面量 / 环境变量硬编码迁出到 YAML 文件，由配置驱动。
//   - 启动时一次性 Load 到内存；运行期不允许 reload，避免并发修改带来的不一致语义。
//   - 加载失败 / 文件缺失时降级到内置兜底（不阻塞进程启动）。
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// =====================================================================
// 第一部分：执行审批配置（exec-approvals.json）
// =====================================================================

// CommandRule 一条命令规则（用于 allowlist；denylist 用 DenylistRule）。
type CommandRule struct {
	// Bash 命令名（如 ls / git / claude-code）。
	Bash string `json:"bash"`
	// Description 命令说明（喂给 LLM 帮助它理解语义；不做安全判定依据）。
	Description string `json:"description"`
}

// DenylistRule 一条拒绝规则。支持两种规则类型：
//
//   - {"bash": "<cmd>",  "description": "..."} —— 命令名规则，匹配 argv[0]
//   - {"path": "<path>", "description": "..."} —— 路径规则，匹配命令字符串里的路径
//
// 两条规则同时启用，命中任一即拒绝（沙箱在 "硬禁止" 之后、"软禁止" 之前检查）。
type DenylistRule struct {
	// Bash 命令名（与 Path 二选一）。
	Bash string `json:"bash,omitempty"`
	// Path 敏感路径前缀（与 Bash 二选一）。
	Path string `json:"path,omitempty"`
	// Description 规则说明（不参与判定）。
	Description string `json:"description"`
}

// IsCommandRule 返回是否命令名规则。
func (r DenylistRule) IsCommandRule() bool {
	return strings.TrimSpace(r.Bash) != ""
}

// IsPathRule 返回是否路径规则。
func (r DenylistRule) IsPathRule() bool {
	return strings.TrimSpace(r.Path) != ""
}

// AgentPolicy 单个 agent 的执行审批策略。
type AgentPolicy struct {
	// Security 模式：allowlist / denylist。
	// 实际安全判定在 localcommand.IsDangerousWithContext 里走
	// "白名单 + 软/硬禁止 + 授权"三段链路，policy 只决定"白名单"这一段。
	Security string `json:"security"`
	// Description 策略意图（仅供人阅读，不参与判定）。
	Description string `json:"description"`
	// Allowlist 允许执行的命令清单。
	Allowlist []CommandRule `json:"allowlist"`
	// Denylist 显式拒绝的规则清单（命令名 + 路径），在 allowlist 之外叠加生效。
	Denylist []DenylistRule `json:"denylist"`
}

// ExecApprovals 整个 exec-approvals.json 文件的根结构。
type ExecApprovals struct {
	Agents map[string]AgentPolicy `json:"agents"`
}

// =====================================================================
// 第二部分：应用配置（application.yml）
// =====================================================================

// ServerConfig HTTP 服务配置。
type ServerConfig struct {
	// Host 监听地址，如 ":28080"
	Host string `yaml:"host"`
}

// OpenAIConfig OpenAI 兼容协议配置。
type OpenAIConfig struct {
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url"`
	ByAzure bool   `yaml:"by_azure"`
}

// ArkConfig 火山方舟配置。
type ArkConfig struct {
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url"`
}

// AnthropicConfig Anthropic 配置（用于 ChatAgent + Minimaxi 兼容端点）。
type AnthropicConfig struct {
	APIKey          string `yaml:"api_key"`
	Model           string `yaml:"model"`
	BaseURL         string `yaml:"base_url"`
	MaxTokens       int64  `yaml:"max_tokens"`
	EnableWebSearch bool   `yaml:"enable_web_search"`
}

// LLMConfig LLM 总配置：选择哪个 ChatModel 实现 + 三家 provider 的配置。
type LLMConfig struct {
	// Type 决定走哪个 ChatModel：
	//   "ark"       → 火山方舟（Ark SDK）
	//   "anthropic" → Anthropic SDK（默认；支持 Minimaxi 服务端工具）
	//   其他/空     → OpenAI 兼容协议
	Type string `yaml:"type"`

	OpenAI    OpenAIConfig    `yaml:"openai"`
	Ark       ArkConfig       `yaml:"ark"`
	Anthropic AnthropicConfig `yaml:"anthropic"`
}

// CozeLoopConfig CozeLoop trace 配置。
type CozeLoopConfig struct {
	Enabled     bool   `yaml:"enabled"`
	WorkspaceID string `yaml:"workspace_id"`
	APIToken    string `yaml:"api_token"`
}

// TracingConfig tracing 配置。
type TracingConfig struct {
	CozeLoop CozeLoopConfig `yaml:"coze_loop"`
}

// PathsConfig 路径配置（相对于 cwd 或绝对路径）。
type PathsConfig struct {
	Workdir   string `yaml:"workdir"`
	Skills    string `yaml:"skills"`
	Memory    string `yaml:"memory"`
	Approvals string `yaml:"approvals"`
}

// RefinerConfig LLM 摘要精炼 worker 配置。
type RefinerConfig struct {
	Workers               int     `yaml:"workers"`
	QueueSize             int     `yaml:"queue_size"`
	PerCallTimeoutSeconds int     `yaml:"per_call_timeout_seconds"`
	ContentSnippetBytes   int     `yaml:"content_snippet_bytes"`
	ModelTemperature      float32 `yaml:"model_temperature"`
	MaxSummaryLength      int     `yaml:"max_summary_length"`
}

// MemoryConfig memory 配置。
type MemoryConfig struct {
	Enabled          bool          `yaml:"enabled"`
	LLMRefineEnabled bool          `yaml:"llm_refine_enabled"`
	Refiner          RefinerConfig `yaml:"refiner"`
}

// SessionConfig session 配置。
type SessionConfig struct {
	MaxRounds int `yaml:"max_rounds"`
}

// LocalCommandConfig 沙箱命令执行配置。
type LocalCommandConfig struct {
	SingleCommandTimeoutSeconds   int `yaml:"single_command_timeout_seconds"`
	PipelineCommandTimeoutSeconds int `yaml:"pipeline_command_timeout_seconds"`
}

// SandboxConfig 沙箱安全配置（2026-07-27 新增，集成四个安全阶段）。
//
// 四个安全阶段的配置入口：
//   - PathACL：read/write/exec 三类 denied 路径
//   - Denybin：是否启用 OS 层 denybin PATH 注入（默认 true）
//   - AST：是否启用 mvdan.cc/sh AST 解析（默认 true）
type SandboxConfig struct {
	// Enabled 是否启用沙箱安全（false = 不做任何安全检查,极不推荐）
	Enabled bool `yaml:"enabled"`

	// AST 阶段 1: mvdan.cc/sh AST 解析安全检查
	AST *ASTConfig `yaml:"ast,omitempty"`

	// Denybin 阶段 2: OS 层 denybin PATH 注入
	Denybin *DenybinConfig `yaml:"denybin,omitempty"`

	// PathACL 阶段 3+4: read/write/exec denied 路径
	PathACL *PathACLConfig `yaml:"path_acl,omitempty"`
}

// ASTConfig AST 解析安全检查配置。
type ASTConfig struct {
	// Enabled 是否启用 AST 解析。默认 true。
	// 设为 false 时,checkShellChainSafety 会跳过 AST 检查（不推荐）。
	Enabled bool `yaml:"enabled"`
	// FailClosedOnParseError 解析失败时是否拒绝执行。默认 true。
	// (false 时退化为仅字符串扫描,降级到阶段 2-4)
	FailClosedOnParseError bool `yaml:"fail_closed_on_parse_error"`
}

// DenybinConfig denybin PATH 注入配置。
type DenybinConfig struct {
	// Enabled 是否启用 denybin。默认 true。
	Enabled bool `yaml:"enabled"`
	// DeniedCommands 自定义拒绝的命令列表（默认使用 path_acl.exec_denied）
	DeniedCommands []string `yaml:"denied_commands"`
}

// PathACLConfig 路径 ACL 配置（read/write/exec 三类）。
type PathACLConfig struct {
	// ReadDenied 禁止读取的路径前缀
	ReadDenied []string `yaml:"read_denied"`
	// WriteDenied 禁止写入的路径前缀
	WriteDenied []string `yaml:"write_denied"`
	// ExecDenied 禁止执行的命令名（同时被 denybin 使用）
	ExecDenied []string `yaml:"exec_denied"`
}

// ApplicationConfig application.yml 根结构。
type ApplicationConfig struct {
	Server       ServerConfig       `yaml:"server"`
	LLM          LLMConfig          `yaml:"llm"`
	Tracing      TracingConfig      `yaml:"tracing"`
	Paths        PathsConfig        `yaml:"paths"`
	Memory       MemoryConfig       `yaml:"memory"`
	Session      SessionConfig      `yaml:"session"`
	LocalCommand LocalCommandConfig `yaml:"localcommand"`
	Sandbox      SandboxConfig      `yaml:"sandbox"`
}

// =====================================================================
// 内存存储：包级单例，启动时一次性 Load，运行期只读。
// =====================================================================

var (
	approvalsMu sync.RWMutex
	approvals   *ExecApprovals
	approvalsAt string

	appMu sync.RWMutex
	app   *ApplicationConfig
	appAt string
)

// LoadApprovals 从 path 加载 exec-approvals.json 并写入内存。多次调用以最后一次为准（仅用于测试）。
//
// 错误策略：
//   - path 不存在：log 警告，返回 nil（调用方应继续用内置兜底）。
//   - JSON 解析失败：log 错误，返回 error（让调用方决定是否 fatal）。
func LoadApprovals(path string) error {
	return loadApprovals(path)
}

// Load 是 LoadApprovals 的兼容别名（保留旧 API）。
//
// Deprecated: 新代码请用 LoadApprovals。
func Load(path string) error {
	return loadApprovals(path)
}

func loadApprovals(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[config] exec-approvals.json not loaded from %s: %v (using built-in fallback)", path, err)
		return nil
	}

	var cfg ExecApprovals
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentPolicy{}
	}

	// 校验：每条 rule 必须有 bash 名；description 可选但建议填。
	for agentName, policy := range cfg.Agents {
		for i, r := range policy.Allowlist {
			if strings.TrimSpace(r.Bash) == "" {
				return fmt.Errorf("config: %s.allowlist[%d]: bash must be non-empty", agentName, i)
			}
		}
		for i, r := range policy.Denylist {
			if !r.IsCommandRule() && !r.IsPathRule() {
				return fmt.Errorf("config: %s.denylist[%d]: bash or path must be non-empty", agentName, i)
			}
		}
	}

	approvalsMu.Lock()
	approvals = &cfg
	approvalsAt = path
	approvalsMu.Unlock()

	log.Printf("[config] exec-approvals.json loaded from %s (agents=%d)", path, len(cfg.Agents))
	return nil
}

// LoadApplication 从 path 加载 application.yml 并写入内存。
//
// 错误策略：
//   - path 不存在：log 警告，返回 nil error（调用方继续用零值兜底）。
//   - YAML 解析失败：返回 error。
//   - 字段缺失：用零值填充（避免整文件缺失一个字段就 fatal）。
func LoadApplication(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[config] application.yml not loaded from %s: %v (using zero-value fallback)", path, err)
		return nil
	}

	cfg := defaultApplicationConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}

	appMu.Lock()
	app = cfg
	appAt = path
	appMu.Unlock()

	log.Printf("[config] application.yml loaded from %s", path)
	return nil
}

// defaultApplicationConfig 返回内置兜底配置（所有字段都给合理默认）。
func defaultApplicationConfig() *ApplicationConfig {
	return &ApplicationConfig{
		Server: ServerConfig{
			Host: ":28080",
		},
		LLM: LLMConfig{
			Type: "anthropic",
			OpenAI: OpenAIConfig{
				ByAzure: false,
			},
			Anthropic: AnthropicConfig{
				Model:           "MiniMax-M3",
				BaseURL:         "https://api.minimaxi.com/anthropic",
				MaxTokens:       4096,
				EnableWebSearch: true,
			},
		},
		Tracing: TracingConfig{
			CozeLoop: CozeLoopConfig{
				Enabled: false,
			},
		},
		Paths: PathsConfig{
			Workdir:   "workdir",
			Skills:    "workdir/skills",
			Memory:    "workdir",
			Approvals: "workdir/config/exec-approvals.json",
		},
		Memory: MemoryConfig{
			Enabled:          true,
			LLMRefineEnabled: true,
			Refiner: RefinerConfig{
				Workers:               2,
				QueueSize:             256,
				PerCallTimeoutSeconds: 30,
				ContentSnippetBytes:   1500,
				ModelTemperature:      0.2,
				MaxSummaryLength:      100,
			},
		},
		Session: SessionConfig{
			MaxRounds: 12,
		},
		LocalCommand: LocalCommandConfig{
			SingleCommandTimeoutSeconds:   30,
			PipelineCommandTimeoutSeconds: 60,
		},
	}
}

// GetApplication 返回当前 application.yml 配置。nil 表示未加载（极端情况）。
//
// 调用方读到的总是非 nil（LoadApplication 在文件缺失时会写入默认配置），
// 但为了兼容未调用 LoadApplication 的场景，返回 nil 也合法。
func GetApplication() *ApplicationConfig {
	appMu.RLock()
	defer appMu.RUnlock()
	return app
}

// ApplicationLoadedPath 返回最后一次成功加载的 application.yml 路径（用于日志 / 调试）。
func ApplicationLoadedPath() string {
	appMu.RLock()
	defer appMu.RUnlock()
	return appAt
}

// GetPolicy 返回 agentName 对应的策略；若不存在或未加载，返回 nil。
func GetPolicy(agentName string) *AgentPolicy {
	approvalsMu.RLock()
	defer approvalsMu.RUnlock()
	if approvals == nil {
		return nil
	}
	p, ok := approvals.Agents[agentName]
	if !ok {
		return nil
	}
	return &p
}

// IsApprovalsConfigured 报告是否已加载过 approvals 配置。
func IsApprovalsConfigured() bool {
	approvalsMu.RLock()
	defer approvalsMu.RUnlock()
	return approvals != nil
}

// LoadedApprovalsPath 返回最后一次成功加载的 approvals 配置文件路径（用于日志 / 调试）。
func LoadedApprovalsPath() string {
	approvalsMu.RLock()
	defer approvalsMu.RUnlock()
	return approvalsAt
}

// ListAgentNames 返回当前配置里所有 agent 名称（按字典序排序）。
//
// 用途：main.go 启动时遍历此列表，对每个 agent 各调用一次
// localcommand.SetAllowedCommands(name) / SetDeniedCommands(name)，
// 让每个子 agent 自动拿到自己的那段策略。未在配置中出现的 agent
// 不会被遍历到，沙箱里"无黑白名单"——具体语义由 localcommand
// 决定（默认会落到"全部走 WhitelistAuth 授权"分支）。
func ListAgentNames() []string {
	approvalsMu.RLock()
	defer approvalsMu.RUnlock()
	if approvals == nil {
		return nil
	}
	names := make([]string, 0, len(approvals.Agents))
	for name := range approvals.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// =====================================================================
// 辅助：把 policy 转成"白名单命令集合"和"黑名单命令集合"，供 localcommand 使用。
// =====================================================================

// AllowCommandSet 返回 policy 中所有 allowlist 命令名的小写 set。
// localcommand 用此 set 替换原来代码内的 AllowedCommands map（命令名部分）。
func AllowCommandSet(p *AgentPolicy) map[string]CommandRule {
	if p == nil {
		return nil
	}
	out := make(map[string]CommandRule, len(p.Allowlist))
	for _, r := range p.Allowlist {
		key := strings.ToLower(strings.TrimSpace(r.Bash))
		if key == "" {
			continue
		}
		out[key] = r
	}
	return out
}

// DenyCommandSet 返回 policy 中所有 denylist 命令名规则的小写 set。
// 仅收集 IsCommandRule() 为真的条目；路径规则不计入。
func DenyCommandSet(p *AgentPolicy) map[string]DenylistRule {
	if p == nil {
		return nil
	}
	out := make(map[string]DenylistRule, len(p.Denylist))
	for _, r := range p.Denylist {
		if !r.IsCommandRule() {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(r.Bash))
		if key == "" {
			continue
		}
		out[key] = r
	}
	return out
}

// DenyPathSet 返回 policy 中所有 denylist 路径规则的 map。
// 仅收集 IsPathRule() 为真的条目；命令规则不计入。
func DenyPathSet(p *AgentPolicy) map[string]DenylistRule {
	if p == nil {
		return nil
	}
	out := make(map[string]DenylistRule, len(p.Denylist))
	for _, r := range p.Denylist {
		if !r.IsPathRule() {
			continue
		}
		path := strings.TrimSpace(r.Path)
		if path == "" {
			continue
		}
		out[path] = r
	}
	return out
}

// FormatCommandsForLLM 把 policy 转成"命令: 描述"的多行字符串，
// 喂给 LLM 当白名单速查表。
func FormatCommandsForLLM(p *AgentPolicy) string {
	if p == nil {
		return ""
	}
	lines := make([]string, 0, len(p.Allowlist)+len(p.Denylist))
	for _, r := range p.Allowlist {
		lines = append(lines, fmt.Sprintf("  %s: %s", r.Bash, r.Description))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
