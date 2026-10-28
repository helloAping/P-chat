/**
 * Delight-1 scene presets — shared by empty-chat guide cards
 * and composer scenario capsules. Keep copy short; cards only
 * prefill the composer (never auto-send).
 */

export type SceneId = 'coding' | 'plan' | 'daily'

export type WorkMode = 'coding' | 'daily'
export type TurnMode = 'build' | 'plan' | 'auto'

export interface SceneCapsule {
  id: SceneId
  label: string
  hint: string
  workMode: WorkMode
  turnMode: TurnMode
}

export interface EmptyGuide {
  id: string
  title: string
  description: string
  prompt: string
  scene: SceneId
}

export const SCENE_CAPSULES: SceneCapsule[] = [
  {
    id: 'coding',
    label: '写代码',
    hint: '编码模式 · 可改文件与跑命令',
    workMode: 'coding',
    turnMode: 'build',
  },
  {
    id: 'plan',
    label: '做计划',
    hint: '计划模式 · 先拆任务再动手',
    workMode: 'coding',
    turnMode: 'plan',
  },
  {
    id: 'daily',
    label: '闲聊',
    hint: '日常模式 · 少工具、偏对话',
    workMode: 'daily',
    turnMode: 'build',
  },
]

export const EMPTY_GUIDES: EmptyGuide[] = [
  {
    id: 'explain',
    title: '解释当前项目',
    description: '摸清结构、入口和怎么跑',
    prompt: '先浏览当前项目结构，用通俗的话说明这是什么项目、主要模块分别做什么，以及本地怎么启动或构建。',
    scene: 'coding',
  },
  {
    id: 'find-bug',
    title: '帮我找 bug',
    description: '定位可疑点并给修复建议',
    prompt: '检查当前项目里最近改过或容易出问题的地方，找出可疑 bug，说明复现思路，并给出具体修复建议。',
    scene: 'coding',
  },
  {
    id: 'write-test',
    title: '写个测试',
    description: '给核心逻辑补有意义的测例',
    prompt: '挑一个核心逻辑补上有意义的单元测试，说明覆盖了什么场景，以及怎么跑这些测试。',
    scene: 'coding',
  },
  {
    id: 'sketch',
    title: '生图试试',
    description: '先问风格，再生成一张概念图',
    prompt: '用图片生成能力画一张与当前项目气质相关的概念图。先简短问我风格偏好，确认后再生成。',
    scene: 'daily',
  },
]

export function sceneById(id: SceneId): SceneCapsule {
  return SCENE_CAPSULES.find(s => s.id === id) || SCENE_CAPSULES[0]
}

export function matchScene(
  workMode: string | undefined,
  turnMode: string | undefined,
): SceneId {
  if (workMode === 'daily') return 'daily'
  if (turnMode === 'plan') return 'plan'
  return 'coding'
}
