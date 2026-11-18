//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// CalculateCostUnified
// ---------------------------------------------------------------------------

func TestCalculateCostUnified_NilResolver_FallsBackToOldPath(t *testing.T) {
	svc := newTestBillingService()

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}
	input := CostInput{
		Model:          "claude-sonnet-4",
		Tokens:         tokens,
		RateMultiplier: 1.0,
		Resolver:       nil, // no resolver
	}
	cost, err := svc.CalculateCostUnified(input)
	require.NoError(t, err)

	// Should match the old-path result exactly
	expected, err := svc.calculateCostInternal("claude-sonnet-4", tokens, 1.0, "", nil)
	require.NoError(t, err)
	require.InDelta(t, expected.TotalCost, cost.TotalCost, 1e-10)
	require.InDelta(t, expected.ActualCost, cost.ActualCost, 1e-10)
	// BillingMode is NOT set by old path through CalculateCostUnified (resolver == nil)
	require.Empty(t, cost.BillingMode)
}

func TestCalculateCostUnified_TokenMode(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}
	input := CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         tokens,
		RateMultiplier: 1.5,
		Resolver:       resolver,
	}
	cost, err := bs.CalculateCostUnified(input)
	require.NoError(t, err)
	require.NotNil(t, cost)

	// Verify token billing: Input: 1000*3e-6=0.003, Output: 500*15e-6=0.0075
	expectedTotal := 1000*3e-6 + 500*15e-6
	require.InDelta(t, expectedTotal, cost.TotalCost, 1e-10)
	require.InDelta(t, expectedTotal*1.5, cost.ActualCost, 1e-10)
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
}

func TestCalculateCostUnified_TokenModeAppliesRateMultiplierToImageTokens(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 600, ImageOutputTokens: 100}
	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         tokens,
		RateMultiplier: 3.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)

	textInput := 1000 * 3e-6
	textOutput := 500 * 15e-6
	imageOutput := 100 * 15e-6
	require.InDelta(t, textInput+textOutput+imageOutput, cost.TotalCost, 1e-10)
	require.InDelta(t, (textInput+textOutput+imageOutput)*3.0, cost.ActualCost, 1e-10)
	require.InDelta(t, imageOutput, cost.ImageOutputCost, 1e-10)
}

func TestCalculateCostUnified_PerRequestMode(t *testing.T) {
	// Set up a ChannelService with a per-request pricing channel
	cs := newTestChannelServiceWithCache(t, &channelCache{
		pricingByGroupModel: map[channelModelKey]*ChannelModelPricing{
			{groupID: 1, model: "claude-sonnet-4"}: {
				BillingMode:     BillingModePerRequest,
				PerRequestPrice: testPtrFloat64(0.05),
			},
		},
		channelByGroupID: map[int64]*Channel{
			1: {ID: 1, Status: StatusActive},
		},
		groupPlatform:           map[int64]string{1: ""},
		wildcardByGroupPlatform: map[channelGroupPlatformKey][]*wildcardPricingEntry{},
		mappingByGroupModel:     map[channelModelKey]string{},
		wildcardMappingByGP:     map[channelGroupPlatformKey][]*wildcardMappingEntry{},
		byID:                    map[int64]*Channel{},
	})

	bs := newTestBillingService()
	resolver := NewModelPricingResolver(cs, bs)
	groupID := int64(1)

	input := CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		GroupID:        &groupID,
		Tokens:         UsageTokens{InputTokens: 100, OutputTokens: 50},
		RequestCount:   3,
		RateMultiplier: 2.0,
		Resolver:       resolver,
	}
	cost, err := bs.CalculateCostUnified(input)
	require.NoError(t, err)
	require.NotNil(t, cost)

	// 3 requests * $0.05 = $0.15
	require.InDelta(t, 0.15, cost.TotalCost, 1e-10)
	// ActualCost = 0.15 * 2.0 = 0.30
	require.InDelta(t, 0.30, cost.ActualCost, 1e-10)
	require.Equal(t, string(BillingModePerRequest), cost.BillingMode)
}

