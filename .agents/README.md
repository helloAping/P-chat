# `.agents/` 目录

本目录是 **canonical agent 规范来源**，供多种 agent 工具（opencode / codex / claude 等）共享。

## 文件

| 文件 | 用途 |
| --- | --- |
| `AGENTS.md` | 主规范文档（与根 `AGENTS.md` 同源） |
| `README.md` | 本文件 |
| `scripts/install.ps1` | Windows 安装脚本（PowerShell 5.1+） |
| `scripts/install.sh` | Unix 安装脚本（bash） |

## 为什么需要这个目录

不同 agent 工具的指令文件路径不同：

| 工具 | 默认读 |
| --- | --- |
| opencode | `<repo-root>/AGENTS.md` |
| codex | `<repo-root>/.codex/AGENTS.md`（约定） |
| claude | `<repo-root>/CLAUDE.md` 或 `<repo-root>/.claude/CLAUDE.md`（约定） |

如果不统一，开发者切换工具时需要维护多份规范。本目录通过**符号链接**让所有工具都指向同一份 `.agents/AGENTS.md`，单点修改、到处生效。

## 安装

### 一键安装（推荐）

```powershell
# Windows
powershell -NoProfile -ExecutionPolicy Bypass -File .agents\scripts\install.ps1

# Unix
bash .agents/scripts/install.sh
```

执行后自动建立：
- `.opencode/AGENTS.md` → `./.agents/AGENTS.md`
- `.codex/AGENTS.md` → `./.agents/AGENTS.md`
- `.claude/CLAUDE.md` → `./.agents/AGENTS.md`

如果检测到根 `AGENTS.md` 是项目初始化的 stub（< 1KB，且模板占位文字「在此描述你的项目」），会同时把根文件替换为指向 `.agents/AGENTS.md` 的符号链接。

### 手动安装

如果不想跑脚本，可以手动创建符号链接（Windows 需要开发者模式）：

```powershell
# Windows（PowerShell 5.1）
New-Item -ItemType SymbolicLink -Path ".opencode\AGENTS.md" -Target ".\.agents\AGENTS.md"
New-Item -ItemType SymbolicLink -Path ".codex\AGENTS.md" -Target ".\.agents\AGENTS.md"
New-Item -ItemType SymbolicLink -Path ".claude\CLAUDE.md" -Target ".\.agents\AGENTS.md"
```

```bash
# Unix
ln -s .agents/AGENTS.md .opencode/AGENTS.md
ln -s .agents/AGENTS.md .codex/AGENTS.md
ln -s .agents/AGENTS.md .claude/CLAUDE.md
```

## 权限

Windows 创建符号链接需要**管理员**或**开发者模式**开启。脚本检测失败时自动 fallback 到 `Copy-Item`（不再是符号链接，修改时需重跑 install）。

## 修改规范

只改 `.agents/AGENTS.md`。所有工具会通过符号链接读取最新内容。

如果用 fallback 模式（copy），需要重跑 install 脚本让其他目录同步。

## 卸载

```powershell
# 移除符号链接
Remove-Item .opencode\AGENTS.md
Remove-Item .codex\AGENTS.md
Remove-Item .claude\CLAUDE.md
```

注意：**不要** `Remove-Item -Recurse` — 那会跟随符号链接删除 `.agents/AGENTS.md` 本身。
