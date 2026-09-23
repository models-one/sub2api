package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/dgraph-io/ristretto"
)

// v25: 两条血脉已连续撞号九次（v16…v24），每次两侧各自 bump 到同一个号、语义却不同。
// 本 fork 的常量必须始终**高于**两条血脉出现过的最大号，否则任一侧的遗留条目
// 都会通过版本校验、而另一侧的字段反序列化成零值。
// 撞号历史（每次两条血脉各自 bump 到同一个号，语义却不同）：
//   - v16：本地 = user parent_merchant_id + group affiliate_rebate_excluded
//     （merchant 子用户守卫 / 邀请返利排除）；上游 = group reasoning effort 上限 + 映射。
//   - v17：本地 = 合并上述两条 v16 血脉的产物；上游 = OpenAI group Live gate（group.AllowLive）。
//   - v18：本地 = 合并上述两条 v17 血脉的产物（merchant 字段 + reasoning effort + AllowLive
//     三套齐全）；上游 = group profit control 三字段（profit_control_enabled /
//     profit_min_margin / profit_safety_buffer）。
//   - v19：本地 = 合并上述两条 v18 血脉的产物（merchant + reasoning + AllowLive + profit
//     control 四套齐全）；上游 = 分组级 search/audio/video 计费字段（search_price_per_1k /
//     audio_realtime_price_per_min / audio_tts_price_per_million_chars /
//     audio_stt_price_per_hour / video_model_prices，迁移 217/218/219）。
//   - v20：第六次撞号。本地 v20 = 合并上述两条 v19 血脉的产物，但**还没有**
//     LongContextPricingEnabled / ModelPricing（上游 0.1.176 迁移 221 只把这两个分组计费
//     字段加进了 repository 的鉴权投影、没加进快照）；上游 0.1.179 独立发现并修复了同一个
//     bug，把这两个字段加进快照后同样 bump 到 20。两侧 v20 语义不同：
//     本地血脉的 v20 缺分组计费字段 ⇒ 长上下文阶梯静默关闭、渠道区间定价塌到最便宜的
//     第一档（纯少收）；上游血脉的 v20 缺本 fork 的 merchant/affiliate 字段
//     （user.parent_merchant_id、group.affiliate_rebate_excluded）⇒ merchant 停用守卫
//     静默失效、返利排除读成 false。
//   - v21：**第七次撞号**，两侧语义完全不同：
//     本地 v21 = 本地 v20 + LongContextPricingEnabled / ModelPricing（本 fork 先于上游
//     修好分组计费字段缺失，字段集里**没有**上游 0.1.180+ 的三个分组开关）；
//     上游 v21（a44a1019f「Carry group Fast through auth snapshots」）= 上游 v20 +
//     group.force_openai_fast（迁移 232_group_force_openai_fast），字段集里**没有**本
//     fork 的 merchant/affiliate 字段。
//   - v22：上游独有（e619ca386「Carry free Fast through auth snapshots」）=
//     上游 v21 + group.free_openai_fast（迁移 233_group_free_openai_fast）。同样缺本
//     fork 的 merchant/affiliate 字段。注意上游 v22 内部还自带一次**未 bump** 的字段扩容：
//     aa7a811e6 把 group.max_reasoning_effort_over_limit（迁移
//     232_group_reasoning_effort_over_limit）加进快照却没有抬版本，因此上游血脉的 v22
//     条目本身就分「带/不带 over_limit」两种，不带的会把该字段读成空串（回落 downgrade）。
//
// 合并后的快照同时携带两侧全部字段（本 fork 的 merchant/affiliate/长上下文计费 +
// 上游的 force_openai_fast / free_openai_fast / max_reasoning_effort_over_limit），
// 因此必须越过 22：任何一条血脉遗留的 ≤v22 条目都会通过版本校验，而另一条血脉的字段
// 反序列化成零值——
//
//   - 命中本地血脉的 v21 遗留条目 ⇒ ForceOpenAIFast / FreeOpenAIFast /
//     MaxReasoningEffortOverLimit 全读零值：分组强制 Fast 静默失效、Fast 免费分组被
//     照常计费（多收）、reasoning effort 超限策略从 deny 静默退化成 downgrade（越权放行）。
//
//   - 命中上游血脉的 v21 / v22 遗留条目 ⇒ 本 fork 的 user.parent_merchant_id 与
//     group.affiliate_rebate_excluded 读成 nil/false：MERCHANT-SYSTEM 停用商户拦截守卫
//     静默失效（已停用商户的子用户照常调用）、邀请返利排除读成 false（本该排除的分组
//     消费照样返利）；同时 LongContextPricingEnabled / ModelPricing 在上游 v21/v22 里
//     虽已存在，但 merchant/affiliate 缺口足以单独构成拒收理由。
//
//   - v23：**第八次撞号**，两侧语义再次完全不同：
//     本地 v23 = 合并上述两条血脉的产物（本 fork 的 merchant/affiliate/长上下文计费 +
//     上游的 force/free_openai_fast + max_reasoning_effort_over_limit 全在）；
//     上游 v23（0.2.1「feat(codex): pinned-accounts Codex model manifest」）=
//     上游 v22 + group.codex_models_manifest_config（迁移
//     234_group_codex_models_manifest_config），字段集里依旧**没有**本 fork 的
//     merchant/affiliate 字段。
//
// 合并后的快照同时携带两侧全部字段，因此必须越过 23：
//
//   - 命中本地血脉的 v23 遗留条目 ⇒ CodexModelsManifestConfig 读成零值，
//     固定账号 Codex manifest 静默退回全量调度（功能降级，不影响计费）。
//
//   - 命中上游血脉的 v23 遗留条目 ⇒ 本 fork 的 merchant/affiliate 字段读成
//     nil/false，MERCHANT-SYSTEM 停用商户拦截守卫静默失效、返利排除失效（要害）。
//
//   - v24：**第九次撞号**，两侧语义再次完全不同：
//     本地 v24 = 合并上述两条 v23 血脉的产物（本 fork 的 merchant/affiliate/长上下文计费 +
//     上游的 force/free_openai_fast + max_reasoning_effort_over_limit +
//     codex_models_manifest_config 全在），分组模型名单仍是老字段
//     Group.ModelsListConfig（JSON 键 models_list_config，语义仅过滤 /v1/models 展示）；
//     上游 v24（0.2.4 cff3f8985「feat(groups)!: enforce model allowlists across gateway
//     endpoints」）= 上游 v23 + models_list_config 更名 model_allowlist
//     （快照 JSON 键同步改成 model_allowlist），语义升级为**同时约束网关请求准入**
//     （新增 GroupModelAllowlist 中间件挂到各网关路由），字段集里依旧**没有**本 fork 的
//     merchant/affiliate 字段。
//
// 合并后的快照同时携带两侧全部字段（本 fork 的 merchant/affiliate + 上游更名后的
// model_allowlist），因此必须越过 24：
//
//   - 命中本地血脉的 v24 遗留条目 ⇒ 旧 JSON 键 models_list_config 无人认领，
//     Group.ModelAllowlist 读成零值（Enabled=false、Models=nil）⇒ ModelAllowlistEnabled()
//     恒假、GroupModelAllowlist 中间件走快速放行路径：分组模型白名单准入静默失效
//     （名单外模型照常调用，越权放行），/v1/models 展示过滤同时失效。
//
//   - 命中上游血脉的 v24 遗留条目 ⇒ 本 fork 的 user.parent_merchant_id 与
//     group.affiliate_rebate_excluded 读成 nil/false，MERCHANT-SYSTEM 停用商户拦截守卫
//     静默失效、返利排除失效（要害）。
//
// 守卫测试见 TestAPIKeyService_RejectsV21… / …RejectsV22… / …RejectsV23… /
// …RejectsV24… 与 TestAPIKeyAuthSnapshotVersion_IsPastAllCollidedLineages
// （lastCollidedVersion = 24）。
// 下轮合并若上游再撞到 25，常量与该测试的 lastCollidedVersion 必须一并继续抬高。
//
// v26：本 fork 独有的 bump（上游 0.2.8 仍停在 24，本轮无撞号）。上游 b8d52fad3 把
// ChannelModelPricing 的单值 max_reasoning_effort_multiplier 换成按推理等级配置的
// reasoning_effort_multipliers 映射，迁移 239 同步改写了 groups.model_pricing 里的 JSON 键。
// 快照里的 Group.ModelPricing 就是 []ChannelModelPricing：v25 遗留条目只有旧键，
// 反序列化后 ReasoningEffortMultipliers=nil ⇒ 分组级推理等级倍率静默按 1 倍计（少收），
// 直到 L2 过期；滚动发布新旧进程并存期间还会被旧进程持续回写。抬到 26 让它们一律回源。
// 守卫测试见 TestAPIKeyService_RejectsV25AuthSnapshotWithLegacyReasoningMultiplierKey。
const apiKeyAuthSnapshotVersion = 26

