import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import LocaleSwitcher from '../LocaleSwitcher.vue'

const { setLocale } = vi.hoisted(() => ({ setLocale: vi.fn() }))

vi.mock('@/i18n', () => ({
  setLocale,
  availableLocales: [
    { code: 'en', name: 'English', flag: '🇺🇸' },
    { code: 'zh', name: '中文', flag: '🇨🇳' }
  ],
  // fork：简体中文仅对管理员开放，LocaleSwitcher 读 auth store 决定是否并入
  adminOnlyLocales: []
}))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: { value: 'zh' } }) }))

const reload = vi.fn()

const mountSwitcher = (props: Record<string, unknown> = {}) =>
  mount(LocaleSwitcher, { props, global: { stubs: { Icon: true, transition: false } } })

const choose = async (wrapper: ReturnType<typeof mountSwitcher>, name: string) => {
  await wrapper.get('button').trigger('click')
  const option = wrapper.findAll('button').find((button) => button.text().includes(name))
  await option!.trigger('click')
  await flushPromises()
}

describe('LocaleSwitcher', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    setLocale.mockReset().mockResolvedValue(undefined)
    reload.mockReset()
    vi.stubGlobal('location', { ...window.location, reload })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // 分组名称等内容由后端按请求语言返回，已加载的数据要靠整页刷新换成新语言
  it('reloads the page after switching to another language', async () => {
    const wrapper = mountSwitcher()

    await choose(wrapper, 'English')

    expect(setLocale).toHaveBeenCalledWith('en')
    expect(reload).toHaveBeenCalledTimes(1)
    expect(setLocale.mock.invocationCallOrder[0]).toBeLessThan(reload.mock.invocationCallOrder[0])
  })

  it('does nothing when the current language is chosen again', async () => {
    const wrapper = mountSwitcher()

    await choose(wrapper, '中文')

    expect(setLocale).not.toHaveBeenCalled()
    expect(reload).not.toHaveBeenCalled()
  })

  // fork：登录 / 注册等页面原地切换语言，不清空正在填写的表单
  it('switches in place without reloading when reload-on-switch is false', async () => {
    const wrapper = mountSwitcher({ reloadOnSwitch: false })

    await choose(wrapper, 'English')

    expect(setLocale).toHaveBeenCalledWith('en')
    expect(reload).not.toHaveBeenCalled()
  })
})
