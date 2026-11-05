/**
 * Global command palette entries. Pure data so the shortcut
 * and filter can be tested without mounting the shell.
 */

export interface GlobalCommand {
  id: string
  label: string
  hint: string
  keywords: string
  enabled: boolean
}

export interface GlobalCommandContext {
  sidebarCollapsed: boolean
  inspectorOpen: boolean
  theme: 'dark' | 'light'
  continueTitle: string | null
}

export function isCommandPaletteShortcut(event: {
  key: string
  ctrlKey: boolean
  metaKey: boolean
  altKey: boolean
  shiftKey: boolean
  isComposing?: boolean
}): boolean {
  if (event.isComposing) return false
  if (event.altKey || event.shiftKey) return false
  if (!event.ctrlKey && !event.metaKey) return false
  return event.key === 'k' || event.key === 'K'
}

export function buildGlobalCommands(ctx: GlobalCommandContext): GlobalCommand[] {
  const continueTitle = (ctx.continueTitle || '').trim()
  return [
    {
      id: 'new-chat',
      label: '新建对话',
      hint: '在当前项目打开空白草稿',
      keywords: 'new chat session 新建 对话',
      enabled: true,
    },
    {
      id: 'continue',
      label: continueTitle ? `继续：${continueTitle}` : '继续上次',
      hint: continueTitle ? '回到当前项目最近一条对话' : '当前项目没有更早的对话',
      keywords: 'continue last 继续 上次',
      enabled: !!continueTitle,
    },
    {
      id: 'projects',
      label: '切换项目',
      hint: '打开项目列表',
      keywords: 'project switch 项目 切换',
      enabled: true,
    },
    {
      id: 'add-project',
      label: '新建项目',
      hint: '登记一个本地目录',
      keywords: 'add project folder 新建 项目',
      enabled: true,
    },
    {
      id: 'sidebar',
      label: ctx.sidebarCollapsed ? '展开侧栏' : '折叠侧栏',
      hint: '会话列表',
      keywords: 'sidebar 侧栏 会话列表',
      enabled: true,
    },
    {
      id: 'inspector',
      label: ctx.inspectorOpen ? '折叠检查器' : '展开检查器',
      hint: '右侧会话与项目面板',
      keywords: 'inspector panel 检查器',
      enabled: true,
    },
    {
      id: 'theme',
      label: ctx.theme === 'dark' ? '切换到浅色' : '切换到深色',
      hint: '主题',
      keywords: 'theme dark light 主题 浅色 深色',
      enabled: true,
    },
  ]
}

export function filterGlobalCommands(commands: GlobalCommand[], query: string): GlobalCommand[] {
  const q = query.trim().toLowerCase()
  if (!q) return commands
  return commands.filter(command =>
    command.label.toLowerCase().includes(q)
    || command.hint.toLowerCase().includes(q)
    || command.keywords.toLowerCase().includes(q),
  )
}
