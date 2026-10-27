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

package subagents

import (
	einomodel "github.com/cloudwego/eino/components/model"
)

// testModelOverride 是单元测试注入 fake chat model 的钩子。
//
// 用途：回归测试需要断言「交给模型的工具列表」里有没有 transfer_to_agent，
// 而 eino 是在运行时（prepareExecContext）才决定注册哪些工具的，
// 只有把 model 换成可控的 fake 才能在测试里稳定地抓到这个列表。
//
// 生产环境恒为 nil —— pickChatModel 会原样返回默认 model，行为与改造前完全一致。
// 该变量只在同包的 _test.go 里被赋值。
var testModelOverride einomodel.ToolCallingChatModel

// pickChatModel 返回该 agent 实际使用的 chat model：
// 优先使用测试注入的 fake model，否则返回调用方给的默认 model。
func pickChatModel(def einomodel.ToolCallingChatModel) einomodel.ToolCallingChatModel {
	if testModelOverride != nil {
		return testModelOverride
	}
	return def
}
