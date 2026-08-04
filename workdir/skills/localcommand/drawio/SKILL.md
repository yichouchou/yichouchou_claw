---
name: drawio
description: 当用户要求生成 draw.io 图表(ERD / 数据库表、UML 类图、时序图、流程图、架构图)并导出为 PNG/SVG 时加载本 skill。LocalCommandAgent 会按本 skill 的规范写 .drawio XML,然后通过 local_command 工具调用主机已安装的 draw.io CLI 完成 PNG 导出。本 skill 输出的源文件通过 buildArtifactFilePart 的 text/* 过滤(2026-08-03 fix)不会污染 LLM 上下文,产物 .png 走 SSE multi_content 推前端。
context: inline
---

# Draw.io 图表生成 Skill

## 何时加载本 skill

- 用户要求数据库图、ERD、实体关系图
- 用户要求 UML 类图、对象结构图
- 用户要求时序图、交互图
- 用户要求流程图、状态图
- 用户要求架构图、系统视图
- 用户提到 "draw.io"、"图表"、"ERD"、"类图"、"时序图"、"流程图"

## ⚠️ 强制规则 — 生成前必读

1. **必须先识别图表类型**(TYPE 1-5),再写 XML
2. **绝不混用不同图表类型的样式** — 每种类型有独立的严格 XML 结构
3. **所有 `mxCell` 必须分配连续递增的数字 ID**(从 0 起)
4. **`value` 属性里禁用 `\n`** — 用多行 cell 或 `&#xa;`
5. **必须用本文件给出的公式精确计算表/类的高度**
6. **保存前必须验证 XML 结构**(well-formed)
7. **必须先导出 PNG 并把 PNG 嵌入到回复里**,再问后续格式 — 这一步不可跳过
8. **数据库表/类实体禁用通用圆角矩形**,必须用 `shape=table` / `swimlane`
9. **元素间距要统一**
   - 元素间水平间距 ≥ **120px**
   - 行/步骤间垂直间距 ≥ **100px**
   - 表/类不能重叠
   - 关系线不能穿过表头

---

## STEP 0 — 图表类型识别

**在生成任何 XML 之前**,先根据用户描述确定类型:

| 用户说... | 图表类型 |
|---|---|
| "数据库"、"ERD"、"表"、"实体"、"外键" | → **TYPE 1: ERD** |
| "类"、"UML"、"继承"、"属性"、"方法" | → **TYPE 2: Class Diagram** |
| "时序"、"交互"、"生命线"、"调用" | → **TYPE 3: Sequence Diagram** |
| "流程"、"处理"、"判断"、"步骤" | → **TYPE 4: Flowchart** |
| "架构"、"系统"、"服务"、"组件" | → **TYPE 5: Architecture** |

## 图表类型参考文件

识别类型后,加载对应的详细模板:

- ERD / 数据库: [`ERD.md`](./ERD.md)
- UML 类图: [`CLASS.md`](./CLASS.md)
- 时序图: [`SEQUENCE.md`](./SEQUENCE.md)
- 流程图: [`FLOWCHART.md`](./FLOWCHART.md)
- 架构图: [`LAYOUT.md`](./LAYOUT.md)

---

## 生成工作流(严格按照顺序执行)

### Step 1 — 识别图表类型
根据用户消息确定 TYPE 1-5,在写 XML 之前。

### Step 2 — 规划所有元素
列出所有实体/类/参与者以及所有关系,再开始编码。

### Step 3 — 探测 drawio 环境 + 决定执行参数(每轮首次必做)

```bash
# 1. 确认 drawio 路径
which drawio
# 期望输出(本机环境): /usr/bin/drawio

# 2. 验证版本
/usr/bin/drawio --version
# 期望输出: 24.x.x 或更高

# 3. 检测当前用户(决定是否加 --no-sandbox)
WHOAMI=$(whoami)
# 输出 "root" 时,后面所有 drawio 命令必须加 --no-sandbox
# 输出非 root 时,不加 --no-sandbox(参数会被新版 drawio 报错)

# 4. 确认输出目录可写
mkdir -p /tmp/diagrams && ls -ld /tmp/diagrams
```

**如果 `which drawio` 找不到** → 提示用户安装 drawio Desktop,不要继续生成。

**root 用户 无 --no-sandbox 报错示例**:
```
[main] Failed to create user namespace: clone3 failed with EPERM
```
或类似 `sandbox_linux.cc` 失败。**必须加 `--no-sandbox`**。

### Step 4 — 创建输出目录

**默认输出目录 `/tmp/diagrams`**(绝对路径,避开 localcommand 沙箱 cwd 解析差异):

```bash
mkdir -p /tmp/diagrams
```

> 如果用户的项目目录可写,也可以用相对路径 `./diagrams`;但**默认优先 `/tmp/diagrams`**。

### Step 5 — 写并保存 XML
保存到 `/tmp/diagrams/<diagram-name>.drawio`

**保存前强制 checklist:**
- [ ] 所有 `mxCell` 都有连续递增的数字 ID
- [ ] ERD 表用 `shape=table` + `shape=tableRow` 行(禁用通用 shape)
- [ ] 类图用 `swimlane`,含属性块、分隔线、方法块
- [ ] 时序图有 lifeline、activation box、正确的箭头样式
- [ ] ERD 关系箭头连接到 **行的 cell ID**,不是表的容器 ID
- [ ] 高度计算正确:ERD 表 `30 + (列数 × 30)`
- [ ] `value` 属性里没有字面 `\n`(多行文本用 `&#xa;`)
- [ ] XML well-formed,所有 tag 都闭合

### Step 6 — 导出 PNG

**Linux / macOS 全平台统一用法**(本项目主用 Linux):

```bash
# === 通用模板 ===
# 普通用户(非 root)
/usr/bin/drawio -x -f png --scale 2 -o /tmp/diagrams/<name>.png /tmp/diagrams/<name>.drawio

# root 用户(必须加 --no-sandbox)
/usr/bin/drawio --no-sandbox -x -f png --scale 2 -o /tmp/diagrams/<name>.png /tmp/diagrams/<name>.drawio

# macOS(if installed via dmg,非 root)
/Applications/draw.io.app/Contents/MacOS/draw.io -x -f png --scale 2 -o /tmp/diagrams/<name>.png /tmp/diagrams/<name>.drawio
```

**实际命令时根据 Step 3 探测结果选择**:
- `whoami = root` → 加 `--no-sandbox`
- `whoami != root` → 不加 `--no-sandbox`

**注意**:`drawio` 是绝对路径,避免 `PATH` 解析问题。如果 `which drawio` 给出其它路径,must update。`--no-sandbox` 位置务必放在 `-x` / `-f` / `--scale` / `-o` 等子命令标志之前（详见后文"root 用户处理"章节）。

### Step 7 — 验证输出

```bash
ls -lh /tmp/diagrams/<name>.png
file /tmp/diagrams/<name>.png
# 期望: PNG image data, 1169 x 827(或更大)
```

### Step 8 — 内联 PNG 到对话(强制)

**LocalCommandAgent 拿到 PNG 后必须转 base64 内联**——详见后文 "LocalCommandAgent PNG → Base64 协议"章节。

### Step 9 — 询问用户后续格式(强制)

展示完 PNG 后,**必须**问用户要哪种格式:

---
图表已生成!你希望要哪种格式?

1 - PNG 图片(已内联展示)
2 - .drawio 源文件(可在 draw.io 编辑)
3 - SVG(矢量图)
4 - PDF
5 - 全部
---

按用户回复处理:
- **1** → 收 PNG 已经展示,无需再做
- **2** → 把 .drawio 文件转 base64 推给前端下载
- **3** → 导出 SVG 并转 base64 推送给前端
- **4** → 导出 PDF 并转 base64 推送给前端
- **5** → 全部导出并推送

---

## 其它导出格式

```bash
# SVG
/usr/bin/drawio -x -f svg -o /tmp/diagrams/<name>.svg /tmp/diagrams/<name>.drawio

# PDF
/usr/bin/drawio -x -f pdf -o /tmp/diagrams/<name>.pdf /tmp/diagrams/<name>.drawio

# 高清 PNG(scale 3)
/usr/bin/drawio -x -f png --scale 3 -o /tmp/diagrams/<name>_hd.png /tmp/diagrams/<name>.drawio

# 透明背景 PNG
/usr/bin/drawio -x -f png -t --scale 2 -o /tmp/diagrams/<name>_transparent.png /tmp/diagrams/<name>.drawio
```

(root 用户版本请在 `-x` 前加 `--no-sandbox`)

## LocalCommandAgent PNG → Base64 协议(关键)

> **这是 LocalCommandAgent 把 PNG 传给前端浏览器的核心机制,必须严格遵守。**

### 流程

```
drawio 工具 → 生成 /tmp/diagrams/order.png (本地文件)
                ↓
LocalCommandAgent 调用 local_command 工具:
  base64 -w 0 /tmp/diagrams/order.png
                ↓
命令输出字符串 (e.g. iVBORw0KGgo... 几 MB 字符串)
                ↓
LocalCommandAgent 把这个 base64 字符串作为工具返回的一部分,告诉前端:
  {
    "image_data": "iVBORw0KGgo...",
    "mime": "image/png",
    "name": "order.png"
  }
                ↓
前端 <img src="data:image/png;base64,iVBORw0KGgo..."> 渲染
```

### 具体命令序列

```bash
# 1. 验证 PNG 存在
[ -f /tmp/diagrams/order.png ] && echo "OK"

# 2. 转 base64(`-w 0` 关闭换行,适合嵌入)
B64=$(base64 -w 0 /tmp/diagrams/order.png)
echo "image_data_length=${#B64}"

# 3. 输出 data URL(可以直接放进 markdown)
echo "data:image/png;base64,${B64}"
```

### 给 markdown 嵌入

```markdown
![order.png](data:image/png;base64,iVBORw0KGgo...)
```

实际 LocalCommandAgent 把 data URL 当成 markdown 内容 append,前端在 SSE 流里渲染。

### 小 PNG 的 fallback(更稳)

如果 PNG 太大(>5MB),工具返回字符串可能撑爆上下文。**降级命令**:

```bash
# Linux: ImageMagick 缩为 1024px 宽
convert /tmp/diagrams/order.png -resize 1024x /tmp/diagrams/order_small.png
# 然后再 base64
```

## 跨平台 drawio 路径速查

| OS | 路径 | 备注 |
|---|---|---|
| **Linux (本项目)** | `/usr/bin/drawio` | 主要;yum/apt/dnf 安装 |
| macOS (AppImage) | `/usr/local/bin/drawio` | 软链到 `/Applications/draw.io.app/Contents/MacOS/drawio` |
| macOS (dmg) | `/Applications/draw.io.app/Contents/MacOS/draw.io` | 默认安装 |
| Windows (installer) | `C:\Program Files\draw.io\drawio.exe` | 默认安装 |
| Windows (scoop) | `~/scoop/apps/drawio/current/drawio.exe` | scoop 用户 |

**LocalCommandAgent 在执行命令前,先用 `which drawio` 探测实际路径**——而不是写死。

## root 用户处理(`--no-sandbox`)

### 背景

drawio Desktop 在 Linux/macOS 上**默认启用 Chromium sandbox** 以提高安全性。**当以 root 身份运行时,Linux 内核的 user namespace 限制会让 sandbox 启动失败**:

```
[main] Failed to create user namespace: clone3 failed with EPERM
```

### 解法

**所有以 root 运行的 drawio 命令都必须加 `--no-sandbox`**,且**放在子命令标志之前**(`-x` / `-f` / `--scale` / `-o` 之前):

```bash
# ✅ 正确
/usr/bin/drawio --no-sandbox -x -f png --scale 2 -o /tmp/diagrams/foo.png /tmp/diagrams/foo.drawio

# ❌ 错误(drawio 25+ 不接受)
/usr/bin/drawio -x --no-sandbox -f png ...
```

### 自动判断

**LocalCommandAgent 写命令前必须先 `whoami` 探测**:

```bash
# 一次性探测,后续所有 drawio 命令沿用结果
if [ "$(whoami)" = "root" ]; then
    NO_SANDBOX="--no-sandbox"
else
    NO_SANDBOX=""
fi

# 后续命令模板
/usr/bin/drawio $NO_SANDBOX -x -f png --scale 2 -o /tmp/diagrams/<name>.png /tmp/diagrams/<name>.drawio
```

或更简单的内联写法:

```bash
# 非 root 跑普通命令
/usr/bin/drawio -x -f png --scale 2 -o /tmp/diagrams/order.png /tmp/diagrams/order.drawio

# root 跑
/usr/bin/drawio --no-sandbox -x -f png --scale 2 -o /tmp/diagrams/order.png /tmp/diagrams/order.drawio
```

### 备选:用非 root 用户跑

如果不便改 drawio 命令,可以让 localcommand 沙箱用 `runuser -u <user> -- drawio ...` 切换用户执行。

## 前置条件(本机环境)

1. **主机已安装 draw.io Desktop**:
   - Linux (本项目): `apt install drawio` 或从 [drawio.com](https://www.drawio.com/) 下载 AppImage
   - macOS: `brew install --cask drawio` 或下载 .dmg
   - Windows: 安装到 `C:\Program Files\draw.io\`
2. **`drawio` 命令在 localcommand 白名单内** — 见 `workdir/config/exec-approvals.json`(已添加)
3. **默认输出目录 `/tmp/diagrams`** — 主机 tmp 文件系统,localcommand 沙箱允许写入
4. **PNG 转 base64 用 `base64` 命令** — 它已在白名单内
5. **root 环境**:drawio 命令必须加 `--no-sandbox`(详见上节)