type apiKeyAuthCacheConfig struct {
	l1Size        int
	l1TTL         time.Duration
	l2TTL         time.Duration
	negativeTTL   time.Duration
	jitterPercent int
	singleflight  bool
}

func newAPIKeyAuthCacheConfig(cfg *config.Config) apiKeyAuthCacheConfig {
	if cfg == nil {
		return apiKeyAuthCacheConfig{}
	}
	auth := cfg.APIKeyAuth
	return apiKeyAuthCacheConfig{
		l1Size:        auth.L1Size,
		l1TTL:         time.Duration(auth.L1TTLSeconds) * time.Second,
		l2TTL:         time.Duration(auth.L2TTLSeconds) * time.Second,
		negativeTTL:   time.Duration(auth.NegativeTTLSeconds) * time.Second,
		jitterPercent: auth.JitterPercent,
		singleflight:  auth.Singleflight,
	}
}

func (c apiKeyAuthCacheConfig) l1Enabled() bool {
	return c.l1Size > 0 && c.l1TTL > 0
}

func (c apiKeyAuthCacheConfig) l2Enabled() bool {
	return c.l2TTL > 0
}

func (c apiKeyAuthCacheConfig) negativeEnabled() bool {
	return c.negativeTTL > 0
}

// jitterTTL 为缓存 TTL 添加抖动，避免多个请求在同一时刻同时过期触发集中回源。
// 这里直接使用 rand/v2 的顶层函数：并发安全，无需全局互斥锁。
func (c apiKeyAuthCacheConfig) jitterTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return ttl
	}
	if c.jitterPercent <= 0 {
		return ttl
	}
	percent := c.jitterPercent
	if percent > 100 {
		percent = 100
	}
	delta := float64(percent) / 100
	randVal := rand.Float64()
	factor := 1 - delta + randVal*(2*delta)
	if factor <= 0 {
		return ttl
	}
	return time.Duration(float64(ttl) * factor)
}