func TestCalculateCostUnified_ImageMode(t *testing.T) {
	cs := newTestChannelServiceWithCache(t, &channelCache{
		pricingByGroupModel: map[channelModelKey]*ChannelModelPricing{
			{groupID: 2, model: "gemini-image"}: {
				BillingMode:     BillingModeImage,
				PerRequestPrice: testPtrFloat64(0.10),
			},
		},
		channelByGroupID: map[int64]*Channel{
			2: {ID: 2, Status: StatusActive},
		},
		groupPlatform:           map[int64]string{2: ""},
		wildcardByGroupPlatform: map[channelGroupPlatformKey][]*wildcardPricingEntry{},
		mappingByGroupModel:     map[channelModelKey]string{},
		wildcardMappingByGP:     map[channelGroupPlatformKey][]*wildcardMappingEntry{},
		byID:                    map[int64]*Channel{},
	})

	bs := &BillingService{
		cfg:            &config.Config{},
		fallbackPrices: map[string]*ModelPricing{},
	}
	resolver := NewModelPricingResolver(cs, bs)
	groupID := int64(2)

	input := CostInput{
		Ctx:            context.Background(),
		Model:          "gemini-image",
		GroupID:        &groupID,
		Tokens:         UsageTokens{},
		RequestCount:   2,
		RateMultiplier: 1.0,
		Resolver:       resolver,
	}
	cost, err := bs.CalculateCostUnified(input)
	require.NoError(t, err)
	require.NotNil(t, cost)

	// 2 * $0.10 = $0.20
	require.InDelta(t, 0.20, cost.TotalCost, 1e-10)
	require.InDelta(t, 0.20, cost.ActualCost, 1e-10)
	require.Equal(t, string(BillingModeImage), cost.BillingMode)
}

func channelTimeResolvedForTest(base *ModelPricing, intervals []PricingInterval) *ResolvedPricing {
	return &ResolvedPricing{
		Mode:        BillingModeToken,
		BasePricing: base,
		Intervals:   intervals,
		Source:      PricingSourceChannel,
		channelPricing: &ChannelModelPricing{
			BillingMode: BillingModeToken,
			TimePricing: &ChannelTimePricing{
				Timezone: "Asia/Shanghai",
				Periods: []ChannelTimePricingPeriod{{
					StartTime:  "09:00",
					EndTime:    "12:00",
					Multiplier: 2,
				}},
			},
		},
		longContextPricingEnabled: true,
	}
}

func TestCalculateCostUnified_ChannelTimePricingScalesBaseAndActualCost(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	resolved := channelTimeResolvedForTest(&ModelPricing{InputPricePerToken: 0.001}, nil)

	cost, err := billing.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          "model",
		Tokens:         UsageTokens{InputTokens: 1000},
		RateMultiplier: 0.8,
		Resolver:       &ModelPricingResolver{},
		Resolved:       resolved,
		PricingAt:      time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, 2.0, cost.InputCost, 1e-12)
	require.InDelta(t, 2.0, cost.TotalCost, 1e-12)
	require.InDelta(t, 1.6, cost.ActualCost, 1e-12)
}

