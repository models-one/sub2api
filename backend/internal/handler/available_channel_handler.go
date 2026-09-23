package handler

import (
	"context"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// AvailableChannelHandler 处理用户侧「可用渠道」查询。
//
// 用户侧接口委托 ChannelService.ListAvailable，并在返回前做四层过滤：
//  1. 行过滤：只保留状态为 Active 且与当前用户可访问分组有交集的渠道；
//  2. 分组过滤：渠道的 Groups 只保留用户可访问的那些；
//  3. 平台过滤：普通分组只保留自身平台模型；Composite 分组按渠道已配置的具体模型平台
//     展开。这样既防止普通分组跨平台泄漏，也让 Composite 正确展示其多平台能力；
//  4. 字段白名单：仅返回用户需要的字段（省略 BillingModelSource / RestrictModels
//     / 内部 ID / Status 等管理字段）。
type AvailableChannelHandler struct {
	channelService  *service.ChannelService
	apiKeyService   *service.APIKeyService
	settingService  *service.SettingService
	billingService  *service.BillingService
	merchantPricing *service.MerchantPricingService
	accountService  *service.AccountService
}

// NewAvailableChannelHandler 创建用户侧可用渠道 handler。
func NewAvailableChannelHandler(
	channelService *service.ChannelService,
	apiKeyService *service.APIKeyService,
	settingService *service.SettingService,
	billingService *service.BillingService,
	merchantPricing *service.MerchantPricingService,
	accountService *service.AccountService,
) *AvailableChannelHandler {
	return &AvailableChannelHandler{
		channelService:  channelService,
		apiKeyService:   apiKeyService,
		settingService:  settingService,
		billingService:  billingService,
		merchantPricing: merchantPricing,
		accountService:  accountService,
	}
}

// featureEnabled 返回 available-channels 开关是否启用。默认关闭（opt-in）。
func (h *AvailableChannelHandler) featureEnabled(c *gin.Context) bool {
	if h.settingService == nil {
		return false
	}
	return h.settingService.GetAvailableChannelsRuntime(c.Request.Context()).Enabled
}

// userAvailableGroup 用户可见的分组概要（白名单字段）。
//
// 前端据此区分专属 vs 公开分组（IsExclusive）、订阅 vs 标准分组（SubscriptionType，
// 订阅视觉加深），并展示默认倍率与高峰倍率规则；用户专属倍率前端走
// /groups/rates，和 API 密钥页面保持一致。
type userAvailableGroup struct {
	ID                 int64   `json:"id"`
	Name               string  `json:"name"`
	Platform           string  `json:"platform"`
	SubscriptionType   string  `json:"subscription_type"`
	RateMultiplier     float64 `json:"rate_multiplier"`
	PeakRateEnabled    bool    `json:"peak_rate_enabled"`
	PeakStart          string  `json:"peak_start"`
	PeakEnd            string  `json:"peak_end"`
	PeakRateMultiplier float64 `json:"peak_rate_multiplier"`
	IsExclusive        bool    `json:"is_exclusive"`
}

// userSupportedModelPricing 用户可见的定价字段白名单。
//
// official_* 字段来自 LiteLLM 价格表（单位 USD / per token），用于前端做"本站价
// vs 官方价"对比展示。当模型在 LiteLLM 列表中未找到或价格为 0 时为 nil。
type userSupportedModelPricing struct {
	BillingMode                string                   `json:"billing_mode"`
	InputPrice                 *float64                 `json:"input_price"`
	OutputPrice                *float64                 `json:"output_price"`
	CacheWritePrice            *float64                 `json:"cache_write_price"`
	CacheWrite1hPrice          *float64                 `json:"cache_write_1h_price"`
	CacheReadPrice             *float64                 `json:"cache_read_price"`
	ReasoningEffortMultipliers map[string]float64       `json:"reasoning_effort_multipliers,omitempty"`
	ImageInputPrice            *float64                 `json:"image_input_price"`
	ImageOutputPrice           *float64                 `json:"image_output_price"`
	PerRequestPrice            *float64                 `json:"per_request_price"`
	Intervals                  []userPricingIntervalDTO `json:"intervals"`
	OfficialInputPrice         *float64                 `json:"official_input_price,omitempty"`
	OfficialOutputPrice        *float64                 `json:"official_output_price,omitempty"`
	OfficialCacheWritePrice    *float64                 `json:"official_cache_write_price,omitempty"`
	OfficialCacheReadPrice     *float64                 `json:"official_cache_read_price,omitempty"`
}

// userPricingIntervalDTO 定价区间白名单（去掉内部 ID、SortOrder 等前端不渲染的字段）。
type userPricingIntervalDTO struct {
	MinTokens            int      `json:"min_tokens"`
	MaxTokens            *int     `json:"max_tokens"`
	TierLabel            string   `json:"tier_label,omitempty"`
	InputPrice           *float64 `json:"input_price"`
	OutputPrice          *float64 `json:"output_price"`
	CacheWritePrice      *float64 `json:"cache_write_price"`
	CacheWrite1hPrice    *float64 `json:"cache_write_1h_price"`
	CacheReadPrice       *float64 `json:"cache_read_price"`
	InputMultiplier      *float64 `json:"input_multiplier"`
	OutputMultiplier     *float64 `json:"output_multiplier"`
	CacheWriteMultiplier *float64 `json:"cache_write_multiplier"`
	CacheReadMultiplier  *float64 `json:"cache_read_multiplier"`
	PerRequestPrice      *float64 `json:"per_request_price"`
}

// userSupportedModel 用户可见的支持模型条目。
type userSupportedModel struct {
	Name     string                     `json:"name"`
	Platform string                     `json:"platform"`
	Pricing  *userSupportedModelPricing `json:"pricing"`
}

// userChannelPlatformSection 单渠道内某个平台的子视图：用户可见的分组 + 该平台
// 支持的模型。按 platform 聚合后让前端可以把渠道名作为 row-group 一次渲染，
// 后面的平台行按 sections 顺序铺开。
type userChannelPlatformSection struct {
	Platform        string               `json:"platform"`
	Groups          []userAvailableGroup `json:"groups"`
	SupportedModels []userSupportedModel `json:"supported_models"`
}

// userAvailableChannel 用户可见的渠道条目（白名单字段）。
//
// 每个渠道聚合为一条记录，内嵌 platforms 子数组：每个 section 对应一个平台，
// 包含该平台的 groups 和 supported_models。
type userAvailableChannel struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Platforms   []userChannelPlatformSection `json:"platforms"`
}

