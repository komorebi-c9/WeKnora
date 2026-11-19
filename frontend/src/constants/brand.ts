/**
 * 全站唯一的品牌名来源。
 *
 * 取值优先级：
 *   1. 运行时配置 `window.__RUNTIME_CONFIG__.BRAND_NAME`
 *      —— 容器部署时由 frontend/docker-entrypoint.sh 注入，
 *         改环境变量即可换品牌，不需要重新构建镜像。
 *   2. 构建期环境变量 `VITE_BRAND_NAME`
 *      —— 用于本地开发或自行构建。
 *   3. 默认值。
 *
 * 只有"用户能看到的文字"应该引用这里；localStorage 键、CSS 类名、
 * 组件名、API 字段等内部标识保持原样，换品牌不应影响它们。
 */
const DEFAULT_BRAND_NAME = 'Lolr Ai'

function normalize(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const trimmed = value.trim()
  return trimmed === '' ? undefined : trimmed
}

// 测试与 SSR 环境下没有 window，必须先判断再读。
function readRuntimeBrandName(): string | undefined {
  if (typeof window === 'undefined') return undefined
  return normalize(window.__RUNTIME_CONFIG__?.BRAND_NAME)
}

function readBuildBrandName(): string | undefined {
  // node:test 等环境没有 import.meta.env，取值前先判空。
  const env = (import.meta as { env?: Record<string, string | undefined> }).env
  return normalize(env?.VITE_BRAND_NAME)
}

export const BRAND_NAME: string =
  readRuntimeBrandName() ?? readBuildBrandName() ?? DEFAULT_BRAND_NAME

/** 当前是否使用内置默认品牌（便于埋点或调试，不影响展示）。 */
export const IS_DEFAULT_BRAND_NAME = BRAND_NAME === DEFAULT_BRAND_NAME

export { DEFAULT_BRAND_NAME }
