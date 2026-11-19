import { createRouter, createWebHistory } from 'vue-router'
import { defineComponent } from 'vue'
import type { RouteLocationNormalized } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useDeploymentCapabilitiesStore } from '@/stores/deploymentCapabilities'
import { useSettingsStore } from '@/stores/settings'
import { autoSetup, exchangeOrionToken, getAuthConfig } from '@/api/auth'
import type { AuthConfigResponse } from '@/api/auth'
import { persistLoginResponse } from '@/utils/authSession'
import { consumeOrionUrlAssertion, consumedOrionAssertion } from '@/utils/orionAssertion'
import type { DeploymentCapabilityKey } from '@/config/deploymentCapabilities'
import { MessagePlugin } from 'tdesign-vue-next'
import i18n from '@/i18n'
import { normalizeSettingsSection } from '@/config/settingsRoute'
import { isToolboxSection, toolboxLocation } from '@/config/toolbox'

/** Lite /桌面 WebView 硬刷新时可能只打开 `/`，用 session 记住上次页面以便恢复 */
const LITE_LAST_PATH_KEY = 'weknora_lite_last_path'

// views/platform/index.vue always mounts the settings modal and opens it when
// the path is /platform/settings, so this route only has to own the URL.
// Rendering Settings.vue here would mount a second, independent copy.
const SettingsRouteOutlet = defineComponent({ name: 'SettingsRouteOutlet', render: () => null })

function isLiteEdition(authStore: ReturnType<typeof useAuthStore>) {
  return authStore.isLiteMode || localStorage.getItem('weknora_lite_mode') === 'true'
}

function isLiteSpaDefaultEntry(to: RouteLocationNormalized) {
  return (
    to.path === '/' ||
    to.path === '/platform' ||
    to.path === '/platform/knowledge-bases' ||
    to.name === 'knowledgeBaseList'
  )
}

function isSafeLiteRestoreTarget(path: string) {
  return path.startsWith('/platform/') && !path.startsWith('/platform/organizations')
}

