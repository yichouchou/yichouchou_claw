//go:build windows

package localcommand

import (
	"os/exec"
	"strings"
	"syscall"
)

// platformSysProcAttr 在 Windows 上让子进程成为新进程组的一员，
// 便于通过 Job 对象或 Process.Kill 统一清理。Windows syscall.SysProcAttr
// 没有 Setpgid 字段，使用 CreationFlags.CREATE_NEW_PROCESS_GROUP 达到近似效果。
func platformSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: 0x00000200, // CREATE_NEW_PROCESS_GROUP
	}
}

// killProcessGroup 在 Windows 上不能向负 PID 发信号，这里直接终止主进程。
// Windows 进程组语义与 Unix 不同，逐进程 Kill 已经能满足沙箱使用场景。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

// sandboxEnv 返回沙箱内子进程使用的环境变量。
// Windows 上尽量精简，仅保留 LANG/TZ/PATH。
func sandboxEnv() []string {
	return []string{
		"HOME=%TEMP%",
		"PATH=%SystemRoot%\\System32;%SystemRoot%;%SystemRoot%\\System32\\WindowsPowerShell\\v1.0\\",
		"TZ=Asia/Shanghai",
		"LANG=zh_CN.UTF-8",
	}
}

// sandboxWorkDir 返回沙箱工作目录。Windows 上用 %TEMP%。
func sandboxWorkDir() string {
	return "%TEMP%"
}

