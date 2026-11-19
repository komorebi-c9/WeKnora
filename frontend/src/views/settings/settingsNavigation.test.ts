import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { computed } from 'vue'
import { INTEGRATION_PREVIEW_ITEMS } from '../../config/integrations'
import { integrationSectionKey } from '../../config/settingsRoute'

// Exercise the actual sidebar grouping without mounting the settings panels.
const source = readFileSync(new URL('./Settings.vue', import.meta.url), 'utf8')
const script = source.match(/<script setup lang="ts">([\s\S]*?)<\/script>/)![1]!
const ast = ts.createSourceFile('Settings.ts', script, ts.ScriptTarget.Latest, true)
const grouping = ast.statements.find((statement) =>
  ts.isVariableStatement(statement) && statement.declarationList.declarations.some(
    (declaration) => ts.isIdentifier(declaration.name) && declaration.name.text === 'navGroups',
  ),
)
assert.ok(grouping, 'Settings must define sidebar groups')
const compiled = ts.transpileModule(grouping.getText(ast), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText

// HIDDEN_SETTINGS_NAV_KEYS 由用例显式传入（默认空），这样"用户自己配置隐藏项"
// 不会让这些断言随配置变化而红；集合本身的合法性由 uiVisibility.test.ts 校验。
function buildGroups(visibleKeys: string[], hidden: Iterable<string> = []) {
  const hiddenSet = new Set(hidden)
  // 真实组件里 navItems 先做单项过滤，navGroups 再做分组过滤；沙箱把两步都还原，
  // 否则单项隐藏与"组内全隐藏"两类断言无法覆盖到。
  const navItems = visibleKeys.filter((key) => !hiddenSet.has(key)).map((key) => ({ key }))
  return runInNewContext(`${compiled}\nnavGroups.value`, {
    computed,
    navItems: { value: navItems },
    t: (key: string) => key,
    integrationSectionKey,
    INTEGRATION_PREVIEW_ITEMS,
    HIDDEN_SETTINGS_NAV_KEYS: hiddenSet,
  }) as Array<{ key: string; items: Array<{ key: string }> }>
}

function integrationMenu(visibleKeys: string[], hidden: Iterable<string> = []): string[] {
  const groups = buildGroups(visibleKeys, hidden)
  const integrationGroup = groups.find((group) => group.key === 'integrations')
  return Array.from(integrationGroup?.items ?? [], (item) => item.key)
}

const integrationKeys = INTEGRATION_PREVIEW_ITEMS.map((item) => integrationSectionKey(item.key))

test('settings sidebar includes every registered integration, including CLI, in navigation order', () => {
  assert.deepEqual(integrationMenu(['general', ...integrationKeys]), integrationKeys)
})

test('settings sidebar preserves visibility filtering without hiding CLI', () => {
  const visible = integrationKeys.filter((key) => key !== 'integration-api' && key !== 'integration-im')
  assert.deepEqual(integrationMenu(visible), visible)
  assert.deepEqual(integrationMenu(['general']), [])
})

test('a hidden section key removes only that entry', () => {
  const visible = ['general', ...integrationKeys]
  const remaining = integrationMenu(visible, ['integration-cli'])
  assert.deepEqual(remaining, integrationKeys.filter((key) => key !== 'integration-cli'))
})

test('a hidden group key removes the whole group', () => {
  const groups = buildGroups(['general', ...integrationKeys], ['integrations'])
  assert.equal(groups.some((group) => group.key === 'integrations'), false)
})

test('hiding every entry of a group also drops the group title', () => {
  const groups = buildGroups(['general', ...integrationKeys], integrationKeys)
  assert.equal(groups.some((group) => group.key === 'integrations'), false)
  assert.equal(groups.some((group) => group.key === 'account'), true)
})

// Keep navigation tests tied to the real close handler, without mounting
// settings panels that require backend services.
const closeHandler = ast.statements.find((statement) =>
  ts.isVariableStatement(statement) && statement.declarationList.declarations.some(
    (declaration) => ts.isIdentifier(declaration.name) && declaration.name.text === 'handleClose',
  ),
)
assert.ok(closeHandler, 'Settings must define a close handler')
const compiledClose = ts.transpileModule(closeHandler.getText(ast), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText

function closeSettings(back: unknown, section = 'members', path = '/platform/settings'): string[] {
  const actions: string[] = []
  class FocusedElement {
    blur() { actions.push('blur') }
  }
  runInNewContext(`${compiledClose}\nhandleClose()`, {
    document: { activeElement: new FocusedElement() },
    HTMLElement: FocusedElement,
    route: { path, query: { section } },
    uiStore: { closeSettings: () => actions.push('close') },
    router: {
      options: { history: { state: { back } } },
      push: (target: string) => actions.push(`push:${target}`),
      back: () => actions.push('back'),
    },
  })
  return actions
}

test('closing a directly opened settings route stays in the application', () => {
  for (const back of [null, undefined, '', 'https://example.com/', '//example.com/', '/login']) {
    assert.deepEqual(closeSettings(back), ['blur', 'close', 'push:/platform/knowledge-bases'])
  }
})

test('closing settings preserves a known in-app previous route', () => {
  for (const back of ['/platform/knowledge-bases', '/platform/chat/session-123']) {
    assert.deepEqual(closeSettings(back), ['blur', 'close', 'back'])
  }
})

test('closing system settings retains its explicit knowledge-base destination', () => {
  for (const section of ['model-catalog', 'system-global', 'runtime-queues', 'platform-api-keys', 'system-audit-log']) {
    assert.deepEqual(closeSettings('/platform/chat/session-123', section), [
      'blur', 'close', 'push:/platform/knowledge-bases',
    ])
  }
})

test('closing an overlaid settings panel leaves the underlying route alone', () => {
  assert.deepEqual(closeSettings(null, 'general', '/platform/chat/session-123'), ['blur', 'close'])
})
