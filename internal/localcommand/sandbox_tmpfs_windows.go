//go:build windows

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

// Windows 没有 POSIX 风格的 O_NOFOLLOW flag。
//
// Windows 上 symlink 行为：
//   - 默认 CreateFile 会跟随 symlink
//   - FILE_FLAG_OPEN_RECALCULATE 不是 O_NOFOLLOW 等价物
//   - 跨进程的 symlink 攻击需要 SeCreateSymbolicLinkPrivilege,普通进程
//     创建不了 symlink（除非启用了开发者模式）
//
// 所以 Windows 上我们信任：
//  1. ensureNoSymlinkInPath 的逐段 Lstat（事前检查）
//  2. postWriteVerify 的写后 Lstat（事后检查，挡住 TOCTOU race）
//
// safeOpenFlags = 0 表示不附加任何 flag，依赖这两层 Lstat。
const safeOpenFlags = 0