// buildVisibleChannels 共享构建逻辑：拉用户可见 groups → 过滤 channels 的 groups
// → 过滤 supported_models（防跨平台泄漏）→ enrich 官方价。
// 不做 feature flag 检查、不做 owner 视角的 sell_rate 替换（调用方按需做）。
func (h *AvailableChannelHandler) buildVisibleChannels(c *gin.Context, userID int64) ([]userAvailableChannel, error) {
	userGroups, err := h.apiKeyService.GetAvailableGroups(c.Request.Context(), userID)
	if err != nil {
		return nil, err
	}
	allowedGroupIDs := make(map[int64]struct{}, len(userGroups))
	for i := range userGroups {
		allowedGroupIDs[userGroups[i].ID] = struct{}{}
	}

	channels, err := h.channelService.ListAvailable(c.Request.Context())
	if err != nil {
		return nil, err
	}

	out := make([]userAvailableChannel, 0, len(channels))
	for _, ch := range channels {
		if ch.Status != service.StatusActive {
			continue
		}
		visibleGroups := filterUserVisibleGroups(ch.Groups, allowedGroupIDs)
		if len(visibleGroups) == 0 {
			continue
		}
		sections := buildPlatformSections(ch, visibleGroups)
		if len(sections) == 0 {
			continue
		}
		out = append(out, userAvailableChannel{
			Name:        ch.Name,
			Description: ch.Description,
			Platforms:   sections,
		})
	}

	h.enrichOfficialPricing(out)
	return out, nil
}

