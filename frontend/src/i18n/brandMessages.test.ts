import assert from 'node:assert/strict'
import { test } from 'node:test'

import { BRAND_NAME } from '../constants/brand.ts'
import { applyBrandToMessages } from './brandMessages.ts'

test('applyBrandToMessages replaces the historical brand name', () => {
  const out = applyBrandToMessages({ greeting: '欢迎使用 WeKnora' })
  assert.equal(out.greeting, `欢迎使用 ${BRAND_NAME}`)
})

test('applyBrandToMessages leaves keys untouched', () => {
  const out = applyBrandToMessages({ 'WeKnora.title': 'WeKnora 标题' })
  assert.equal(out['WeKnora.title'], `${BRAND_NAME} 标题`)
})

test('applyBrandToMessages normalises a standalone Lolr', () => {
  const out = applyBrandToMessages({ standard: 'Lolr 标准模板', keep: 'Lolr Ai 已就绪' })
  assert.equal(out.standard, `${BRAND_NAME} 标准模板`)
  assert.equal(out.keep, 'Lolr Ai 已就绪')
})

test('applyBrandToMessages keeps commands and links intact', () => {
  const command = 'git clone https://github.com/Tencent/WeKnora.git'
  const readme = 'https://github.com/Tencent/WeKnora/blob/main/cli/README.md'
  const out = applyBrandToMessages({ command, readme })
  assert.equal(out.command, command)
  assert.equal(out.readme, readme)
})

test('applyBrandToMessages walks nested objects and arrays', () => {
  const out = applyBrandToMessages({
    section: { title: 'WeKnora 帮助', items: ['关于 WeKnora', 42, true, null] },
  })
  assert.equal(out.section.title, `${BRAND_NAME} 帮助`)
  assert.deepEqual(out.section.items, [`关于 ${BRAND_NAME}`, 42, true, null])
})

test('applyBrandToMessages preserves functions and non-object input', () => {
  const fn = () => 'WeKnora'
  const withFn = applyBrandToMessages({ label: 'WeKnora', build: fn })
  assert.equal(withFn.label, BRAND_NAME)
  assert.equal(withFn.build, fn)

  assert.equal(applyBrandToMessages('WeKnora'), 'WeKnora')
  assert.equal(applyBrandToMessages(null), null)
})
