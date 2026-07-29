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

package model

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/cloudwego/eino-ext/components/model/ark"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	cbutils "github.com/cloudwego/eino/utils/callbacks"
	arkModel "github.com/volcengine/volcengine-go-sdk/service/arkruntime/model"

	"github.com/yichouchou/yichouchou_claw/internal/config"
)

// NewChatModel 根据 application.yml 中的 llm 配置创建 ChatModel。
//
// 优先级：yaml 配置 > 环境变量（环境变量作为兜底）。
// MODEL_TYPE 环境变量会覆盖 yaml 的 llm.type（兼容旧行为）。
func NewChatModel() model.ToolCallingChatModel {
	appCfg := config.GetApplication()
	if appCfg == nil {
		// 配置未加载（极端情况），退回纯环境变量模式。
		return newChatModelFromEnv()
	}

	modelType := strings.ToLower(firstNonEmpty(os.Getenv("MODEL_TYPE"), appCfg.LLM.Type))

	switch modelType {
	case "ark":
		apiKey := firstNonEmpty(os.Getenv("ARK_API_KEY"), appCfg.LLM.Ark.APIKey)
		modelName := firstNonEmpty(os.Getenv("ARK_MODEL"), appCfg.LLM.Ark.Model)
		baseURL := firstNonEmpty(os.Getenv("ARK_BASE_URL"), appCfg.LLM.Ark.BaseURL)

		cm, err := ark.NewChatModel(context.Background(), &ark.ChatModelConfig{
			APIKey:  apiKey,
			Model:   modelName,
			BaseURL: baseURL,
			Thinking: &arkModel.Thinking{
				Type: arkModel.ThinkingTypeDisabled,
			},
		})
		if err != nil {
			log.Fatalf("ark.NewChatModel failed: %v", err)
		}
		return cm
	default:
		// OpenAI 兼容协议（包括 anthropic 兼容端点，但这里走的是 OpenAI SDK 路径，
		// 用 Anthropic SDK 的路径见 NewChatModelForChatAgent）
		apiKey := firstNonEmpty(os.Getenv("OPENAI_API_KEY"), appCfg.LLM.OpenAI.APIKey)
		modelName := firstNonEmpty(os.Getenv("OPENAI_MODEL"), appCfg.LLM.OpenAI.Model)
		baseURL := firstNonEmpty(os.Getenv("OPENAI_BASE_URL"), appCfg.LLM.OpenAI.BaseURL)
		byAzure := os.Getenv("OPENAI_BY_AZURE") == "true"
		if !byAzure {
			byAzure = appCfg.LLM.OpenAI.ByAzure
		}

		cm, err := openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
			APIKey:  apiKey,
			Model:   modelName,
			BaseURL: baseURL,
			ByAzure: byAzure,
		})
		if err != nil {
			log.Fatalf("openai.NewChatModel failed: %v", err)
		}
		return cm
	}
}

// newChatModelFromEnv 是 NewChatModel 的纯环境变量版本（兜底）。
func newChatModelFromEnv() model.ToolCallingChatModel {
	modelType := strings.ToLower(os.Getenv("MODEL_TYPE"))
	if modelType == "ark" {
		cm, err := ark.NewChatModel(context.Background(), &ark.ChatModelConfig{
			APIKey:  os.Getenv("ARK_API_KEY"),
			Model:   os.Getenv("ARK_MODEL"),
			BaseURL: os.Getenv("ARK_BASE_URL"),
			Thinking: &arkModel.Thinking{
				Type: arkModel.ThinkingTypeDisabled,
			},
		})
		if err != nil {
			log.Fatalf("ark.NewChatModel failed: %v", err)
		}
		return cm
	}
	cm, err := openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		Model:   os.Getenv("OPENAI_MODEL"),
		BaseURL: os.Getenv("OPENAI_BASE_URL"),
		ByAzure: os.Getenv("OPENAI_BY_AZURE") == "true",
	})
	if err != nil {
		log.Fatalf("openai.NewChatModel failed: %v", err)
	}
	return cm
}

