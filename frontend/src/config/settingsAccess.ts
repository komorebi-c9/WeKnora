export type SettingsRoleKey = 'viewer' | 'contributor' | 'admin' | 'owner'

/**
 * Workspace-scoped settings access policy.
 *
 * Keep this as the single frontend source of truth for both the complete
 * Settings navigation and any shortcuts that lead into it. Backend route
 * guards remain authoritative.
 */
export const SETTINGS_SECTION_MIN_ROLE: Record<string, SettingsRoleKey> = {
  general: 'viewer',
  ollama: 'admin',
  weknoracloud: 'admin',
  models: 'viewer',
  websearch: 'admin',
  chathistory: 'admin',
  vectorstore: 'admin',
  parser: 'admin',
  storage: 'admin',
  sandbox: 'admin',
  // Install writes a root shell into the sandbox image every session of
  // that config boots. Same Admin+ bar as the sandbox editor itself.
  skills: 'admin',
  mcp: 'admin',
  system: 'viewer',
  userprofile: 'viewer',
  browserconnection: 'viewer',
  tenant: 'viewer',
  members: 'viewer',
  mymemory: 'viewer',
  memory: 'admin',
  // Every member fills in their own environment variables; the workspace-wide
  // values stay on the Admin+ skills page.
  envvars: 'viewer',
}

/**
 * A management-labelled avatar shortcut has a stricter threshold than the
 * corresponding read-only Settings page.
 */
export const SETTINGS_MANAGEMENT_SHORTCUT_MIN_ROLE = {
  members: 'owner',
  models: 'admin',
} as const satisfies Record<string, SettingsRoleKey>

export const SYSTEM_ADMIN_SETTINGS_SECTIONS = new Set([
  'system-global',
  'model-catalog',
  'runtime-queues',
  'platform-api-keys',
  'system-audit-log',
])

/**
 * 暂时隐藏的「全部设置」入口。想不显示哪个就往这里加哪个，删掉即恢复。
 *
 * key 分两类（key 与菜单的对应关系见 Settings.vue）：
 *   一、隐藏整个分组 → 填「分组 key」；
 *   二、只隐藏组里的某几项 → 填「分区 key」（即下表各组列出的项）。
 * 命中即不渲染；某个分组内的条目被全部隐藏时，该分组标题也会一起消失。
 *
 * 分组与分区对照（分组 key → 中文名；缩进行是组内的分区 key → 中文名）：
 *
 *   account  账户
 *     general 常规设置、userprofile 用户信息、mymemory 我的记忆、
 *     envvars 环境变量（Lite 部署显示为「沙箱密钥」）
 *   workspace  空间
 *     tenant 空间信息、members 成员管理、chathistory 消息管理、memory 长期记忆
 *   models_runtime  模型
 *     models 模型管理、ollama Ollama、weknoracloud WeKnora Cloud
 *   integrations  发布集成
 *     integration-im IM 集成、integration-embed 网页嵌入、integration-api API 集成、
 *     integration-mcpserver MCP Server、integration-cli CLI、
 *     integration-chrome Chrome 插件、integration-claw Claw Skill
 *   data_extensions  数据与扩展
 *     vectorstore 向量数据库引擎、parser 解析引擎、storage 存储引擎、
 *     sandbox 沙箱配置、websearch 网络搜索
 *   system_administration  系统管理
 *     system-global 系统设置、model-catalog 模型目录、runtime-queues 任务队列、
 *     platform-api-keys 平台 API Key、system-audit-log 审计日志
 *   platform  平台
 *     system 版本信息
 *
 * 配置示例：
 *   new Set(['integrations'])
 *     → 整组隐藏「发布集成」
 *   new Set(['integration-mcpserver', 'integration-api'])
 *     → 只隐藏「发布集成」组里的 MCP Server 和 API 集成两项
 *   new Set(['integrations', 'models_runtime', 'storage'])
 *     → 分组 key 与分区 key 可以混着写
 *
 * 纯展示开关，不改后端路由鉴权；带 ?section= 的深链接仍能直接打开被隐藏的分区。
 */
export const HIDDEN_SETTINGS_NAV_KEYS = new Set<string>([
  // 发布集成（整组）：IM 集成 / 网页嵌入 / API 集成 / MCP Server / CLI / Chrome 插件 / Claw Skill
  // 'integrations',
  'weknoracloud',
])
