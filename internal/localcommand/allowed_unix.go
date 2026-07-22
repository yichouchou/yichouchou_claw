//go:build linux || darwin

package localcommand

import (
	"os"
	"os/exec"
	"syscall"
)

// AllowedCommands 白名单允许的命令及描述（Linux/Darwin）
var AllowedCommands = map[string]string{
	"ls":         "列出当前或指定目录下文件与文件夹，只读查询",
	"ll":         "ls -la 别名，完整展示文件权限、大小、修改时间、所有者，只读查询",
	"cat":        "读取并打印完整文件内容，仅用于查看文本文件，禁止读取密钥/敏感配置",
	"head":       "读取文件前N行内容，默认10行，文件只读查看",
	"tail":       "读取文件末尾N行，支持实时追踪日志文件输出，常用于日志排查",
	"wc":         "统计文件行数、单词数、字符字节数，文本分析只读工具",
	"grep":       "文本检索，按关键词匹配文件内容、过滤命令输出，日志/配置检索核心工具",
	"find":       "按名称、大小、时间递归查找目录下文件，仅查询，不做删除操作",
	"ps":         "查看主机运行进程列表，查看进程PID、占用、启动命令，只读",
	"df":         "查看磁盘分区挂载、总容量、已用/剩余空间，磁盘资源查询",
	"du":         "统计目录/文件实际占用磁盘大小，排查大文件占用场景",
	"free":       "查看系统内存、Swap交换分区使用总量与剩余，资源监控",
	"uptime":     "展示系统开机时长、当前负载平均值，快速查看机器负载",
	"date":       "输出当前系统时间、时区，仅查看，不支持修改系统时间",
	"echo":       "打印自定义文本，可配合管道传递数据或重定向(>)写入文件，仅限工作目录内文件操作",
	"printf":     "格式化打印文本，支持格式化输出，可配合重定向写入文件",
	"pwd":        "打印当前所在工作绝对路径，目录定位工具",
	"whoami":     "输出当前执行命令的操作系统用户名，身份查询",
	"hostname":   "输出本机主机名称，节点标识查询",
	"uname":      "查看系统内核版本、操作系统架构，主机环境信息查询",
	"top":        "实时查看CPU/内存进程占用，交互式工具，执行超时自动终止",
	"netstat":    "查看本机TCP/UDP监听端口、网络连接、路由状态，网络排查只读",
	"ping":       "ICMP网络连通性探测，测试目标IP/域名延迟与丢包，网络诊断",
	"curl":       "发起 HTTP/HTTPS 请求工具。【允许】任意 HTTP 方法（GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS 等）用于与合法服务进行接口调试；【禁止】以下行为：①在请求 URL、请求头（-H/--header）、请求体（-d/--data/--data-raw/--data-binary/-F/--form/-T/--upload-file 等）中夹带敏感信息（密码/Token/Authorization/Cookie/API Key/私钥/身份证/手机号/银行卡等），②任何下载/落盘行为（-o/--output/-O/--remote-name 及重定向到本地文件、管道写入文件、tee 落盘），③禁止访问内网（10.x/172.16-31.x/192.168.x/169.254.x/127.x）和高危地址。所有响应内容只输出到 stdout，由调用方自行处理。",
	"wget":       "远程下载网络文件到本地工作目录，仅允许公开静态资源下载",
	"tar":        "文件打包/解压工具，仅读写工作目录内压缩包，不操作系统目录",
	"zip":        "将目录/文件打包为zip压缩文件，仅限工作目录操作",
	"unzip":      "解压zip压缩包至当前目录，仅限工作目录操作",
	"mkdir":      "创建空文件夹，仅允许在沙箱工作目录内新建目录",
	"cp":         "复制文件/文件夹，仅支持工作目录内拷贝，禁止复制系统敏感文件",
	"mv":         "文件/目录移动、重命名，仅限工作目录内部操作，不可移动系统文件",
	"diff":       "对比两个文本文件内容差异，配置文件比对工具",
	"sort":       "对文本内容按行排序，配合管道做日志数据整理",
	"uniq":       "去除文本连续重复行，日志去重统计工具",
	"awk":        "高级文本列提取、数值统计、格式化输出，日志结构化分析核心工具",
	"sed":        "流式文本替换、过滤，仅允许工作目录内临时文件修改，禁止编辑系统配置",
	"cut":        "按分隔符截取文本指定列，解析日志、表格类文本",
	"tree":       "树形递归打印目录层级结构，直观查看文件夹结构，只读",
	"sensors":    "读取硬件CPU、硬盘温度传感器数据，服务器硬件状态监控",
	"ip":         "查看网卡IP地址、路由表、网络设备状态，新一代网络查询工具，支持重定向写入文件",
	"ifconfig":   "传统网卡信息查看，兼容旧系统网络接口查询，只读，支持重定向写入文件",
	"touch":      "创建空文件或更新文件时间戳，仅限沙箱工作目录内使用",
	"tee":        "从标准输入读取并同时写入标准输出和文件，配合管道使用，支持写入文件内容",
	"journalctl": "查看系统服务日志，支持按服务、时间过滤，系统故障排查只读",
	"dmesg":      "查看内核启动、硬件报错日志，服务器异常排查工具",
	"ss":         "高性能替代netstat，查看系统套接字、端口连接状态",
	"dig":        "DNS解析查询，域名A记录/CNAME解析诊断",
	"nslookup":   "兼容式DNS域名解析工具，排查域名解析异常",
	"traceroute": "路由追踪，排查网络链路延迟、断链节点",
	"gzip":       "单文件压缩解压，仅工作目录内使用",
	"gunzip":     "解压gzip格式文件",
	"md5sum":     "计算文件MD5哈希，校验文件完整性、防篡改",
	"sha256sum":  "计算文件sha256校验值，文件完整性校验",
	"watch":      "周期性重复执行查询命令，持续监控磁盘、进程、接口状态",
	"base64":     "Base64编码、解码文本，用于解析配置内加密字符串，只读转换",
	"jq":         "JSON格式化、过滤解析工具，解析接口返回、k8s json配置",
	"crontab":    "查看当前用户定时任务列表，仅-l查询，禁止编辑/删除定时任务",

	// ===== 文本编辑与查看 =====
	"vi":      "标准文本编辑器，仅允许编辑沙箱工作目录内文件，禁止编辑系统配置文件",
	"vim":     "vi 增强版编辑器，仅限工作目录内使用，禁止编辑系统配置文件",
	"less":    "分页查看文本文件，支持上下翻页/搜索，常用于大文件/日志查看",
	"more":    "分页查看文本文件，简易翻页工具",
	"tac":     "反向按行打印文件内容（cat 反向），用于倒序查看日志",
	"rev":     "按字符反向打印每行内容，配合 tac 可逆序查看日志",
	"strings": "提取二进制/文本文件中的可打印字符串，常用于查看二进制日志、core 文件、SO 库版本信息",
	"xxd":     "十六进制查看/转储文件，分析二进制文件结构、编码问题",
	"file":    "识别文件类型（ELF/UTF-8/压缩格式等），排查文件异常",
	"od":      "八进制/十六进制转储文件，分析二进制日志、编码异常",
	"nl":      "显示文件内容并附带行号，比 cat -n 更易读的日志查看",

	// ===== 性能与故障分析（主机排障核心） =====
	"iostat":    "CPU 磁盘 I/O 统计，分析磁盘读写延迟、IOPS、util%，是磁盘瓶颈定位首选工具",
	"vmstat":    "虚拟内存统计，查看 si/so 换页、bi/bo 块 IO、r 队列，内存与系统负载核心指标",
	"mpstat":    "多核 CPU 统计，分析每颗 CPU 核心负载、软硬中断、context switch，定位 CPU 瓶颈",
	"sar":       "系统活动历史采样（需 sysstat），回看 CPU/内存/磁盘/网络历史曲线，做趋势分析",
	"pidstat":   "按进程采样 CPU/内存/IO/上下文切换，定位单进程资源占用与抖动",
	"perf":      "Linux 性能分析工具，统计 CPU 热点、cache miss、tracepoint，内核/应用级性能分析",
	"strace":    "追踪进程系统调用与信号，分析进程卡死、文件读写、IO 行为",
	"ltrace":    "追踪进程库函数调用，辅助排查动态库调用问题",
	"lsof":      "列出进程打开的文件、socket、管道，排查文件句柄泄漏、端口占用、删除但未释放的文件",
	"iotop":     "按进程查看磁盘 IO 实时排名，定位 IO 抢占最严重的进程",
	"iftop":     "按连接查看网络流量排名，定位带宽占用高的会话/源 IP",
	"nethogs":   "按进程查看网络流量，分析哪个进程在消耗带宽",
	"nload":     "实时网卡流入/流出流量图，快速观察带宽水位",
	"glances":   "一站式系统监控（CPU/内存/磁盘/网络/进程），相当于 top 的增强版",
	"htop":      "增强版 top，支持鼠标、进程树、CPU/内存条形可视化",
	"atop":      "高级系统监控，支持长期回放历史资源数据，分析过去某时刻的资源状态",
	"powertop":  "Intel 平台功耗与唤醒分析工具，定位异常耗电/唤醒源",
	"turbostat": "Intel CPU 频率/温度/功耗/C-state 采样，CPU 性能与散热分析",
	"numastat":  "NUMA 架构内存访问统计，多路服务器内存本地性分析",
	"numactl":   "NUMA 策略控制工具，查看/设置进程 NUMA 绑定，排查跨 socket 性能问题",
	"sysctl":    "查看内核运行参数（仅 -a 只读查询），排查 TCP 缓冲区、连接数等内核限制",

	// ===== 网络抓包与诊断 =====
	"tcpdump":   "网络抓包分析，可按 host/port/protocol 过滤，常用于网络故障、丢包、乱序分析",
	"telnet":    "TCP 端口连通性探测，测试远端端口是否可达，HTTP 协议手动测试",
	"nc":        "Netcat 网络瑞士军刀，用于端口探测、临时 HTTP 测试，仅允许只读探测，禁止反向 shell",
	"nmap":      "主机发现与端口扫描，仅允许扫描指定目标主机的常见端口，禁止扫描内网",
	"mtr":       "ping + traceroute 组合，实时统计每一跳丢包率与延迟，链路质量诊断首选",
	"ethtool":   "查询/设置网卡驱动与硬件参数，查看网卡速率、丢包、offload 状态",
	"arp":       "查看/操作 ARP 缓存表，分析同网段主机连通性",
	"iptables":  "查看防火墙规则（仅 -L/-S 只读查询），分析端口放行/拦截",
	"conntrack": "查看 conntrack 连接跟踪表，分析 NAT/防火墙会话状态",

	// ===== 进程与服务排查 =====
	"pgrep":         "按名称/PID 查找进程，配合 ps/strace 排查服务状态",
	"pidof":         "查找正在运行的服务主进程 PID",
	"pkill":         "按名称发送信号给进程，仅允许 SIGTERM/SIGINFO，禁止 SIGKILL 系统关键进程",
	"pstree":        "进程树视图，分析父子进程关系、孤儿进程、守护进程派生链",
	"kill":          "按 PID 发送信号（默认禁用 SIGKILL 给关键 PID），用于温和停止自有进程",
	"systemctl":     "查看 systemd 服务状态（status/list-units），禁止 start/stop/restart/mask 等变更操作",
	"service":       "SysVinit 兼容服务状态查看，禁止变更操作",
	"supervisorctl": "查看 supervisord 托管服务状态，禁止 start/stop/restart",

	// ===== 日志与故障现场 =====
	"last":     "查看最近登录用户与登录时间，排查异常登录",
	"lastb":    "查看登录失败记录，排查暴力破解",
	"lastlog":  "查看所有用户最近一次登录时间",
	"who":      "查看当前登录用户与终端",
	"w":        "查看当前登录用户及其正在执行的命令",
	"utmpdump": "解析 utmp/wtmp/btmp 二进制日志，可搜索特定用户/IP 历史登录",

	// ===== 内核与硬件 =====
	"lspci":     "列出 PCI 设备，定位网卡/HBA/显卡型号",
	"lsusb":     "列出 USB 设备，排查外设识别问题",
	"lsblk":     "树形列出块设备（磁盘/分区/LVM），排查存储拓扑",
	"blkid":     "查看块设备文件系统类型、UUID、LABEL",
	"lsmod":     "列出已加载的内核模块，排查驱动加载情况",
	"modinfo":   "查看内核模块详细信息（参数、版本、依赖）",
	"dmidecode": "读取 DMI/SMBIOS 硬件信息，查看 CPU/内存/主板/BIOS 配置",
	"lscpu":     "CPU 架构信息（核数、线程、NUMA、flags），替代 cat /proc/cpuinfo 的更易读方式",
	"lsmem":     "内存块拓扑（容量、块大小），排查内存通道",
	"lshw":      "全面硬件信息清单（CPU/内存/磁盘/网卡），深度硬件盘点",
	"hwinfo":    "硬件探测器，比 lshw 更详细的设备信息",
	"hdparm":    "查看 SATA/ATA 磁盘参数（仅 -I/-t 测试读取速度），禁止低级操作",
	"smartctl":  "查看 SMART 磁盘健康度（仅 -H/-A/-l 查询），预测磁盘故障",

	// ===== 软件包与运行时 =====
	"rpm":       "RPM 包查询（仅 -q/-qi/-ql/-qa 只读查询），排查已安装包版本与依赖",
	"yum":       "YUM 包查询（仅 list/search/info 只读查询），禁止 install/remove/update",
	"dnf":       "DNF 包查询（仅 list/search/info 只读查询），禁止 install/remove/update",
	"dpkg":      "Debian 包查询（仅 -l/-s/-L 只读查询），禁止安装/卸载",
	"apt-cache": "APT 缓存查询（仅 search/show policy），禁止 install/remove",
	"ldd":       "查看可执行文件动态库依赖，排查 so 缺失/版本错配",
	"ldconfig":  "查看动态库缓存（仅 -p 打印），排查库加载路径",
	"getconf":   "查询系统配置限制（PATH_MAX/OPEN_MAX/CLK_TCK 等）",

	// ===== 容器与虚拟化（只读排查） =====
	"docker":  "Docker 容器查询（仅 ps/images/logs/inspect/top/stat/stats），禁止 run/exec/pull/push/rm",
	"podman":  "Podman 容器查询（仅 ps/images/logs/inspect/top/stat），禁止 run/exec/rm",
	"ctr":     "containerd CLI 仅查询（containers/tasks/images），禁止 run/exec/rm",
	"crictl":  "Kubernetes CRI 仅查询（ps/images/logs/inspectp/stats），禁止 run/exec",
	"kubectl": "Kubernetes 集群查询（仅 get/describe/logs），禁止 apply/delete/edit/exec/scale",

	// ===== 应用与 Web 排障 =====
	"openssl":  "OpenSSL 工具，可做 s_client TLS 握手诊断、x509 证书查看，禁止生成私钥",
	"httpstat": "curl 包装，统计 HTTP 各阶段耗时（DNS/TCP/TLS/TTFB）",
	"httping":  "持续 HTTP 延迟探测，类似 ping 但针对 HTTP/HTTPS",

	// ===== 文件搜索与统计 =====
	"stat":     "查看文件/文件系统详细信息（权限、时间、inode），排查文件属性",
	"locate":   "基于数据库的全盘快速文件查找（需 updatedb 仅管理员可建库）",
	"which":    "定位可执行文件路径",
	"type":     "判断命令类型（alias/builtin/function/file）",
	"command":  "POSIX shell builtin，配合 -v/-V 定位可执行文件，等价于 which/type；其他用法见 builtin 白名单",
	"xargs":    "从标准输入构造命令行参数，配合 grep/find 做批量处理，禁止 rm 等危险命令",
	"realpath": "解析符号链接的真实绝对路径",
	"readlink": "读取符号链接目标",

	// ===== 时间与时序分析 =====
	"timedatectl": "查看时区与时间同步状态（status 只读），排查时钟漂移",
	"chronyc":     "chrony 时间同步查询（tracking/sources），分析 NTP 同步精度",
	"ntpq":        "传统 NTP 查询（-p 打印对等节点），分析时钟源",

	// ===== 开发工具链（编译器/解释器/包管理器/VCS）=====
	// 安全约束：仅在工作目录内运行、禁止写系统目录、禁止执行任意远程脚本。

	// --- Go ---
	"go":            "Go 工具链入口（build/run/test/mod/env/version 等），仅允许工作目录内构建",
	"gofmt":         "格式化 Go 源码（-d 打印差异/-w 写回），仅限工作目录",
	"goimports":     "Go import 自动整理工具，仅限工作目录",
	"golangci-lint": "Go 静态检查聚合器（run/--help），仅限工作目录",
	"gopls":         "Go 语言服务器（gopls check/version），仅限工作目录",

	// --- Python / Node / 通用脚本运行时 ---
	"python":  "Python 2 解释器入口；建议使用 python3，仅限工作目录",
	"python3": "Python 3 解释器入口（-c/-m/-V/-h 等），仅限工作目录",
	"py.test": "pytest 测试运行器（-k/-x/-v 等），仅限工作目录",
	"pytest":  "pytest 测试运行器（--collect-only/--help/-q），仅限工作目录",
	"pip":     "pip 包管理器（仅 list/show/check/freeze 等只读查询），禁止 install/uninstall",
	"pip3":    "pip3 包管理器（仅 list/show/check/freeze），禁止 install/uninstall",
	"pipx":    "pipx 工具管理器（仅 list/list-all/run），禁止 install/inject",
	"uv":      "Astral uv（仅 pip list/tree 等只读查询），禁止 add/remove/sync",
	"poetry":  "Poetry 依赖管理（仅 show/check/version），禁止 install/add/remove",
	"pdm":     "PDM 依赖管理（仅 list/show/info），禁止 add/remove/install",
	"conda":   "Conda 环境管理（仅 list/info/search/version），禁止 install/create/remove",
	"node":    "Node.js 运行时（-v/-e/-p 等），仅限工作目录，禁止 -e 执行远程代码",
	"npm":     "npm 包管理器（仅 list/view/ls/--version/audit/outdated），禁止 install/uninstall/update/run",

	// --- 包管理器（Linux） ---
	"apt":      "APT 前端（仅 list/search/show/depends 等只读查询），禁止 install/remove/update/upgrade",
	"apt-get":  "APT 后端（仅 list/search 等只读查询），禁止 install/remove/update",
	"aptitude": "aptitude 前端（仅 search/show/why），禁止 install/remove",
	"pacman":   "Arch 系包管理器（仅 -Q/-Ss 查询），禁止 -S/-R/-U",
	"zypper":   "openSUSE 包管理器（仅 search/info/--version），禁止 install/remove",
	"emerge":   "Gentoo 包管理器（仅 --search --info），禁止 --pretend 之外的真实安装",
	"nix":      "Nix 包管理器（仅 search/show-env --version），禁止 --install",
	"brew":     "Homebrew（macOS 常用，仅 list/search/info --version），禁止 install/uninstall",

	// --- 编译/构建工具 ---
	"gcc":        "GCC C 编译器（仅 --version/-v/-print-prog-name/-E/-S 编译到 stdout），禁止 -o 写到工作目录外",
	"g++":        "G++ C++ 编译器（同 gcc 约束），仅限工作目录",
	"clang":      "Clang 编译器（--version/-E/-S），仅限工作目录",
	"clang++":    "Clang++ C++ 编译器（同 clang 约束）",
	"cc":         "系统默认 C 编译器符号链接",
	"make":       "Make 构建工具（仅工作目录内 Makefile，禁止 -C /etc /boot /usr）",
	"cmake":      "CMake 配置工具（--version/--help），仅限工作目录",
	"ninja":      "Ninja 构建工具（--version），仅限工作目录",
	"meson":      "Meson 配置工具（--version/--help），仅限工作目录",
	"autoconf":   "Autoconf（--version），仅限工作目录",
	"automake":   "Automake（--version），仅限工作目录",
	"libtool":    "GNU Libtool（--version），仅限工作目录",
	"pkg-config": "查询编译库依赖信息（--version/--list-all/<mod>），仅限工作目录",

	// --- VCS / 协作 ---
	"git":        "Git 版本控制（status/log/diff/show/branch/remote/fetch/log/blame/ls-files/reflog/grep/stash list 等只读/查询操作）",
	"git-log":    "git log（用户多次使用，可直接调用）",
	"git-diff":   "git diff（工作区/暂存区差异）",
	"git-status": "git status（查看仓库状态）",
	"git-show":   "git show（查看提交/对象）",
	"git-blame":  "git blame（定位修改行）",
	"svn":        "Subversion（仅 log/info/status/diff/cat/look 等只读），禁止 commit/update/add",
	"hg":         "Mercurial（仅 log/status/diff/cat 等只读），禁止 commit/update/add",
	"gh":         "GitHub CLI（仅 repo view/pr view/issue view/status/--version），禁止 pr create/repo create/api 写操作",

	// --- 网络 / API 调试开发期常用 ---
	"httpie": "HTTPie（仅 http --offline GET / --version）",
	"xh":     "Rust 写的 httpie 替代品（仅 GET / --version）",

	// --- Linter / Formatter（多语言） ---
	"shellcheck":  "Shell 脚本静态检查（仅工作目录内的 .sh 文件）",
	"shfmt":       "Shell 脚本格式化（仅 -d/-l 打印差异，禁止 -w）",
	"yamllint":    "YAML 静态检查（仅工作目录）",
	"prettier":    "Prettier 格式化（仅 --check/-l 打印），禁止 --write",
	"eslint":      "ESLint（仅 --version/--print-config 等只读），禁止 --fix",
	"flake8":      "Python flake8（仅 --version/--statistics），仅限工作目录",
	"ruff":        "Python ruff（仅 check --no-fix/--version），仅限工作目录",
	"black":       "Python black（仅 --check/--diff --version），仅限工作目录",
	"mypy":        "Python mypy（仅 --version/--no-incremental 工作目录）",
	"gofmt-check": "等价 gofmt -l（用户多次使用，可直接调用）",

	// ===== 常用辅助 =====
	"env":     "打印当前环境变量，排查变量传递问题",
	"set":     "列出所有 shell 变量与函数（仅打印）",
	"alias":   "列出已定义别名（仅打印）",
	"history": "查看当前 shell 历史命令",
	"man":     "查看命令手册",
	"tldr":    "简化版命令示例查询",
	"whatis":  "一行命令说明",
	"apropos": "按关键词搜索 man 页面",
	"whereis": "定位命令的二进制/源码/man 路径",
}