// List 列出当前用户可见的「可用渠道」。
// GET /api/v1/channels/available
func (h *AvailableChannelHandler) List(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	// Feature 未启用时返回空数组（不暴露渠道信息）。检查放在认证之后，
	// 保持与未开关前的 401 行为一致：未登录先 401，登录后再按开关决定。
	if !h.featureEnabled(c) {
		response.Success(c, []userAvailableChannel{})
		return
	}

	out, err := h.buildVisibleChannels(c, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.applyMerchantSellRate(c, subject.UserID, out)

	response.Success(c, out)
}

// userPricingModel 端点（group）下的单条模型。
//
// 价格字段两套：
//   - input_price / output_price / cache_write_price / cache_read_price：
//     渠道（channel）管理员显式配置的基础单价（per token, USD）。nil 表示该
//     字段未在 channel 上配置，前端回退到 official_* 字段。
//   - official_*：LiteLLM 官方价（per token, USD）。
//
// 两套都是"基础单价"语义：前端在 site 模式下仍然按 (group.rate_multiplier /
// fx_rate) 乘出本站价，和实际计费链路 `actualCost = totalCost × RateMultiplier`
// 保持一致 —— 渠道价覆盖只是替换 token 单价数据源，不绕过分组倍率。
//
// billing_mode / intervals 用于非 token 模式（per_request / image）的模型：
//   - billing_mode 为空或 "token" 时，前端按 token 列展示（input/output/cache）；
//   - billing_mode 为 "per_request" / "image" 时，前端按 intervals 列表渲染
//     tier 分层定价（tier_label 含分隔符 "-" 时进一步 pivot 成二维矩阵）。
type userPricingModel struct {
	Name                    string                   `json:"name"`
	BillingMode             string                   `json:"billing_mode,omitempty"`
	InputPrice              *float64                 `json:"input_price,omitempty"`
	OutputPrice             *float64                 `json:"output_price,omitempty"`
	CacheWritePrice         *float64                 `json:"cache_write_price,omitempty"`
	CacheReadPrice          *float64                 `json:"cache_read_price,omitempty"`
	PerRequestPrice         *float64                 `json:"per_request_price,omitempty"`
	Intervals               []userPricingIntervalDTO `json:"intervals,omitempty"`
	OfficialInputPrice      *float64                 `json:"official_input_price,omitempty"`
	OfficialOutputPrice     *float64                 `json:"official_output_price,omitempty"`
	OfficialCacheWritePrice *float64                 `json:"official_cache_write_price,omitempty"`
	OfficialCacheReadPrice  *float64                 `json:"official_cache_read_price,omitempty"`
}

// userPricingGroup 「模型定价」展示页的端点 = 一个 group。
// 每个端点的"折扣"由 rate_multiplier 自己决定，跟具体上游 channel 无关。
//
// PreviewSellRateMultiplier 仅在「商户 owner 本人」查看时非 nil：owner 自用按主站价
// 计费（rate_multiplier 保持主站倍率），但会额外带上自己配置的售价倍率，供前端做
// "客户视角预览"。纯展示，前端预览开关关闭时不使用此字段。
type userPricingGroup struct {
	ID                        int64              `json:"id"`
	Name                      string             `json:"name"`
	Platform                  string             `json:"platform"`
	RateMultiplier            float64            `json:"rate_multiplier"`
	PreviewSellRateMultiplier *float64           `json:"preview_sell_rate_multiplier,omitempty"`
	IsExclusive               bool               `json:"is_exclusive"`
	Models                    []userPricingModel `json:"models"`
}

// PricingGroupList 列出用户可见的「定价端点」——每个端点对应一个 group。
//
// 模型集合算法（默认全自动，admin 需要时可手动覆盖）：
//  0. 分组开启了自定义模型列表（models_list）→ 直接以它为准，跳过后续推断
//  1. 拉该 group 下所有 active account（account_groups 多对多）
//  2. 每个 account 的"支持模型"= account.credentials.model_mapping 的非通配
//     符 from key 集合：
//     - 空 mapping 或只有通配符 → 视为"透传/不限制"，不参与并集
//     - 有非通配符 mapping → 这些 key 就是该账号显式支持的模型
//  3. 所有参与的 account 之间取并集 → 该 group 的模型列表
//  4. 没有 account / 全部透传 → 用 LiteLLM 按 group.platform 过滤的 chat
//     模型作为兜底
//
// 不受 available_channels_enabled 开关限制。商户 sub_user 视角下，rate_multiplier
// 替换为商户配置的 sell_rate（与计费路径一致）。
//
// GET /api/v1/pricing/groups
func (h *AvailableChannelHandler) PricingGroupList(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	ctx := c.Request.Context()
	userGroups, err := h.apiKeyService.GetAvailableGroups(ctx, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if len(userGroups) == 0 {
		response.Success(c, []userPricingGroup{})
		return
	}

	// 官方价 lookup 缓存
	priceCache := make(map[string]*service.ModelPricing, 32)
	lookupOfficial := func(model string) *service.ModelPricing {
		if p, ok := priceCache[model]; ok {
			return p
		}
		if h.billingService == nil {
			priceCache[model] = nil
			return nil
		}
		p, err := h.billingService.GetModelPricing(model)
		if err != nil {
			priceCache[model] = nil
			return nil
		}
		priceCache[model] = p
		return p
	}

	// LiteLLM 兜底：每个 platform 一次拉全表缓存。
	litellmByPlatform := map[string][]string{}
	getLiteLLMModels := func(platform string) []string {
		if v, ok := litellmByPlatform[platform]; ok {
			return v
		}
		var out []string
		if h.billingService != nil {
			for _, e := range h.billingService.ListAllModelPricings(platform) {
				out = append(out, e.Model)
			}
		}
		litellmByPlatform[platform] = out
		return out
	}

	out := make([]userPricingGroup, 0, len(userGroups))
	for i := range userGroups {
		g := userGroups[i]
		// 商户 sub_user 视角下的 rate 替换
		rate := g.RateMultiplier
		if h.merchantPricing != nil {
			if sell, ok := h.merchantPricing.LookupSellRateForUser(ctx, subject.UserID, g.ID); ok {
				rate = sell
			}
		}

		// 计算模型集合：分组自定义列表优先，否则取账号 model_mapping 非通配 from key 并集
		modelNames := h.resolveGroupModelsByAccount(ctx, &g, getLiteLLMModels)

		item := userPricingGroup{
			ID:             g.ID,
			Name:           g.Name,
			Platform:       g.Platform,
			RateMultiplier: rate,
			IsExclusive:    g.IsExclusive,
			Models:         []userPricingModel{},
		}
		// 商户 owner 本人视角：额外带上自己配置的售价倍率供「客户视角预览」。
		// rate_multiplier 仍是主站价（owner 自用按主站计费），预览字段只是展示。
		if h.merchantPricing != nil {
			if preview, ok := h.merchantPricing.LookupOwnerSellRate(ctx, subject.UserID, g.ID); ok {
				p := preview
				item.PreviewSellRateMultiplier = &p
			}
		}
		for _, name := range modelNames {
			item.Models = append(item.Models, h.buildPricingModel(ctx, g.ID, name, lookupOfficial))
		}
		out = append(out, item)
	}

	response.Success(c, out)
}

// PricingGroupListPublic 返回所有公开（非专属、非订阅）的活跃分组，用于未登录访客
// 的"模型广场"展示。和 PricingGroupList 共享同一份模型解析与官方价 lookup 逻辑，但
// 不应用任何用户/商户上下文。
//
// GET /api/v1/pricing/public/groups
func (h *AvailableChannelHandler) PricingGroupListPublic(c *gin.Context) {
	ctx := c.Request.Context()
	groups, err := h.apiKeyService.ListPublicGroups(ctx)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if len(groups) == 0 {
		response.Success(c, []userPricingGroup{})
		return
	}

	priceCache := make(map[string]*service.ModelPricing, 32)
	lookupOfficial := func(model string) *service.ModelPricing {
		if p, ok := priceCache[model]; ok {
			return p
		}
		if h.billingService == nil {
			priceCache[model] = nil
			return nil
		}
		p, err := h.billingService.GetModelPricing(model)
		if err != nil {
			priceCache[model] = nil
			return nil
		}
		priceCache[model] = p
		return p
	}

	litellmByPlatform := map[string][]string{}
	getLiteLLMModels := func(platform string) []string {
		if v, ok := litellmByPlatform[platform]; ok {
			return v
		}
		var out []string
		if h.billingService != nil {
			for _, e := range h.billingService.ListAllModelPricings(platform) {
				out = append(out, e.Model)
			}
		}
		litellmByPlatform[platform] = out
		return out
	}

	// 白标域名感知：DomainDetectMiddleware 已按请求 Host 注入 MerchantContext（即使匿名请求）。
	// 命中某商户域名时，用该商户配置的 sell_rate 覆盖主站 rate_multiplier，让白标广场的
	// 展示价 = 该商户客户实际付的价，而不是主站价。未配 sell_rate 的 group 回退主站价。
	var merchantID int64
	if h.merchantPricing != nil {
		if mctx := middleware.MerchantFromContext(c); mctx != nil && mctx.Merchant != nil {
			merchantID = mctx.Merchant.ID
		}
	}

	out := make([]userPricingGroup, 0, len(groups))
	for i := range groups {
		g := groups[i]
		rate := g.RateMultiplier
		if merchantID > 0 {
			if sell, ok := h.merchantPricing.LookupSellRateByMerchant(ctx, merchantID, g.ID); ok {
				rate = sell
			}
		}
		modelNames := h.resolveGroupModelsByAccount(ctx, &g, getLiteLLMModels)
		item := userPricingGroup{
			ID:             g.ID,
			Name:           g.Name,
			Platform:       g.Platform,
			RateMultiplier: rate,
			IsExclusive:    g.IsExclusive,
			Models:         []userPricingModel{},
		}
		for _, name := range modelNames {
			item.Models = append(item.Models, h.buildPricingModel(ctx, g.ID, name, lookupOfficial))
		}
		out = append(out, item)
	}

	response.Success(c, out)
}

// buildPricingModel 拼装单条模型的定价 DTO：渠道配置的基础单价（input/output/
// cache_write/cache_read）覆盖到独立字段，LiteLLM 官方价填到 official_*。
// 两套字段都保持"基础单价"语义，前端按 group.rate_multiplier / fx_rate 算本站价。
//
// channelService 未注入或 group 未绑定 channel 时，channel 价部分留空。
func (h *AvailableChannelHandler) buildPricingModel(
	ctx context.Context,
	groupID int64,
	name string,
	lookupOfficial func(string) *service.ModelPricing,
) userPricingModel {
	m := userPricingModel{Name: name}
	if p := lookupOfficial(name); p != nil {
		m.OfficialInputPrice = positiveFloatPtr(p.InputPricePerToken)
		m.OfficialOutputPrice = positiveFloatPtr(p.OutputPricePerToken)
		m.OfficialCacheWritePrice = positiveFloatPtr(p.CacheCreationPricePerToken)
		m.OfficialCacheReadPrice = positiveFloatPtr(p.CacheReadPricePerToken)
	}
	if h.channelService != nil {
		if cp := h.channelService.GetChannelModelPricing(ctx, groupID, name); cp != nil {
			m.BillingMode = string(cp.BillingMode)
			m.InputPrice = cp.InputPrice
			m.OutputPrice = cp.OutputPrice
			m.CacheWritePrice = cp.CacheWritePrice
			m.CacheReadPrice = cp.CacheReadPrice
			m.PerRequestPrice = cp.PerRequestPrice
			if len(cp.Intervals) > 0 {
				m.Intervals = make([]userPricingIntervalDTO, 0, len(cp.Intervals))
				for _, iv := range cp.Intervals {
					m.Intervals = append(m.Intervals, userPricingIntervalDTO{
						MinTokens:       iv.MinTokens,
						MaxTokens:       iv.MaxTokens,
						TierLabel:       iv.TierLabel,
						InputPrice:      iv.InputPrice,
						OutputPrice:     iv.OutputPrice,
						CacheWritePrice: iv.CacheWritePrice,
						CacheReadPrice:  iv.CacheReadPrice,
						PerRequestPrice: iv.PerRequestPrice,
					})
				}
			}
		}
	}
	return m
}

// resolveGroupModelsByAccount 计算 group 在「模型列表」页展示的模型集合。
//
// 优先级：
//  1. 分组开启了自定义模型列表（models_list.enabled 且非空）→ 直接以它为准。
//     这是 admin 的手动闸门：账号 mapping 写法古怪、或新模型还没进 LiteLLM 价目表
//     时，后台勾一下就能让模型出现在页面上，不必等发版。保留后台配置的顺序。
//  2. 否则按账号推断：该 group 下所有 active account 的 model_mapping 非通配符
//     from key 取**并集**。
//     - 空 mapping / 只有通配符 from → 该账号透传，不参与并集
//     历史实现这里取的是交集，导致「三个账号里只有一个支持的新模型」被静默抹掉；
//     网关 /v1/models（GatewayService.GetAvailableModels）一直是并集，这里跟它对齐。
//  3. 无 account / 全部透传 → LiteLLM 按 platform 兜底
func (h *AvailableChannelHandler) resolveGroupModelsByAccount(
	ctx context.Context,
	g *service.Group,
	getLiteLLMModels func(platform string) []string,
) []string {
	if g == nil {
		return nil
	}
	if models := groupCustomModelsList(g); len(models) > 0 {
		return models
	}

	var accounts []service.Account
	if h.accountService != nil {
		list, err := h.accountService.ListByGroup(ctx, g.ID)
		if err == nil {
			accounts = list
		}
	}
	return resolveGroupModelsFromAccounts(g.Platform, accounts, func() []string {
		return getLiteLLMModels(g.Platform)
	})
}

// groupCustomModelsList 取分组自定义模型列表中可展示的条目（去空、去重、去通配符）。
// 通配符条目在网关侧是过滤 pattern，价格页展示不出具体单价，跳过。
//
// 字段来源在上游 0.2.4（cff3f8985，破坏性变更）从 ModelsListConfig 更名为
// ModelAllowlist：同一份数据的语义由「仅影响模型列表展示」升级为「同时约束请求准入」。
// 本函数只读它做展示，跟着改名即可；但要注意语义已经变了——
// 本 fork 3aab54ba7 把这份列表当成价格页的「手动闸门」（可以加出账号没有的型号），
// 而上游现在拿同一份列表在网关做白名单拒绝。两者叠加的后果见合并说明。
func groupCustomModelsList(g *service.Group) []string {
	if !g.ModelAllowlistEnabled() {
		return nil
	}
	seen := make(map[string]struct{}, len(g.ModelAllowlist.Models))
	out := make([]string, 0, len(g.ModelAllowlist.Models))
	for _, model := range g.ModelAllowlist.Models {
		model = strings.TrimSpace(model)
		if model == "" || strings.HasSuffix(model, "*") {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

// resolveGroupModelsFromAccounts 是账号推断分支的纯算法部分（并集 + LiteLLM 兜底）。
func resolveGroupModelsFromAccounts(
	platform string,
	accounts []service.Account,
	litellmFallback func() []string,
) []string {
	union := make(map[string]struct{})
	for i := range accounts {
		acc := accounts[i]
		if acc.Status != service.StatusActive {
			continue
		}
		if acc.Platform != platform {
			// 防御性：理论上 account.platform 跟 group.platform 应该一致
			continue
		}
		mapping := acc.GetModelMapping()
		if len(mapping) == 0 {
			// 透传，不参与并集
			continue
		}
		for from := range mapping {
			if strings.HasSuffix(from, "*") {
				continue // 通配符 from 不算具体模型
			}
			union[from] = struct{}{}
		}
	}

	if len(union) == 0 {
		if litellmFallback == nil {
			return nil
		}
		return append([]string(nil), litellmFallback()...)
	}
	out := make([]string, 0, len(union))
	for k := range union {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// applyMerchantSellRate 商户 sub_user 视角下，把每个 group 的 RateMultiplier
// 替换成商户配置的 sell_rate（商户没配的 group 维持主站价）。
// 这保证前端展示的"我看到的倍率"和实际计费时 sub_user 余额扣的钱一致。
func (h *AvailableChannelHandler) applyMerchantSellRate(c *gin.Context, userID int64, out []userAvailableChannel) {
	if h.merchantPricing == nil || userID <= 0 {
		return
	}
	ctx := c.Request.Context()
	cache := make(map[int64]float64, 8)
	miss := make(map[int64]struct{}, 8)
	lookup := func(groupID int64) (float64, bool) {
		if v, ok := cache[groupID]; ok {
			return v, true
		}
		if _, hit := miss[groupID]; hit {
			return 0, false
		}
		v, ok := h.merchantPricing.LookupSellRateForUser(ctx, userID, groupID)
		if ok {
			cache[groupID] = v
		} else {
			miss[groupID] = struct{}{}
		}
		return v, ok
	}
	for ci := range out {
		for si := range out[ci].Platforms {
			groups := out[ci].Platforms[si].Groups
			for gi := range groups {
				if v, ok := lookup(groups[gi].ID); ok {
					groups[gi].RateMultiplier = v
				}
			}
		}
	}
}

// enrichOfficialPricing 为每个模型补充官方价（来自 LiteLLM 价格表，USD/per token）。
// 查不到或价格为 0 时静默跳过，前端按 nil 展示为"-"。
func (h *AvailableChannelHandler) enrichOfficialPricing(out []userAvailableChannel) {
	if h.billingService == nil {
		return
	}
	cache := make(map[string]*service.ModelPricing, 32)
	lookup := func(model string) *service.ModelPricing {
		if p, ok := cache[model]; ok {
			return p
		}
		p, err := h.billingService.GetModelPricing(model)
		if err != nil {
			cache[model] = nil
			return nil
		}
		cache[model] = p
		return p
	}
	for ci := range out {
		for si := range out[ci].Platforms {
			models := out[ci].Platforms[si].SupportedModels
			for mi := range models {
				if models[mi].Pricing == nil {
					continue
				}
				p := lookup(models[mi].Name)
				if p == nil {
					continue
				}
				models[mi].Pricing.OfficialInputPrice = positiveFloatPtr(p.InputPricePerToken)
				models[mi].Pricing.OfficialOutputPrice = positiveFloatPtr(p.OutputPricePerToken)
				models[mi].Pricing.OfficialCacheWritePrice = positiveFloatPtr(p.CacheCreationPricePerToken)
				models[mi].Pricing.OfficialCacheReadPrice = positiveFloatPtr(p.CacheReadPricePerToken)
			}
		}
	}
}

func positiveFloatPtr(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}

// buildPlatformSections 把一个渠道按 visibleGroups 的平台集合拆成有序的 section 列表：
// 每个 section 对应一个具体平台，只包含该平台的 groups 和 supported_models。
//
// Composite 分组可访问渠道中所有已配置的具体平台，因此会被展开到每个有支持模型的
// 平台 section。普通分组仍严格留在自身平台，避免跨平台模型信息泄漏。Composite 渠道
// 尚未配置任何模型时保留 composite section，以便前端继续展示该分组和“未配置模型”状态。
// 输出按 platform 字母序稳定排序，便于前端等效比较与回归测试。
func buildPlatformSections(
	ch service.AvailableChannel,
	visibleGroups []userAvailableGroup,
) []userChannelPlatformSection {
	groupsByPlatform := make(map[string][]userAvailableGroup, 4)
	compositeGroups := make([]userAvailableGroup, 0, 1)
	for _, g := range visibleGroups {
		if g.Platform == "" {
			continue
		}
		if g.Platform == service.PlatformComposite {
			compositeGroups = append(compositeGroups, g)
			continue
		}
		groupsByPlatform[g.Platform] = append(groupsByPlatform[g.Platform], g)
	}

	if len(compositeGroups) > 0 {
		modelPlatforms := make(map[string]struct{}, len(ch.SupportedModels))
		for i := range ch.SupportedModels {
			if platform := ch.SupportedModels[i].Platform; platform != "" {
				modelPlatforms[platform] = struct{}{}
			}
		}
		if len(modelPlatforms) == 0 {
			groupsByPlatform[service.PlatformComposite] = append(
				groupsByPlatform[service.PlatformComposite],
				compositeGroups...,
			)
		} else {
			for platform := range modelPlatforms {
				groupsByPlatform[platform] = append(groupsByPlatform[platform], compositeGroups...)
			}
		}
	}
	if len(groupsByPlatform) == 0 {
		return nil
	}

	platforms := make([]string, 0, len(groupsByPlatform))
	for p := range groupsByPlatform {
		platforms = append(platforms, p)
	}
	sort.Strings(platforms)

	sections := make([]userChannelPlatformSection, 0, len(platforms))
	for _, platform := range platforms {
		platformSet := map[string]struct{}{platform: {}}
		sections = append(sections, userChannelPlatformSection{
			Platform:        platform,
			Groups:          groupsByPlatform[platform],
			SupportedModels: toUserSupportedModels(ch.SupportedModels, platformSet),
		})
	}
	return sections
}

// filterUserVisibleGroups 仅保留用户可访问的分组。
func filterUserVisibleGroups(
	groups []service.AvailableGroupRef,
	allowed map[int64]struct{},
) []userAvailableGroup {
	visible := make([]userAvailableGroup, 0, len(groups))
	for _, g := range groups {
		if _, ok := allowed[g.ID]; !ok {
			continue
		}
		visible = append(visible, userAvailableGroup{
			ID:                 g.ID,
			Name:               g.Name,
			Platform:           g.Platform,
			SubscriptionType:   g.SubscriptionType,
			RateMultiplier:     g.RateMultiplier,
			PeakRateEnabled:    g.PeakRateEnabled,
			PeakStart:          g.PeakStart,
			PeakEnd:            g.PeakEnd,
			PeakRateMultiplier: g.PeakRateMultiplier,
			IsExclusive:        g.IsExclusive,
		})
	}
	return visible
}

// toUserSupportedModels 将 service 层支持模型转换为用户 DTO（字段白名单）。
// 仅保留平台在 allowedPlatforms 中的条目，防止跨平台模型信息泄漏。
// allowedPlatforms 为 nil 时不做平台过滤（保留全部，供测试或明确无过滤场景使用）。
func toUserSupportedModels(
	src []service.SupportedModel,
	allowedPlatforms map[string]struct{},
) []userSupportedModel {
	out := make([]userSupportedModel, 0, len(src))
	for i := range src {
		m := src[i]
		if allowedPlatforms != nil {
			if _, ok := allowedPlatforms[m.Platform]; !ok {
				continue
			}
		}
		out = append(out, userSupportedModel{
			Name:     m.Name,
			Platform: m.Platform,
			Pricing:  toUserPricing(m.Pricing),
		})
	}
	return out
}

// toUserPricingIntervals 将定价区间转换为用户 DTO 白名单形态；nil 入参返回 nil（JSON omitempty 可省略）。
func toUserPricingIntervals(src []service.PricingInterval) []userPricingIntervalDTO {
	if src == nil {
		return nil
	}
	intervals := make([]userPricingIntervalDTO, 0, len(src))
	for _, iv := range src {
		intervals = append(intervals, userPricingIntervalDTO{
			MinTokens:            iv.MinTokens,
			MaxTokens:            iv.MaxTokens,
			TierLabel:            iv.TierLabel,
			InputPrice:           iv.InputPrice,
			OutputPrice:          iv.OutputPrice,
			CacheWritePrice:      iv.CacheWritePrice,
			CacheWrite1hPrice:    iv.CacheWrite1hPrice,
			CacheReadPrice:       iv.CacheReadPrice,
			InputMultiplier:      iv.InputMultiplier,
			OutputMultiplier:     iv.OutputMultiplier,
			CacheWriteMultiplier: iv.CacheWriteMultiplier,
			CacheReadMultiplier:  iv.CacheReadMultiplier,
			PerRequestPrice:      iv.PerRequestPrice,
		})
	}
	return intervals
}

// toUserPricing 将 service 层定价转换为用户 DTO；入参为 nil 时返回 nil。
func toUserPricing(p *service.ChannelModelPricing) *userSupportedModelPricing {
	if p == nil {
		return nil
	}
	intervals := toUserPricingIntervals(p.Intervals)
	if intervals == nil {
		// 用户侧定价的 intervals 固定输出数组（空配置为 []），保持既有契约。
		intervals = []userPricingIntervalDTO{}
	}
	billingMode := string(p.BillingMode)
	if billingMode == "" {
		billingMode = string(service.BillingModeToken)
	}
	return &userSupportedModelPricing{
		BillingMode:                billingMode,
		InputPrice:                 p.InputPrice,
		OutputPrice:                p.OutputPrice,
		CacheWritePrice:            p.CacheWritePrice,
		CacheWrite1hPrice:          p.CacheWrite1hPrice,
		CacheReadPrice:             p.CacheReadPrice,
		ReasoningEffortMultipliers: p.ReasoningEffortMultipliers,
		ImageInputPrice:            p.ImageInputPrice,
		ImageOutputPrice:           p.ImageOutputPrice,
		PerRequestPrice:            p.PerRequestPrice,
		Intervals:                  intervals,
	}
}
