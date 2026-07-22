---
name: system_diagnosis
description: 当用户要求"查磁盘 / 内存 / CPU / 进程 / 端口 / 资源占用 / 系统状态 / 健康检查"时，加载本 skill 按推荐命令序列诊断。
context: inline
---

# 系统诊断标准流程

按以下顺序执行，**先汇总后总结**，不要一次跑 10 条命令后等结果。

## 第一步：磁盘

```bash
df -h
echo "---"
du -sh /var/log /tmp /home 2>/dev/null | sort -hr | head -10
echo "---"
lsblk 2>/dev/null || echo "lsblk 不可用"
```

解读要点：
- `Use%` > 90% → 磁盘满，定位大目录
- `/var/log` 异常大 → 日志未轮转
- inode 满（df -i）→ 小文件过多

## 第二步：内存

```bash
free -h
echo "---"
ps -eo pid,user,pmem,pcpu,comm --sort=-pmem | head -10
```

解读要点：
- `available` < `total / 4` → 内存吃紧
- top 进程内存占用 > 20% → 异常进程

## 第三步：CPU

```bash
uptime
echo "---"
top -bn1 | head -20
echo "---"
ps -eo pid,user,pcpu,comm --sort=-pcpu | head -10
```

解读要点：
- load average > CPU 核心数 → 过载
- 单进程 CPU > 80% 且持续 → 异常进程

## 第四步：端口 + 网络

```bash
ss -tlnp 2>/dev/null || netstat -tlnp
echo "---"
ss -tnp 2>/dev/null | head -20
```

解读要点：
- LISTEN 端口包含预期服务？
- TIME_WAIT / CLOSE_WAIT 异常多？

## 第五步：日志错误速览

```bash
journalctl -p err -n 50 --no-pager 2>/dev/null || \
  tail -n 200 /var/log/syslog /var/log/messages 2>/dev/null | grep -iE "error|fail|panic" | tail -30
```

## 输出报告格式

把上面 5 步的结果按章节输出，最后给一段"诊断结论 + 建议"。

如果用户**只问一个维度**（比如"看看内存够不够"），跳过其他步骤，直接跑对应那一步。