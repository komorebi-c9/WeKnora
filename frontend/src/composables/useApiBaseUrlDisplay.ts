import { computed, onMounted, ref } from 'vue'
import { getApiBaseUrl } from '@/utils/api-base'

type WeKnoraDesktopWindow = Window & {
  __WEKNORA_API_BASE__?: string
  go?: {
    main?: {
      App?: {
        GetAPIBaseURL?: () => Promise<string> | string
      }
    }
  }
}

export function useApiBaseUrlDisplay() {
  const wailsApiBaseURL = ref<string | null>(null)

  const apiBaseUrlDisplay = computed(() => {
    if (wailsApiBaseURL.value) {
      return wailsApiBaseURL.value
    }
    const configured = getApiBaseUrl().trim().replace(/\/$/, '')
    let origin = typeof window !== 'undefined' ? window.location.origin : ''
    if (!origin || origin === 'null') {
      origin = ''
    }
    // 子路径部署（如 VITE_BASE_PATH=/lolr-kb/）时 configured 是路径而不是完整
    // URL；必须锚定到当前 origin，否则 MCP/Chrome 等集成片段会拿到
    // "/lolr-kb/mcp/..." 这种没有 host 的相对地址。
    const base = configured
      ? (configured.startsWith('/') ? `${origin}${configured}` : configured)
      : origin
    return `${base}/api/v1`
  })

  async function loadApiBaseUrl() {
    const win = window as WeKnoraDesktopWindow
    for (let i = 0; i < 40; i++) {
      const injected = win.__WEKNORA_API_BASE__
      if (typeof injected === 'string' && injected.trim()) {
        wailsApiBaseURL.value = injected.trim().replace(/\/$/, '')
        return
      }
      const fn = win.go?.main?.App?.GetAPIBaseURL
      if (typeof fn === 'function') {
        try {
          const raw = await Promise.resolve(fn())
          if (typeof raw === 'string' && raw.trim()) {
            wailsApiBaseURL.value = raw.trim().replace(/\/$/, '')
            return
          }
        } catch {
          /* binding error */
        }
      }
      await new Promise<void>((resolve) => setTimeout(resolve, 50))
    }
  }

  onMounted(() => {
    void loadApiBaseUrl()
  })

  return { apiBaseUrlDisplay }
}