// linuxEquivalent 给 Windows 用户在跑 Linux 命令时看到等价命令提示。
// 命中后追加到 stderr 末尾，帮助 LLM/用户立刻知道该用什么代替。
var linuxEquivalent = map[string]string{
	// 文件/目录
	"ls":     "dir / Get-ChildItem (gci)",
	"ll":     "ls -la 等价 dir /Q 或 gci | Format-Table",
	"cat":    "type / Get-Content (gc)",
	"head":   "Get-Content -TotalCount N",
	"tail":   "Get-Content -Tail N",
	"wc":     "(Get-Content).Count / Measure-Object",
	"grep":   "findstr / Select-String (sls)",
	"find":   "Get-ChildItem -Recurse -Filter 或 dir /s",
	"cp":     "copy / Copy-Item (cp / cpi)",
	"mv":     "move / Rename-Item / Move-Item (mi)",
	"rm":     "del / Remove-Item (ri) — 沙箱已禁用",
	"mkdir":  "mkdir / New-Item -ItemType Directory",
	"touch":  "New-Item -ItemType File 或者 (Get-Item).LastWriteTime = Get-Date",
	"diff":   "fc / Compare-Object (diff / compare)",
	"sort":   "sort / Sort-Object",
	"uniq":   "Get-Unique",
	"awk":    "Select-String / Where-Object / ForEach-Object",
	"sed":    "ForEach-Object / -replace 运算符",
	"cut":    "Select-Object -ExpandProperty / -split",
	"tree":   "tree.com / Show-Tree (PowerShell 7)",
	"chmod":  "icacls",
	"stat":   "Get-Item / Get-ItemProperty",
	"which":  "where / Get-Command",
	"type":   "Get-Command — 注意 type 在 CMD 是查看文件",
	"xargs":  "ForEach-Object { ... }",
	"locate": "Get-ChildItem -Recurse + Select-String 替代",

	// 基础信息
	"uname":    "ver / systeminfo / Get-CimInstance Win32_OperatingSystem",
	"hostname": "hostname (CMD/PowerShell 内置)",
	"whoami":   "whoami / whoami /priv / whoami /groups",
	"uptime":   "(Get-Date) - (Get-CimInstance Win32_OperatingSystem).LastBootUpTime",
	"date":     "date / Get-Date",
	"free":     "Get-CimInstance Win32_OperatingSystem | select FreePhysicalMemory / systeminfo",
	"top":      "Get-Process | Sort CPU -Descending (实时刷新加 while)",
	"ps":       "tasklist / Get-Process (gps / ps)",
	"kill":     "taskkill / Stop-Process (kill / spps)",
	"pkill":    "taskkill /IM <name> / Stop-Process -Name <name>",
	"pgrep":    "Get-Process -Name <name> / tasklist /FI",
	"pidof":    "Get-Process <name> | Select Id",
	"pstree":   "Get-CimInstance Win32_Process | 自行格式化父子关系",
	"killall":  "taskkill /IM <name>.exe /F",

	// 服务
	"systemctl":     "Get-Service / sc query",
	"service":       "Get-Service / sc query",
	"supervisorctl": "无对应；用 Get-Service / Get-Process 配合",

	// 网络
	"netstat":    "netstat -ano / Get-NetTCPConnection",
	"ss":         "Get-NetTCPConnection / netstat -ano",
	"ifconfig":   "ipconfig / Get-NetIPAddress",
	"ip":         "ipconfig / Get-NetIPAddress / Get-NetRoute",
	"ping":       "ping (内置)",
	"traceroute": "tracert / Test-NetConnection -TraceRoute",
	"mtr":        "pathping / Test-NetConnection -TraceRoute",
	"tcpdump":    "netsh trace / pktmon（需安装 Npcap 后用 Wireshark）",
	"telnet":     "Test-NetConnection -Port / curl telnet://",
	"nc":         "Test-NetConnection / PowerShell TcpClient",
	"nmap":       "需额外安装 Nmap；或用 Test-NetConnection 做简单端口扫描",
	"ethtool":    "Get-NetAdapter / Get-NetAdapterStatistics",
	"arp":        "arp -a / Get-NetNeighbor",
	"iptables":   "netsh advfirewall show / Get-NetFirewallRule",
	"conntrack":  "Get-NetConnectionProfile / Get-NetFirewallRule",
	"dig":        "Resolve-DnsName / nslookup",
	"nslookup":   "nslookup (内置) / Resolve-DnsName",
	"curl":       "curl (Windows 10+ 内置) / Invoke-WebRequest (iwr)",
	"wget":       "需额外安装；或用 Invoke-WebRequest",

	// 文件查看/编辑
	"vi":        "notepad / vim (需 Git for Windows)",
	"vim":       "vim (需 Git for Windows) / notepad",
	"less":      "more / Get-Content | Out-Host -Paging",
	"more":      "more (CMD 内置)",
	"tac":       "Get-Content | Select-Object -Last 1 .. First 1（自行倒序）",
	"rev":       "[Array]::Reverse($line.ToCharArray()) -join ''",
	"strings":   "Select-String -Pattern '.+' / strings (Git for Windows)",
	"xxd":       "Format-Hex (PowerShell 5.1+)",
	"file":      "Get-Item | Select-Object Extension / certutil 用于查二进制签名",
	"od":        "Format-Hex",
	"nl":        "Get-Content | ForEach-Object-Object { $i++; \"$i $_\" }",
	"md5sum":    "Get-FileHash -Algorithm MD5",
	"sha256sum": "Get-FileHash -Algorithm SHA256",
	"base64":    "certutil -encode / -decode / [Convert]::ToBase64String",
	"tee":       "Tee-Object (tee)",
	"tar":       "tar (Windows 10+ 内置) / Expand-Archive",
	"zip":       "Compress-Archive",
	"unzip":     "Expand-Archive",
	"gzip":      "tar -z / .NET GZipStream",
	"gunzip":    "tar -z / .NET GZipStream",

	// 性能/故障分析
	"iostat":    "Get-Counter '\\PhysicalDisk(*)\\*' / typeperf \"\\PhysicalDisk(*)\\*\"",
	"vmstat":    "Get-Counter '\\Memory\\*' / '\\Processor(_Total)\\*'",
	"mpstat":    "Get-Counter '\\Processor(*)\\*'",
	"sar":       "typeperf / Get-Counter（按时间采样）",
	"pidstat":   "Get-Counter '\\Process(*)\\*'",
	"perf":      "Get-Counter / wpr / wpa（Windows Performance Recorder/Analyzer）",
	"strace":    "Process Monitor (procmon) / wpr",
	"ltrace":    "Process Monitor (procmon)",
	"lsof":      "Get-Process | Select Id, Handles / tasklist /m / handle (Sysinternals)",
	"iotop":     "Get-Process | Sort IO -Descending (PowerShell 7+ 支持 IO metrics)",
	"iftop":     "Get-NetAdapterStatistics / nethogs 等价需要 Npcap",
	"nethogs":   "需 Npcap + 自定义脚本；Get-Process | Sort IO 近似",
	"nload":     "Get-NetAdapterStatistics 轮询",
	"glances":   "Get-Counter + 自定义面板 / glances Windows 移植版",
	"htop":      "Get-Process | Out-GridView (GUI)",
	"atop":      "typeperf -si 5 -o log.csv + 后处理",
	"powertop":  "powercfg /energy",
	"turbostat": "Get-CimInstance Win32_Processor | Select *",
	"numastat":  "Get-NetAdapterHardwareInfo / 无 NUMA 等价",
	"numactl":   "无 NUMA 概念",
	"sysctl":    "Get-ItemProperty HKLM:\\SYSTEM\\CurrentControlSet\\Services\\Tcpip\\Parameters",

	// 日志
	"journalctl": "Get-EventLog / Get-WinEvent / wevtutil qe",
	"dmesg":      "Get-WinEvent -LogName System / wevtutil qe System",
	"last":       "Get-WinEvent -LogName Security / qwinsta",
	"lastb":      "Get-WinEvent -LogName Security -FilterXPath \"*[System[EventID=4625]]\"",
	"lastlog":    "Get-LocalUser | Select LastLogon",
	"who":        "quser / qwinsta",
	"w":          "quser",
	"utmpdump":   "wevtutil / Get-WinEvent",

	// 内核/硬件
	"lspci":     "Get-PnpDevice / pnputil /enum-devices",
	"lsusb":     "Get-PnpDevice -Class USB",
	"lsblk":     "Get-Disk / Get-Partition / Get-Volume",
	"blkid":     "Get-Volume | Select DriveLetter, FileSystemLabel, UniqueId",
	"lsmod":     "Get-WindowsDriver / driverquery",
	"modinfo":   "pnputil /enum-drivers",
	"dmidecode": "Get-CimInstance Win32_* (BIOS/BaseBoard/Processor/MemoryChip)",
	"lscpu":     "Get-CimInstance Win32_Processor",
	"lsmem":     "Get-CimInstance Win32_PhysicalMemory",
	"lshw":      "msinfo32 / Get-CimInstance Win32_*",
	"hwinfo":    "msinfo32 / dxdiag",
	"hdparm":    "无对应；HDD 健康用 Get-PhysicalDisk | Select HealthStatus",
	"smartctl":  "Get-PhysicalDisk | Select HealthStatus, OperationalStatus",

	// 软件包
	"rpm":       "Get-Package / winget list",
	"yum":       "winget / choco / scoop（仅查询）",
	"dnf":       "winget / choco / scoop（仅查询）",
	"dpkg":      "Get-Package",
	"apt-cache": "winget search / choco search",
	"ldd":       "无对应（Windows 用 dumpbin /Dependencies）",
	"ldconfig":  "无对应",
	"getconf":   "Get-ItemProperty 'HKLM:\\Software\\Microsoft\\Windows NT\\CurrentVersion'",

	// 容器
	"docker":  "docker (Docker Desktop for Windows)",
	"podman":  "podman (Windows 移植)",
	"ctr":     "ctr (containerd Windows)",
	"crictl":  "crictl (Windows 节点)",
	"kubectl": "kubectl (Windows 客户端)",

	// Web 排障
	"openssl":  "已无原生；用 Git for Windows 的 openssl 或 PowerShell .NET TLS API",
	"httpstat": "无对应；用 Invoke-WebRequest -Verbose + Measure-Command",
	"httping":  "Test-NetConnection / 简单脚本",

	// 时间
	"timedatectl": "Get-TimeZone / w32tm /query /status",
	"chronyc":     "w32tm /query /status",
	"ntpq":        "w32tm /query /peers",

	// 辅助
	"env":     "Get-ChildItem Env: / set",
	"alias":   "Get-Alias / Get-Command",
	"history": "Get-History (h)",
	"man":     "Get-Help",
	"tldr":    "无对应；可用 tldr-windows",
	"whatis":  "Get-Command -Syntax",
	"apropos": "Get-Command | Where-Object Name -like",
	"whereis": "where / Get-Command",
	"jq":      "ConvertFrom-Json / jq.exe (Git for Windows)",
	"crontab": "Get-ScheduledTask / schtasks /query",

	// 不建议/沙箱禁用
	"shutdown":    "shutdown.exe — 沙箱已禁用",
	"reboot":      "restart-computer — 沙箱已禁用",
	"halt":        "stop-computer — 沙箱已禁用",
	"init 0":      "stop-computer — 沙箱已禁用",
	"init 6":      "restart-computer — 沙箱已禁用",
	"rmdir":       "Remove-Item / rd — 沙箱严格限制",
	"dd":          "无对应；磁盘克隆用 robocopy / dd for Windows",
	"mkfs":        "无对应；格式化用 Format-Volume / diskpart（高危）",
	"fdisk":       "diskpart — 高危，沙箱已禁用",
	"parted":      "diskpart — 高危，沙箱已禁用",
	"partprobe":   "无对应",
	"eval":        "Invoke-Expression — 沙箱已禁用",
	"exec":        "无直接对应；沙箱已禁用",
	"fork":        "无对应",
	"netcat":      "ncat (Nmap) / Test-NetConnection",
	"hping":       "无对应；用 Nping 或 Scapy",
	"chmod 4755":  "icacls（设 setuid）",
	"/dev/sd":     "无对应；磁盘路径 \\.\\PHYSICALDRIVE0",
	"/dev/tcp":    "无对应；用 .NET TcpClient",
	"/dev/udp":    "无对应；用 .NET UdpClient",
	"/etc/passwd": "Get-LocalUser / HKLM:\\SAM",
	"/etc/shadow": "无对应（Windows 用 SAM 哈希）",
}

// hintForCommand 给 Windows 上跑的 Linux 命令附加等价命令提示。
// Linux 上此函数始终返回空字符串（在 allowed_unix.go 中实现）。
func hintForCommand(cmdName string) string {
	// 只取第一个 token（去除路径）
	base := cmdName
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "\\"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.ToLower(base)
	if eq, ok := linuxEquivalent[base]; ok {
		return "Windows 等价命令: " + eq +
			"\n提示：当前运行在 Windows 上，本白名单为 Windows 原生命令重新组织。" +
			"请使用 GetAllowedCommands() 查看 Windows 平台允许的命令。"
	}
	return ""
}
