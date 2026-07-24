package memory

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
)

const fileUpdaterLogPrefix = "[memory.updater]"

// FileUpdater 负责在已落盘的 markdown 文件里"in-place 替换特定 front-matter
// 块的 summary 字段"。
//
// 用法:refiner worker 拿到 LLM 生成的精准摘要后,调用 FindAndReplaceSummary
// 找到匹配 llm_trace_id 的 front-matter 块,只替换那一行的 summary 字段,
// 不动其他 entry / front-matter / body。
//
// 并发安全:
//   - 每个文件路径独立 mutex,不同文件并发写不阻塞
//   - 同一文件串行写(防止两个 worker 同时改一个文件)
//
// 实现策略:read → parse front-matter 块(以 `---` 为界)→ 替换目标块的 summary
// 行 → 原子 rename (.tmp → 真实文件)。Read-modify-write 而非 in-place patch。
type FileUpdater struct {
	muMap sync.Map // map[string]*sync.Mutex,按文件路径锁
}

// NewFileUpdater 构造 updater。
func NewFileUpdater() *FileUpdater {
	return &FileUpdater{}
}

// FindAndReplaceSummary 读取 filePath,找到 llm_trace_id 匹配的 front-matter
// 块,只替换那一块的 summary 字段。
//
// 工作流程:
//  1. 锁 per-file mutex(同文件串行)
//  2. 读全文件 → split by `--- ... ---` 块
//  3. 找含 llm_trace_id = traceID 的块的 summary 行
//  4. 替换 → 拼回 → 原子 rename
//
// 错误:
//
//	file 不存在 → 静默返回 nil(refine 之前 entry 可能被 archive 走了)
//	traceID 不匹配 → 静默返回 nil(refine 任务可能迟到)
//	写盘失败 → 返回 error
func (u *FileUpdater) FindAndReplaceSummary(filePath string, traceID LLMTraceID, newSummary string) error {
	if u == nil {
		return fmt.Errorf("nil updater")
	}
	if filePath == "" || traceID == "" {
		return nil
	}

	mu := u.lockFor(filePath)
	mu.Lock()
	defer mu.Unlock()

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", filePath, err)
	}

	updated, replaced := replaceSummaryInFrontMatter(data, traceID, newSummary)
	if !replaced {
		return nil // 没找到匹配块,静默忽略
	}

	// 原子写:写到 tmp,再 rename(Windows 上 rename 会覆盖,所以先 unlink)
	tmpPath := filePath + ".tmp.refine"
	if err := os.WriteFile(tmpPath, updated, 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s: %w", filePath, err)
	}
	return nil
}

