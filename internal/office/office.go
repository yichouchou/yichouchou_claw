// Package office 提供 .docx / .xlsx / .pptx 文本抽取能力,
//
// 在 yichouchou_claw 服务里,这段代码用于把上传的 Office 文档就地抽成纯文本
// (不落盘,符合 Claude Code 精神);然后改 mime 为 text/plain,继续走
// Anthropic PlainTextSource 路径,让 LLM 能"读"这些文档。
//
// 使用约定:
//   - 入口: office.ExtractText(data, mime) -> (text string, ok bool)
//   - 判定: office.IsOOXML(mime) -> bool
//
// 不支持的能力(需要 LibreOffice 转 PDF 才能补):
//   - 旧版 OLE 二进制 .doc / .xls / .ppt
//   - 嵌入式图片 / 图表
//   - 复杂公式(`<f>` 节点;只读 `<v>` 缓存结果)
//
// 2026-07-30 创建。
package office

// IsOOXML 判断 mime 是否是 OOXML 格式的 Office 文档(可被当前包解析)。
//
// 只支持新格式:
//   - .docx → wordprocessingml.document
//   - .xlsx → spreadsheetml.sheet
//   - .pptx → presentationml.presentation
//
// 旧格式 .doc/.xls/.ppt 走 false,调用方应提示用户转 PDF 或升级格式。
func IsOOXML(mime string) bool {
	switch mime {
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return true
	}
	return false
}

// ExtractText 把 Office 字节流转成纯文本。
//
// 入参:
//   - data: 原始文件字节
//   - mime: 文件 MIME;必须先 IsOOXML 通过(否则直接返回 ok=false)
//
// 返回:
//   - text: 抽取的纯文本(XLSX 走 markdown 表格)
//   - ok:   是否成功(失败:旧格式 / 损坏文件 / 空文件)
//
// 设计: 全部在内存里完成,绝不落盘。
func ExtractText(data []byte, mime string) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	switch mime {
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		t, err := ExtractDocxText(data)
		if err != nil {
			return "", false
		}
		return t, true
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		t, err := ExtractXlsxText(data)
		if err != nil {
			return "", false
		}
		return t, true
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		t, err := ExtractPptxText(data)
		if err != nil {
			return "", false
		}
		return t, true
	default:
		return "", false
	}
}
