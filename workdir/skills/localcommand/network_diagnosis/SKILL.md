---
name: network_diagnosis
description: 当用户任务涉及网络访问（git fetch / push / curl / gh API / docker pull / npm install / pip install / apt update 等）且工具返回 "Could not resolve host"、"Connection refused"、"Connection timed out"、"Timeout" 等网络错误时，加载本 skill 按推荐命令序列诊断根因。
context: inline
---

# 网络诊断标准流程

## ⚠️ 重要前提

**沙箱代码层面没有任何网络限制**——子进程和 Go 进程共享同一个 Linux 网络栈。  
**不要立刻告诉用户"沙箱限制网络"** —— 这不是事实，先按本 skill 跑诊断再说。

## 第一步：看 DNS 配置

```bash
cat /etc/resolv.conf
echo "---"
cat /etc/nsswitch.conf 2>/dev/null | grep hosts
```

WSL2 经常 `/etc/resolv.conf` 指向无效 nameserver（127.0.0.53 或过期 IP）。

## 第二步：直接测试目标域名

```bash
curl -v https://github.com 2>&1 | head -25
```

看输出哪一步失败：
- `Could not resolve host` → DNS 失败（第一步）
- `Connection refused` → 端口不通（防火墙 / 服务挂了）
- `Connection timed out` → 网络层不通（路由 / 防火墙拦截）
- `SSL handshake failed` → TLS / 证书问题

## 第三步：单独测 DNS

```bash
nslookup github.com 8.8.8.8
echo "---"
nslookup github.com
```

如果有 nameserver 但 nslookup 也失败 → nameserver 本身有问题，换 8.8.8.8 试试。

## 第四步：代理配置

```bash
git config --global --get http.proxy
git config --global --get https.proxy
echo "---"
echo "HTTP_PROXY=$HTTP_PROXY"
echo "HTTPS_PROXY=$HTTPS_PROXY"
echo "http_proxy=$http_proxy"
echo "https_proxy=$https_proxy"
echo "no_proxy=$no_proxy"
```

公司内网经常需要 export `http_proxy=...` 才出得去。git 配代理和 shell 配代理是两套。

## 第五步：区分"全部不通" vs "只 github 不通"

```bash
curl -I -m 5 https://www.baidu.com
echo "---"
curl -I -m 5 https://api.github.com
echo "---"
curl -I -m 5 https://1.1.1.1
```

- 全部超时 → 整网不通 / 防火墙全面拦截
- 只 github 不通 → DNS 污染 / github 屏蔽 / 网络运营商问题
- baidu 通但 github 不通 → 大概率 DNS 问题

## 第六步：WSL2 特殊检查

```powershell
# 在 Windows PowerShell 里跑（不是沙箱里）
wsl --status
netsh interface portproxy show all
Get-NetFirewallRule | Where-Object {$_.Enabled -eq "True"} | Select-Object -First 5 DisplayName
```

## 诊断结论分类

| 症状 | 建议 |
|---|---|
| `/etc/resolv.conf` nameserver 失效 | `echo "nameserver 8.8.8.8" \| sudo tee /etc/resolv.conf` |
| 公司代理未生效 | export `HTTP_PROXY` / `HTTPS_PROXY` 后重试 |
| 整网不通 | 检查 WSL2 网络模式（NAT vs mirrored）+ Windows 防火墙 |
| 仅 github 不通 | 换镜像（ghproxy.com）或用 SSH 协议 |
| SSL handshake 失败 | `git config --global http.sslVerify false`（仅临时） |

## 输出给用户

跑完诊断后，**把每一步的关键输出贴给用户**，让用户看到证据，再给具体修复建议。  
不要"我自己判断网络不行"——让证据说话。