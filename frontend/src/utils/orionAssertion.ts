/**
 * Orion 凭据可能由门户以 `?NS-TOKEN=...` 的形式挂在落地地址上（Orion 自己的
 * 提取顺序就是 Header → Query → Cookie）。地址栏会把它带进历史记录、Referer
 * 与截图，所以这里在首次读取时立刻从 URL 抹掉，并只保存在内存里。
 *
 * 只保存在内存是刻意的：刷新页面后凭据消失，用户需要重新从 Orion 进入，
 * 而不是让一串长期凭据留在 localStorage 里。
 */
const ORION_TOKEN_KEYS = ['NS-TOKEN', 'ns_token', 'nsToken']

let consumed: string | undefined
let read = false

/** 取出并清除地址栏里的 Orion 凭据（幂等，只读一次地址栏）。 */
export function consumeOrionUrlAssertion(): string | undefined {
  if (read) return consumed
  read = true
  if (typeof window === 'undefined') return undefined

  let url: URL
  try {
    url = new URL(window.location.href)
  } catch {
    return undefined
  }

  let found: string | undefined

  for (const key of ORION_TOKEN_KEYS) {
    const value = url.searchParams.get(key)
    if (!value) continue
    found = value
    url.searchParams.delete(key)
  }

  // 少数入口把凭据放在 hash 里（与 OIDC 回跳同一习惯）。
  const rawHash = url.hash.startsWith('#') ? url.hash.slice(1) : url.hash
  if (rawHash) {
    const hashParams = new URLSearchParams(rawHash)
    for (const key of ORION_TOKEN_KEYS) {
      const value = hashParams.get(key)
      if (!value) continue
      if (!found) found = value
      hashParams.delete(key)
    }
    const rest = hashParams.toString()
    if (rest !== rawHash) {
      url.hash = rest ? `#${rest}` : ''
    }
  }

  if (found) {
    // 立刻把凭据从地址栏抹掉，避免它继续出现在历史记录与 Referer 里。
    window.history.replaceState(
      window.history.state,
      '',
      `${url.pathname}${url.search}${url.hash}`,
    )
  }

  consumed = found
  return consumed
}

/** 本次页面加载期间已知的凭据（可能来自地址栏，也可能为空）。 */
export function consumedOrionAssertion(): string | undefined {
  return consumed
}