func TestCalculateCostUnified_ChannelTimePricingScalesMatchingInterval(t *testing.T) {
	intervalInputPrice := 0.003
	resolved := channelTimeResolvedForTest(
		&ModelPricing{InputPricePerToken: 0.001},
		[]PricingInterval{{MinTokens: 0, InputPrice: &intervalInputPrice}},
	)
	billing := NewBillingService(&config.Config{}, nil)

	cost, err := billing.CalculateCostUnified(CostInput{
		Ctx:       context.Background(),
		Model:     "model",
		Tokens:    UsageTokens{InputTokens: 1000},
		Resolver:  &ModelPricingResolver{},
		Resolved:  resolved,
		PricingAt: time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, 6.0, cost.InputCost, 1e-12)
	require.InDelta(t, 6.0, cost.TotalCost, 1e-12)
}

func TestCalculateCostUnified_ChannelTimePricingScalesBaseOnUnmatchedInterval(t *testing.T) {
	intervalInputPrice := 0.003
	resolved := channelTimeResolvedForTest(
		&ModelPricing{InputPricePerToken: 0.001},
		[]PricingInterval{{MinTokens: 2000, InputPrice: &intervalInputPrice}},
	)
	billing := NewBillingService(&config.Config{}, nil)

	cost, err := billing.CalculateCostUnified(CostInput{
		Ctx:       context.Background(),
		Model:     "model",
		Tokens:    UsageTokens{InputTokens: 1000},
		Resolver:  &ModelPricingResolver{},
		Resolved:  resolved,
		PricingAt: time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, 2.0, cost.InputCost, 1e-12)
	require.InDelta(t, 2.0, cost.TotalCost, 1e-12)
}

func TestCalculateCostUnified_ChannelTimePricingDoesNotApplyToGroupPricing(t *testing.T) {
	resolved := channelTimeResolvedForTest(&ModelPricing{InputPricePerToken: 0.001}, nil)
	resolved.Source = PricingSourceGroup
	billing := NewBillingService(&config.Config{}, nil)

	cost, err := billing.CalculateCostUnified(CostInput{
		Ctx:       context.Background(),
		Model:     "model",
		Tokens:    UsageTokens{InputTokens: 1000},
		Resolver:  &ModelPricingResolver{},
		Resolved:  resolved,
		PricingAt: time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, 1.0, cost.TotalCost, 1e-12)
}

func TestCalculateCostUnified_ChannelTimePricingDoesNotApplyOutsideMatchingTime(t *testing.T) {
	resolved := channelTimeResolvedForTest(&ModelPricing{InputPricePerToken: 0.001}, nil)
	billing := NewBillingService(&config.Config{}, nil)

	for _, pricingAt := range []time.Time{
		time.Time{},
		time.Date(2026, 8, 17, 5, 0, 0, 0, time.UTC),
	} {
		cost, err := billing.CalculateCostUnified(CostInput{
			Ctx:       context.Background(),
			Model:     "model",
			Tokens:    UsageTokens{InputTokens: 1000},
			Resolver:  &ModelPricingResolver{},
			Resolved:  resolved,
			PricingAt: pricingAt,
		})
		require.NoError(t, err)
		require.InDelta(t, 1.0, cost.TotalCost, 1e-12)
	}
}

func TestApplyCostBreakdownMultiplierScalesAllMonetaryFields(t *testing.T) {
	cost := &CostBreakdown{
		InputCost:                 1,
		ImageInputCost:            2,
		OutputCost:                3,
		ImageOutputCost:           4,
		CacheCreationCost:         5,
		CacheReadCost:             6,
		TotalCost:                 21,
		ActualCost:                42,
		BillingMode:               string(BillingModeToken),
		LongContextBillingApplied: true,
	}

	applyCostBreakdownMultiplier(cost, 1.5)

	require.InDelta(t, 1.5, cost.InputCost, 1e-12)
	require.InDelta(t, 3.0, cost.ImageInputCost, 1e-12)
	require.InDelta(t, 4.5, cost.OutputCost, 1e-12)
	require.InDelta(t, 6.0, cost.ImageOutputCost, 1e-12)
	require.InDelta(t, 7.5, cost.CacheCreationCost, 1e-12)
	require.InDelta(t, 9.0, cost.CacheReadCost, 1e-12)
	require.InDelta(t, 31.5, cost.TotalCost, 1e-12)
	require.InDelta(t, 63.0, cost.ActualCost, 1e-12)
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
	require.True(t, cost.LongContextBillingApplied)
}

// TestCalculateCostUnified_RateMultiplierZeroProducesZero 锁定新行为：
// 保存时强制 > 0；若 0 仍泄漏到计费层，按 0 计费（而非历史上的 1.0）。
func TestCalculateCostUnified_RateMultiplierZeroProducesZero(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}

	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         tokens,
		RateMultiplier: 0,
		Resolver:       resolver,
	})
	require.NoError(t, err)
	require.Greater(t, cost.TotalCost, 0.0)
	require.InDelta(t, 0.0, cost.ActualCost, 1e-10)
}

// TestCalculateCostUnified_NegativeRateMultiplierClampedToZero 锁定新行为：
// 负数倍率按 0 计费，避免历史的 <=0 → 1.0 把配置异常静默按标准价扣费。
func TestCalculateCostUnified_NegativeRateMultiplierClampedToZero(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000}

	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         tokens,
		RateMultiplier: -5.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)
	require.Greater(t, cost.TotalCost, 0.0)
	require.InDelta(t, 0.0, cost.ActualCost, 1e-10)
}

func TestCalculateCostUnified_BillingModeFieldFilled(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         UsageTokens{InputTokens: 100},
		RateMultiplier: 1.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)
	require.Equal(t, "token", cost.BillingMode)
}

