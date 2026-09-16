<template>
  <span
    :class="[
      'inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-xs font-medium transition-colors',
      badgeClass
    ]"
  >
    <!-- Platform logo -->
    <PlatformIcon v-if="platform" :platform="platform" size="sm" />
    <!-- Group name -->
    <span>{{ name }}</span>
    <!-- Right side label -->
    <span v-if="showLabel" :class="labelClass">
      <template v-if="hasCustomRate">
        <!-- 原倍率删除线 + 专属倍率高亮 -->
        <span class="line-through opacity-50 mr-0.5">{{ rateMultiplier }}x</span>
        <span class="font-bold">{{ userRateMultiplier }}x</span>
      </template>
      <template v-else>
        {{ labelText }}
      </template>
    </span>
    <span v-if="hasPeakRate" :class="peakRateClass" :title="peakRateTitle">
      {{ peakRateText }}
    </span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SubscriptionType, GroupPlatform } from '@/types'
import { useAppStore } from '@/stores/app'
import { formatPeakRateWindow, serverTimezoneLabel } from '@/utils/peak-rate'
import PlatformIcon from './PlatformIcon.vue'

interface Props {
  name: string
  platform?: GroupPlatform
  subscriptionType?: SubscriptionType
  rateMultiplier?: number
  userRateMultiplier?: number | null // 用户专属倍率
  peakRateEnabled?: boolean
  peakStart?: string
  peakEnd?: string
  peakRateMultiplier?: number
  showRate?: boolean
  daysRemaining?: number | null // 剩余天数（订阅类型时使用）
  /**
   * 订阅分组默认在右侧 label 展示"订阅"或剩余天数；
   * 开启后订阅分组也改为显示倍率（保留订阅主题色 label，配合可用渠道这类
   * 只关心费率、不关心有效期的场景）。
   */
  alwaysShowRate?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  subscriptionType: 'standard',
  showRate: true,
  daysRemaining: null,
  userRateMultiplier: null,
  peakRateEnabled: false,
  alwaysShowRate: false
})

const { t } = useI18n()

const isSubscription = computed(() => props.subscriptionType === 'subscription')

// 是否有专属倍率（且与默认倍率不同）
const hasCustomRate = computed(() => {
  return (
    props.userRateMultiplier !== null &&
    props.userRateMultiplier !== undefined &&
    props.rateMultiplier !== undefined &&
    props.userRateMultiplier !== props.rateMultiplier
  )
})

const appStore = useAppStore()

const hasPeakRate = computed(() => {
  return Boolean(props.showRate && props.peakRateEnabled && props.peakStart && props.peakEnd)
})

const peakRateText = computed(() => {
  return formatPeakRateWindow(
    {
      peak_rate_enabled: props.peakRateEnabled,
      peak_start: props.peakStart,
      peak_end: props.peakEnd,
      peak_rate_multiplier: props.peakRateMultiplier
    },
    serverTimezoneLabel(appStore.cachedPublicSettings?.server_utc_offset)
  )
})

const peakRateTitle = computed(() => {
  return t('common.peakRateTooltip', { window: peakRateText.value })
})

// 是否显示右侧标签
const showLabel = computed(() => {
  if (!props.showRate) return false
  // 订阅类型：显示天数或"订阅"
  if (isSubscription.value) return true
  // 标准类型：显示倍率（包括专属倍率）
  return props.rateMultiplier !== undefined || hasCustomRate.value
})

// Label text
const labelText = computed(() => {
  const rateLabel = props.rateMultiplier !== undefined ? `${props.rateMultiplier}x` : ''
  if (isSubscription.value && !props.alwaysShowRate) {
    // 如果有剩余天数，显示天数
    if (props.daysRemaining !== null && props.daysRemaining !== undefined) {
      if (props.daysRemaining <= 0) {
        return t('admin.users.expired')
      }
      return t('admin.users.daysRemaining', { days: props.daysRemaining })
    }
    // 否则显示"订阅"
    return t('groups.subscription')
  }
  return rateLabel
})

