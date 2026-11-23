import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

import { INTEGRATION_PREVIEW_ITEMS } from './integrations'
import { integrationSectionKey } from './settingsRoute'
import { HIDDEN_SETTINGS_NAV_KEYS, SETTINGS_NAV_GROUP_KEYS } from './settingsAccess'
import { HIDDEN_SIDEBAR_ITEM_KEYS, HIDDEN_SIDEBAR_ITEMS, HIDDEN_PARSER_ENGINE_NAMES } from './uiVisibility'

const settingsSource = readFileSync(
  new URL('../views/settings/Settings.vue', import.meta.url),
  'utf8',
)

const parserEngineSettingsSource = readFileSync(
  new URL('../views/settings/ParserEngineSettings.vue', import.meta.url),
  'utf8',
)

// 组件里所有形如 key: 'xxx' 的字面量：既包含导航项，也包含分组 key。
// 取超集即可满足"隐藏 key 必须存在"的包含性断言。
const componentKeys = new Set(
  Array.from(settingsSource.matchAll(/key:\s*'([a-z0-9_-]+)'/g), (match) => match[1]!),
)

const integrationKeys = INTEGRATION_PREVIEW_ITEMS.map((item) => integrationSectionKey(item.key))

const knownSettingKeys = new Set<string>([...componentKeys, ...integrationKeys])

/** 与 Settings.vue 中 DEV 告警同口径：返回写错的隐藏 key。 */
function unknownHiddenKeys(hidden: Iterable<string>, known: Set<string> = knownSettingKeys) {
  return [...hidden].filter((key) => !known.has(key))
}

test('every hidden settings key maps to a real group or section', () => {
  assert.deepEqual(unknownHiddenKeys(HIDDEN_SETTINGS_NAV_KEYS), [])
})

test('the key check is not vacuous', () => {
  // 拼错/已废弃的 key 必须被判为未知，否则上面的断言等于没校验。
  assert.deepEqual(unknownHiddenKeys(['model_runtime']), ['model_runtime'])
  assert.deepEqual(unknownHiddenKeys(['skills', 'mcp']), ['skills', 'mcp'])
})

test('declared group keys match the groups declared in Settings.vue', () => {
  // 只截取 navGroups 这一段：段内的 key: 'xxx' 全部是分组 key，
  // 分区 key 是以字符串数组形式传给 pickItems 的，不会命中这个模式。
  const start = settingsSource.indexOf('const navGroups = computed')
  const end = settingsSource.indexOf('const handleNavClick')
  assert.ok(start > 0 && end > start, 'Settings.vue 结构变化，请同步本测试的切片边界')
  const declaredGroups = Array.from(
    settingsSource.slice(start, end).matchAll(/key:\s*'([a-z0-9_]+)'/g),
    (match) => match[1]!,
  )
  assert.deepEqual(
    [...SETTINGS_NAV_GROUP_KEYS].sort(),
    declaredGroups.sort(),
    'settingsAccess 与 Settings.vue 的分组 key 必须一致',
  )
  for (const group of SETTINGS_NAV_GROUP_KEYS) {
    assert.ok(componentKeys.has(group), `分组 key ${group} 未出现在 Settings.vue 中`)
  }
})

test('hidden sidebar keys are limited to the supported set', () => {
  for (const key of HIDDEN_SIDEBAR_ITEMS) {
    assert.ok(
      (HIDDEN_SIDEBAR_ITEM_KEYS as readonly string[]).includes(key),
      `未知的左侧栏隐藏 key: ${key}`,
    )
  }
})

test('hidden parser engine names are real engines and are filtered in the view', () => {
  // ENGINE_ORDER 列出了该页展示的全部引擎名，用它当"合法引擎名"的对照表，
  // 避免在这里再抄一份列表。
  const orderBlock = parserEngineSettingsSource.slice(
    parserEngineSettingsSource.indexOf('const ENGINE_ORDER'),
    parserEngineSettingsSource.indexOf('const sortedEngines'),
  )
  const knownEngines = new Set(
    Array.from(orderBlock.matchAll(/^\s{2}([a-z0-9_]+):/gm), (match) => match[1]!),
  )
  assert.ok(knownEngines.size > 0, 'ParserEngineSettings.vue 结构变化，请同步本测试的切片边界')

  for (const name of HIDDEN_PARSER_ENGINE_NAMES) {
    assert.ok(knownEngines.has(name), `未知的解析引擎隐藏 key: ${name}`)
  }

  // 只声明不接线等于没隐藏：确认渲染列表确实做了过滤。
  assert.match(
    parserEngineSettingsSource,
    /filter\(\s*e\s*=>\s*!HIDDEN_PARSER_ENGINE_NAMES\.has\(e\.Name\)\s*\)/,
    '解析引擎列表未按 HIDDEN_PARSER_ENGINE_NAMES 过滤',
  )
})