func TestCalculateCostUnified_UsesPreResolvedPricing(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	// Pre-resolve with per_request mode to verify it's used instead of re-resolving
	preResolved := &ResolvedPricing{
		Mode:                   BillingModePerRequest,
		DefaultPerRequestPrice: 0.07,
	}

	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          "claude-sonnet-4",
		Tokens:         UsageTokens{InputTokens: 100},
		RequestCount:   2,
		RateMultiplier: 1.0,
		Resolver:       resolver,
		Resolved:       preResolved,
	})
	require.NoError(t, err)
	require.NotNil(t, cost)

	// 2 * $0.07 = $0.14
	require.InDelta(t, 0.14, cost.TotalCost, 1e-10)
	require.Equal(t, string(BillingModePerRequest), cost.BillingMode)
}

// ---------------------------------------------------------------------------
// GPT-5.6 长上下文默认倍率 × 渠道约定价（fork 定制回归，见 calculateTokenCost）
// ---------------------------------------------------------------------------

// forkOpenAILadderCatalogJSON 是本节 fork 回归用例的目录 fixture。
// 上游共享的 openAILadderCatalogJSON 只收了 gpt-5.4 / gpt-5.5-pro，本节还需要
// gpt-5.5 与 gpt-5.6-sol 两条带 above_272k 的条目。
//
// 为什么必须挂目录：上游 0.1.185 之前，长上下文阶梯由 applyModelSpecificPricingPolicy
// 按模型名硬编码补齐，所以这几条用例挂空目录（newTestBillingService）也能拿到阶梯；
// 0.1.185 起阶梯**只**来自目录 above_XXXk 字段，空目录 = 根本没有阶梯，
// 这几条用例会变成「无阶梯可剥」的空转断言，fork 定制的 stripLongContextPricing
// 就此失去回归覆盖而无人察觉。因此本节统一改挂带阶梯的目录。
const forkOpenAILadderCatalogJSON = `{
	"gpt-5.5": {"litellm_provider": "openai", "mode": "chat",
		"input_cost_per_token": 5e-06, "output_cost_per_token": 3e-05,
		"cache_read_input_token_cost": 5e-07,
		"input_cost_per_token_above_272k_tokens": 1e-05,
		"output_cost_per_token_above_272k_tokens": 4.5e-05,
		"cache_read_input_token_cost_above_272k_tokens": 1e-06},
	"gpt-5.4": {"litellm_provider": "openai", "mode": "chat",
		"input_cost_per_token": 2.5e-06, "output_cost_per_token": 1.5e-05,
		"cache_read_input_token_cost": 2.5e-07,
		"input_cost_per_token_above_272k_tokens": 5e-06,
		"output_cost_per_token_above_272k_tokens": 2.25e-05,
		"cache_read_input_token_cost_above_272k_tokens": 5e-07},
	"gpt-5.6-sol": {"litellm_provider": "openai", "mode": "chat",
		"input_cost_per_token": 5e-06, "output_cost_per_token": 3e-05,
		"cache_read_input_token_cost": 5e-07,
		"input_cost_per_token_above_272k_tokens": 1e-05,
		"output_cost_per_token_above_272k_tokens": 4.5e-05,
		"cache_read_input_token_cost_above_272k_tokens": 1e-06},
	"gpt-6.2-sol": {"litellm_provider": "openai", "mode": "chat",
		"input_cost_per_token": 2e-06, "output_cost_per_token": 1e-05,
		"cache_read_input_token_cost": 1e-07,
		"input_cost_per_token_above_272k_tokens": 4e-06,
		"output_cost_per_token_above_272k_tokens": 1.5e-05,
		"cache_read_input_token_cost_above_272k_tokens": 2e-07},
	"gpt-6.2-astra": {"litellm_provider": "openai", "mode": "chat",
		"input_cost_per_token": 8e-06, "output_cost_per_token": 4e-05,
		"cache_read_input_token_cost": 4e-07,
		"input_cost_per_token_above_272k_tokens": 1.6e-05,
		"output_cost_per_token_above_272k_tokens": 6e-05,
		"cache_read_input_token_cost_above_272k_tokens": 8e-07},
	"gpt-6-astra": {"litellm_provider": "openai", "mode": "chat",
		"input_cost_per_token": 1e-05, "output_cost_per_token": 5e-05,
		"cache_read_input_token_cost": 1e-06,
		"input_cost_per_token_above_272k_tokens": 2e-05,
		"output_cost_per_token_above_272k_tokens": 7.5e-05,
		"cache_read_input_token_cost_above_272k_tokens": 2e-06},
	"gpt-6.1-sol": {"litellm_provider": "openai", "mode": "chat",
		"input_cost_per_token": 2e-06, "output_cost_per_token": 1e-05,
		"cache_read_input_token_cost": 1e-07,
		"input_cost_per_token_above_272k_tokens": 4e-06,
		"output_cost_per_token_above_272k_tokens": 1.5e-05,
		"cache_read_input_token_cost_above_272k_tokens": 2e-07}
}`

