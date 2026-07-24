package memory

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Language 标识一段文本的主要语言。
type Language string

const (
	LangUnknown Language = "unknown"
	LangChinese Language = "zh"
	LangEnglish Language = "en"
)

// DetectLanguage 用启发式判断文本的主要语言。
//
// 算法:扫描前 300 个 rune,统计 CJK 字符数 vs ASCII 字母数:
//   - 存在任意 CJK 字符(>= 1) → 中文(只要有一个中文字符,就判为中文)
//   - ASCII 字母数 >= 15 且没有 CJK → 英文
//   - 都没有 → unknown
//
// 阈值说明:
//   - 300 字符:短输入全覆盖,长输入前 300 也反映"开场语言"
//   - 中文判定极宽松:只要有 1 个中文字符就判为中文,符合"只要会话有一个
//     中文,就使用中文摘要"的需求。即使内容主要是英文代码、英文术语,
//     只要夹杂一个中文字符(比如中文用户的反馈/提示),摘要就用中文输出。
//   - 英文阈值 15 个 ASCII 字母:有意义的英文句子才判定为英文,
//     避免 "Hello world"(10 字母)这种短句被误判。
func DetectLanguage(s string) Language {
	if s == "" {
		return LangUnknown
	}
	cjk, ascii := 0, 0
	total := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjk++
			total++
		} else if r <= 0x7F && unicode.IsLetter(r) {
			ascii++
			total++
		}
		// 300 字符够了,提前 break
		if total >= 300 {
			break
		}
	}
	switch {
	case cjk >= 1:
		// 最高优先级:只要有 1 个 CJK 字符,就判为中文
		// 满足"只要会话有一个中文,就用中文摘要"的诉求
		return LangChinese
	case ascii == 0:
		return LangUnknown
	case ascii >= 15:
		return LangEnglish
	default:
		return LangUnknown
	}
}

// RuleSummaryKind 根据 kind 选用合适的规则摘要策略。
//
// 分流原因:
//   - llm_input / llm_output:内容是 LLM trace 输入/输出,通常是格式化后的
//     [NN] role=xxx 元信息混排,适合用 RuleSummary(剥离元信息 + 按语言过滤)
//   - user_request / user_response:内容是用户提问/助手回复,简短直接,
//     不应做中文 ASCII 过滤(会过滤掉英文术语),只用 RuleSummaryForSessions
//
// 同步版本,Record* 调用时立即执行,保证 front-matter 落地后立即可读。
// 后续异步 LLM refine worker 会覆盖 summary 字段为更精准版本。
func RuleSummaryKind(kind string, content []byte) string {
	switch kind {
	case "user_request", "user_response":
		return RuleSummaryForSessions(content)
	default:
		return RuleSummary(content)
	}
}

// RuleSummaryForSessions 是 sessions 类 kind(user_request / user_response)的
// 专用规则摘要生成。
//
// 与 RuleSummary 的区别:
//   - 不做 stripFormattingMeta(用户输入/响应通常不含 [NN] role=xxx 这种格式)
//   - 不做按语言过滤(用户提问可能含英文术语,如"CPU 使用率","memory 模块")
//     简单按语言过滤会把这些英文术语全去掉,变成一堆空格 + 孤立中文字符
//   - 直接折叠空白 + 按 rune 数截断
//
// 用 rune-level filterByLanguage 会导致"出现大量空格 + 极少数 CJK + 截断尾缀"的
// 垃圾摘要(线上事故:user_response 的 summary 被洗成 "         安装      ..")。
//
// 当用户/助手响应内容过短(< maxLen)时,直接折叠返回;过长则按 rune 数截断并附加
// ".." 标记。截断后做"垃圾摘要"防御:若结果中"有意义的字母/数字字符"占可见字符
// 比例 < 30%(经典 case:只剩空白 + 几个孤立中文字符),视为垃圾并再次压缩为
// simple-first-N 摘要,保留更多有效字符。
func RuleSummaryForSessions(content []byte) string {
	s := string(content)
	if s == "" {
		return ""
	}
	s = collapseWhitespace(s)

	maxLen := MaxSummaryLength
	runeCount := utf8.RuneCountInString(s)

	if runeCount <= maxLen {
		return s
	}

	// 超长:简单前 maxLen-2 个 rune + ".."
	target := maxLen - 2
	if target < 0 {
		target = 0
	}
	var b strings.Builder
	count := 0
	for _, r := range s {
		if count >= target {
			break
		}
		b.WriteRune(r)
		count++
	}
	candidate := b.String() + ".."

	// 防御:截断后若主要是空白 + 孤立 token,改为前 maxLen-2 个 rune 的简易摘要
	if isJunkSummary(candidate, target) {
		var b2 strings.Builder
		count = 0
		for _, r := range s {
			if count >= target {
				break
			}
			b2.WriteRune(r)
			count++
		}
		return b2.String() + ".."
	}

	return candidate
}

