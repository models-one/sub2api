/**
 * User Channels API endpoints (non-admin)
 * 用户侧「可用渠道」聚合查询：渠道 + 用户可访问的分组 + 支持模型（含定价）。
 */

import { apiClient } from './client'
import type { BillingMode } from '@/constants/channel'

export interface UserAvailableGroup {
  id: number
  name: string
  platform: string
  /** 'standard' | 'subscription' — 订阅分组视觉加深，和 API 密钥页保持一致。 */
  subscription_type: string
  /** 分组默认倍率。用户专属倍率（若有）通过 /groups/rates 获取后在前端 join。 */
  rate_multiplier: number
  peak_rate_enabled: boolean
  peak_start: string
  peak_end: string
  peak_rate_multiplier: number
  /** true = 专属分组（小范围授权）；false = 公开分组。 */
  is_exclusive: boolean
}

export interface UserPricingInterval {
  min_tokens: number
  max_tokens: number | null
  tier_label?: string
  input_price: number | null
  output_price: number | null
  cache_write_price: number | null
  cache_write_1h_price?: number | null
  cache_read_price: number | null
  input_multiplier?: number | null
  output_multiplier?: number | null
  cache_write_multiplier?: number | null
  cache_read_multiplier?: number | null
  per_request_price: number | null
}

export interface UserSupportedModelPricing {
  billing_mode: BillingMode
  input_price: number | null
  output_price: number | null
  cache_write_price: number | null
  cache_write_1h_price?: number | null
  cache_read_price: number | null
  reasoning_effort_multipliers?: Record<string, number> | null
  image_input_price: number | null
  image_output_price: number | null
  per_request_price: number | null
  intervals: UserPricingInterval[]
  /** 官方原厂价（USD / per token），来自 LiteLLM 价格表。模型不在表里或为 0 时缺失。 */
  official_input_price?: number | null
  official_output_price?: number | null
  official_cache_write_price?: number | null
  official_cache_read_price?: number | null
}

export interface UserSupportedModel {
  name: string
  platform: string
  pricing: UserSupportedModelPricing | null
}

/**
 * 渠道下单个平台的子视图：用户可访问的分组 + 该平台支持的模型。
 * 后端把一个渠道按平台聚合成 sections，前端可以把渠道名作为 row-group
 * 一次渲染，后面按 sections 顺序用 rowspan 铺开。
 */
export interface UserChannelPlatformSection {
  platform: string
  groups: UserAvailableGroup[]
  supported_models: UserSupportedModel[]
}

export interface UserAvailableChannel {
  name: string
  description: string
  platforms: UserChannelPlatformSection[]
}

/** 列出当前用户可见的「可用渠道」（与 /groups/available 保持一致，返回平数组）。 */
export async function getAvailable(options?: { signal?: AbortSignal }): Promise<UserAvailableChannel[]> {
  const { data } = await apiClient.get<UserAvailableChannel[]>('/channels/available', {
    signal: options?.signal
  })
  return data
}

/**
 * 「模型定价」展示页的"端点" = 一个 group。
 * 每个 group 自己的 rate_multiplier 决定折扣；models 是所有 active channel
 * 中关联此 group 的 supported_models 并集（按 platform 匹配）。完全不暴露
 * channel 概念。
 */
export interface UserPricingModel {
  name: string
  /**
   * 计费模式：'token'（默认）按 token 计费，input/output/cache 列展示；
   * 'per_request' / 'image' 按次/图片计费，渲染 intervals 子表（tier_label 分隔符 '-'
   * 时 pivot 成二维矩阵）。
   */
  billing_mode?: BillingMode | string
  /**
   * 渠道（channel）管理员显式配置的基础单价（USD / per token）。
   * 与 official_* 同为"基础单价"语义：site 模式仍按 group.rate_multiplier / fx_rate
   * 计算本站价，与计费链路 actualCost = totalCost × RateMultiplier 一致。
   * 未在 channel 上配置时为 nil，前端回退到对应 official_* 字段。
   */
  input_price?: number | null
  output_price?: number | null
  cache_write_price?: number | null
  cache_read_price?: number | null
  /** per_request / image 模式：每次请求/每图片的基础单价（USD）。 */
  per_request_price?: number | null
  /** per_request / image 模式：tier 分层定价（tier_label + per_request_price）。 */
  intervals?: UserPricingInterval[]
  /** LiteLLM 官方价（USD / per token）。模型不在 LiteLLM 表里或为 0 时缺失。 */
  official_input_price?: number | null
  official_output_price?: number | null
  official_cache_write_price?: number | null
  official_cache_read_price?: number | null
}

export interface UserPricingGroup {
  id: number
  name: string
  platform: string
  rate_multiplier: number
  /**
   * 仅当「商户 owner 本人」查看时出现：owner 配置的对外售价倍率。
   * rate_multiplier 仍是主站价（owner 自用按主站计费），此字段供前端做「客户视角预览」。
   * 普通用户 / 子用户 / 主站用户均无此字段。
   */
  preview_sell_rate_multiplier?: number | null
  is_exclusive: boolean
  models: UserPricingModel[]
}

/** GET /pricing/groups — 列出用户可见的定价端点（按 group 维度）。 */
export async function getPricingGroups(options?: { signal?: AbortSignal }): Promise<UserPricingGroup[]> {
  const { data } = await apiClient.get<UserPricingGroup[]>('/pricing/groups', {
    signal: options?.signal,
  })
  return data
}

/** GET /pricing/public/groups — 模型广场公开端点：只返回非专属、非订阅的活跃分组。 */
export async function getPublicPricingGroups(options?: { signal?: AbortSignal }): Promise<UserPricingGroup[]> {
  const { data } = await apiClient.get<UserPricingGroup[]>('/pricing/public/groups', {
    signal: options?.signal,
  })
  return data
}

export const userChannelsAPI = { getAvailable, getPricingGroups, getPublicPricingGroups }

export default userChannelsAPI