func newForkLadderBillingService(t *testing.T) *BillingService {
	t.Helper()
	return NewBillingService(&config.Config{}, newStubPricingServiceFromJSON(t, forkOpenAILadderCatalogJSON))
}

// 渠道 flat 约定价的 GPT-5.6：总上下文超过 272K 默认阈值时，不得叠加默认
// 2x/1.5x 长上下文倍率（渠道价是与客户约定的固定单价）。
// 回归场景：合并上游后 applyModelSpecificPricingPolicy 扩到 GPT-5.6 +
// BasePricing 继承内置长上下文字段，导致渠道 flat 价超 272K 被静默按 2x/1.5x 多收。
func TestCalculateCostUnified_GPT56ChannelFlatPricingSkipsDefaultLongContext(t *testing.T) {
	cs := newTestChannelServiceWithTokenPricing(t, "gpt-5.6-sol", &ChannelModelPricing{
		BillingMode:    BillingModeToken,
		InputPrice:     testPtrFloat64(1e-6),
		OutputPrice:    testPtrFloat64(2e-6),
		CacheReadPrice: testPtrFloat64(1e-7),
	})
	bs := newForkLadderBillingService(t)
	resolver := NewModelPricingResolver(cs, bs)
	groupID := int64(1)

	// 总上下文 = 100000 input + 200000 cache_read = 300000 > 272000
	tokens := UsageTokens{InputTokens: 100000, CacheReadTokens: 200000, OutputTokens: 1000}
	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          "gpt-5.6-sol",
		GroupID:        &groupID,
		Tokens:         tokens,
		RateMultiplier: 1.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)

	expectedInput := 100000 * 1e-6
	expectedCacheRead := 200000 * 1e-7
	expectedOutput := 1000 * 2e-6
	require.InDelta(t, expectedInput, cost.InputCost, 1e-10,
		"channel flat input price must not be multiplied by default long-context policy")
	require.InDelta(t, expectedCacheRead, cost.CacheReadCost, 1e-10,
		"channel flat cache-read price must not be multiplied by default long-context policy")
	require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10,
		"channel flat output price must not be multiplied by default long-context policy")
	require.InDelta(t, expectedInput+expectedCacheRead+expectedOutput, cost.TotalCost, 1e-10)
}

// 渠道 flat 约定价的 GPT-6.2 Sol/Astra：与 GPT-5.6 相同，总上下文超过 272K
// 时不得叠加模型目录自带的长上下文倍率。GPT-6.2 是本次合并新并入的模型，
// 其 fallback 自带 above_272k 字段，必须显式纳入 fork 的渠道 flat 保护。
func TestCalculateCostUnified_GPT62ChannelFlatPricingSkipsDefaultLongContext(t *testing.T) {
	for _, model := range []string{"gpt-6.2-sol", "gpt-6.2-astra"} {
		t.Run(model, func(t *testing.T) {
			cs := newTestChannelServiceWithTokenPricing(t, model, &ChannelModelPricing{
				BillingMode:    BillingModeToken,
				InputPrice:     testPtrFloat64(1e-6),
				OutputPrice:    testPtrFloat64(2e-6),
				CacheReadPrice: testPtrFloat64(1e-7),
			})
			bs := newForkLadderBillingService(t)
			resolver := NewModelPricingResolver(cs, bs)
			groupID := int64(1)

			// 总上下文 = 100000 input + 200000 cache_read = 300000 > 272000
			cost, err := bs.CalculateCostUnified(CostInput{
				Ctx:            context.Background(),
				Model:          model,
				GroupID:        &groupID,
				Tokens:         UsageTokens{InputTokens: 100000, CacheReadTokens: 200000, OutputTokens: 1000},
				RateMultiplier: 1.0,
				Resolver:       resolver,
			})
			require.NoError(t, err)

			expectedInput := 100000 * 1e-6
			expectedCacheRead := 200000 * 1e-7
			expectedOutput := 1000 * 2e-6
			require.InDelta(t, expectedInput, cost.InputCost, 1e-10,
				"GPT-6.2 channel flat input price must not be multiplied by default long-context policy")
			require.InDelta(t, expectedCacheRead, cost.CacheReadCost, 1e-10,
				"GPT-6.2 channel flat cache-read price must not be multiplied by default long-context policy")
			require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10,
				"GPT-6.2 channel flat output price must not be multiplied by default long-context policy")
			require.InDelta(t, expectedInput+expectedCacheRead+expectedOutput, cost.TotalCost, 1e-10)
		})
	}
}