// isJunkSummary 检测摘要是否退化为"垃圾":可见字符中字母/数字占比过低。
//
// 启发式:visible = 非空白字符数;alnum = 字母+数字字符数。
// 若 visible > 0 且 alnum/visible < 0.30 → 视为垃圾。
//
// 典型垃圾模式:
//   - "                                                    安装                     .."
//     只有 1 个汉字 + 大量空格 + ".."
//   - "(无可见内容)" 占位符也属低 alnum,被识别为垃圾时再压缩为 simple-first-N
func isJunkSummary(s string, _ int) bool {
	if s == "" {
		return true
	}
	visible := 0
	alnum := 0
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '.' {
			continue
		}
		visible++
		if isASCIILetterOrDigit(r) {
			alnum++
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			alnum++
		}
	}
	if visible == 0 {
		return true
	}
	return alnum*10 < visible*3 // 等价 alnum/visible < 0.30
}

func isASCIILetterOrDigit(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// RuleSummary 用规则生成 ≤ MaxSummaryLength 字的中文摘要。
//
// 行为:
//  1. 智能剥离元信息(常见的"前缀噪声")——
//     [NN] role=xxx content_len=NNN tool_calls=N 这种结构化元信息
//     以及 "     content: " 这种重复前缀
//  2. 提取实际正文内容后再按规则截断
//  3. 短内容(<maxLen)直接折叠空白返回
//  4. 长内容:直接按 rune 数截断到 maxLen-2 + ".." (强制中文,不做语言过滤)
//
// 同步版本,Record* 调用时立即执行,保证 front-matter 落地后立即可读。
// 后续异步 LLM refine worker 会覆盖 summary 字段为更精准版本。
func RuleSummary(content []byte) string {
	s := string(content)
	if s == "" {
		return ""
	}

	// 1. 剥离"格式化元信息"——例如:
	//    "[00] role=system content_len=6442 tool_calls=0\n     content: 实际内容"
	//    这种 formatMessageAsInput 输出的格式,前面的 [NN] role=... 部分是噪声。
	cleaned := stripFormattingMeta(s)
	s = collapseWhitespace(cleaned)

	maxLen := MaxSummaryLength
	runeCount := utf8.RuneCountInString(s)

	if runeCount <= maxLen {
		return s
	}

	// 超长:直接按 rune 数截断 (强制中文,不做语言过滤)
	target := maxLen - 2
	if target < 0 {
		target = 0
	}
	var b strings.Builder
	count := 0
	for _, r := range s {
		if count >= target {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String() + ".."
}

// stripFormattingMeta 剥离输入内容中的"格式化元信息"前缀噪声。
//
// 典型输入(formatMessageAsInput 输出格式):
//
//	[00] role=system content_len=6442 tool_calls=0
//	     content: 你是一个智能任务路由器,负责把任务委派给最合适的专家 agent。
//	可用的专家 agent 如下:
//	- ChatAgent:日常闲聊、通用知识问答...
//	[01] role=user content_len=21 tool_calls=0
//	     content: 北京天气怎样
//
// 噪声模式:
//  1. 行首的 [NN] role=xxx content_len=NNN tool_calls=N(只剥离元信息行,保留后续实际内容)
//  2. 行首的"     content: "这种缩进前缀
//  3. 末尾的 "...(more)" 提示
//
// 优化策略:不是正则剥离(容易破坏中文),而是把每行首的"元信息 token"换成短前缀标签。
func stripFormattingMeta(s string) string {
	lines := strings.Split(s, "\n")
	var b strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue // 空行跳过
		}

		// 判断是否是 [NN] role=xxx 元信息行
		if m := roleMetaPattern.FindStringSubmatch(trimmed); len(m) > 1 {
			role := m[1]
			// 提取后续内容
			after := roleMetaPattern.ReplaceAllString(trimmed, "")
			after = contentLenPattern.ReplaceAllString(after, "")
			after = toolCallsPattern.ReplaceAllString(after, "")
			after = strings.TrimSpace(after)
			after = strings.TrimPrefix(after, "content:")
			after = strings.TrimSpace(after)

			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("[" + role + "]")
			if after != "" {
				b.WriteString(" " + after)
			}
			continue
		}

		// 缩进 content: 前缀(角色行下面的内容行)
		if strings.HasPrefix(trimmed, "content:") {
			after := strings.TrimSpace(trimmed[len("content:"):])
			if after != "" {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(after)
			}
			continue
		}

		// 普通文本行:直接追加
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(trimmed)
	}

	return b.String()
}

// 正则预编译(模块级别,避免每次调用重新编译)
var (
	roleMetaPattern   = regexp.MustCompile(`^\[\d+\]\s+role=(\w+)\s+`)
	contentLenPattern = regexp.MustCompile(`\s*content_len=\d+\s*`)
	toolCallsPattern  = regexp.MustCompile(`\s*tool_calls=\d+\s*`)
)

// filterByLanguage 按语言保留"主要语言字符"。
//
// LangChinese:保留 CJK / 标点 / 数字 / 短 ASCII 标签(如 [system] [user])
// LangEnglish:保留 ASCII 字母 / 数字 / 空格 / 基础标点
// LangUnknown:按 utf8 rune 顺序前 target 个
//
// 用 rune-level filtering 而不是字节级,避免破坏 utf8。
//
// 重要: 中文摘要也保留 ASCII 短标识符([system] [user] 等),
// 这样剥去了元信息的输入,角色标签不会丢失。
func filterByLanguage(s string, lang Language, target int) string {
	var b strings.Builder
	count := 0
	// 跟踪当前是否在 "[xxx]" 标签内(短 ASCII 标识需要保留)
	inBracketLabel := false
	bracketLabelCount := 0

	for _, r := range s {
		if count >= target {
			break
		}
		keep := true
		switch lang {
		case LangChinese:
			if r == '[' {
				inBracketLabel = true
				bracketLabelCount = 0
			}
			if r <= 0x7F {
				if unicode.IsDigit(r) {
					// keep
				} else if r == ' ' || r == '\n' || r == '\t' {
					inBracketLabel = false
				} else if inBracketLabel {
					bracketLabelCount++
					if bracketLabelCount > 12 {
						keep = false
						inBracketLabel = false
					}
					if r == ']' {
						inBracketLabel = false
					}
				} else {
					keep = false
				}
			} else if !unicode.Is(unicode.Han, r) &&
				!unicode.Is(unicode.Hiragana, r) &&
				!unicode.Is(unicode.Katakana, r) &&
				!unicode.Is(unicode.Hangul, r) {
				keep = false
				inBracketLabel = false
			}
		case LangEnglish:
			if r > 0x7F || !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r)) {
				if !(r == ',' || r == '.' || r == ';' || r == ':' || r == '!' || r == '?' || r == '\'') {
					keep = false
				}
			}
		default:
			// unknown:全保留
		}
		if keep {
			b.WriteRune(r)
			count++
		}
	}
	return b.String()
}

// collapseWhitespace 折叠连续空白为单空格,trim 首尾。
func collapseWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// TruncateBytes 截断字节到 maxLen 字节(不破 UTF-8 边界)。
func TruncateBytes(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	for maxLen > 0 && !utf8.RuneStart(s[maxLen]) {
		maxLen--
	}
	return s[:maxLen]
}