func (s *APIKeyService) initAuthCache(cfg *config.Config) {
	s.authCfg = newAPIKeyAuthCacheConfig(cfg)
	if s.authCfg.negativeEnabled() {
		negativeSize := defaultNegativeAuthCacheSize
		if s.authCfg.l1Size > 0 && s.authCfg.l1Size < negativeSize {
			negativeSize = s.authCfg.l1Size
		}
		cache, err := ristretto.NewCache(&ristretto.Config{
			NumCounters: int64(negativeSize) * 10,
			MaxCost:     int64(negativeSize),
			BufferItems: 64,
		})
		if err == nil {
			s.authNegativeCacheL1 = cache
		}
	}
	if s.authCfg.l1Enabled() {
		cache, err := ristretto.NewCache(&ristretto.Config{
			NumCounters: int64(s.authCfg.l1Size) * 10,
			MaxCost:     int64(s.authCfg.l1Size),
			BufferItems: 64,
		})
		if err == nil {
			s.authCacheL1 = cache
		}
	}
}

// StartAuthCacheInvalidationSubscriber starts the Pub/Sub subscriber for L1 cache invalidation.
// This should be called after the service is fully initialized.
func (s *APIKeyService) StartAuthCacheInvalidationSubscriber(ctx context.Context) {
	if s.cache == nil || (s.authCacheL1 == nil && s.authNegativeCacheL1 == nil) {
		return
	}
	s.authInvalidationStart.Do(func() {
		subscriberCtx, cancel := context.WithCancel(ctx)
		subscriberCtx = withAuthCacheSubscriptionReady(subscriberCtx, func() {
			s.authInvalidationConnected.Store(true)
		})
		s.authInvalidationCancel = cancel
		s.authInvalidationWG.Add(1)
		go func() {
			defer s.authInvalidationWG.Done()
			backoff := time.Second
			for {
				err := s.cache.SubscribeAuthCacheInvalidation(subscriberCtx, func(cacheKey string) {
					s.invalidateLocalAuthCache(cacheKey)
				})
				wasConnected := s.authInvalidationConnected.Swap(false)
				if subscriberCtx.Err() != nil {
					return
				}
				if wasConnected {
					backoff = time.Second
				}
				s.authInvalidationFailures.Add(1)
				if err == nil {
					err = errors.New("auth cache invalidation subscription closed")
				}
				slog.Warn("failed to start auth cache invalidation subscriber; retrying", "error", err, "retry_in", backoff)
				timer := time.NewTimer(backoff)
				select {
				case <-subscriberCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				if backoff < 30*time.Second {
					backoff *= 2
					if backoff > 30*time.Second {
						backoff = 30 * time.Second
					}
				}
			}
		}()
	})
}

func (s *APIKeyService) invalidateLocalAuthCache(cacheKey string) {
	if s == nil {
		return
	}
	if s.authCacheL1 != nil {
		s.authCacheL1.Del(cacheKey)
	}
	if s.authNegativeCacheL1 != nil {
		s.authNegativeCacheL1.Del(cacheKey)
	}
}

type AuthCacheInvalidationSubscriberHealth struct {
	Connected bool   `json:"connected"`
	Failures  uint64 `json:"failures"`
}

func (s *APIKeyService) AuthCacheInvalidationSubscriberHealth() AuthCacheInvalidationSubscriberHealth {
	if s == nil {
		return AuthCacheInvalidationSubscriberHealth{}
	}
	return AuthCacheInvalidationSubscriberHealth{
		Connected: s.authInvalidationConnected.Load(),
		Failures:  s.authInvalidationFailures.Load(),
	}
}

func (s *APIKeyService) StopAuthCacheInvalidationSubscriber() {
	if s == nil {
		return
	}
	s.authInvalidationStop.Do(func() {
		if s.authInvalidationCancel != nil {
			s.authInvalidationCancel()
		}
		s.authInvalidationWG.Wait()
	})
}

func (s *APIKeyService) authCacheKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (s *APIKeyService) getAuthCacheEntry(ctx context.Context, cacheKey string) (*APIKeyAuthCacheEntry, bool) {
	if s.authCacheL1 != nil {
		if val, ok := s.authCacheL1.Get(cacheKey); ok {
			if entry, ok := val.(*APIKeyAuthCacheEntry); ok {
				return entry, true
			}
		}
	}
	if s.authNegativeCacheL1 != nil {
		if val, ok := s.authNegativeCacheL1.Get(cacheKey); ok {
			if entry, ok := val.(*APIKeyAuthCacheEntry); ok && entry.NotFound {
				return entry, true
			}
		}
	}
	if s.cache == nil || !s.authCfg.l2Enabled() {
		return nil, false
	}
	entry, err := s.cache.GetAuthCache(ctx, cacheKey)
	if err != nil {
		return nil, false
	}
	s.setAuthCacheL1(cacheKey, entry)
	return entry, true
}

func (s *APIKeyService) setAuthCacheL1(cacheKey string, entry *APIKeyAuthCacheEntry) {
	if entry == nil {
		return
	}
	if entry.NotFound {
		if s.authNegativeCacheL1 != nil && s.authCfg.negativeTTL > 0 {
			_ = s.authNegativeCacheL1.SetWithTTL(cacheKey, entry, 1, s.authCfg.jitterTTL(s.authCfg.negativeTTL))
		}
		return
	}
	if s.authCacheL1 == nil {
		return
	}
	ttl := s.authCfg.l1TTL
	ttl = s.authCfg.jitterTTL(ttl)
	_ = s.authCacheL1.SetWithTTL(cacheKey, entry, 1, ttl)
}

func (s *APIKeyService) setAuthCacheEntry(ctx context.Context, cacheKey string, entry *APIKeyAuthCacheEntry, ttl time.Duration) {
	if entry == nil {
		return
	}
	s.setAuthCacheL1(cacheKey, entry)
	if s.cache == nil || !s.authCfg.l2Enabled() {
		return
	}
	_ = s.cache.SetAuthCache(ctx, cacheKey, entry, s.authCfg.jitterTTL(ttl))
}

func (s *APIKeyService) deleteAuthCache(ctx context.Context, cacheKey string) {
	if s.authCacheL1 != nil {
		s.authCacheL1.Del(cacheKey)
	}
	if s.authNegativeCacheL1 != nil {
		s.authNegativeCacheL1.Del(cacheKey)
	}
	if s.cache == nil {
		return
	}
	_ = s.cache.DeleteAuthCache(ctx, cacheKey)
	// Publish invalidation message to other instances
	_ = s.cache.PublishAuthCacheInvalidation(ctx, cacheKey)
}

func (s *APIKeyService) loadAuthCacheEntry(ctx context.Context, key, cacheKey string) (*APIKeyAuthCacheEntry, error) {
	apiKey, err := s.lookupAPIKeyForAuth(ctx, key)
	if err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			entry := &APIKeyAuthCacheEntry{NotFound: true}
			if s.authCfg.negativeEnabled() {
				// Invalid keys are attacker-controlled and high-cardinality. Keep their
				// negative entries in the bounded process-local cache; do not amplify
				// random-key scans into Redis writes on every instance.
				s.setAuthCacheL1(cacheKey, entry)
			}
			return entry, nil
		}
		return nil, fmt.Errorf("get api key: %w", err)
	}
	apiKey.Key = key
	snapshot := s.snapshotFromAPIKey(ctx, apiKey)
	if snapshot == nil {
		return nil, fmt.Errorf("get api key: %w", ErrAPIKeyNotFound)
	}
	entry := &APIKeyAuthCacheEntry{Snapshot: snapshot}
	s.setAuthCacheEntry(ctx, cacheKey, entry, s.authCfg.l2TTL)
	return entry, nil
}