// 目录定价（非渠道价）的 GPT-6.2：长上下文阶梯照常生效，证明保护只作用于
// 渠道 flat 约定价，不会把官方目录阶梯一并剥掉。
func TestCalculateCostUnified_GPT62BuiltinPricingKeepsDefaultLongContext(t *testing.T) {
	for _, tc := range []struct {
		model        string
		inputPerTok  float64
		outputPerTok float64
	}{
		{"gpt-6.2-sol", 2e-6, 1e-5},
		{"gpt-6.2-astra", 8e-6, 4e-5},
	} {
		t.Run(tc.model, func(t *testing.T) {
			bs := newForkLadderBillingService(t)
			resolver := NewModelPricingResolver(nil, bs)

			cost, err := bs.CalculateCostUnified(CostInput{
				Ctx:            context.Background(),
				Model:          tc.model,
				Tokens:         UsageTokens{InputTokens: 300000, OutputTokens: 1000},
				RateMultiplier: 1.0,
				Resolver:       resolver,
			})
			require.NoError(t, err)

			expectedInput := 300000 * tc.inputPerTok * 2.0
			expectedOutput := 1000 * tc.outputPerTok * 1.5
			require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
			require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
		})
	}
}

// GPT-6 / GPT-6.1 渠道 flat 价维持合并前口径：长上下文倍率照常生效。
// 本次保护只新增 GPT-6.2 Sol/Astra，不能扩大到 GPT-6 / GPT-6.1。
func TestCalculateCostUnified_GPT6And61ChannelFlatPricingKeepsLongContext(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6.1-sol"} {
		t.Run(model, func(t *testing.T) {
			cs := newTestChannelServiceWithTokenPricing(t, model, &ChannelModelPricing{
				BillingMode: BillingModeToken,
				InputPrice:  testPtrFloat64(1e-6),
				OutputPrice: testPtrFloat64(2e-6),
			})
			bs := newForkLadderBillingService(t)
			resolver := NewModelPricingResolver(cs, bs)
			groupID := int64(1)

			cost, err := bs.CalculateCostUnified(CostInput{
				Ctx:            context.Background(),
				Model:          model,
				GroupID:        &groupID,
				Tokens:         UsageTokens{InputTokens: 300000, OutputTokens: 1000},
				RateMultiplier: 1.0,
				Resolver:       resolver,
			})
			require.NoError(t, err)

			expectedInput := 300000 * 1e-6 * 2.0
			expectedOutput := 1000 * 2e-6 * 1.5
			require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
			require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
		})
	}
}

// 渠道显式配置了长上下文分层（区间定价）的 GPT-5.6：区间价照常生效。
func TestCalculateCostUnified_GPT56ChannelIntervalPricingStillApplies(t *testing.T) {
	maxTokens := 272000
	cs := newTestChannelServiceWithTokenPricing(t, "gpt-5.6-sol", &ChannelModelPricing{
		BillingMode: BillingModeToken,
		Intervals: []PricingInterval{
			{MinTokens: 0, MaxTokens: &maxTokens, InputPrice: testPtrFloat64(1e-6), OutputPrice: testPtrFloat64(2e-6)},
			{MinTokens: 272000, InputPrice: testPtrFloat64(2e-6), OutputPrice: testPtrFloat64(3e-6)},
		},
	})
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(cs, bs)
	groupID := int64(1)

	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          "gpt-5.6-sol",
		GroupID:        &groupID,
		Tokens:         UsageTokens{InputTokens: 300000, OutputTokens: 1000},
		RateMultiplier: 1.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)

	// 300000 > 272000 命中第二区间；区间价已含上下文分层，不再叠加长上下文倍率
	expected := 300000*2e-6 + 1000*3e-6
	require.InDelta(t, expected, cost.TotalCost, 1e-10)
}

