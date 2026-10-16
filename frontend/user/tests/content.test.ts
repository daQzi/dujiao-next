import test from 'node:test'
import assert from 'node:assert/strict'
import { htmlToPlainText } from '../src/utils/htmlText.ts'

test('converts rich product descriptions to readable plain text', () => {
  assert.equal(
    htmlToPlainText('<p>账号信息</p><ul><li>自动发货</li><li>售后保障</li></ul>'),
    '账号信息\n• 自动发货\n• 售后保障',
  )
})

test('drops script and style content while decoding common HTML entities', () => {
  assert.equal(
    htmlToPlainText('<p>A &amp; B&nbsp;C</p><script>alert(1)</script><style>.x{}</style>'),
    'A & B C',
  )
})

test('preserves plain text descriptions and accepts missing values', () => {
  assert.equal(htmlToPlainText('普通商品描述'), '普通商品描述')
  assert.equal(htmlToPlainText(null), '')
})
