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

// Package config 提供执行审批配置的加载与内存缓存。
//
// 设计目标：
//   - 把 localcommand 包内的"白名单 / 黑名单"从代码字面量迁出到 JSON 文件
//     （workdir/config/exec-approvals.json），由配置驱动而非代码硬编码。
//   - 启动时一次性 Load 到内存；运行期不允许 reload，避免并发修改带来的
//     不一致语义。
//   - 加载失败 / 文件缺失时降级到内置兜底（不阻塞进程启动）。
//
// 配置 schema 见 exec-approvals.json；顶层结构：
//
//	{
//	  "agents": {
//	    "<agent_name>": {
//	      "security":    "allowlist" | "denylist",
//	      "description": "策略说明",
//	      "allowlist":   [{"bash": "ls", "description": "..."}, ...],
//	      "denylist":    [{"bash": "rm", "description": "..."}, ...]
//	    }
//	  }
//	}
//
// security 字段语义：
//   - "allowlist"：只允许 allowlist 列出的命令；denylist 在此基础上"再次
//     显式拒绝"某些命令（双重保险，例如 rm 既不在 allowlist 也在 denylist）。
//   - "denylist"：默认允许所有命令，仅拒绝 denylist 列出的命令。
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
)

// CommandRule 一条命令规则。
type CommandRule struct {
	// Bash 命令名（如 ls / git / claude-code）。
	Bash string `json:"bash"`
	// Description 命令说明（喂给 LLM 帮助它理解语义；不做安全判定依据）。
	Description string `json:"description"`
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
	// Denylist 显式拒绝的命令清单（在 allowlist 之外叠加生效）。
	Denylist []CommandRule `json:"denylist"`
}

// ExecApprovals 整个 exec-approvals.json 文件的根结构。
type ExecApprovals struct {
	Agents map[string]AgentPolicy `json:"agents"`
}

// =====================================================================
// 内存存储：包级单例，启动时一次性 Load，运行期只读。
// =====================================================================

var (
	storeMu  sync.RWMutex
	store    *ExecApprovals
	loadedAt string // 用于日志：记录配置文件路径，便于排查"到底加载了哪份配置"
)

// Load 从 path 加载配置文件并写入内存。多次调用以最后一次为准（仅用于测试）。
//
// 错误策略：
//   - path 不存在：log 警告，返回 nil（调用方应继续用内置兜底）。
//   - JSON 解析失败：log 错误，返回 error（让调用方决定是否 fatal）。
func Load(path string) error {
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
			if strings.TrimSpace(r.Bash) == "" {
				return fmt.Errorf("config: %s.denylist[%d]: bash must be non-empty", agentName, i)
			}
		}
	}

	storeMu.Lock()
	store = &cfg
	loadedAt = path
	storeMu.Unlock()

	log.Printf("[config] exec-approvals.json loaded from %s (agents=%d)", path, len(cfg.Agents))
	return nil
}

// GetPolicy 返回 agentName 对应的策略；若不存在或未加载，返回 nil。
func GetPolicy(agentName string) *AgentPolicy {
	storeMu.RLock()
	defer storeMu.RUnlock()
	if store == nil {
		return nil
	}
	p, ok := store.Agents[agentName]
	if !ok {
		return nil
	}
	return &p
}

// IsConfigured 报告是否已加载过配置（用于在 main.go 启动时判断是否走内置兜底）。
func IsConfigured() bool {
	storeMu.RLock()
	defer storeMu.RUnlock()
	return store != nil
}

// LoadedPath 返回最后一次成功加载的配置文件路径（用于日志 / 调试）。
func LoadedPath() string {
	storeMu.RLock()
	defer storeMu.RUnlock()
	return loadedAt
}

// ListAgentNames 返回当前配置里所有 agent 名称（按字典序排序）。
//
// 用途：main.go 启动时遍历此列表，对每个 agent 各调用一次
// localcommand.SetAllowedCommands(name) / SetDeniedCommands(name)，
// 让每个子 agent 自动拿到自己的那段策略。未在配置中出现的 agent
// 不会被遍历到，沙箱里"无黑白名单"——具体语义由 localcommand
// 决定（默认会落到"全部走 WhitelistAuth 授权"分支）。
func ListAgentNames() []string {
	storeMu.RLock()
	defer storeMu.RUnlock()
	if store == nil {
		return nil
	}
	names := make([]string, 0, len(store.Agents))
	for name := range store.Agents {
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

// DenyCommandSet 返回 policy 中所有 denylist 命令名的小写 set。
func DenyCommandSet(p *AgentPolicy) map[string]CommandRule {
	if p == nil {
		return nil
	}
	out := make(map[string]CommandRule, len(p.Denylist))
	for _, r := range p.Denylist {
		key := strings.ToLower(strings.TrimSpace(r.Bash))
		if key == "" {
			continue
		}
		out[key] = r
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
