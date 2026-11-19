import { reactive, ref, computed, watch } from 'vue'
import { defineStore } from 'pinia'
import i18n from '@/i18n'
import { useAuthStore } from '@/stores/auth'
import { useDeploymentCapabilitiesStore } from '@/stores/deploymentCapabilities'
import type { DeploymentCapabilityKey } from '@/config/deploymentCapabilities'
import type { QuestionOrigin } from '@/utils/questionOrigin'
import { HIDDEN_SIDEBAR_MENU_PATHS } from '@/config/uiVisibility'

type MenuChild = Record<string, any>

interface MenuItem {
  title: string
  titleKey?: string
  icon: string
  path: string
  childrenPath?: string
  children?: MenuChild[]
  requiredCapability?: DeploymentCapabilityKey
}

const createMenuChildren = () => reactive<MenuChild[]>([])

export const useMenuStore = defineStore('menuStore', () => {
  const menuArr = reactive<MenuItem[]>([
    {
      title: '',
      titleKey: 'menu.newChat',
      icon: 'prefixIcon',
      path: 'creatChat',
      childrenPath: 'chat',
      children: createMenuChildren()
    },
    { title: '', titleKey: 'menu.knowledgeBase', icon: 'zhishiku', path: 'knowledge-bases' },
    // Artifacts only exist where skills run in a sandbox.
    { title: '', titleKey: 'menu.artifacts', icon: 'artifact', path: 'artifacts', requiredCapability: 'settings.sandbox' },
    { title: '', titleKey: 'menu.agents', icon: 'agent', path: 'agents', requiredCapability: 'agents' },
    { title: '', titleKey: 'menu.automations', icon: 'automation', path: 'automations' },
    { title: '', titleKey: 'toolbox.title', icon: 'toolbox', path: 'toolbox' },
    { title: '', titleKey: 'menu.organizations', icon: 'organization', path: 'organizations', requiredCapability: 'organizations' },
    { title: '', titleKey: 'menu.settings', icon: 'setting', path: 'settings' },
    { title: '', titleKey: 'menu.logout', icon: 'logout', path: 'logout' }
  ])

  const isFirstSession = ref(false)
  const firstQuery = ref('')
  const firstMentionedItems = ref<any[]>([])
  const firstModelId = ref('')
  const firstImageFiles = ref<any[]>([])
  const firstAttachmentFiles = ref<any[]>([])
  const firstQuestionOrigin = ref<QuestionOrigin | null>(null)
  const prefillQuery = ref('')

  const applyMenuTranslations = () => {
    menuArr.forEach(item => {
      if (item.titleKey) {
        item.title = i18n.global.t(item.titleKey)
      }
    })
  }

  applyMenuTranslations()

  watch(
    () => [
      i18n.global.locale.value,
      i18n.global.messages?.value?.[
        i18n.global.locale.value as keyof typeof i18n.global.messages.value
      ],
    ],
    () => {
      applyMenuTranslations()
    },
  )

  const liteHiddenPaths = new Set(['logout', 'organizations'])

  // 菜单全部可选项（上方 menuArr 的 path → 名称，i18n key 见注释）：
  //   creatChat       → 新对话（menu.newChat）
  //   knowledge-bases → 知识库（menu.knowledgeBase）
  //   artifacts       → 产物（menu.artifacts，仅 settings.sandbox 能力可用时才会出现）
  //   agents          → 智能体（menu.agents）
  //   toolbox         → 工具箱（toolbox.title）
  //   organizations   → 共享空间（menu.organizations）
  //   settings        → 系统设置（menu.settings）
  //   logout          → 退出登录（menu.logout）
  //
  // 「暂时隐藏」的菜单入口按 path 匹配，统一配置在 @/config/uiVisibility 的
  // HIDDEN_SIDEBAR_MENU_PATHS 里（同处还有侧栏搜索/助手入口与设置菜单的开关）。
  // 需要恢复某项时，从那个集合里删掉对应 path 即可。
  const temporarilyHiddenPaths = HIDDEN_SIDEBAR_MENU_PATHS

  // 共享空间 (organizations) 仅对当前空间的 admin / owner 暴露入口。
  // viewer / contributor 即便在共享空间里拥有资源，也无需自行管理共享关系，
  // 入口在侧栏只会徒增噪音；后端 RBAC 才是权限的最终来源（见 middleware/rbac.go）。
  const visibleMenuArr = computed(() => {
    const authStore = useAuthStore()
    const deploymentCapabilities = useDeploymentCapabilitiesStore()
    return menuArr.filter(item => {
      if (authStore.isLiteMode && liteHiddenPaths.has(item.path)) {
        return false
      }
      if (temporarilyHiddenPaths.has(item.path)) {
        return false
      }
      if (item.path === 'organizations' && !authStore.hasRole('admin')) {
        return false
      }
      if (!deploymentCapabilities.isSupported(item.requiredCapability)) {
        return false
      }
      return true
    })
  })

  const chatMenuIndex = menuArr.findIndex(item => item.path === 'creatChat')

  const clearMenuArr = () => {
    const chatMenu = menuArr[chatMenuIndex]
    if (chatMenu && chatMenu.children) {
      chatMenu.children = createMenuChildren()
    }
  }

  const updatemenuArr = (obj: any) => {
    const chatMenu = menuArr[chatMenuIndex]
    if (!chatMenu.children) {
      chatMenu.children = createMenuChildren()
    }
    const exists = chatMenu.children.some((item: MenuChild) => item.id === obj.id)
    if (!exists) {
      chatMenu.children.push(obj)
    }
  }

  const updataMenuChildren = (item: MenuChild) => {
    const chatMenu = menuArr[chatMenuIndex]
    if (!chatMenu.children) {
      chatMenu.children = createMenuChildren()
    }
    chatMenu.children.unshift(item)
  }

  const updatasessionTitle = (sessionId: string, title: string) => {
    const chatMenu = menuArr[chatMenuIndex]
    chatMenu.children?.forEach((item: MenuChild) => {
      if (item.id === sessionId) {
        item.title = title
        item.isNoTitle = false
      }
    })
  }

  const changeIsFirstSession = (payload: boolean) => {
    isFirstSession.value = payload
  }

  const changeFirstQuery = (payload: string, mentionedItems: any[] = [], modelId: string = '', imageFiles: any[] = [], attachmentFiles: any[] = [], questionOrigin: QuestionOrigin | null = null) => {
    firstQuery.value = payload
    firstMentionedItems.value = mentionedItems
    firstModelId.value = modelId
    firstImageFiles.value = imageFiles
    firstAttachmentFiles.value = attachmentFiles
    firstQuestionOrigin.value = questionOrigin
  }

  const setPrefillQuery = (q: string) => {
    prefillQuery.value = q
  }

  const consumePrefillQuery = () => {
    const q = prefillQuery.value
    prefillQuery.value = ''
    return q
  }

  return {
    menuArr,
    visibleMenuArr,
    isFirstSession,
    firstQuery,
    firstMentionedItems,
    firstModelId,
    firstImageFiles,
    firstAttachmentFiles,
    firstQuestionOrigin,
    prefillQuery,
    clearMenuArr,
    updatemenuArr,
    updataMenuChildren,
    updatasessionTitle,
    changeIsFirstSession,
    changeFirstQuery,
    setPrefillQuery,
    consumePrefillQuery
  }
})
