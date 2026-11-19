import { BRAND_NAME } from '../constants/brand.ts'

/**
 * 语言包里出现的历史品牌名。界面文案里的品牌字样集中在语言包中，
 * 在这里统一改写，避免在 6 个语言包里散落上百处硬编码。
 */
const SOURCE_BRAND = 'WeKnora'

/**
 * 单独出现的 `Lolr`（后面没有跟着 `Ai`）也归一为完整品牌名，
 * 避免界面上同时出现 "Lolr" / "Lolr Ai" / "WeKnora" 三种写法。
 */
const STANDALONE_LOLR = /\bLolr\b(?!\s*Ai)/g

/**
 * 命令与链接必须原样保留：替换会把可执行内容改坏（例如 git clone 地址、
 * docker/npm 命令）。命中任一特征就整条字符串跳过。
 */
const PROTECTED_FRAGMENTS = ['://', 'github.com', '.git', 'WeKnora/', 'git ', 'docker ', 'npm ', 'curl ']

function isProtected(value: string): boolean {
  return PROTECTED_FRAGMENTS.some((fragment) => value.includes(fragment))
}

function rewriteString(value: string): string {
  if (isProtected(value)) return value
  // 用函数形式替换，避免 BRAND_NAME 里的 $& 之类被当作替换模式解析。
  return value.split(SOURCE_BRAND).join(BRAND_NAME).replace(STANDALONE_LOLR, () => BRAND_NAME)
}

function rewriteValue(value: unknown): unknown {
  if (typeof value === 'string') return rewriteString(value)
  if (Array.isArray(value)) return value.map(rewriteValue)
  if (value !== null && typeof value === 'object') {
    const source = value as Record<string, unknown>
    const result: Record<string, unknown> = {}
    for (const key of Object.keys(source)) {
      // 键名保持原样：把 key 改掉会让所有 t() 调用失效。
      result[key] = rewriteValue(source[key])
    }
    return result
  }
  // 数字、布尔、函数（vue-i18n 支持消息函数）原样返回。
  return value
}

/**
 * 把语言包消息里的品牌字样替换为当前品牌名。
 *
 * 只改字符串值，不改键名，也不改非字符串结构；任何异常都退回原始对象，
 * 保证语言包装载不会因为品牌替换失败而中断。
 */
export function applyBrandToMessages<T>(bundle: T): T {
  if (bundle === null || typeof bundle !== 'object') return bundle
  try {
    return rewriteValue(bundle) as T
  } catch {
    return bundle
  }
}
