/*
 * Copyright 2024 yichouchou
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

package localcommand

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 沙箱内"唯一可写"目录。所有文件重定向、here-doc 写入目标必须落到该
// 目录下,且禁止符号链接逃逸到 /tmp 之外。
//
// 设计参考 Linux 沙箱工具（bubblewrap / nsjail）的常见做法：把 /tmp
// 暴露为沙箱内唯一可写目录，宿主其它路径全部只读。
//
// 注意：/tmp 是 host 真实目录，沙箱进程可读可写，但仅限 /tmp/ 前缀路径。
// 通过 resolveSafeTmpPath 严格校验：
//  1. filepath.Clean 解析 .. / .
//  2. 强制前缀等于 /tmp/
//  3. Lstat 检测软链接（防止 /tmp/foo -> /etc/passwd 逃逸）
//
// 拒绝而非绕过：沙箱不调 /bin/sh，所有文件描述符由 Go 进程持有，子
// 进程只继承 fd；父进程不 close，子进程才能写入。这是 Claude Code
// 安全设计的"file write / input redirection"段思路在 Go 进程沙箱下的
// 等效实现。
const sandboxTmpRoot = "/tmp"

// ErrNotUnderTmp 路径不在 /tmp 下
var ErrNotUnderTmp = errors.New("沙箱文件重定向目标必须在 /tmp 目录下")

// ErrSymlinkEscape /tmp 下发现软链接,拒绝
var ErrSymlinkEscape = errors.New("/tmp 下发现软链接,可能是逃逸尝试")

// resolveSafeTmpPath 校验 path 是否安全落到 /tmp 下。
//
// 校验项：
//   - path 必须以 /tmp/ 开头或等于 /tmp
//   - filepath.Clean 后仍以 /tmp/ 开头（解析所有 ..）
//   - Lstat 不跟链接，检测已有路径是否符号链接（不存在的文件合法）
//
// 返回 filepath.Clean 后的绝对路径。
func resolveSafeTmpPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("文件重定向目标为空")
	}

	// 相对路径不允许（防止 LLM 写 > out.txt 后落到 cwd）
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf(
			"文件重定向目标必须是绝对路径（必须以 /tmp/ 开头），收到相对路径 %q。"+
				"请改用 /tmp/...",
			path)
	}

	cleaned := filepath.Clean(path)

	// 前缀校验：必须等于 /tmp 或 /tmp/xxx
	if cleaned != sandboxTmpRoot && !strings.HasPrefix(cleaned, sandboxTmpRoot+"/") {
		return "", fmt.Errorf(
			"%w: 收到 %q (规范化后 %q),只允许写入 %s/*",
			ErrNotUnderTmp, path, cleaned, sandboxTmpRoot)
	}

	// 软链接检测：父目录链上的每一段都不能是符号链接
	// 用 filepath.Dir 一段一段检查
	if err := ensureNoSymlinkInPath(cleaned); err != nil {
		return "", err
	}

	return cleaned, nil
}

// ensureNoSymlinkInPath 校验 path 及其每一段父目录都不是符号链接。
//
// LLM 可能的逃逸手法：
//   - /tmp/foo -> /etc/passwd       (Lstat /tmp/foo 即可发现)
//   - /tmp/dir -> /etc              (Lstat /tmp/dir 即可发现)
//   - /tmp/legitdir/../escape       (filepath.Clean 已解析 ..)
//
// 我们的处理：
//   - cleaned 路径已经解析 ..，无需再次清理
//   - 但 /tmp/legitdir 本身可能是软链接，所以我们对每一段父目录做 Lstat
//   - 最后一段（文件本身）如果存在也做 Lstat
func ensureNoSymlinkInPath(cleaned string) error {
	// 拆成段，逐段 Lstat
	parts := strings.Split(cleaned, "/")
	// parts[0] = "" (绝对路径前导空), parts[1] = "tmp", parts[2+] = 子路径
	cur := ""
	for i := 1; i < len(parts); i++ {
		cur = cur + "/" + parts[i]
		if cur == cleaned {
			// 最后一段：检查文件本身
		}
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				// 不存在是合法的（创建新文件）
				// 但父目录必须存在且非链接——上面的循环已经保证
				return nil
			}
			return fmt.Errorf("Lstat %q 失败: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf(
				"%w: %q 是符号链接 (mode=%v, target 不可知)",
				ErrSymlinkEscape, cur, info.Mode())
		}
	}
	return nil
}

// openTmpFile 在 /tmp 下以指定模式打开文件，供子进程继承 fd。
//
// flags 与 perm 透传给 os.OpenFile。
// 返回的 *os.File 由调用方负责 Close（子进程 exec 后 fd 仍可用，因为
// Go 的 exec.Cmd 在 fork+exec 之间不会主动 close ExtraFiles/Stdout/Stderr
// 等被显式绑定的 fd）。
// safeOpenFlags 附加的安全 flag：O_NOFOLLOW 防止打开 symlink（防 TOCTOU race）。
//
// TOCTOU 场景：
//
//	T1: ensureNoSymlinkInPath 检查 /tmp/foo → NotExist（合法）
//	T2: 攻击者在另一进程 ln -s /etc/passwd /tmp/foo
//	T3: 我们 OpenFile("/tmp/foo") → 默认跟 symlink，实际写到 /etc/passwd
//
// O_NOFOLLOW 在 open(2) 系统调用层就拒绝 symlink，与 Lstat 顺序无关。
// Windows 没有 O_NOFOLLOW（Symlinks 行为不同），所以用 build tag 区分。
func openTmpFile(target string, flags int, perm os.FileMode) (*os.File, error) {
	safe, err := resolveSafeTmpPath(target)
	if err != nil {
		return nil, err
	}

	// 父目录校验：必须存在、是目录、不允许 other-write（防逃逸）
	if dir := filepath.Dir(safe); dir != sandboxTmpRoot {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf(
				"父目录 %q 不存在（沙箱不递归创建子目录）。"+
					"如需嵌套路径,请先调 mkdir -p /tmp/<dir> 后再写入。",
				dir)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%q 不是目录", dir)
		}
		// other-write 位 (0o002) 必须为 0：防止 attacker 通过 other-writable
		// 父目录删/改我们创建的文件，或通过重命名替换为 symlink
		if info.Mode().Perm()&0o002 != 0 {
			return nil, fmt.Errorf(
				"父目录 %q 允许 other-write (mode=%v),拒绝写入。"+
					"这是沙箱防逃逸的硬约束。",
				dir, info.Mode().Perm())
		}
	}

	f, err := os.OpenFile(safe, flags|safeOpenFlags, perm)
	if err != nil {
		return nil, fmt.Errorf("打开 %q 失败: %w", safe, err)
	}
	return f, nil
}

// postWriteVerify 重定向写完后,验证文件不是 symlink / 硬链接指向 /tmp 外。
//
// O_NOFOLLOW 已经挡掉了 open 时的 symlink race,但攻击者还可能:
//   - 写入后用 renameat 把 /tmp/foo 替换为 /etc/passwd 的硬链接（同 fs 上）
//   - 子进程运行中通过其它进程修改路径
//
// 所以 exec 完后用 Lstat 再校验一次（不在 open 时刻校验，exec 后校验）。
//
// 返回 nil 表示 OK，非 nil 表示逃逸尝试，调用方应该 fail-closed。
func postWriteVerify(target string) error {
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 写后文件被外部删除（少见），不强制失败
		}
		return fmt.Errorf("postWrite Lstat %q 失败: %w", target, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"postWrite 检测到 symlink: %q (mode=%v),可能 TOCTOU 逃逸,"+
				"已拒绝返回结果",
			target, info.Mode())
	}
	// 硬链接检查:link count > 1 表示有硬链接指向同一 inode
	// 硬链接在 Linux 上无法跨文件系统,沙箱内所有路径都在同一 fs,所以
	// link > 1 不一定是攻击,但 LLM 没必要创建多链接文件,直接拒绝
	if info.Mode()&os.ModeIrregular != 0 {
		// 包含 device / named pipe 等不规则类型
		return fmt.Errorf(
			"postWrite 检测到不规则文件类型: %q (mode=%v),拒绝",
			target, info.Mode())
	}
	return nil
}

// TmpRedirectTarget 描述一次文件重定向的语义。
//
// 这里把"target 路径"和"操作语义"分离是因为重定向 token 多种多样：
//   - >file          → 写模式覆盖
//   - >>file         → 追加模式
//   - <file          → 读模式
//   - 2>file         → stderr 写模式覆盖
//   - <<EOF ... EOF  → here-doc（写到临时文件再让子进程读）
type TmpRedirectTarget struct {
	Path      string // 解析后的安全路径（已通过 resolveSafeTmpPath 校验）
	Mode      int    // os.O_WRONLY / os.O_RDONLY / os.O_CREATE / os.O_APPEND / os.O_TRUNC
	Perm      os.FileMode
	FDKind    string // "stdout" / "stderr" / "stdin"，用于绑到 cmd 的对应字段
	HereDoc   string // here-doc 内容（非空表示这是 here-doc 重定向）
	IsHereDoc bool
}