function hasPendingOIDCCallback() {
  if (typeof window === 'undefined') return false
  const hash = window.location.hash || ''
  return hash.includes('oidc_result=') || hash.includes('oidc_error=')
}

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: "/",
      redirect: "/platform/knowledge-bases",
    },
    {
      path: "/login",
      name: "login",
      component: () => import("../views/auth/Login.vue"),
      meta: { requiresAuth: false, requiresInit: false }
    },
    // Embed chat is a separate entry (embed.html + embed-main.ts), not this SPA.
    {
      path: "/register",
      name: "registerByInvite",
      // Share-link landing page reuses the Login form: the same Vue
      // component renders both modes and detects ?token=xxx on mount
      // to switch into invite-register flow. Avoids a parallel page
      // that would duplicate the OIDC / language-switch / styling
      // surface for one extra field.
      component: () => import("../views/auth/Login.vue"),
      meta: { requiresAuth: false, requiresInit: false }
    },
    {
      // Landing page for visitors without a usable Orion assertion. It is the
      // only entry surface when orion_auth.login_fallback is off: there is no
      // login form and no registration link on it.
      path: "/entry-required",
      name: "orionEntryRequired",
      component: () => import("../views/auth/OrionEntryRequired.vue"),
      meta: { requiresAuth: false, requiresInit: false }
    },
    {
      path: "/onboarding/workspace",
      name: "workspaceOnboarding",
      component: () => import("../views/auth/WorkspaceOnboarding.vue"),
      meta: { requiresAuth: true, requiresInit: false, requiresTenant: false }
    },
    {
      path: "/join",
      name: "joinOrganization",
      // 重定向到组织列表页，并将 code 参数转换为 invite_code
      redirect: (to) => {
        const code = to.query.code as string
        return {
          path: '/platform/organizations',
          query: code ? { invite_code: code } : {}
        }
      },
      meta: { requiresInit: true, requiresAuth: true }
    },
    {
      path: "/knowledgeBase",
      name: "home",
      component: () => import("../views/knowledge/KnowledgeBase.vue"),
      meta: { requiresInit: true, requiresAuth: true }
    },
    {
      path: "/platform",
      name: "Platform",
      redirect: "/platform/knowledge-bases",
      component: () => import("../views/platform/index.vue"),
      meta: { requiresInit: true, requiresAuth: true },
      children: [
        {
          path: "tenant",
          redirect: "/platform/settings"
        },
        {
          path: "settings",
          name: "settings",
          component: SettingsRouteOutlet,
          meta: { requiresInit: true, requiresAuth: true }
        },
        {
          path: "knowledge-bases",
          name: "knowledgeBaseList",
          component: () => import("../views/knowledge/KnowledgeBaseList.vue"),
          meta: { requiresInit: true, requiresAuth: true }
        },
        {
          path: "knowledge-bases/:kbId",
          name: "knowledgeBaseDetail",
          component: () => import("../views/knowledge/KnowledgeBase.vue"),
          meta: { requiresInit: true, requiresAuth: true }
        },
        {
          path: "knowledge-search",
          // 旧路径保留为重定向，打开全局命令面板（⌘K），带上可选的 q 参数
          redirect: (to) => {
            const q = to.query.q
            return {
              path: '/platform/knowledge-bases',
              query: typeof q === 'string' ? { cmdk: q } : { cmdk: '' },
            }
          },
        },
        {
          path: "artifacts",
          name: "artifactLibrary",
          component: () => import("../views/artifacts/ArtifactLibrary.vue"),
          meta: { requiresInit: true, requiresAuth: true, requiredCapability: 'settings.sandbox' }
        },
        {
          path: "toolbox/:section?",
          name: "toolbox",
          component: () => import("../views/toolbox/Toolbox.vue"),
          meta: { requiresInit: true, requiresAuth: true }
        },
        {
          path: "automations/:automationId?",
          name: "automationList",
          component: () => import("../views/automation/AutomationList.vue"),
          meta: { requiresInit: true, requiresAuth: true }
        },
        {
          path: "agents",
          name: "agentList",
          component: () => import("../views/agent/AgentList.vue"),
          meta: { requiresInit: true, requiresAuth: true, requiredCapability: 'agents' }
        },
        {
          path: "integrations",
          redirect: (to) => {
            const tab = typeof to.query.tab === 'string' ? to.query.tab : undefined
            const incoming = typeof to.query.section === 'string' ? to.query.section : 'integrations'
            const rest = { ...to.query }
            delete rest.tab
            return {
              path: '/platform/settings',
              query: {
                ...rest,
                section: normalizeSettingsSection(incoming, tab),
              },
            }
          },
          meta: { requiresInit: true, requiresAuth: true }
        },
        {
          path: "creatChat",
          name: "globalCreatChat",
          component: () => import("../views/creatChat/creatChat.vue"),
          meta: { requiresInit: true, requiresAuth: true }
        },
        {
          path: "knowledge-bases/:kbId/creatChat",
          name: "kbCreatChat",
          component: () => import("../views/creatChat/creatChat.vue"),
          meta: { requiresInit: true, requiresAuth: true }
        },
        {
          path: "chat/:chatid",
          name: "chat",
          component: () => import("../views/chat/index.vue"),
          meta: { requiresInit: true, requiresAuth: true }
        },
        {
          path: "organizations",
          name: "organizationList",
          component: () => import("../views/organization/OrganizationList.vue"),
          meta: { requiresInit: true, requiresAuth: true, requiredCapability: 'organizations' }
        },
        // Compatibility redirects for /platform/system/* URLs. System
        // administration surfaces live as dedicated sections inside the
        // standard Settings modal; keep stable URLs for bookmarks and
        // external links.
        {
          path: "system",
          redirect: { path: "/platform/settings", query: { section: "system-global" } },
          meta: { requiresInit: true, requiresAuth: true, requiresSystemAdmin: true },
        },
        {
          path: "system/settings",
          name: "systemSettings",
          redirect: { path: "/platform/settings", query: { section: "system-global" } },
          meta: { requiresInit: true, requiresAuth: true, requiresSystemAdmin: true },
        },
        {
          path: "system/admins",
          name: "systemAdmins",
          redirect: { path: "/platform/settings", query: { section: "system-global" } },
          meta: { requiresInit: true, requiresAuth: true, requiresSystemAdmin: true },
        },
        {
          path: "system/queues",
          name: "systemQueues",
          redirect: { path: "/platform/settings", query: { section: "runtime-queues" } },
          meta: { requiresInit: true, requiresAuth: true, requiresSystemAdmin: true },
        },
      ],
    },
    // Dev-only markdown rendering test page
    ...(import.meta.env.DEV ? [{
      path: '/platform/dev/markdown',
      name: 'markdownTest',
      component: () => import('../views/dev/MarkdownTestPage.vue'),
      meta: { requiresAuth: false, requiresInit: false }
    }] : []),
  ],
});