// 目录定价（非渠道价）的 GPT-5.6：长上下文阶梯照常生效——stripLongContextPricing
// 只针对「渠道 flat 约定价」，不得把目录阶梯一并剥掉。
// 上游 0.1.185 起阶梯只来自目录 above_272k 字段，故本用例从空目录改挂 fork ladder 目录；
// 断言的倍率（input×2.0 / output×1.5）与改动前一致。
func TestCalculateCostUnified_GPT56BuiltinPricingKeepsDefaultLongContext(t *testing.T) {
	bs := newForkLadderBillingService(t)
	resolver := NewModelPricingResolver(nil, bs)

	cost, err := bs.CalculateCostUnified(CostInput{
		Ctx:            context.Background(),
		Model:          "gpt-5.6-sol",
		Tokens:         UsageTokens{InputTokens: 300000, OutputTokens: 1000},
		RateMultiplier: 1.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)

	// 目录 gpt-5.6-sol: input 5e-6, output 30e-6；超 272K 后 input×2.0 / output×1.5
	expectedInput := 300000 * 5e-6 * 2.0
	expectedOutput := 1000 * 30e-6 * 1.5
	require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
	require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
}

// gpt-5.4 / gpt-5.5 渠道 flat 价：长上下文倍率照常对渠道价生效。
// 这条用例守的是 fork 定制 stripLongContextPricing 的**作用域**——只剥 GPT-5.6，
// 不得扩散到 5.4/5.5，否则这两个存量模型 >272K 的请求会静默少收。
// 同样从空目录改挂 fork ladder 目录（0.1.185 起阶梯只来自目录 above_272k 字段）。
func TestCalculateCostUnified_GPT54And55ChannelFlatPricingKeepsLongContext(t *testing.T) {
	for _, model := range []string{"gpt-5.4", "gpt-5.5"} {
		t.Run(model, func(t *testing.T) {
			cs := newTestChannelServiceWithTokenPricing(t, model, &ChannelModelPricing{
				BillingMode: BillingModeToken,
				InputPrice:  testPtrFloat64(1e-6),
				OutputPrice: testPtrFloat64(2e-6),
			})
			bs := newForkLadderBillingService(t)
			resolver := NewModelPricingResolver(cs, bs)
			groupID := int64(1)

			cost, err := bs.CalculateCostUnified(CostInput{
				Ctx:            context.Background(),
				Model:          model,
				GroupID:        &groupID,
				Tokens:         UsageTokens{InputTokens: 300000, OutputTokens: 1000},
				RateMultiplier: 1.0,
				Resolver:       resolver,
			})
			require.NoError(t, err)

			// 既有口径：超 272K 后渠道价也叠加目录阶梯 input×2.0 / output×1.5
			expectedInput := 300000 * 1e-6 * 2.0
			expectedOutput := 1000 * 2e-6 * 1.5
			require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
			require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTestChannelServiceWithTokenPricing 构造带单条 token 模式渠道定价的 ChannelService
// （groupID 固定为 1）。
func newTestChannelServiceWithTokenPricing(t *testing.T, model string, pricing *ChannelModelPricing) *ChannelService {
	t.Helper()
	return newTestChannelServiceWithCache(t, &channelCache{
		pricingByGroupModel: map[channelModelKey]*ChannelModelPricing{
			{groupID: 1, model: model}: pricing,
		},
		channelByGroupID: map[int64]*Channel{
			1: {ID: 1, Status: StatusActive},
		},
		groupPlatform:           map[int64]string{1: ""},
		wildcardByGroupPlatform: map[channelGroupPlatformKey][]*wildcardPricingEntry{},
		mappingByGroupModel:     map[channelModelKey]string{},
		wildcardMappingByGP:     map[channelGroupPlatformKey][]*wildcardMappingEntry{},
		byID:                    map[int64]*Channel{},
	})
}

// newTestChannelServiceWithCache creates a ChannelService with a pre-populated
// cache snapshot, bypassing the repository layer entirely.
func newTestChannelServiceWithCache(t *testing.T, cache *channelCache) *ChannelService {
	t.Helper()
	cs := &ChannelService{}
	cache.loadedAt = time.Now()
	cs.cache.Store(cache)
	return cs
}