// NewChatModelForChatAgent 创建专用于 ChatAgent 的 ChatModel，
// 使用 Anthropic SDK 并配置 Minimaxi 的 web_search 服务端工具。
//
// 配置来源：application.yml 的 llm.anthropic；环境变量作为兜底。
//
// 根据文档：https://platform.minimaxi.com/docs/guides/server-tools
// web_search 是服务端工具，通过 Anthropic SDK 的 tools 参数添加。
func NewChatModelForChatAgent() model.ToolCallingChatModel {
	appCfg := config.GetApplication()
	if appCfg == nil {
		// 配置未加载，退回纯环境变量版本
		return newChatModelForChatAgentFromEnv()
	}

	modelType := strings.ToLower(firstNonEmpty(os.Getenv("MODEL_TYPE"), appCfg.LLM.Type))

	// Ark 不支持 Minimaxi 的服务端工具，返回普通 ChatModel
	if modelType == "ark" {
		return NewChatModel()
	}

	// 读取 Anthropic 配置
	apiKey := firstNonEmpty(os.Getenv("OPENAI_API_KEY"), os.Getenv("ANTHROPIC_API_KEY"), appCfg.LLM.Anthropic.APIKey)
	modelName := firstNonEmpty(os.Getenv("OPENAI_MODEL"), appCfg.LLM.Anthropic.Model)
	baseURL := firstNonEmpty(os.Getenv("ANTHROPIC_BASE_URL"), appCfg.LLM.Anthropic.BaseURL)
	maxTokens := appCfg.LLM.Anthropic.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	enableWebSearch := appCfg.LLM.Anthropic.EnableWebSearch

	// 创建 Anthropic Adapter
	var adapterOpts []AnthropicAdapterOption
	adapterOpts = append(adapterOpts,
		WithAPIKey(apiKey),
		WithBaseURL(baseURL),
		WithModel(modelName),
		WithMaxTokens(maxTokens),
	)

	if enableWebSearch {
		// Minimaxi 官方调用示例（curl）：
		//   "tools": [{
		//       "type": "web_search_20250305",
		//       "name": "web_search"
		//   }]
		adapterOpts = append(adapterOpts, WithServerTools([]anthropic.ToolUnionParam{
			{
				OfWebSearchTool20250305: &anthropic.WebSearchTool20250305Param{
					Name: "web_search",          // constant.WebSearch = "web_search"
					Type: "web_search_20250305", // constant.WebSearch20250305 = "web_search_20250305"
				},
			},
		}))
	}

	return NewAnthropicAdapter(adapterOpts...)
}

// newChatModelForChatAgentFromEnv 是 NewChatModelForChatAgent 的纯环境变量版本（兜底）。
func newChatModelForChatAgentFromEnv() model.ToolCallingChatModel {
	modelType := strings.ToLower(os.Getenv("MODEL_TYPE"))
	if modelType == "ark" {
		return NewChatModel()
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	modelName := os.Getenv("OPENAI_MODEL")
	if modelName == "" {
		modelName = "MiniMax-M3"
	}
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.minimaxi.com/anthropic"
	}

	return NewAnthropicAdapter(
		WithAPIKey(apiKey),
		WithBaseURL(baseURL),
		WithModel(modelName),
		WithMaxTokens(4096),
		WithServerTools([]anthropic.ToolUnionParam{
			{
				OfWebSearchTool20250305: &anthropic.WebSearchTool20250305Param{
					Name: "web_search",
					Type: "web_search_20250305",
				},
			},
		}),
	)
}

// firstNonFallback 返回第一个非空字符串；全空时返回 fallback。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func GetInputLoggerCallback() callbacks.Handler {
	return cbutils.NewHandlerHelper().ChatModel(&cbutils.ModelCallbackHandler{
		OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *model.CallbackInput) context.Context {
			time.Sleep(20 * time.Second)
			fmt.Printf("\n========================================\n")
			fmt.Printf("[ChatModel Input] Agent: %s\n", info.Name)
			fmt.Printf("========================================\n")
			for i, msg := range input.Messages {
				fmt.Printf("  Message %d [%s]: %s\n", i+1, msg.Role, msg.Content)
				if len(msg.ToolCalls) > 0 {
					fmt.Printf("    Tool Calls: %d\n", len(msg.ToolCalls))
					for j, tc := range msg.ToolCalls {
						fmt.Printf("      %d. %s: %s\n", j+1, tc.Function.Name, tc.Function.Arguments)
					}
				}
			}
			fmt.Printf("========================================\n\n")
			return ctx
		},
	}).Handler()
}
