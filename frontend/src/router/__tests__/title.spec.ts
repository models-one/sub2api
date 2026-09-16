import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { PURCHASE_ROUTE_NAME, resolveDocumentTitle, resolveRouteDocumentTitle, resolveRouteMetaKeys } from '@/router/title'
import { useMerchantStore } from '@/stores/merchant'

// 语言包在测试环境是懒加载的，这里只提供本文件用到的几个 key，其余原样返回 key（触发 meta.title 回退）。
// getLocale 也要一并提供：商户用例经 stores/merchant → @/api/client 间接引用它。
vi.mock('@/i18n', () => {
  const messages: Record<string, string> = {
    'nav.recharge': '充值',
    'nav.subscribe': '订阅',
    'nav.buySubscription': '充值/订阅',
  }
  return {
    i18n: { global: { t: (key: string) => messages[key] ?? key } },
    getLocale: () => 'zh-CN',
  }
})

describe('resolveDocumentTitle', () => {
  it('路由存在标题时，使用“路由标题 - 站点名”格式', () => {
    expect(resolveDocumentTitle('Usage Records', 'My Site')).toBe('Usage Records - My Site')
  })

  it('路由无标题时，回退到站点名', () => {
    expect(resolveDocumentTitle(undefined, 'My Site')).toBe('My Site')
  })

  it('站点名为空时，回退默认站点名', () => {
    expect(resolveDocumentTitle('Dashboard', '')).toBe('Dashboard - Sub2API')
    expect(resolveDocumentTitle(undefined, '   ')).toBe('Sub2API')
  })

  it('站点名变更时仅影响后续路由标题计算', () => {
    const before = resolveDocumentTitle('Admin Dashboard', 'Alpha')
    const after = resolveDocumentTitle('Admin Dashboard', 'Beta')

    expect(before).toBe('Admin Dashboard - Alpha')
    expect(after).toBe('Admin Dashboard - Beta')
  })
})

describe('resolveRouteDocumentTitle', () => {
  it('自定义页面菜单加载后，使用菜单名称作为标题', () => {
    const route = {
      name: 'CustomPage',
      params: { id: 'scheduler' },
      meta: {
        title: 'Custom Page'
      }
    }

    expect(resolveRouteDocumentTitle(route, 'EzouAPI')).toBe('Custom Page - EzouAPI')
    expect(resolveRouteDocumentTitle(route, 'EzouAPI', [
      {
        id: 'scheduler',
        label: '账号调度器',
        icon_svg: '',
        url: 'https://example.com',
        visibility: 'admin',
        sort_order: 0
      }
    ])).toBe('账号调度器 - EzouAPI')
  })
})

describe('resolveRouteMetaKeys', () => {
  const purchaseRoute = {
    name: PURCHASE_ROUTE_NAME,
    meta: { titleKey: 'nav.buySubscription', descriptionKey: 'purchase.description' }
  }

  it('默认（充值 & 订阅或未知）沿用路由 meta 的标题/描述 key', () => {
    expect(resolveRouteMetaKeys(purchaseRoute)).toEqual({
      titleKey: 'nav.buySubscription',
      descriptionKey: 'purchase.description'
    })
    expect(resolveRouteMetaKeys(purchaseRoute, { billingMode: 'recharge_and_subscription' })).toEqual({
      titleKey: 'nav.buySubscription',
      descriptionKey: 'purchase.description'
    })
  })

  it('仅充值时 /purchase 切换为纯充值文案', () => {
    expect(resolveRouteMetaKeys(purchaseRoute, { billingMode: 'recharge_only' })).toEqual({
      titleKey: 'nav.recharge',
      descriptionKey: 'purchase.rechargeDescription'
    })
  })

  it('仅订阅时 /purchase 切换为纯订阅文案', () => {
    expect(resolveRouteMetaKeys(purchaseRoute, { billingMode: 'subscription_only' })).toEqual({
      titleKey: 'nav.subscribe',
      descriptionKey: 'purchase.subscriptionDescription'
    })
  })

  it('站点类型不影响其他路由', () => {
    const route = { name: 'Subscriptions', meta: { titleKey: 'userSubscriptions.title' } }
    expect(resolveRouteMetaKeys(route, { billingMode: 'recharge_only' })).toEqual({
      titleKey: 'userSubscriptions.title',
      descriptionKey: undefined
    })
  })
})

describe('resolveRouteDocumentTitle 站点类型', () => {
  const purchaseRoute = {
    name: PURCHASE_ROUTE_NAME,
    params: {},
    meta: { title: 'Purchase Subscription', titleKey: 'nav.buySubscription' }
  }

  it('仅充值时 document.title 不再带「订阅」', () => {
    const title = resolveRouteDocumentTitle(purchaseRoute, 'EzouAPI', [], { billingMode: 'recharge_only' })
    expect(title).toBe('充值 - EzouAPI')
  })

  it('仅订阅时 document.title 只剩「订阅」', () => {
    const title = resolveRouteDocumentTitle(purchaseRoute, 'EzouAPI', [], { billingMode: 'subscription_only' })
    expect(title).toBe('订阅 - EzouAPI')
  })

  it('充值 & 订阅时保留原标题', () => {
    const title = resolveRouteDocumentTitle(purchaseRoute, 'EzouAPI', [], { billingMode: 'recharge_and_subscription' })
    expect(title).toBe('充值/订阅 - EzouAPI')
  })
})

// 商户品牌站用例放在最后：beforeEach 里的 setActivePinia 会留下全局 active pinia，
// 排在上游用例之后可避免污染它们（那些用例依赖 getActivePinia() 为空走默认分支）。
describe('resolveRouteDocumentTitle - 商户品牌站', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  const route = { name: 'Home', params: {}, meta: { title: 'Dashboard' } }

  it('商户站且有 seoTitle 时，页签标题固定为 seoTitle（覆盖路由标题）', () => {
    const merchant = useMerchantStore()
    merchant.brand = { is_merchant_site: true, seo_title: 'BrandCo - AI Gateway' }

    expect(resolveRouteDocumentTitle(route, 'EzouAPI')).toBe('BrandCo - AI Gateway')
  })

  it('非商户站时仍走默认“路由标题 - 站点名”', () => {
    const merchant = useMerchantStore()
    merchant.brand = { is_merchant_site: false }

    expect(resolveRouteDocumentTitle(route, 'EzouAPI')).toBe('Dashboard - EzouAPI')
  })

  it('商户站但 seoTitle 为空时，回退默认路由标题', () => {
    const merchant = useMerchantStore()
    merchant.brand = { is_merchant_site: true }

    expect(resolveRouteDocumentTitle(route, 'EzouAPI')).toBe('Dashboard - EzouAPI')
  })
})