// Label style based on type and days remaining
const labelClass = computed(() => {
  const base = 'px-1.5 py-0.5 rounded text-[10px] font-semibold'

  if (!isSubscription.value) {
    // Standard: subtle background (不再为专属倍率使用不同的背景色)
    return `${base} bg-black/10 dark:bg-white/10`
  }

  // 订阅类型：根据剩余天数显示不同颜色
  if (props.daysRemaining !== null && props.daysRemaining !== undefined) {
    if (props.daysRemaining <= 0 || props.daysRemaining <= 3) {
      // 已过期或紧急（<=3天）：红色
      return `${base} bg-red-200/80 text-red-800 dark:bg-red-800/50 dark:text-red-300`
    }
    if (props.daysRemaining <= 7) {
      // 警告（<=7天）：橙色
      return `${base} bg-amber-200/80 text-amber-800 dark:bg-amber-800/50 dark:text-amber-300`
    }
  }

  // 正常状态或无天数：根据平台显示主题色
  const labelColors: Record<string, string> = {
    anthropic: 'bg-orange-200/60 text-orange-800 dark:bg-orange-800/40 dark:text-orange-300',
    openai: 'bg-emerald-200/60 text-emerald-800 dark:bg-emerald-800/40 dark:text-emerald-300',
    gemini: 'bg-blue-200/60 text-blue-800 dark:bg-blue-800/40 dark:text-blue-300',
    antigravity: 'bg-purple-200/60 text-purple-800 dark:bg-purple-800/40 dark:text-purple-300',
    kimi: 'bg-pink-200/60 text-pink-800 dark:bg-pink-800/40 dark:text-pink-300',
    zhipu: 'bg-indigo-200/60 text-indigo-800 dark:bg-indigo-800/40 dark:text-indigo-300',
    deepseek: 'bg-teal-200/60 text-teal-800 dark:bg-teal-800/40 dark:text-teal-300',
    grok: 'bg-zinc-300/70 text-zinc-800 dark:bg-zinc-700/60 dark:text-zinc-200',
    minimax: 'bg-rose-200/60 text-rose-800 dark:bg-rose-800/40 dark:text-rose-300',
    opencode_go: 'bg-amber-200/60 text-amber-900 dark:bg-amber-800/40 dark:text-amber-300',
    composite: 'bg-cyan-200/70 text-cyan-900 dark:bg-cyan-900/50 dark:text-cyan-300',
  }
  return `${base} ${labelColors[props.platform || ''] || 'bg-violet-200/60 text-violet-800 dark:bg-violet-800/40 dark:text-violet-300'}`
})

const peakRateClass = computed(() => {
  return 'px-1.5 py-0.5 rounded text-[10px] font-semibold bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
})

// Badge color based on platform and subscription type
const BADGE_COLORS: Record<string, { sub: string; std: string }> = {
  anthropic:   { sub: 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400',    std: 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-400' },
  openai:      { sub: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400', std: 'bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-400' },
  gemini:      { sub: 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400',            std: 'bg-sky-50 text-sky-700 dark:bg-sky-900/20 dark:text-sky-400' },
  antigravity: { sub: 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400',    std: 'bg-purple-50 text-purple-700 dark:bg-purple-900/20 dark:text-purple-400' },
  kimi:        { sub: 'bg-pink-100 text-pink-700 dark:bg-pink-900/30 dark:text-pink-400',           std: 'bg-pink-50 text-pink-700 dark:bg-pink-900/20 dark:text-pink-400' },
  zhipu:       { sub: 'bg-indigo-100 text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-400',    std: 'bg-indigo-50 text-indigo-700 dark:bg-indigo-900/20 dark:text-indigo-400' },
  deepseek:    { sub: 'bg-teal-100 text-teal-700 dark:bg-teal-900/30 dark:text-teal-400',            std: 'bg-teal-50 text-teal-700 dark:bg-teal-900/20 dark:text-teal-400' },
  grok:        { sub: 'bg-zinc-200 text-zinc-800 dark:bg-zinc-700 dark:text-zinc-100',               std: 'bg-zinc-100 text-zinc-700 dark:bg-zinc-800 dark:text-zinc-200' },
  minimax:     { sub: 'bg-rose-100 text-rose-700 dark:bg-rose-900/30 dark:text-rose-400',            std: 'bg-rose-50 text-rose-700 dark:bg-rose-900/20 dark:text-rose-400' },
  opencode_go: { sub: 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300',        std: 'bg-amber-50 text-amber-800 dark:bg-amber-900/20 dark:text-amber-300' },
  composite:   { sub: 'bg-cyan-100 text-cyan-800 dark:bg-cyan-900/30 dark:text-cyan-300',            std: 'bg-cyan-50 text-cyan-800 dark:bg-cyan-900/20 dark:text-cyan-300' },
}
const BADGE_DEFAULT = { sub: 'bg-violet-100 text-violet-700 dark:bg-violet-900/30 dark:text-violet-400', std: 'bg-gray-100 text-gray-700 dark:bg-gray-900/20 dark:text-gray-400' }
const badgeClass = computed(() => {
  const colors = BADGE_COLORS[props.platform || ''] || BADGE_DEFAULT
  return isSubscription.value ? colors.sub : colors.std
})
</script>
