package localcommand

import (
	"os"
	"strings"
)

// stripOtherLLMEnv 剥离宿主机环境变量里"其他 LLM 提供方"前缀,
// 避免 Claude Code CLI 启动时被路由到 MiniMax / OpenAI / Volcengine
// 等不可用 provider,导致 claude -p 输出空 stdout 或无响应。
//
// 保留:
//   - ANTHROPIC_* / CLAUDE_* —— Claude Code CLI 真实 API key
//   - 系统级: PATH / TZ / LANG / USER / HOME 等
//   - 业务级: GH_TOKEN / DOCKER_* / KUBE_* 等用户配置
//
// 剥离:
//   - ARK_* / MINIMAX_* —— MiniMax(项目 .env 里有)
//   - OPENAI_* / AZURE_OPENAI_* —— OpenAI/Azure
//   - VOLCENGINE_* / VOLC_* —— 火山引擎
//   - COZE_* / COZELOOP_* —— 扣子
//   - GEMINI_* / MISTRAL_* / GROQ_* / DEEPSEEK_* / MOONSHOT_*
//     / QIANFAN_* / BAIDU_* / TENCENT_* —— 国内外其他 LLM 提供方
//   - EINO_* / HERTZ_* —— 框架内部 env,子进程用不到
//
// 实现说明:基于前缀匹配,如果未来有更多 provider,直接扩展 strippedPrefixes。
func stripOtherLLMEnv(env []string) []string {
	strippedPrefixes := []string{
		"ARK_", "MINIMAX_",
		"OPENAI_", "AZURE_OPENAI_",
		"VOLCENGINE_", "VOLC_",
		"COZE_", "COZELOOP_",
		"GEMINI_", "MISTRAL_", "GROQ_", "DEEPSEEK_",
		"MOONSHOT_", "QIANFAN_", "BAIDU_", "TENCENT_",
		// 框架内部 env
		"EINO_", "HERTZ_",
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		skip := false
		for _, p := range strippedPrefixes {
			if strings.HasPrefix(kv, p) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}

// ensureBaseEnv 兜底补全 PATH/TZ/LANG 等系统级 env,避免子进程找不到系统命令
// 或时间/语言异常。这是沙箱"最小 env 集"保证,与 stripOtherLLMEnv 组合使用。
func ensureBaseEnv(env []string) []string {
	if !hasEnvPrefix(env, "PATH=") {
		env = append(env, "PATH="+defaultPATH())
	}
	if !hasEnvPrefix(env, "TZ=") {
		env = append(env, "TZ=Asia/Shanghai")
	}
	if !hasEnvPrefix(env, "LANG=") {
		env = append(env, "LANG=zh_CN.UTF-8")
	}
	return env
}

func hasEnvPrefix(env []string, prefix string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// defaultPATH 返回当前平台的默认 PATH。
// Unix 和 Windows 各有自己的默认值,由 build-tagged 文件提供 defaultPATH 实现。
//
// 注意:如果当前 OS 走的是 WSL,PATH 应该用 Unix 形式。
func defaultPATH() string {
	if v, ok := lookupDefaultPATH(); ok {
		return v
	}
	return "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
}

// sandboxBaseEnv 是 sandboxEnv 的跨平台共享实现:
// 1. 取宿主机 env
// 2. 剥离"其他 LLM provider"前缀
// 3. 兜底补 PATH / TZ / LANG
func sandboxBaseEnv() []string {
	env := os.Environ()
	env = stripOtherLLMEnv(env)
	env = ensureBaseEnv(env)
	return env
}
