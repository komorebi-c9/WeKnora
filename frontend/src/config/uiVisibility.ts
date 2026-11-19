/**
 * 交付时的界面裁剪开关（纯展示开关，不改后端鉴权，不能当作权限使用）。
 *
 * 所有"隐藏某个入口"的配置都能在这一屏里找到落点：
 *
 *   ① 设置抽屉的菜单项
 *      → ./settingsAccess.ts 的 HIDDEN_SETTINGS_NAV_KEYS（本文件已 re-export）
 *      → 可填分组 key 隐藏整组，或分区 key 隐藏组内单项；对照表见该文件注释
 *
 *   ② 头像下拉菜单
 *      → ../components/UserMenu.vue 的 HIDDEN_USER_MENU_ITEMS
 *      → 可用 key：help（帮助与文档）、github（GitHub）、logout（退出登录）
 *
 *   ③ 左侧栏入口
 *      → 本文件的 HIDDEN_SIDEBAR_ITEMS
 *
 * 往对应集合里加 key 即隐藏，删掉即恢复。
 */

export { HIDDEN_SETTINGS_NAV_KEYS } from './settingsAccess.ts'

/** 左侧栏可隐藏的入口。 */
export const HIDDEN_SIDEBAR_ITEM_KEYS = ['search', 'assistant', 'sessions-empty-state'] as const

export type HiddenSidebarItemKey = (typeof HIDDEN_SIDEBAR_ITEM_KEYS)[number]

/**
 * 左侧栏要隐藏的入口：
 *   'search'               → 左上角搜索按钮、侧栏折叠后的搜索图标项，以及 ⌘K / Ctrl+K、"/" 快捷键
 *   'assistant'            → 左上角的 Lolr AI 助手入口
 *   'sessions-empty-state' → 会话列表为空时的「暂无对话」占位文案
 *
 * 深链 `?cmdk=` 不受影响（只藏入口，不封锁入口链接）。
 */
export const HIDDEN_SIDEBAR_ITEMS = new Set<HiddenSidebarItemKey>([
  'search',
  'assistant',
  'sessions-empty-state',
])

/**
 * 左侧栏的菜单项（按 path 匹配，定义见 ../stores/menu.ts 的 menuArr）。
 * 加进来即从侧栏移除，删掉即恢复；路由不受影响，直接访问地址仍可打开。
 *
 *   creatChat        新对话
 *   knowledge-bases  知识库
 *   artifacts        产物（仅当沙箱能力可用时出现）
 *   agents           智能体
 *   automations      自动化
 *   toolbox          工具箱
 *   organizations    共享空间（仅 admin/owner 可见）
 *   settings         系统设置
 *   logout           退出登录
 *
 * 当前隐藏：产物、智能体、工具箱（沿用既有默认），以及新对话、自动化。
 */
export const HIDDEN_SIDEBAR_MENU_PATHS = new Set<string>([
  'artifacts',
  'agents',
  'toolbox',
  'creatChat',
  'automations',
])