func (s *APIKeyService) lookupAPIKeyForAuth(ctx context.Context, key string) (*APIKey, error) {
	if s == nil || s.apiKeyRepo == nil {
		return nil, ErrAPIKeyNotFound
	}
	if s.authLookupSlots == nil {
		return s.apiKeyRepo.GetByKeyForAuth(ctx, key)
	}
	s.authLookupTotal.Add(1)
	select {
	case s.authLookupSlots <- struct{}{}:
		s.authLookupInFlight.Add(1)
		defer func() {
			s.authLookupInFlight.Add(-1)
			<-s.authLookupSlots
		}()
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		s.authLookupRejected.Add(1)
		return nil, ErrAPIKeyAuthOverloaded
	}
	return s.apiKeyRepo.GetByKeyForAuth(ctx, key)
}

func (s *APIKeyService) applyAuthCacheEntry(key string, entry *APIKeyAuthCacheEntry) (*APIKey, bool, error) {
	if entry == nil {
		return nil, false, nil
	}
	if entry.NotFound {
		return nil, true, ErrAPIKeyNotFound
	}
	if entry.Snapshot == nil {
		return nil, false, nil
	}
	if entry.Snapshot.Version != apiKeyAuthSnapshotVersion {
		return nil, false, nil
	}
	return s.snapshotToAPIKey(key, entry.Snapshot), true, nil
}

