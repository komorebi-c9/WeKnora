/**
 * 全站唯一的品牌色配置来源。
 *
 * 想换主题色时只改这个文件：
 *   1. `BRAND_PRIMARY` 是品牌主色（浅色模式对应 `--td-brand-color-4`，
 *      深色模式对应 `--td-brand-color-6`）。
 *   2. `LIGHT_BRAND_PALETTE` / `DARK_BRAND_PALETTE` 分别定义两套 10 级
 *      品牌色阶（TDesign 规范：浅色模式 1 最浅 → 10 最深；深色模式反向），
 *      以及禁用态、品牌色文字、品牌色填充上的文字颜色。
 *
 * 应用方式：`applyBrandPalette()` 在启动（useTheme.initTheme / embed 入口）
 * 和明暗切换时把这些值写入 `documentElement` 的内联 CSS 变量；theme.css
 * 不再写死品牌色值，只保留对色阶变量的派生引用。
 *
 * 色值来源：Orion（AgentOS 设计变量 v3.0，lolr-orion/docs/prototype/tokens.css）
 * 的青蓝色系。基准主色 #3fa9c9 取 Orion 深色模式 --color-primary（用户确认的
 * 实际观感色）；深色模式主色相应提亮一档取 Orion 深色 hover #5cbbd8。
 * 色阶 1 对齐 Orion 选中底语义（主色 11% 混白）；中间色阶按 CSS
 * color-mix(in oklab, ...) 语义在主色与白/墨色之间取值，保证 1→10
 * 单调变深、明度平滑衔接。
 *
 * 对比度说明：#3fa9c9 上白字对比度仅 ≈2.7:1，不可读；Orion 规范对亮青
 * 主色的官方搭配是深墨文字 --color-primary-ink: #06222b（≈6:1），因此
 * `onBrand` 浅色/深色均取 #06222b，按钮等填充组件通过
 * tdesign-overrides.less 的 .t-button--variant-base 覆盖使用它；
 * `textBrand` 取主色向墨色压深（白底对比度 ≈5:1），保证浅色底上的
 * 品牌色文字/链接可读；深色模式文字/链接沿用 Orion --color-link #7ccadf。
 */

export const BRAND_PRIMARY = '#3fa9c9'

export type EffectiveThemeMode = 'light' | 'dark'

interface BrandPalette {
  /** 10 级品牌色阶：浅色模式 1 最浅 → 10 最深；深色模式反向，6 为主色。 */
  ramp: [
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
    string,
  ]
  /** 禁用态品牌色（--td-brand-color-disabled）。 */
  disabled: string
  /** 浅色底上的品牌色文字/链接（--td-text-color-brand / --td-text-color-link）。 */
  textBrand: string
  /** 品牌色填充上的文字颜色（--app-on-brand），供按钮等填充组件使用。 */
  onBrand: string
}

const LIGHT_BRAND_PALETTE: BrandPalette = {
  ramp: [
    '#ecf6f9',
    '#cee7f0',
    '#90cade',
    '#3fa9c9',
    '#3a95b1',
    '#348199',
    '#2e6e82',
    '#295c6c',
    '#244d5a',
    '#1f3e49',
  ],
  disabled: '#b0d9e7',
  textBrand: '#31788e',
  onBrand: '#06222b',
}

const DARK_BRAND_PALETTE: BrandPalette = {
  ramp: [
    '#5cbbd820',
    '#1f3e49',
    '#244d5a',
    '#295c6c',
    '#2e6e82',
    '#348199',
    '#5cbbd8',
    '#73c3dd',
    '#88cce2',
    '#aadaea',
  ],
  disabled: '#2f5865',
  textBrand: '#7ccadf',
  onBrand: '#06222b',
}

function paletteFor(mode: EffectiveThemeMode): BrandPalette {
  return mode === 'dark' ? DARK_BRAND_PALETTE : LIGHT_BRAND_PALETTE
}

/** 把指定明暗模式的品牌色阶写入 :root 内联变量（覆盖 CSS 中的静态定义）。 */
export function applyBrandPalette(mode: EffectiveThemeMode): void {
  if (typeof document === 'undefined') return
  const palette = paletteFor(mode)
  const rootStyle = document.documentElement.style
  palette.ramp.forEach((hex, index) => {
    rootStyle.setProperty(`--td-brand-color-${index + 1}`, hex)
  })
  rootStyle.setProperty('--td-brand-color-disabled', palette.disabled)
  rootStyle.setProperty('--td-text-color-brand', palette.textBrand)
  rootStyle.setProperty('--td-text-color-link', palette.textBrand)
  rootStyle.setProperty('--app-on-brand', palette.onBrand)
}

/** 按 html[theme-mode] 当前生效的明暗模式应用品牌色板（无该属性时按浅色）。 */
export function applyBrandPaletteForCurrentMode(): void {
  const mode =
    typeof document !== 'undefined' &&
    document.documentElement.getAttribute('theme-mode') === 'dark'
      ? 'dark'
      : 'light'
  applyBrandPalette(mode)
}

export { LIGHT_BRAND_PALETTE, DARK_BRAND_PALETTE }