async function hydrateSessionFromToken(authStore: ReturnType<typeof useAuthStore>) {
  const token = localStorage.getItem('weknora_token')
  if (!token) return false

  if (!authStore.token) {
    authStore.setToken(token)
  }

  const storedRefreshToken = localStorage.getItem('weknora_refresh_token')
  if (storedRefreshToken && !authStore.refreshToken) {
    authStore.setRefreshToken(storedRefreshToken)
  }

  // /auth/me 的落库逻辑只在 auth store 里维护一份（user / tenant / memberships /
  // capabilities），这里只负责先把 token 放进 store。请求本身与启动、侧栏共用去重。
  return authStore.refreshFromAuthMe()
}

// Orion SSO entry resolution.
//
// The exchange runs at most once per page load: it is the only way to obtain a
// session in SSO-only deployments, and retrying it on every guard run would
// hammer Orion after a failure.
let orionExchangeAttempted = false
let orionResyncDone = false
let authEntryConfig: AuthConfigResponse | null = null

async function loadAuthEntryConfig(): Promise<AuthConfigResponse> {
  if (!authEntryConfig) {
    authEntryConfig = await getAuthConfig()
  }
  return authEntryConfig
}

/** Where an unauthenticated visitor belongs: the local login page, or the hint. */
async function resolveEntryRoute(): Promise<string> {
  const config = await loadAuthEntryConfig()
  return config.login_fallback_enabled === false ? '/entry-required' : '/login'
}

/** True when the local login/registration surfaces are supposed to be absent. */
async function isLocalLoginDisabled(): Promise<boolean> {
  const config = await loadAuthEntryConfig()
  return config.login_fallback_enabled === false
}

/**
 * Try to turn an Orion assertion into a WeKnora session. Returns true when the
 * store now holds a valid session. Never throws: a failure simply means the
 * visitor continues to the entry-resolution branch.
 */
async function exchangeOrionOnce(authStore: ReturnType<typeof useAuthStore>): Promise<boolean> {
  if (orionExchangeAttempted) return false
  const config = await loadAuthEntryConfig()
  if (!config.orion_sso_enabled) return false
  // Mark before awaiting so a concurrent guard run cannot start a second call.
  orionExchangeAttempted = true
  try {
    // The assertion may come from the landing URL (?NS-TOKEN=...), which is the
    // form the Orion console produces. It was already stripped from the address
    // bar by consumeOrionUrlAssertion(); forward it as a header so it is not
    // written to any access log on the way to the API.
    const response = await exchangeOrionToken(consumedOrionAssertion())
    if (!response?.success || !response.token) return false
    persistLoginResponse(authStore, response)
    return true
  } catch {
    return false
  }
}

/**
 * 同浏览器里换掉 Orion 账号后，WeKnora 的旧会话（24h）仍然有效，界面会继续
 * 显示上一个人。这里在每次页面加载时静默做一次换取：Orion 有会话就以后者为准，
 * 覆盖本地会话；没有 Orion 会话（换取失败）时保持原会话不动，不影响离线可用性。
 */
