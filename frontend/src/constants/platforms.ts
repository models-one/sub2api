import type { AccountPlatform, GroupPlatform } from '@/types'

export interface PlatformOption<T extends string = string> {
  value: T
  label: string
}

/**
 * Concrete upstream platforms supported by accounts and request routing.
 * Keep platform selectors derived from this catalog so newly added providers
 * do not silently disappear from list filters.
 */
export const CONCRETE_PLATFORM_OPTIONS = [
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'antigravity', label: 'Antigravity' },
  { value: 'grok', label: 'Grok' },
  { value: 'kimi', label: 'Kimi' },
  { value: 'zhipu', label: 'Zhipu GLM' },
  { value: 'deepseek', label: 'DeepSeek' },
  { value: 'minimax', label: 'MiniMax' },
  { value: 'opencode_go', label: 'OpenCode' }
] as const satisfies readonly PlatformOption<AccountPlatform>[]

/**
 * Platforms that carry per-user quota rows (user_platform_quotas).
 * 后端权威源是 service.AllowedQuotaPlatforms；此处按 CONCRETE_PLATFORM_OPTIONS 派生，
 * 避免各处 UI 各自硬编码一份而在新增平台时静默漏掉——
 * 上游 0.2.4 加 minimax 时就漏了六处，导致该平台配额既看不到也配不了 = 事实上无限额。
 */
export const QUOTA_PLATFORM_ORDER = CONCRETE_PLATFORM_OPTIONS.map(
  (option) => option.value
) as readonly AccountPlatform[]

/** Platforms that can own a group. */
export const GROUP_PLATFORM_OPTIONS = [
  ...CONCRETE_PLATFORM_OPTIONS,
  { value: 'composite', label: 'Composite' }
] as const satisfies readonly PlatformOption<GroupPlatform>[]
