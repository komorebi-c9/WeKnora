/**
 * Orion 凭据可能由门户以 URL 参数的形式挂在落地地址上：
 *   - 会话凭据：`?NS-TOKEN=...`（Orion 控制台生成的链接就是这个形态）
 *   - License SK：`?sk=sk-...` / `?NS-APIKEY=sk-...` / `?NS-SK=sk-...`
 *     （Orion 正在把 SK 的请求头从 NS-APIKEY 改名为 NS-SK，两种名字都认）
 * Orion 自己的提取顺序就是 Header → Query → Cookie。地址栏会把它带进历史记录、
 * Referer 与截图，所以这里在首次读取时立刻从 URL 抹掉，并只保存在内存里。
 *
 * 只保存在内存是刻意的：刷新页面后凭据消失，用户需要重新从 Orion 进入，
 * 而不是让一串长期凭据留在 localStorage 里。
 */
const ORION_TOKEN_KEYS = ['NS-TOKEN', 'ns_token', 'nsToken']
const ORION_API_KEY_KEYS = [
  'NS-APIKEY',
  'ns_apikey',
  // Orion 正在把 SK 的请求头从 NS-APIKEY 改名为 NS-SK；跳转链接若改用新名字
  // 也必须能识别，否则用户会落在"请从 Orion 重新进入"的页面上。
  'NS-SK',
  'ns_sk',
  'sk',
  'apiKey',
]

/** 落地地址上所有需要摘除的 Orion 凭据参数名。 */
export const ORION_CREDENTIAL_QUERY_KEYS = [...ORION_TOKEN_KEYS, ...ORION_API_KEY_KEYS]

/** Orion 凭据：会话 NS-TOKEN 与 License SK 二选一（或同时出现，由后端优先取 token）。 */
export interface OrionCredential {
  /** NS-TOKEN 会话凭据。 */
  token?: string
  /** License SK（sk-...）。 */
  apiKey?: string
}

let consumed: OrionCredential = {}
let read = false

function takeFirst(params: URLSearchParams, keys: string[]): string | undefined {
  let found: string | undefined
  for (const key of keys) {
    const value = params.get(key)
    if (!value) continue
    if (found === undefined) found = value
    params.delete(key)
  }
  return found
}

/**
 * 取出并清除地址栏里的 Orion 凭据（幂等，只读一次地址栏）。
 *
 * token 与 SK 各自解析：两者都保留下来交给后端决定优先级，前端不做取舍——
 * 后端在完全没有 token 时才使用 SK。
 */
export function consumeOrionUrlCredential(): OrionCredential {
  if (read) return consumed
  read = true
  if (typeof window === 'undefined') return consumed

  let url: URL
  try {
    url = new URL(window.location.href)
  } catch {
    return consumed
  }

  const token = takeFirst(url.searchParams, ORION_TOKEN_KEYS)
  const apiKey = takeFirst(url.searchParams, ORION_API_KEY_KEYS)

  // 少数入口把凭据放在 hash 里（与 OIDC 回跳同一习惯）。
  const rawHash = url.hash.startsWith('#') ? url.hash.slice(1) : url.hash
  let hashToken: string | undefined
  let hashApiKey: string | undefined
  if (rawHash) {
    const hashParams = new URLSearchParams(rawHash)
    hashToken = takeFirst(hashParams, ORION_TOKEN_KEYS)
    hashApiKey = takeFirst(hashParams, ORION_API_KEY_KEYS)
    const rest = hashParams.toString()
    if (rest !== rawHash) {
      url.hash = rest ? `#${rest}` : ''
    }
  }

  const resolvedToken = token ?? hashToken
  const resolvedApiKey = apiKey ?? hashApiKey
  const credential: OrionCredential = {}
  if (resolvedToken) credential.token = resolvedToken
  if (resolvedApiKey) credential.apiKey = resolvedApiKey

  if (resolvedToken || resolvedApiKey) {
    // 立刻把凭据从地址栏抹掉，避免它继续出现在历史记录与 Referer 里。
    window.history.replaceState(
      window.history.state,
      '',
      `${url.pathname}${url.search}${url.hash}`,
    )
  }

  consumed = credential
  return consumed
}

/** 本次页面加载期间已知的凭据（可能来自地址栏，也可能为空）。 */
export function consumedOrionCredential(): OrionCredential {
  return consumed
}