// PlatformName 当前平台标识
const PlatformName = "linux"

// platformSysProcAttr 在 Linux/Darwin 上把子进程放到新的进程组，
// 这样可以一次性 kill 整组（包括 fork 出来的子进程），避免泄漏。
func platformSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 通过负 PID 把整个进程组一起 SIGKILL。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// sandboxEnv 返回沙箱内子进程使用的环境变量。
// 限制 PATH、HOME、时区，避免子进程访问宿主机的敏感环境。
//
// 透传策略：
//   - HOME 固定为 /tmp，隔离 ~/.ssh / ~/.bash_history / ~/.aws 等敏感目录
//   - GH_TOKEN / GITHUB_TOKEN / GH_CONFIG_DIR 透传父进程设置，
//     解决 gh / git 等工具需要用户已登录凭证的问题
//   - 其他环境变量不传，避免沙箱进程继承宿主机的随机配置
func sandboxEnv() []string {
	env := []string{
		//"HOME=/tmp",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TZ=Asia/Shanghai",
	}
	// 透传与"工具认证 / 配置"相关的少量环境变量；这些是
	// 用户主动在 shell 里 export 的，传递它们能避免沙箱内工具需要重复登录。
	for _, key := range []string{
		"GH_TOKEN", "GITHUB_TOKEN", "GH_CONFIG_DIR", "GH_HOST",
		"DOCKER_HOST", "KUBECONFIG",
	} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// sandboxWorkDir 返回沙箱工作目录。
func sandboxWorkDir() string {
	return "/tmp"
}

// hintForCommand 在 Linux 上不需要"平台提示"——所有命令都是原生支持，
// 命令找不到一般是确实没装。这里返回空字符串即可。
func hintForCommand(cmdName string) string {
	return ""
}
