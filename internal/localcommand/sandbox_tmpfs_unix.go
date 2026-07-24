//go:build unix

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

import "syscall"

// Unix / Linux / macOS 上 syscal.O_NOFOLLOW 在 open(2) 层阻止跟随 symlink,
// 从根本上消除 Lstat → OpenFile 之间的 TOCTOU race。
//
// 注意:对最终路径生效。如果路径里某一段父目录是 symlink,O_NOFOLLOW 也
// 会拒绝打开（POSIX 行为,见 open(2) man page "If the trailing component
// (i.e., basename) of pathname is a symbolic link, then the open fails,
// with the error ELOOP"）。
//
// 但 O_NOFOLLOW 只阻止"路径末段是 symlink",不阻止"路径中间段是 symlink"
// （这要看 fs 实现）。所以 ensureNoSymlinkInPath 的逐段 Lstat 仍是必要
// 的一层防御。
const safeOpenFlags = syscall.O_NOFOLLOW