// lockFor 取出(惰性创建)per-file mutex。
func (u *FileUpdater) lockFor(filePath string) *sync.Mutex {
	v, _ := u.muMap.LoadOrStore(filePath, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// FindAndReplaceSummaryByGroupID 按 request_group_id + side 定位 sessions
// 类的 entry 并替换 summary。
//
// 适用场景:refiner 处理 user_request / user_response 时,这些 entry 只有
// request_group_id(没有 llm_trace_id,因为请求组横跨多个 LLM 调用)。
//
// side:
//   - "request"  → kind=="user_request"
//   - "response" → kind=="user_response"
//
// 匹配规则(都满足):
//  1. front-matter 块的 request_group_id 等于 groupID
//  2. front-matter 块的 kind 等于 "user_<side>"
//
// 错误语义同 FindAndReplaceSummary:
//   - file 不存在 / 找不到匹配块 → 静默返回 nil
func (u *FileUpdater) FindAndReplaceSummaryByGroupID(filePath, groupID, side, newSummary string) error {
	if u == nil {
		return fmt.Errorf("nil updater")
	}
	if filePath == "" || groupID == "" || side == "" {
		return nil
	}
	kindWant := "user_" + side
	if side != "request" && side != "response" {
		return nil
	}

	mu := u.lockFor(filePath)
	mu.Lock()
	defer mu.Unlock()

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", filePath, err)
	}

	updated, replaced := replaceSummaryByGroupAndKind(data, groupID, kindWant, newSummary)
	if !replaced {
		return nil
	}

	tmpPath := filePath + ".tmp.refine.group"
	if err := os.WriteFile(tmpPath, updated, 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s: %w", filePath, err)
	}
	return nil
}

// replaceSummaryByGroupAndKind 找到匹配 (groupID + kind) 的 front-matter 块
// 并替换 summary 行。
//
// 匹配策略(同一块内两个条件都满足,但 field 在独立行):
//  1. 包含 `request_group_id: "<groupID>"` 行(单独一行,front-matter 字段格式)
//  2. 包含 `kind: "<kindWant>"` 行
//
// 然后替换该块第一个 summary 行。
func replaceSummaryByGroupAndKind(data []byte, groupID, kindWant, newSummary string) ([]byte, bool) {
	lines := bytes.Split(data, []byte("\n"))
	groupLine := []byte(`request_group_id: "` + groupID + `"`)
	kindLine := []byte(`kind: "` + kindWant + `"`)
	summaryKey := []byte("summary:")

	// 在每个 `--- ... ---` front-matter 块内:
	//   - 见到 group_line 标记 hasGroup = true
	//   - 见到 kind_line 标记 hasKind = true
	//   - 见到 summary: 行时,若 hasGroup && hasKind → 替换
	inFrontMatter := false
	atFirstDash := true // 文件首个 --- 是文档起,也当作块起
	hasGroup := false
	hasKind := false
	replaced := false

	reset := func() {
		hasGroup = false
		hasKind = false
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := bytes.TrimRight(line, " \t\r")

		if bytes.Equal(trimmed, []byte("---")) {
			if !inFrontMatter && atFirstDash {
				// 文件首个 ---:文档起始
				inFrontMatter = true
				atFirstDash = false
				reset()
				continue
			}
			if inFrontMatter {
				// 当前块结束的 ---:摘要号已落,跳出 front-matter
				inFrontMatter = false
				reset()
				continue
			}
			// 当前不在 front-matter → 这个 --- 是新块的开始
			inFrontMatter = true
			reset()
			continue
		}

		if !inFrontMatter {
			// body 区域,忽略
			continue
		}

		// 在 front-matter 内的扫描
		if bytes.Equal(line, groupLine) {
			hasGroup = true
		}
		if bytes.Equal(line, kindLine) {
			hasKind = true
		}
		if hasGroup && hasKind && bytes.HasPrefix(line, summaryKey) {
			lines[i] = []byte(fmt.Sprintf(`summary: %q`, newSummary))
			replaced = true
			hasGroup = false
			hasKind = false
		}
	}

	if !replaced {
		return nil, false
	}

	out := bytes.Join(lines, []byte("\n"))
	if len(data) > 0 && data[len(data)-1] == '\n' && len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return out, true
}

// replaceSummaryInFrontMatter 找到含 traceID 的 front-matter 块并替换 summary。
// 返回 (更新后字节, 是否实际替换过)。
//
// 文件结构假设(由 markdown_writer.go 写入):
//
//	---                                       ← 块起
//	session_id: "..."
//	llm_trace_id: "llm-..."                   ← 用于定位
//	agent: "..."
//	kind: "..."
//	time: "..."
//	summary: "rule-summary ..."               ← 要替换
//	---                                       ← 块止
//
//	```text
//	...content...
//	```
//
//	---                                       ← 下一个块起
//	...
//
// 解析策略:从文件头开始,按 `---` 切成块,每块内查 llm_trace_id 与 summary 行。
func replaceSummaryInFrontMatter(data []byte, traceID LLMTraceID, newSummary string) ([]byte, bool) {
	lines := bytes.Split(data, []byte("\n"))
	traceStr := []byte(`llm_trace_id: "` + traceID + `"`)
	summaryKey := []byte("summary:")
	traceMatched := false
	replaced := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := bytes.TrimRight(line, " \t\r")

		// 块起 marker `---`(单独一行)
		if bytes.Equal(trimmed, []byte("---")) {
			// 新块开始,重置匹配标记
			traceMatched = false
			continue
		}

		// 检测 llm_trace_id 行
		if bytes.Contains(line, traceStr) {
			traceMatched = true
			continue
		}

		// 在匹配的块里,替换 summary 行
		if traceMatched && bytes.HasPrefix(line, summaryKey) {
			lines[i] = []byte(fmt.Sprintf(`summary: %q`, newSummary))
			traceMatched = false
			replaced = true
			// 找到后继续(不 break),允许一次调用替换多处(同 trace_id 多 entry)
		}
	}

	if !replaced {
		return nil, false
	}

	// 用 LF 拼回
	out := bytes.Join(lines, []byte("\n"))
	// bytes.Split 会丢掉末尾的 \n,如果原文件最后是 \n,这里补回
	if len(data) > 0 && data[len(data)-1] == '\n' && len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return out, true
}

// deleteFrontMatterForTrace 在文件里删除特定 llm_trace_id 的 front-matter 块
// (包括对应的 fenced code block body)。
// 保留为未来"用户请求删除某 trace"用,本次未导出。
//
// nolint:unused
func deleteFrontMatterForTrace(data []byte, traceID LLMTraceID) ([]byte, bool) {
	return nil, false
}

// 防止 strings 包被 unused 检查干掉
var _ = strings.TrimSpace
