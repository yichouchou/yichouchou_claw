package office

import (
	"archive/zip"
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ExtractPptxText 从 .pptx 抽每页幻灯片的文本框内容。
//
// PPTX 把每页存在 ppt/slides/slideN.xml,文本存在 <a:t> 节点。
// 我们用正则匹配所有 <a:t>...</a:t>(DrawingML a 命名空间)。
//
// 不支持:表格、图表、嵌入式形状 — 都不抽。
func ExtractPptxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	var slides []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			slides = append(slides, f.Name)
		}
	}
	sort.Strings(slides)

	var sb strings.Builder
	for idx, slidePath := range slides {
		if idx > 0 {
			sb.WriteString("\n\n")
		}
		name := strings.TrimPrefix(slidePath, "ppt/slides/")
		name = strings.TrimSuffix(name, ".xml")
		sb.WriteString("## ")
		sb.WriteString(name)
		sb.WriteString("\n\n")

		xmlBytes, err := readZipEntry(data, slidePath)
		if err != nil {
			fmt.Fprintf(&sb, "(无法读取: %v)\n", err)
			continue
		}
		for _, t := range extractAText(xmlBytes) {
			sb.WriteString("- ")
			sb.WriteString(t)
			sb.WriteString("\n")
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// extractAText 抽 XML 中所有 <a:t>...</a:t> 节点 chardata。
//
// 简化版,用正则(支持 xml:space="preserve" 等属性)。
// 对嵌套结构(罕见)a:r/<a:t> 也能命中 — 因为 regex 不严格匹配栈。
func extractAText(xmlBytes []byte) []string {
	re := regexp.MustCompile(`<a:t(?:\s[^>]*)?>([^<]*)</a:t>`)
	matches := re.FindAllSubmatch(xmlBytes, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		t := strings.TrimSpace(string(m[1]))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
