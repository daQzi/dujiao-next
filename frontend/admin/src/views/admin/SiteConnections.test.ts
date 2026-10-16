import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import SiteConnections from './SiteConnections.vue'
import i18n from '@/i18n'

vi.mock('@/api/admin', () => ({
  adminAPI: { getSiteConnections: vi.fn(async () => ({ data: { data: [] } })) },
}))

let dispose: (() => void) | undefined
afterEach(() => {
  dispose?.()
  document.body.innerHTML = ''
})

describe('external upstream connection configuration', () => {
  it('renders both protocols and keeps Yichiyuan procurement disabled by default', async () => {
    const root = document.createElement('div')
    document.body.append(root)
    const app = createApp(SiteConnections)
    app.use(i18n)
    const vm = app.mount(root)
    dispose = () => app.unmount()
    await nextTick()
    await nextTick()
    // Inspect the real component state and template, without a running backend.
    const state = (vm.$ as unknown as { setupState: Record<string, any> }).setupState
    state.openCreateModal()
    await nextTick()
    state.form.protocol = 'yichiyuan'
    await nextTick()
    expect(state.form.ycy_delivery_status_confirmed).toBe('false')
    expect(document.body.textContent).toContain('delivery_status')
    expect(document.body.textContent).toContain('应用 ID')
    expect(document.body.textContent).toContain('下单结果不明')
    expect(state.buildPayload().ycy_delivery_status_confirmed).toBe(false)
    state.form.protocol = '16688'
    await nextTick()
    expect(document.body.textContent).toContain('运营钱包')
    expect(document.body.textContent).not.toContain('上游已补充可靠的发货状态字段')
    state.resetForm()
    expect(state.form.protocol).toBe('dujiao-next')
    expect(state.form.ycy_delivery_status_confirmed).toBe('false')
  })

  it.each(['zh-CN', 'zh-TW', 'en-US'] as const)('provides protocol guidance in %s', (locale) => {
    for (const key of ['yichiyuan', 'platform16688', 'yichiyuanHint', 'platform16688Hint', 'externalSafetyHint', 'ycyDeliveryStatusConfirmed', 'ycyDeliveryStatusHint']) {
      const path = `siteConnections.form.${key}`
      expect(i18n.global.te(path, locale)).toBe(true)
    }
  })
})
