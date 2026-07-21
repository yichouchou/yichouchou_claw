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
	"context"
	"fmt"
	"log"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"

	"github.com/yichouchou/yichouchou_claw/adk/common/model"
	messagehandler "github.com/yichouchou/yichouchou_claw/adk/middlewares/messageHandler"
	"github.com/yichouchou/yichouchou_claw/internal/localcommand"
	"github.com/yichouchou/yichouchou_claw/internal/session"
)

type GetWeatherInput struct {
	City string `json:"city"`
}

func NewWeatherAgent() adk.Agent {
	weatherTool, err := utils.InferTool(
		"get_weather",
		"获取指定城市的当前天气。",
		func(ctx context.Context, input *GetWeatherInput) (string, error) {
			return fmt.Sprintf(`%s 的温度是 25°C`, input.City), nil
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "WeatherAgent",
		Description: "这个 agent 可以获取指定城市的当前天气。",
		Instruction: `你的唯一职责是使用 'get_weather' 工具获取指定城市的天气。
调用工具后，直接向用户报告结果。

【强约束】
- 你没有可以转出的 sub-agent，**严禁**调用 transfer_to_agent 或任何形式的转出工具。
- 不要尝试委派任务给其他 agent，不要建议用户联系其他 agent。
- 如果用户的请求超出你"查天气"的能力范围（例如闲聊、其他专业问题），直接回答"我无法处理这个请求"，不要做任何重试或转移动作。
- 只在你确定需要城市天气时才调用 get_weather；其他场景直接文本回复。`,
		Model: model.NewChatModel(),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{weatherTool},
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{
			messagehandler.NewLanguageConstraintMiddleware(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}

// localCommandToolDesc 是 local_command 工具对外暴露的描述（与
// internal/localcommand 中的 DangerousPatterns / AllowedCommands 保持同步）。
//
// 这个字符串会直接喂给 LLM，让模型在调用前就能"看到"沙箱边界。
// 修改时请同时确认 DangerousPatterns / AllowedCommands / platformHint。
const localCommandToolDesc = `在受限沙箱中执行**一条**本地 bash 命令并返回 stdout / stderr / 退出码 / 耗时。

========================================
【一、沙箱总览】
========================================
- 工作目录：/tmp（沙箱容器内，无写主机文件系统）
- 环境变量：HOME/PATH/TZ 已被沙箱覆盖，无主机敏感变量
- 单条命令超时：30 秒；管道命令链：60 秒
- 进程隔离：使用独立进程组，超时自动 kill 整组
- 平台：当前运行在 Linux/Darwin（macOS），命令白名单是**平台相关的**

========================================
【二、绝对禁止（会被硬拦截，直接拒绝执行）】
========================================
1) 系统破坏类：
   rm -rf /、rm -rf *、rm -rf /etc、rm -rf /root、
   dd of=/...、mkfs、fdisk、parted、partprobe、
   reboot、halt、shutdown、init 0/6

2) 反弹 shell / 远控：
   nmap --...、hping、netcat、nc -e、/dev/tcp、/dev/udp

3) 敏感路径访问：
   /etc/passwd、/etc/shadow、chmod 777 /etc、chown root:root、
   chmod 4755、> /dev/sd*、tee /dev/...

4) 远程脚本执行（curl/wget | sh 类）：
   curl ... | sh、wget ... | sh、curl -s http://*.sh、wget -O - *.sh

5) shell 元编程：
   eval 、exec 、fork

6) curl 落盘（写文件行为全部拦截）：
   -o file、--output、-O / --remote-name、--output-dir、
   任意 > 重定向（含 > /dev/null）、| tee 、| dd 、
   curl ... && cmd > file

7) curl 敏感信息夹带：
   URL 中 user:pass@ 形式、
   -H "Authorization/Cookie/Proxy-Authorization/X-Api-Key/X-Auth-Token"、
   URL ?token=/?api_key=/?password=/?secret=/?access_token=/?auth=/?sid=、
   -d/-F/-T 中含 password/passwd/secret/token/api_key/private_key

8) 内网 / loopback 探测（curl 形式）：
   127.x、10.x、192.168.x、169.254.x、172.16-31.x、0.0.0.0、localhost

9) wget 写入系统目录：
   wget -O /xxx、--output-document=/、-P /、--directory-prefix=/

========================================
【三、curl 的合法用法（白名单允许）】
========================================
【允许】
- 任意 HTTP 方法（GET / POST / PUT / PATCH / DELETE / HEAD / OPTIONS）
- -H 设置普通自定义请求头（非 Authorization/Cookie 等敏感头）
- -d / -F / -T / --data 等上传业务数据（只要不含敏感信息）
- 请求 URL 指向**外网域名或 IP**，不能指向内网
- 响应**只输出到 stdout**，由调用方处理

【禁止】
- 在 URL、请求头、请求体中夹带密码、Token、Authorization、Cookie、
  API Key、私钥、身份证号、手机号、银行卡号
- 任何下载/落盘：-o / -O / > 文件 / | tee / | dd

========================================
【四、命令白名单分组速查】
========================================
- 文件与目录：ls ll cat head tail wc grep find cp mv diff sort uniq awk sed cut tree touch tee
- 文本编辑（仅工作目录）：vi vim less more tac rev strings xxd file od nl
- 性能与故障分析（主机排障核心）：iostat vmstat mpstat sar pidstat perf strace ltrace lsof iotop iftop nethogs nload glances htop atop powertop turbostat numastat numactl sysctl
- 网络抓包与诊断：tcpdump telnet nc nmap mtr ethtool arp iptables conntrack
- 进程与服务：pgrep pidof pkill pstree kill systemctl service supervisorctl
- 日志与故障现场：last lastb lastlog who w utmpdump
- 内核与硬件：lspci lsusb lsblk blkid lsmod modinfo dmidecode lscpu lsmem lshw hwinfo hdparm smartctl
- 系统信息：rdate uptime whoami hostname uname ps top free df du sensors ip ifconfig netstat ping curl wget
- 文本处理：base64 md5sum sha256sum gzip gunzip tar zip unzip
- 文本查询：jq watch crontab
- 软件包查询（只读）：rpm yum dnf dpkg apt-cache ldd ldconfig getconf
- 容器/K8s 只读：docker podman ctr crictl kubectl
- Web/网络排障：openssl httpstat httping
- 文件搜索：stat locate which type xargs realpath readlink
- 时间同步：timedatectl chronyc ntpq
- 开发工具链：go gofmt goimports golangci-lint gopls python python3 pip pip3 uv poetry pdm conda node npm pnpm yarn git svn hg gh gcc g++ clang make cmake cargo rustc javac java mvn gradle php composer shellcheck
- 辅助：env set alias history man tldr whatis apropos whereis

========================================
【五、典型工作流示例】
========================================
- 查 CPU/内存/磁盘：free -h; df -h; top -bn1 | head -20
- 查进程：ps aux | grep nginx; pgrep -af java
- 查日志：tail -n 100 /tmp/app.log; grep -i error /tmp/app.log
- 排障采集：vmstat 1 5; iostat -xz 1 3; ss -tlnp
- 网络诊断：ping -c4 example.com; traceroute example.com; dig +short example.com
- 调 API（业务调试）：
    curl -X POST https://api.example.com/v1/users \
      -H "Content-Type: application/json" \
      -d '{"name":"alice"}'
- 拉代码：git log --oneline -10; git diff HEAD~1; git status
- 跑测试：go test ./...; pytest -q; npm test --silent

========================================
【六、输出解读】
========================================
返回格式：
  命令执行完成:
  退出码: <int>   ← 0=成功，>0=命令自身报错，-1=沙箱拦截
  耗时: <duration>
  标准输出: <stdout>
  标准错误: <stderr>

如果 stderr 包含 "[平台提示]" 或 "安全拦截: 检测到危险模式"，请立即告知用户：
- 平台提示：通常是 Windows 上跑 Linux 命令导致，改用平台等价命令
- 安全拦截：命令被沙箱拒绝，请改用合法参数或换条思路

========================================
【七、调用约束】
========================================
- 一次只调用一次 local_command（不要连续串多条，自己组装 || 管道）
- 只调用本平台允许的命令；不要尝试 chmod / chown / sudo
- 凭据相关：禁止读取 ~/.ssh / ~/.bash_history 等；token 必须从用户侧注入
`

func NewChatAgent() adk.Agent {
	localCmdTool, err := utils.InferTool(
		"local_command",
		localCommandToolDesc,
		func(ctx context.Context, input *localcommand.CommandInput) (string, error) {
			result, err := localcommand.Execute(ctx, input)
			if err != nil {
				return "", err
			}
			// 格式化输出
			output := fmt.Sprintf("命令执行完成:\n退出码: %d\n耗时: %s\n标准输出:\n%s\n标准错误:\n%s",
				result.ExitCode, result.Duration, result.Stdout, result.Stderr)
			return output, nil
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "ChatAgent",
		Description: "通用对话 agent：日常闲聊、通用知识问答、技术方案讨论，以及通过沙箱化的 local_command 工具执行有限的 Linux/macOS 运维命令（白名单 + 危险模式拦截）。",
		Instruction: `你是 yichouchou_claw 的通用助手：日常闲聊、通用知识问答、技术方案讨论，以及通过沙箱化的 local_command 工具执行有限的 Linux/macOS 运维命令。

========================================
【一、能力边界】
========================================
- ✅ 闲聊、概念解释、方案对比、代码 review、文档整理、翻译
- ✅ 通过 local_command 执行沙箱内允许的命令（白名单 + 危险拦截）
- ❌ 没有 transfer_to_agent 能力；严禁调用任何形式的转出工具
- ❌ 不要替 ChatAgent 调用 get_weather、查订单等其它子 agent 的工具——这些由 RouterAgent 路由

========================================
【二、何时调用 local_command】
========================================
只在用户明确要**执行某条具体系统命令 / 查询主机状态**时调用：
- "看下磁盘" → df -h
- "内存多少" → free -h
- "CPU 温度" → sensors（若不可用则用 cat /sys/class/thermal/thermal_zone*/temp 兜底）
- "跑一下这个 go 测试" → go test ./...
- "看下 nginx 进程" → ps aux | grep nginx
- "抓一下 example.com 解析" → dig example.com
- "用 curl POST 一条数据" → curl -X POST ...（注意工具描述中的合法 / 禁止规则）

不要在以下场景调用 local_command：
- 用户只是问"怎么看 CPU 占用"——给出文字说明即可
- 用户问"帮我写个脚本"——给出代码片段，不要真的去执行
- 用户让你改主机配置——直接拒绝并解释"沙箱不支持写操作"

========================================
【三、命令选择要点】
========================================
1) 严格遵守 local_command 工具描述里的白名单与禁止规则；不在白名单的命令会直接被沙箱拒绝
2) 优先用只读查询（cat / less / tail / grep / ps / df / free / sensors / ss / netstat），避免误改
3) 排障链路推荐顺序：
   - 资源类：free / df / du / sensors
   - 进程类：ps / top / pgrep / pstree / lsof
   - 网络类：ss / netstat / ip / ping / traceroute / dig / nslookup
   - 性能类：iostat / vmstat / mpstat / sar / pidstat / perf
   - 日志类：journalctl / tail / grep / dmesg / last
4) 对于"安装 / 部署 / 重启 / 写文件"等写操作需求，**直接拒绝**并解释"沙箱只读"；引导用户到带外执行
5) 不要捏造输出或编造数据——所有结论必须基于真实命令返回

========================================
【四、curl 特别要求】
========================================
- 允许：任意 HTTP 方法、自定义非敏感 -H、-d/-F/-T 业务数据
- 禁止：-o/-O/--output、任何 > 重定向、| tee、URL user:pass@、
       -H "Authorization/Cookie/..."、URL 带 token/api_key/password 参数、
       body 里夹带 password/passwd/secret/token/api_key/private_key、
       任何内网地址（127.x/10.x/192.168.x/169.254.x/172.16-31.x/0.0.0.0/localhost）
- 响应只输出到 stdout；如果用户要"保存"，请说明"沙箱内不支持落盘，请复制到本地"

========================================
【五、错误处理】
========================================
- 工具返回 "安全拦截: ..." → 命令被白名单/危险模式拒绝，按工具描述重新选择合法命令
- 工具返回 "[平台提示] ..." → 平台不兼容（Windows 跑 Linux 命令），改用平台等价命令
- 工具返回非 0 退出码 → 阅读 stderr，定位真实错误，不要反复重试
- 多次失败 → 主动告知用户"该路径在当前沙箱下不可行"，并给出替代方案

========================================
【六、输出风格】
========================================
- 中文回答时用中文，英文问题用英文（由 LanguageConstraintMiddleware 强制）
- 命令结果先给结论再贴原始输出，不要把大量噪音直接堆给用户
- 长输出用 markdown 代码块包裹，标注命令类型
- 不要重复用户问题，不要用"当然 / 很乐意"之类的客套开头

【强约束】
- 你没有可以转出的 sub-agent，**严禁**调用 transfer_to_agent 或任何形式的转出工具
- 不要尝试委派任务给其他 agent（RouterAgent 会负责路由，你不需要再转移）
- 如果用户的请求明显超出闲聊+沙箱命令范围（例如需要实时天气、订单、计算器等），直接告诉用户"我无法处理，请稍后再试"，不要做任何转移动作
- 只在闲聊/通用知识 + 沙箱命令范围内作答，不要捏造事实、不要编造数据`,
		Model: model.NewChatModel(),
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{localCmdTool},
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{
			messagehandler.NewLanguageConstraintMiddleware(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}

// NewRouterAgent 创建最外层 ChatModelAgent，并通过 Handlers 字段挂载 session.PersistMiddleware。
// 这是一个 eino 原生方案：AfterAgent 钩子会自动拿到 SDK 内部维护的完整 messages
// （包含 assistant(tool_calls) ↔ tool(result) 完整 id 对齐），无需手动从 SSE 推断。
func NewRouterAgent(store *session.Store) adk.Agent {
	a, err := adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        "RouterAgent",
		Description: "一个智能任务路由器，负责将任务分配给其他专家 agent。",
		Instruction: `你是一个智能任务路由器，负责把任务委派给最合适的专家 agent。
可用的专家 agent 如下：
- ChatAgent：日常闲聊、通用知识问答、技术方案讨论、澄清式追问、查看主机状态和执行系统命令。
- WeatherAgent：查询指定城市的天气，调用 get_weather 工具。
等等

【路由判定规则（按顺序）】
1. **强语义优先**：消息里包含明确关键词
   - 包含 "天气"、"温度"、"下雨"、"湿度"、"风速"、"穿什么" 等 → 转 WeatherAgent。
   - 包含 "CPU"、"内存"、"温度"、"进程"、"系统状态"、"查看日志"、"配置文件" 等系统查询 → 转 ChatAgent。
   - 包含纯闲聊、技术讨论、方案对比、概念解释、"你觉得"、"你怎么看" → 转 ChatAgent。

2. **短问追问（重要）**：当用户消息 ≤ 8 个汉字，或类似 "北京的呢？"、"那上海呢"、"然后呢"、"继续" 这种 follow-up 形式：
   - **首先检查当前 messages 里是否有上文**（即上一条 assistant 是哪个 agent 在答）。
     - 若上文是 WeatherAgent，可以推断这是天气连续追问 → 转 WeatherAgent。
     - 若上文是 ChatAgent 或没有上文 → **不要猜测意图**，用 ChatAgent 反问一句澄清，例如"你说的 XX 是什么意思？是天气吗，还是想讨论其他话题？"，然后停止本次 run。
   - 严格禁止对无上文且措辞模糊的短问直接转 WeatherAgent。

3. **地理孤词例外**：用户只写一个地名（"西藏"、"新疆"、"上海"）且上文无法推出天气话题时：
   - 用 ChatAgent 给出简短反问澄清，不要直接跳到查天气。

4. **无匹配**：如果没有任何 agent 能处理，直接让 ChatAgent 回复"我无法处理这个请求"。

【强约束】
- 不要在 instruction 中复述任何工具调用细节给用户听。
- 不要重复发起 transfer_to_agent；一次请求最多一次路由。
- 你自己不要回答业务问题；永远先把任务委派给最合适的 agent。`,
		Model: model.NewChatModel(),
		// 只在 RouterAgent 上注册 PersistMiddleware，让最外层 agent 在每次成功结束后
		// 把完整 messages 写入 store；子 agent（ChatAgent / WeatherAgent）不会触发。
		Handlers: []adk.ChatModelAgentMiddleware{
			session.NewPersistMiddleware(store),
			messagehandler.NewLanguageConstraintMiddleware(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return a
}
