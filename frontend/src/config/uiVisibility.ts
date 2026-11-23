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
 *      → 本文件的 HIDDEN_SIDEBAR_ITEMS / HIDDEN_SIDEBAR_MENU_PATHS
 *
 *   ④ 共享空间设置弹窗的分区菜单
 *      → 本文件的 HIDDEN_ORG_SETTINGS_SECTIONS
 *
 *   ⑤ 解析引擎设置的引擎卡片
 *      → 本文件的 HIDDEN_PARSER_ENGINE_NAMES
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

/**
 * 共享空间设置弹窗里可隐藏的分区（定义见
 * ../views/organization/OrganizationSettingsModal.vue 的 navItems / navGroups）。
 * 加进来即从弹窗左侧菜单移除，删掉即恢复；分区内容仍保留在模板里，
 * 只是入口不渲染（弹窗每次打开都重置回 basic，不存在深链直入被藏分区）。
 *
 *   sharedAgents → 共享智能体
 */
export const HIDDEN_ORG_SETTINGS_SECTION_KEYS = ['sharedAgents'] as const

export type HiddenOrgSettingsSectionKey = (typeof HIDDEN_ORG_SETTINGS_SECTION_KEYS)[number]

export const HIDDEN_ORG_SETTINGS_SECTIONS = new Set<HiddenOrgSettingsSectionKey>([
  'sharedAgents',
])

/**
 * 解析引擎设置（../views/settings/ParserEngineSettings.vue）里要隐藏的引擎卡片。
 * 引擎名取自后端引擎注册表（internal/infrastructure/docparser/engines.go）。
 * 加进来即不渲染该卡片，删掉即恢复；后端接口与知识库解析规则不受影响。
 *
 *   weknoracloud → Lolr Ai Cloud（云端文档解析，需 APPID/APPSECRET）
 */
export const HIDDEN_PARSER_ENGINE_NAMES = new Set<string>(['weknoracloud'])

/**
 * 空间「成员管理」页（../views/settings/TenantMembers.vue）可隐藏的操作按钮。
 * 加进来即从页头移除，删掉即恢复；纯展示开关，不改后端鉴权。
 *
 *   invite     → 邀请成员（邮箱邀请）
 *   share-link → 生成共享链接
 */
export const HIDDEN_TENANT_MEMBER_ACTION_KEYS = ['invite', 'share-link'] as const

export type HiddenTenantMemberActionKey = (typeof HIDDEN_TENANT_MEMBER_ACTION_KEYS)[number]

export const HIDDEN_TENANT_MEMBER_ACTIONS = new Set<HiddenTenantMemberActionKey>([
  // 'invite',
  // 'share-link',
])
