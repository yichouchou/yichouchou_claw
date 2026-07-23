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

package localcommand

import (
	"sync"

	"github.com/yichouchou/yichouchou_claw/internal/config"
)

// =====================================================================
// per-agent 白/黑名单缓存
// =====================================================================
//
// 旧设计（v1）：单进程共享一份 allowedCommandsCache / deniedCommandsCache。
//   缺点：后调用的 Set 覆盖前一次，多 agent 共存时互相覆盖。
//
// 新设计（v2）：以 agent 名（e.g. "RouterAgent" / "LocalCommandAgent" /
//   "WeatherAgent"）为 key 分别缓存。isCommandAllowed / isCommandDenied
//   在做安全判定时再根据"当前 agent 名"取自己那段。
//
// main.go 启动时遍历 config.ListAgentNames()，对每个 agent 名调用一次
// SetAllowedCommands / SetDeniedCommands。未在配置中出现的 agent 名，
// cache 里查不到，沙箱走"全部 WhitelistAuth 授权"兜底。

var (
	cacheMu            sync.RWMutex
	allowedByAgent     = map[string]map[string]string{} // agentName -> cmd -> description
	deniedByAgent      = map[string]map[string]string{} // agentName -> cmd -> description
	loadedAgentNamesMu sync.RWMutex
	loadedAgentNames   []string
)

// SetAllowedCommands 把 agentName 的白名单注入到 per-agent 缓存。
//
// 行为：
//   - config.GetPolicy(agentName) 为 nil → 该 agent 在缓存里映射为空 map
//     （沙箱里"无白名单"，所有命令落 WhitelistAuth 授权分支）。
//   - 否则把 allowlist 注入到 allowedByAgent[agentName]。
func SetAllowedCommands(agentName string) {
	policy := config.GetPolicy(agentName)
	var newSet map[string]string
	if policy != nil {
		set := config.AllowCommandSet(policy)
		newSet = make(map[string]string, len(set))
		for cmd, rule := range set {
			newSet[cmd] = rule.Description
		}
	} else {
		newSet = map[string]string{}
	}

	cacheMu.Lock()
	allowedByAgent[agentName] = newSet
	cacheMu.Unlock()

	markLoaded(agentName)
}

// SetDeniedCommands 把 agentName 的 denylist 注入到 per-agent 缓存。
func SetDeniedCommands(agentName string) {
	policy := config.GetPolicy(agentName)
	var newSet map[string]string
	if policy != nil {
		set := config.DenyCommandSet(policy)
		newSet = make(map[string]string, len(set))
		for cmd, rule := range set {
			newSet[cmd] = rule.Description
		}
	} else {
		newSet = map[string]string{}
	}

	cacheMu.Lock()
	deniedByAgent[agentName] = newSet
	cacheMu.Unlock()

	markLoaded(agentName)
}

// markLoaded 记录已加载过配置的 agent 名，供 LoadedAgentNames 返回。
// 重复 mark 是幂等的（同一 agent 名只记一次）。
func markLoaded(agentName string) {
	if agentName == "" {
		return
	}
	loadedAgentNamesMu.Lock()
	for _, n := range loadedAgentNames {
		if n == agentName {
			loadedAgentNamesMu.Unlock()
			return
		}
	}
	loadedAgentNames = append(loadedAgentNames, agentName)
	loadedAgentNamesMu.Unlock()
}

// LoadedAgentNames 返回已通过 Set 注入过的 agent 名列表（供 main.go 启动日志用）。
func LoadedAgentNames() []string {
	loadedAgentNamesMu.RLock()
	defer loadedAgentNamesMu.RUnlock()
	out := make([]string, len(loadedAgentNames))
	copy(out, loadedAgentNames)
	return out
}

// allowedForAgent 取 agentName 的 allowlist 缓存（只读视图，内部勿修改）。
// 未加载的 agent 返回 nil（沙箱走兜底授权）。
func allowedForAgent(agentName string) map[string]string {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	m, ok := allowedByAgent[agentName]
	if !ok {
		return nil
	}
	return m
}

// deniedForAgent 取 agentName 的 denylist 缓存（只读视图）。
func deniedForAgent(agentName string) map[string]string {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	m, ok := deniedByAgent[agentName]
	if !ok {
		return nil
	}
	return m
}