func (s *APIKeyService) snapshotFromAPIKey(ctx context.Context, apiKey *APIKey) *APIKeyAuthSnapshot {
	if apiKey == nil || apiKey.User == nil {
		return nil
	}
	snapshot := &APIKeyAuthSnapshot{
		Version:     apiKeyAuthSnapshotVersion,
		APIKeyID:    apiKey.ID,
		UserID:      apiKey.UserID,
		GroupID:     apiKey.GroupID,
		Name:        apiKey.Name,
		Status:      apiKey.Status,
		IPWhitelist: apiKey.IPWhitelist,
		IPBlacklist: apiKey.IPBlacklist,
		Quota:       apiKey.Quota,
		QuotaUsed:   apiKey.QuotaUsed,
		ExpiresAt:   apiKey.ExpiresAt,
		RateLimit5h: apiKey.RateLimit5h,
		RateLimit1d: apiKey.RateLimit1d,
		RateLimit7d: apiKey.RateLimit7d,
		User: APIKeyAuthUserSnapshot{
			ID:                         apiKey.User.ID,
			Status:                     apiKey.User.Status,
			Role:                       apiKey.User.Role,
			Balance:                    apiKey.User.Balance,
			Concurrency:                apiKey.User.Concurrency,
			AllowedGroups:              apiKey.User.AllowedGroups,
			Email:                      apiKey.User.Email,
			Username:                   apiKey.User.Username,
			BalanceNotifyEnabled:       apiKey.User.BalanceNotifyEnabled,
			RestrictPublicGroups:       apiKey.User.RestrictPublicGroups,
			BalanceNotifyThresholdType: apiKey.User.BalanceNotifyThresholdType,
			BalanceNotifyThreshold:     apiKey.User.BalanceNotifyThreshold,
			BalanceNotifyExtraEmails:   apiKey.User.BalanceNotifyExtraEmails,
			TotalRecharged:             apiKey.User.TotalRecharged,
			RPMLimit:                   apiKey.User.RPMLimit,
			ParentMerchantID:           apiKey.User.ParentMerchantID,
		},
	}

	// 填充 (user, group) RPM override —— snapshot 构建时查一次 DB，后续请求零 DB 往返。
	if apiKey.GroupID != nil && *apiKey.GroupID > 0 && s.userGroupRateRepo != nil {
		override, err := s.userGroupRateRepo.GetRPMOverrideByUserAndGroup(ctx, apiKey.UserID, *apiKey.GroupID)
		if err == nil && override != nil {
			snapshot.User.UserGroupRPMOverride = override
		}
		// 查询失败或无 override 时留 nil，checkRPM 会回退到 DB 查询
	}
	if apiKey.Group != nil {
		snapshot.Group = &APIKeyAuthGroupSnapshot{
			ID:                              apiKey.Group.ID,
			Name:                            apiKey.Group.Name,
			Platform:                        apiKey.Group.Platform,
			IsExclusive:                     apiKey.Group.IsExclusive,
			Status:                          apiKey.Group.Status,
			SubscriptionType:                apiKey.Group.SubscriptionType,
			RateMultiplier:                  apiKey.Group.RateMultiplier,
			DailyLimitUSD:                   apiKey.Group.DailyLimitUSD,
			WeeklyLimitUSD:                  apiKey.Group.WeeklyLimitUSD,
			MonthlyLimitUSD:                 apiKey.Group.MonthlyLimitUSD,
			AllowImageGeneration:            apiKey.Group.AllowImageGeneration,
			AllowBatchImageGeneration:       apiKey.Group.AllowBatchImageGeneration,
			ImageRateIndependent:            apiKey.Group.ImageRateIndependent,
			ImageRateMultiplier:             apiKey.Group.ImageRateMultiplier,
			ImagePrice1K:                    apiKey.Group.ImagePrice1K,
			ImagePrice2K:                    apiKey.Group.ImagePrice2K,
			ImagePrice4K:                    apiKey.Group.ImagePrice4K,
			VideoRateIndependent:            apiKey.Group.VideoRateIndependent,
			VideoRateMultiplier:             apiKey.Group.VideoRateMultiplier,
			VideoPrice480P:                  apiKey.Group.VideoPrice480P,
			VideoPrice720P:                  apiKey.Group.VideoPrice720P,
			VideoPrice1080P:                 apiKey.Group.VideoPrice1080P,
			VideoModelPrices:                NormalizeVideoModelPrices(apiKey.Group.VideoModelPrices),
			WebSearchPricePerCall:           apiKey.Group.WebSearchPricePerCall,
			SearchPricePer1k:                apiKey.Group.SearchPricePer1k,
			AudioRealtimePricePerMin:        apiKey.Group.AudioRealtimePricePerMin,
			AudioTTSPricePerMillionChars:    apiKey.Group.AudioTTSPricePerMillionChars,
			AudioSTTPricePerHour:            apiKey.Group.AudioSTTPricePerHour,
			LongContextPricingEnabled:       apiKey.Group.LongContextPricingEnabled,
			ModelPricing:                    apiKey.Group.ModelPricing,
			ClaudeCodeOnly:                  apiKey.Group.ClaudeCodeOnly,
			FallbackGroupID:                 apiKey.Group.FallbackGroupID,
			FallbackGroupIDOnInvalidRequest: apiKey.Group.FallbackGroupIDOnInvalidRequest,
			ModelRouting:                    apiKey.Group.ModelRouting,
			ModelRoutingEnabled:             apiKey.Group.ModelRoutingEnabled,
			MCPXMLInject:                    apiKey.Group.MCPXMLInject,
			SupportedModelScopes:            apiKey.Group.SupportedModelScopes,
			AllowMessagesDispatch:           apiKey.Group.AllowMessagesDispatch,
			AllowLive:                       apiKey.Group.AllowLive,
			ForceOpenAIFast:                 apiKey.Group.ForceOpenAIFast,
			FreeOpenAIFast:                  apiKey.Group.FreeOpenAIFast,
			DefaultMappedModel:              apiKey.Group.DefaultMappedModel,
			MessagesDispatchModelConfig:     apiKey.Group.MessagesDispatchModelConfig,
			ModelAllowlist:                  apiKey.Group.ModelAllowlist,
			CodexModelsManifestConfig:       apiKey.Group.CodexModelsManifestConfig,
			RPMLimit:                        apiKey.Group.RPMLimit,
			AffiliateRebateExcluded:         apiKey.Group.AffiliateRebateExcluded,
			MaxReasoningEffort:              apiKey.Group.MaxReasoningEffort,
			MaxReasoningEffortOverLimit:     apiKey.Group.MaxReasoningEffortOverLimit,
			ReasoningEffortMappings:         apiKey.Group.ReasoningEffortMappings,
			PeakRateEnabled:                 apiKey.Group.PeakRateEnabled,
			PeakStart:                       apiKey.Group.PeakStart,
			PeakEnd:                         apiKey.Group.PeakEnd,
			PeakRateMultiplier:              apiKey.Group.PeakRateMultiplier,
			ProfitControlEnabled:            apiKey.Group.ProfitControlEnabled,
			ProfitMinMargin:                 apiKey.Group.ProfitMinMargin,
			ProfitSafetyBuffer:              apiKey.Group.ProfitSafetyBuffer,
		}
	}
	return snapshot
}

