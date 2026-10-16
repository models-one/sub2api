//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// fork：Gemini 显式缓存的固定费用预留必须按钱包实扣金额（含商户加价）做余额准入。
// 上游 QuoteUsageCost 只给主站价，商户子用户会按主站价通过准入、实扣却是加价后的金额。
func TestGeminiCachedContentQuoteIncludesMerchantMarkup(t *testing.T) {
	ctx := context.Background()
	svc, usageRepo := newGeminiCacheBillingGatewayForTest(t)
	const userID, merchantID = int64(631), int64(100)
	groupID := int64(35)
	svc.merchantPricing, _ = newPricingTestService(true,
		map[int64]int64{userID: merchantID},
		map[int64]*CachedMerchantPricing{merchantID: {
			MerchantID: merchantID, OwnerUserID: 999, Status: "active",
			GroupSellRates: map[int64]float64{groupID: 3},
		}},
	)
	apiKey := &APIKey{ID: 831, GroupID: &groupID, User: &User{ID: userID},
		Group: &Group{ID: groupID, Platform: PlatformGemini, RateMultiplier: 1, Hydrated: true}}
	tokenHours := GeminiCachedContentStorageTokenHours(1000, 24*time.Hour)
	input := &RecordUsageInput{
		Result: &ForwardResult{RequestID: "cc-merchant", Model: "gemini-2.5-pro",
			Usage: ClaudeUsage{InputTokens: 1000}, CacheStorageTokenHours: tokenHours},
		APIKey: apiKey, User: apiKey.User, Account: &Account{ID: 714, Platform: PlatformGemini},
	}

	base, wallet := svc.QuoteUsageWalletCost(ctx, input)
	site := 1000*0.00000125 + tokenHours*0.0000045
	require.InDelta(t, site, base, 1e-12, "基础金额仍是主站价（额度 / 限速口径）")
	require.InDelta(t, site*3, wallet, 1e-12, "钱包金额按商户售价倍率加价")
	require.InDelta(t, base, svc.QuoteUsageCost(ctx, input), 1e-12, "QuoteUsageCost 保持上游语义")

	require.NoError(t, svc.RecordUsage(ctx, input))
	require.InDelta(t, wallet, usageRepo.lastLog.ActualCost, 1e-12, "预留报价与实际钱包扣款同口径")
}

func TestReserveFixedChargeWithWalletChecksBalanceAgainstWalletAmount(t *testing.T) {
	ctx := context.Background()
	user := &User{ID: 1}
	cfg := &config.Config{}
	cfg.Billing.InflightReservation.Enabled = true
	cache := &fixedChargeCacheStub{balance: 100}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(svc.Stop)

	// 主站价 54.6 够付，但商户加价后的 163.9 不够：必须拒绝。
	_, err := svc.ReserveFixedChargeWithWallet(ctx, user, nil, nil, nil, 54.6, 163.9)
	require.ErrorIs(t, err, ErrInsufficientBalance)

	// API Key 额度按基础金额校验：额度 60 足以覆盖 54.6，钱包金额 90 也在余额内。
	apiKey := &APIKey{ID: 5, Quota: 60}
	res, err := svc.ReserveFixedChargeWithWallet(ctx, user, apiKey, nil, nil, 54.6, 90)
	require.NoError(t, err)
	res.Release()

	// 钱包金额非法时回落基础金额。
	res, err = svc.ReserveFixedChargeWithWallet(ctx, user, nil, nil, nil, 50, 0)
	require.NoError(t, err)
	res.Release()
	for key, reservations := range cache.reserved {
		require.Empty(t, reservations, "拒绝与释放都不留下预留：%s", key)
	}
}
