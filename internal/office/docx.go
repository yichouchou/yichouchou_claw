package office

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// ExtractDocxText 从 .docx(zip 容器)抽纯文本。
//
// 实现思路:
//  1. 把整体当作 zip 打开
//  2. 找到 word/document.xml
//  3. 用 encoding/xml 解开,只关心 <w:p> → <w:r> → <w:t> 的 chardata
//  4. 段落之间用 "\n\n" 分隔
//
// 不支持:图片、表格嵌套样式、批注、脚注 — 都丢掉。
func ExtractDocxText(data []byte) (string, error) {
	xmlBytes, err := readZipEntry(data, "word/document.xml")
	if err != nil {
		return "", fmt.Errorf("read word/document.xml: %w", err)
	}
	var doc docxDoc
	if err := xml.Unmarshal(xmlBytes, &doc); err != nil {
		return "", fmt.Errorf("xml unmarshal: %w", err)
	}
	var sb bytes.Buffer
	for i, p := range doc.Body.Paragraphs {
		for _, r := range p.Runs {
			sb.WriteString(r.Text.Value)
		}
		if i < len(doc.Body.Paragraphs)-1 {
			sb.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// === DOCX XML 极简映射(只抽文本) ===

type docxDoc struct {
	XMLName xml.Name `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main document"`
	Body    docxBody `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main body"`
}

type docxBody struct {
	Paragraphs []docxParagraph `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main p"`
}

type docxParagraph struct {
	Runs []docxRun `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main r"`
}

type docxRun struct {
	Text docxText `xml:"http://schemas.openxmlformats.org/wordprocessingml/2006/main t"`
}

type docxText struct {
	Space string `xml:"xml:space,attr"`
	Value string `xml:",chardata"`
}

// === helpers ===

// readZipEntry 从整个 zip 字节流里找出指定 entry 的内容。
func readZipEntry(data []byte, entryName string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != entryName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open entry %s: %w", entryName, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read entry %s: %w", entryName, err)
		}
		return b, nil
	}
	return nil, fmt.Errorf("entry %s not found", entryName)
}