func (s *APIKeyService) snapshotToAPIKey(key string, snapshot *APIKeyAuthSnapshot) *APIKey {
	if snapshot == nil {
		return nil
	}
	apiKey := &APIKey{
		ID:          snapshot.APIKeyID,
		UserID:      snapshot.UserID,
		GroupID:     snapshot.GroupID,
		Key:         key,
		Name:        snapshot.Name,
		Status:      snapshot.Status,
		IPWhitelist: snapshot.IPWhitelist,
		IPBlacklist: snapshot.IPBlacklist,
		Quota:       snapshot.Quota,
		QuotaUsed:   snapshot.QuotaUsed,
		ExpiresAt:   snapshot.ExpiresAt,
		RateLimit5h: snapshot.RateLimit5h,
		RateLimit1d: snapshot.RateLimit1d,
		RateLimit7d: snapshot.RateLimit7d,
		User: &User{
			ID:                         snapshot.User.ID,
			Status:                     snapshot.User.Status,
			Role:                       snapshot.User.Role,
			Balance:                    snapshot.User.Balance,
			Concurrency:                snapshot.User.Concurrency,
			AllowedGroups:              snapshot.User.AllowedGroups,
			Email:                      snapshot.User.Email,
			Username:                   snapshot.User.Username,
			BalanceNotifyEnabled:       snapshot.User.BalanceNotifyEnabled,
			RestrictPublicGroups:       snapshot.User.RestrictPublicGroups,
			BalanceNotifyThresholdType: snapshot.User.BalanceNotifyThresholdType,
			BalanceNotifyThreshold:     snapshot.User.BalanceNotifyThreshold,
			BalanceNotifyExtraEmails:   snapshot.User.BalanceNotifyExtraEmails,
			TotalRecharged:             snapshot.User.TotalRecharged,
			RPMLimit:                   snapshot.User.RPMLimit,
			UserGroupRPMOverride:       snapshot.User.UserGroupRPMOverride,
			ParentMerchantID:           snapshot.User.ParentMerchantID,
		},
	}
	if snapshot.Group != nil {
		apiKey.Group = &Group{
			ID:                              snapshot.Group.ID,
			Name:                            snapshot.Group.Name,
			Platform:                        snapshot.Group.Platform,
			IsExclusive:                     snapshot.Group.IsExclusive,
			Status:                          snapshot.Group.Status,
			Hydrated:                        true,
			SubscriptionType:                snapshot.Group.SubscriptionType,
			RateMultiplier:                  snapshot.Group.RateMultiplier,
			DailyLimitUSD:                   snapshot.Group.DailyLimitUSD,
			WeeklyLimitUSD:                  snapshot.Group.WeeklyLimitUSD,
			MonthlyLimitUSD:                 snapshot.Group.MonthlyLimitUSD,
			AllowImageGeneration:            snapshot.Group.AllowImageGeneration,
			AllowBatchImageGeneration:       snapshot.Group.AllowBatchImageGeneration,
			ImageRateIndependent:            snapshot.Group.ImageRateIndependent,
			ImageRateMultiplier:             snapshot.Group.ImageRateMultiplier,
			ImagePrice1K:                    snapshot.Group.ImagePrice1K,
			ImagePrice2K:                    snapshot.Group.ImagePrice2K,
			ImagePrice4K:                    snapshot.Group.ImagePrice4K,
			VideoRateIndependent:            snapshot.Group.VideoRateIndependent,
			VideoRateMultiplier:             snapshot.Group.VideoRateMultiplier,
			VideoPrice480P:                  snapshot.Group.VideoPrice480P,
			VideoPrice720P:                  snapshot.Group.VideoPrice720P,
			VideoPrice1080P:                 snapshot.Group.VideoPrice1080P,
			VideoModelPrices:                NormalizeVideoModelPrices(snapshot.Group.VideoModelPrices),
			WebSearchPricePerCall:           snapshot.Group.WebSearchPricePerCall,
			SearchPricePer1k:                snapshot.Group.SearchPricePer1k,
			AudioRealtimePricePerMin:        snapshot.Group.AudioRealtimePricePerMin,
			AudioTTSPricePerMillionChars:    snapshot.Group.AudioTTSPricePerMillionChars,
			AudioSTTPricePerHour:            snapshot.Group.AudioSTTPricePerHour,
			LongContextPricingEnabled:       snapshot.Group.LongContextPricingEnabled,
			ModelPricing:                    snapshot.Group.ModelPricing,
			ClaudeCodeOnly:                  snapshot.Group.ClaudeCodeOnly,
			FallbackGroupID:                 snapshot.Group.FallbackGroupID,
			FallbackGroupIDOnInvalidRequest: snapshot.Group.FallbackGroupIDOnInvalidRequest,
			ModelRouting:                    snapshot.Group.ModelRouting,
			ModelRoutingEnabled:             snapshot.Group.ModelRoutingEnabled,
			MCPXMLInject:                    snapshot.Group.MCPXMLInject,
			SupportedModelScopes:            snapshot.Group.SupportedModelScopes,
			AllowMessagesDispatch:           snapshot.Group.AllowMessagesDispatch,
			AllowLive:                       snapshot.Group.AllowLive,
			ForceOpenAIFast:                 snapshot.Group.ForceOpenAIFast,
			FreeOpenAIFast:                  snapshot.Group.FreeOpenAIFast,
			DefaultMappedModel:              snapshot.Group.DefaultMappedModel,
			MessagesDispatchModelConfig:     snapshot.Group.MessagesDispatchModelConfig,
			ModelAllowlist:                  snapshot.Group.ModelAllowlist,
			CodexModelsManifestConfig:       snapshot.Group.CodexModelsManifestConfig,
			RPMLimit:                        snapshot.Group.RPMLimit,
			AffiliateRebateExcluded:         snapshot.Group.AffiliateRebateExcluded,
			MaxReasoningEffort:              snapshot.Group.MaxReasoningEffort,
			MaxReasoningEffortOverLimit:     snapshot.Group.MaxReasoningEffortOverLimit,
			ReasoningEffortMappings:         snapshot.Group.ReasoningEffortMappings,
			PeakRateEnabled:                 snapshot.Group.PeakRateEnabled,
			PeakStart:                       snapshot.Group.PeakStart,
			PeakEnd:                         snapshot.Group.PeakEnd,
			PeakRateMultiplier:              snapshot.Group.PeakRateMultiplier,
			ProfitControlEnabled:            snapshot.Group.ProfitControlEnabled,
			ProfitMinMargin:                 snapshot.Group.ProfitMinMargin,
			ProfitSafetyBuffer:              snapshot.Group.ProfitSafetyBuffer,
		}
	}
	s.compileAPIKeyIPRules(apiKey)
	return apiKey
}