async function resyncWithOrion(authStore: ReturnType<typeof useAuthStore>): Promise<void> {
  const config = await loadAuthEntryConfig()
  if (!config.orion_sso_enabled) return
  try {
    const response = await exchangeOrionToken(consumedOrionAssertion())
    if (response?.success && response.token) {
      const previousUserId = authStore.user?.id
      if (previousUserId && response.user && response.user.id !== previousUserId) {
        // 换了人：先清掉上一位用户的缓存（知识库列表、租户选择等），
        // 否则界面会继续显示上一个账号的数据。
        authStore.logout()
      }
      persistLoginResponse(authStore, response)
    }
  } catch {
    // 没有可用的 Orion 会话时保留现有登录态。
  }
}

/** Route target with the Orion assertion removed from the query string. */
function targetWithoutOrionAssertion(to: RouteLocationNormalized) {
  const query = { ...to.query }
  let changed = false
  for (const key of ['NS-TOKEN', 'ns_token', 'nsToken']) {
    if (key in query) {
      delete query[key]
      changed = true
    }
  }
  return changed ? { path: to.path, query, hash: to.hash } : to.fullPath
}

let autoSetupAttempted = false
let liteDeepLinkRestoreDone = false

// 路由守卫：检查认证状态和系统初始化状态
router.beforeEach(async (to, from, next) => {
  const authStore = useAuthStore()

  // 落地地址可能带 ?NS-TOKEN=...（Orion 控制台生成的链接就是这个形态）。
  // 尽早读一次：不管后面走哪条分支，都先把凭据从地址栏抹掉。
  consumeOrionUrlAssertion()
  // 已经确认过的导航仍会把 query 写回地址栏，所以这里主动替换成去掉凭据的
  // 目标地址；守卫会为这个干净地址再跑一次（凭据已缓存在内存里）。
  const cleanTarget = targetWithoutOrionAssertion(to)
  if (typeof cleanTarget !== 'string') {
    next({ ...cleanTarget, replace: true })
    return
  }

  // 每次页面加载与 Orion 对齐一次身份（仅在本地已有会话时）。
  // 这让"在 Orion 里换了账号再点进来"立刻生效，而不是继续显示上一个人。
  if (!orionResyncDone && localStorage.getItem('weknora_token')) {
    orionResyncDone = true
    await resyncWithOrion(authStore)
  }

  // OIDC 回跳登录结果依赖 App.vue 在挂载后消费 URL hash。
  // 如果这里先按“未登录”拦截到 /login，会导致回调结果没有机会落盘。
  if (hasPendingOIDCCallback()) {
    next()
    return
  }

  // Preserve bookmarks for tools that have moved out of Settings.
  if (to.path === '/platform/settings' && isToolboxSection(to.query.section)) {
    next({ ...toolboxLocation(to.query.section,
      typeof to.query.sandboxId === 'string' ? to.query.sandboxId : undefined), replace: true })
    return
  }

  // Lite：硬刷新后若落在默认首页，恢复本次会话中最后访问的 /platform 子路径
  if (!liteDeepLinkRestoreDone) {
    liteDeepLinkRestoreDone = true
    if (isLiteEdition(authStore)) {
      const saved = sessionStorage.getItem(LITE_LAST_PATH_KEY)
      if (saved && isSafeLiteRestoreTarget(saved) && isLiteSpaDefaultEntry(to)) {
        if (saved !== to.fullPath) {
          next(saved)
          return
        }
      }
    }
  }

  // Tenantless onboarding still requires a valid user token even though it
  // deliberately skips the normal tenant/system-initialization gates.
  if (to.path === '/onboarding/workspace') {
    if (!authStore.isLoggedIn) {
      const restored = await hydrateSessionFromToken(authStore) || await exchangeOrionOnce(authStore)
      if (!restored) {
        next(await resolveEntryRoute())
        return
      }
    }
    if (authStore.hasValidTenant) {
      next('/platform/knowledge-bases')
    } else {
      next()
    }
    return
  }

  // 如果访问的是登录页面或初始化页面，直接放行
  if (to.meta.requiresAuth === false || to.meta.requiresInit === false) {
    // 如果已登录用户访问登录页面，重定向到知识库列表页面
    if (to.path === '/login' && authStore.isLoggedIn) {
      next(authStore.hasValidTenant ? '/platform/knowledge-bases' : '/onboarding/workspace')
      return
    }
    // SSO-only：登录页与注册页不存在。即便用户手输地址或从收藏夹进入，
    // 也只展示“重新从 Orion 系统跳转”，不给任何本地凭据入口。
    if ((to.path === '/login' || to.path === '/register') && await isLocalLoginDisabled()) {
      next('/entry-required')
      return
    }
    next()
    return
  }

  // 检查用户认证状态
  if (to.meta.requiresAuth !== false) {
    if (!authStore.isLoggedIn) {
      const restored = await hydrateSessionFromToken(authStore)
      if (restored) {
        next(
          !authStore.hasValidTenant && to.meta.requiresTenant !== false
            ? '/onboarding/workspace'
            : targetWithoutOrionAssertion(to),
        )
        return
      }

      // Orion SSO: the visitor arrives with an Orion session cookie and no
      // WeKnora token. One exchange attempt is enough to mint a local session;
      // afterwards the SPA behaves exactly like a password login.
      if (await exchangeOrionOnce(authStore)) {
        next(
          !authStore.hasValidTenant && to.meta.requiresTenant !== false
            ? '/onboarding/workspace'
            : targetWithoutOrionAssertion(to),
        )
        return
      }

      if (!autoSetupAttempted) {
        autoSetupAttempted = true
        localStorage.removeItem('weknora_auto_setup_failed')
        try {
          const response = await autoSetup()
          if (response.success) {
            persistLoginResponse(authStore, response)
            authStore.setLiteMode(true)
            next(to.fullPath)
            return
          }
        } catch {
          // Auto-setup may be unavailable outside the native Lite shell.
        }
      }
      next(await resolveEntryRoute())
      return
    }
  }

  if (to.meta.requiresTenant !== false && !authStore.hasValidTenant) {
    next('/onboarding/workspace')
    return
  }

  // 部署能力只描述“后端是否提供该功能”，不反映服务健康或是否已配置。
  // 探测失败时 Store 会 fail-open，真正的权限和可用性仍由后端接口校验。
  const deploymentCapabilities = useDeploymentCapabilitiesStore()
  await deploymentCapabilities.ensureLoaded()
  const requiredCapability = to.meta.requiredCapability as DeploymentCapabilityKey | undefined
  if (requiredCapability && !deploymentCapabilities.isSupported(requiredCapability)) {
    MessagePlugin.warning(i18n.global.t('settings.capabilityUnavailable'))
    next('/platform/knowledge-bases')
    return
  }

  // SystemAdmin gate — checked AFTER auth so a non-admin who's logged
  // out gets redirected to /login first (consistent with how the rest
  // of the auth flow works), and only an authenticated non-admin sees
  // the bounce. This is UI-only; the server enforces the real check.
  if (to.meta.requiresSystemAdmin === true) {
    if (!authStore.isSystemAdmin) {
      next('/platform/knowledge-bases')
      return
    }
  }

  next()
})

router.beforeResolve((to, from) => {
  if ((to.name === 'globalCreatChat' || to.name === 'kbCreatChat') && to.fullPath !== from.fullPath) {
    const settingsStore = useSettingsStore()
    const references = settingsStore.consumeNewChatReferences()
    if (references) settingsStore.setNewChatReferences(references.kbIds, references.fileIds)
    else settingsStore.clearChatReferences()
  }
})

router.afterEach((to) => {
  if (!isLiteEdition(useAuthStore())) return
  if (to.path === '/login') return
  if (!to.path.startsWith('/platform')) return
  sessionStorage.setItem(LITE_LAST_PATH_KEY, to.fullPath)
})

export default router
