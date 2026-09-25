# P-Chat Skill 体系实施方案

## 1. 目标与验收标准

Skill 必须是一份可被发现、完整安装、按需加载、可诊断、可追踪的目录包，而不是仅把
`SKILL.md` 文本拼进系统提示词。

完成态需满足：

1. 任意外部 CLI 或安装器写入标准目录后，新会话可直接发现相应 Skill。
2. P-Chat 安装 Skill 时保留 `SKILL.md`、`references/`、`scripts/`、`assets/` 等完整目录。
3. 项目 Skill 覆盖用户级 Skill；来源与覆盖关系可查询，不依赖服务进程重启。
4. 静态提示词只包含 Skill 名称、说明和来源；正文仅在显式激活或 `skill(load)` 时加载。
5. 每次真正加载领域 Skill，服务端必须先发结构化事件，界面明确展示
   `当前调用 Skill：<name>`；失败也必须可见。
6. 安装成功必须经过重新扫描与 doctor 校验。仅下载成功不能报告“安装完成”。
7. 旧 `skill_context` 请求继续兼容，但新客户端只发送 `active_skills`。

## 2. 统一目录与优先级

从高到低合并，同名只暴露优先级最高者：

| 优先级 | 目录 | 角色 |
| --- | --- | --- |
| 1 | `<project>/.p-chat/skills` | P-Chat 管理的项目 Skill |
| 2 | `<project>/.agents/skills` | 跨 Agent 标准项目 Skill |
| 3 | `<PCHAT_DATA_HOME>/skills` | P-Chat 管理的用户级 Skill |
| 4 | `$HOME/.agents/skills` | 跨 Agent 标准用户级 Skill |

`.codex/skills`、`.claude/skills` 等产品私有目录只作为导入来源，不默认加入运行时，
避免同一份 Skill 被多次发现或来源含义不清。目录名用于定位，frontmatter `name` 是逻辑名；
两者不一致时产生诊断。

## 3. 深层模块边界

`internal/skill.Manager` 统一封装发现、加载、资源读取、安装/导入、删除和诊断。
Agent、HTTP、CLI、工具层都不再自行拼目录或复制安装逻辑。

```go
type Manager interface {
    Catalog(ctx context.Context, query CatalogQuery) (Catalog, error)
    Load(ctx context.Context, request LoadRequest) (LoadedSkill, error)
    Apply(ctx context.Context, change ChangeRequest) (ChangeResult, error)
}
```

- `Catalog`：每次按目录快照读取，返回生效 Skill、被覆盖来源和诊断。
- `Load`：读取完整指令或包内资源，校验路径不能逃逸目录，并返回依赖诊断。
- `Apply`：将本地目录或远端仓库暂存、校验后原子发布；删除同样走统一边界。

安装过程限制文件数、总大小和单文件大小；拒绝路径穿越、包内 junction/symlink 逃逸，
安装阶段不执行脚本。远端安装属于外部写入，仍走现有权限确认策略。

## 4. 工具收敛

模型可见工具合并为两个：

- `skill`：`list | inspect | load | read_resource | doctor`
- `skill_manage`：`install | import | remove`

`list/inspect/doctor` 只是管理操作，不算使用领域 Skill；只有 `load` 会触发 Skill 调用事件。
依赖 Skill 由同一次加载解析并附带，不为每个依赖重复发主提示。

## 5. 调用与可见性协议

显式斜杠命令和模型自主调用进入同一条加载路径：

```text
请求 active_skills=["docs-tool"]
  -> emit skill(start): 当前调用 Skill：docs-tool
  -> Manager.Load + 依赖校验
  -> emit skill(ready|error)
  -> ready 后才把正文加入 LLM 上下文
```

硬性时序是不允许 Skill 正文先进入模型、可见事件后补。单回合按主 Skill 名称去重。

SSE 事件：

```json
{
  "type": "skill",
  "skill_name": "docs-tool",
  "skill_status": "start|ready|error",
  "skill_scope": "project_managed|project_standard|global_managed|user_standard",
  "skill_source": ".../SKILL.md",
  "skill_dependencies": ["shared-auth"],
  "skill_error": ""
}
```

Assistant `parts` 新增 `kind: "skill"` 并随消息元数据持久化。GUI 与 CLI 均渲染
`当前调用 Skill：<name>`；历史回看仍可见。结构化事件是真源，不要求模型自己复述。

## 6. 外部能力提供方完成判定

外部 CLI/工具仅作为 Skill 包的生产者，统一按下面的契约接入：

1. 外部工具把完整 Skill 包导出到标准 `.agents/skills/<name>/`，或一个可定位的私有目录。
2. 标准目录由 P-Chat 直接发现；私有目录通过
   `skill_manage(action=import, source_path=<导出目录>)` 导入。
3. `source_path` 指向含 `SKILL.md` 的目录时按单包处理；否则按集合处理，每个直接子目录为
   一个 Skill 包。集合省略 `name` 时导入全部，指定名称时自动补齐集合内声明依赖。
4. 校验 frontmatter、依赖 Skill、所需二进制和包内资源，重新扫描后确认目标包生效。
5. doctor 无阻断错误后才向用户报告安装成功，并显示实际安装路径。

P-Chat 核心不调用供应商 CLI、不解析供应商私有协议，也不为单个产品增加 `source_cli` 分支。
集合内所有包先 staging，再整体发布和验证，任一失败统一回滚；安装阶段不执行 Skill 脚本。
发布/验证期间用进程级读写锁隔离，确保并发 `Catalog` / `Load` 只能看到事务前或事务后的
完整状态。

## 7. 分阶段落地

1. Manager 与目录发现、frontmatter、依赖/资源读取测试。
2. 完整目录导入、原子发布、安全校验与 doctor。
3. `skill` / `skill_manage` 工具及宿主路径自描述。
4. `active_skills`、Skill SSE、parts 持久化、GUI/CLI 展示。
5. 斜杠命令改用名称激活，保留旧 `skill_context` 兼容。
6. 更新项目文档，完成 Go、TypeScript、前端构建与 review。

本次不新增 SQLite 列，也不新增运行时数据目录格式版本；`parts` 沿用现有 metadata JSON，
因此无需 schema/upgrade 迁移。未来若引入 `skills.lock.json`，必须通过
`internal/upgrade` 注册幂等升级步骤。
