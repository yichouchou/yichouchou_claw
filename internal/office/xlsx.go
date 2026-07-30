package office

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
)

// ExtractXlsxText 把 .xlsx 转成 markdown 表格.
//
// 实现:
//   - xl/sharedStrings.xml → 字符串表
//   - xl/worksheets/sheetN.xml → 每个 sheet 的 rows + cells
//   - cell type 字段 t="s" 时,v 是 sharedStrings 索引;t=inlineStr/str 读 is.t
//   - 每个 sheet 输出成 markdown table
//
// 不支持:公式源码 `<f>`、合并单元格、图表、图片、条件格式 — 都不输出。
func ExtractXlsxText(data []byte) (string, error) {
	sharedStrings, err := readSharedStrings(data)
	if err != nil {
		// 没有 sharedStrings 也允许(整个 xlsx 全是 inline string)
		sharedStrings = []string{}
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	var sheets []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			sheets = append(sheets, f.Name)
		}
	}
	sort.Strings(sheets)

	var sb strings.Builder
	for idx, sheetPath := range sheets {
		if idx > 0 {
			sb.WriteString("\n\n")
		}
		name := stripSheetName(sheetPath)
		sb.WriteString("## Sheet ")
		sb.WriteString(name)
		sb.WriteString("\n\n")

		xmlBytes, err := readZipEntry(data, sheetPath)
		if err != nil {
			fmt.Fprintf(&sb, "(无法读取 %s: %v)\n", name, err)
			continue
		}
		rows, err := parseSheetXML(xmlBytes, sharedStrings)
		if err != nil {
			fmt.Fprintf(&sb, "(无法解析 %s: %v)\n", name, err)
			continue
		}
		for _, row := range rows {
			sb.WriteString("| ")
			sb.WriteString(strings.Join(row, " | "))
			sb.WriteString(" |\n")
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// stripSheetName 拼出 sheetN → sheet1, sheet2 ...
// 仅显示 sheet 文件名去掉路径和后缀。
func stripSheetName(p string) string {
	n := strings.TrimPrefix(p, "xl/worksheets/")
	n = strings.TrimSuffix(n, ".xml")
	return n
}

func readSharedStrings(data []byte) ([]string, error) {
	xmlBytes, err := readZipEntry(data, "xl/sharedStrings.xml")
	if err != nil {
		return nil, err
	}
	type sst struct {
		XMLName xml.Name `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main sst"`
		Sis     []struct {
			T string `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main t"`
		} `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main si"`
	}
	var v sst
	if err := xml.Unmarshal(xmlBytes, &v); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(v.Sis))
	for _, si := range v.Sis {
		out = append(out, si.T)
	}
	return out, nil
}

// parseSheetXML 解析 sheetN.xml 为二维字符串。
//
// XLSX cell 类型:
//   - type 缺省 / "n" → <v> 是数值
//   - type="s"        → <v> 是 sharedStrings 的索引
//   - type="str"       → <v> 是字符串(老 formatOpen XML 的 array formula 结果)
//   - type="inlineStr" → <is><t>...</t></is> 是 inline string
func parseSheetXML(xmlBytes []byte, sharedStrings []string) ([][]string, error) {
	type sheet struct {
		XMLName   xml.Name `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main worksheet"`
		SheetData struct {
			Rows []struct {
				XMLName xml.Name `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main row"`
				Cells   []struct {
					XMLName xml.Name `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main c"`
					T       string   `xml:"t,attr"`
					V       string   `xml:"v"`
					Is      *struct {
						T string `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main t"`
					} `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main is"`
				} `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main c"`
			} `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main row"`
		} `xml:"http://schemas.openxmlformats.org/spreadsheetml/2006/main sheetData"`
	}
	var s sheet
	if err := xml.Unmarshal(xmlBytes, &s); err != nil {
		return nil, err
	}
	out := make([][]string, 0, len(s.SheetData.Rows))
	for _, row := range s.SheetData.Rows {
		line := make([]string, 0, len(row.Cells))
		for _, c := range row.Cells {
			switch c.T {
			case "s":
				idx := parseIntSafe(c.V)
				if idx >= 0 && idx < len(sharedStrings) {
					line = append(line, sharedStrings[idx])
				} else {
					line = append(line, c.V)
				}
			case "inlineStr", "str":
				if c.Is != nil {
					line = append(line, c.Is.T)
				} else {
					line = append(line, c.V)
				}
			default:
				line = append(line, c.V)
			}
		}
		out = append(out, line)
	}
	return out, nil
}

// parseIntSafe 解无符号整数。失败返回 -1。
// 避免引 strconv 包(无依赖)。
func parseIntSafe(s string) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return -1
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
