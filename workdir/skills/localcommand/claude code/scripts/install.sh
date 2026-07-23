#!/bin/bash
# Claude Code Skill 安装脚本（Eino 适配版，仅 Linux）
#
# 用途：把 skill 目录（含 SKILL.md）安装到 eino 的 skills 工作目录，
#       让 eino skill middleware 自动加载。
#
# 执行场景：
#   1) skill 已在 eino 项目内（例如 git clone 后）：
#        cd /path/to/yichouchou_claw
#        ./workdir/skills/localcommand/claude\ code/scripts/install.sh
#      脚本会自动从源目录路径推断 EINO_WORKDIR。
#
#   2) skill 在任意临时目录，通过环境变量指定目标：
#        EINO_WORKDIR=/path/to/yichouchou_claw/workdir/skills \
#            ./scripts/install.sh
#
#   3) CI/CD 批量安装：
#        EINO_WORKDIR=... SKILL_NAME=... ./scripts/install.sh
#
# 默认安装路径：$EINO_WORKDIR/localcommand/$SKILL_NAME/
#   EINO_WORKDIR 默认从 SKILL_SRC 路径推断，或回退到 $PWD/workdir/skills
#   SKILL_NAME    默认 = 当前 skill 源目录名（保留 "claude code" 这种带空格形式）
#
# 注意：本脚本只处理"把文件放到 eino 能扫到的地方"。
#       白名单放开（allowed_unix.go / allowed_windows.go）以及
#       主机安装 claude-code CLI 由人工另行处理。

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
SKILL_SRC="$( dirname "$SCRIPT_DIR" )"

echo "========================================"
echo "Claude Code Skill 安装（Eino 适配版）"
echo "========================================"
echo ""
echo "源 skill 目录: $SKILL_SRC"

# 1) 校验源目录：必须能在父目录找到 SKILL.md
if [[ ! -f "$SKILL_SRC/SKILL.md" ]]; then
    echo "❌ 错误: $SKILL_SRC/SKILL.md 不存在"
    echo "请在 skill 根目录（含 SKILL.md）下运行此脚本"
    exit 1
fi

# 2) 推断 EINO_WORKDIR（可被环境变量覆盖）
if [[ -z "$EINO_WORKDIR" ]]; then
    case "$SKILL_SRC" in
        */workdir/skills/localcommand/*)
            # 源目录已经在 eino 项目内的最终位置 —— 直接用当前位置
            EINO_WORKDIR="${SKILL_SRC%/*/*}"
            ;;
        */workdir/skills/*/*)
            # 源目录在 eino 项目内但类别不是 localcommand（少见）
            EINO_WORKDIR="${SKILL_SRC%/*/*}"
            ;;
        *)
            # 源目录在临时位置 —— 回退到当前工作目录的 workdir/skills
            EINO_WORKDIR="$PWD/workdir/skills"
            ;;
    esac
fi

# 3) 推断 SKILL_NAME（可被环境变量覆盖）
#    保留目录名原样，包括可能存在的空格（如 "claude code"）。
if [[ -z "$SKILL_NAME" ]]; then
    SKILL_NAME="$( basename "$SKILL_SRC" )"
fi

TARGET_DIR="$EINO_WORKDIR/localcommand/$SKILL_NAME"

echo "Eino skills 根: $EINO_WORKDIR"
echo "目标 skill 名: $SKILL_NAME"
echo "安装目标路径: $TARGET_DIR"
echo ""

# 4) 校验 eino skills 根目录存在
if [[ ! -d "$EINO_WORKDIR" ]]; then
    echo "❌ 错误: eino skills 根目录 $EINO_WORKDIR 不存在"
    echo "请设置 EINO_WORKDIR 环境变量，或确认 eino 项目的 workdir/skills 已就绪"
    exit 1
fi

# 5) 确保 localcommand 分类目录存在
if [[ ! -d "$EINO_WORKDIR/localcommand" ]]; then
    echo "创建目录: $EINO_WORKDIR/localcommand/"
    mkdir -p "$EINO_WORKDIR/localcommand"
fi

# 6) 创建 skill 目标目录
mkdir -p "$TARGET_DIR"

# 7) 复制文件
#    注意：cp -rf 在目标 scripts/ 已存在时会合并，不删除 stale 文件。
#    如需强制重装，先手动 rm -rf "$TARGET_DIR/scripts"。
cp -f "$SKILL_SRC/SKILL.md" "$TARGET_DIR/"
cp -rf "$SKILL_SRC/scripts/" "$TARGET_DIR/"

# 8) 可执行权限
chmod +x "$TARGET_DIR/scripts/install.sh"
if [[ -f "$TARGET_DIR/scripts/claude-code.py" ]]; then
    chmod +x "$TARGET_DIR/scripts/claude-code.py" 2>/dev/null || true
fi

echo "✅ 安装完成"
echo ""
echo "========================================"
echo "安装后检查"
echo "========================================"
echo ""
echo "1. 确认 eino BaseDir 配置（main.go 中）："
echo "     skillsRoot 应指向 $EINO_WORKDIR"
echo ""
echo "2. 确认 claude-code 在 localcommand 白名单（任一文件）："
echo "     internal/localcommand/allowed_unix.go    (Linux)"
echo "     internal/localcommand/allowed_windows.go (Windows)"
echo ""
echo "3. 主机已安装 claude-code CLI:"
echo "     https://claude.com/code"
echo ""
echo "4. 重启 yichouchou_claw 服务，eino 会扫描到新 skill。"
echo ""
echo "5. 用法（LocalCommandAgent 通过 local_command 工具调用主机 CLI）:"
echo "     claude-code query <topic>"
echo "     claude-code task -d <description> [--priority high] [--model <name>]"
echo "     claude-code docs [section]"
echo "     claude-code info"
echo ""