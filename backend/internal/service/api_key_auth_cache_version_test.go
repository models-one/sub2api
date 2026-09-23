package service

import "testing"

func TestAPIKeyService_RejectsV10AuthSnapshotWithoutModelAllowlist(t *testing.T) {
	groupID := int64(9)
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-models-list", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{
			Version:  10,
			APIKeyID: 1,
			UserID:   2,
			GroupID:  &groupID,
			Status:   StatusActive,
			User: APIKeyAuthUserSnapshot{
				ID:          2,
				Status:      StatusActive,
				Role:        RoleUser,
				Balance:     10,
				Concurrency: 3,
			},
			Group: &APIKeyAuthGroupSnapshot{
				ID:               groupID,
				Name:             "openai",
				Platform:         PlatformOpenAI,
				Status:           StatusActive,
				SubscriptionType: SubscriptionTypeStandard,
				RateMultiplier:   1,
			},
		},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatalf("expected v10 auth snapshot to be rejected after model_allowlist was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

// v14 快照缺 user.parent_merchant_id：若不整体拒绝，L2 Redis 中最长存活一个 TTL 窗口的
// 旧快照反序列化后 ParentMerchantID 恒为 nil，merchant suspended 拦截与 batch_image
// merchant 子用户守卫在灰度窗口内会被静默绕过（回归场景）。
func TestAPIKeyService_RejectsV14AuthSnapshotWithoutParentMerchantID(t *testing.T) {
	groupID := int64(9)
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-parent-merchant", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{
			Version:  14,
			APIKeyID: 1,
			UserID:   2,
			GroupID:  &groupID,
			Status:   StatusActive,
			User: APIKeyAuthUserSnapshot{
				ID:          2,
				Status:      StatusActive,
				Role:        RoleUser,
				Balance:     10,
				Concurrency: 3,
			},
			Group: &APIKeyAuthGroupSnapshot{
				ID:               groupID,
				Name:             "openai",
				Platform:         PlatformOpenAI,
				Status:           StatusActive,
				SubscriptionType: SubscriptionTypeStandard,
				RateMultiplier:   1,
			},
		},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatalf("expected v14 auth snapshot to be rejected after parent_merchant_id was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyService_RejectsV15AuthSnapshotWithoutReasoningEffortPolicy(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-reasoning-mappings", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 15},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v15 auth snapshot to be rejected after reasoning effort policy was added")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

// v16 曾被两条独立血脉同时使用、语义不同：本地 v16 = user.parent_merchant_id +
// group.affiliate_rebate_excluded，上游 v16 = group reasoning effort ceiling/mappings。
// 合并后快照同时含两套字段，必须整体拒绝任何 v16 条目，否则灰度窗口内命中另一血脉写的
// v16 缓存会通过版本校验、缺失字段读零值（merchant 停用守卫失效 / reasoning 策略读空）。
func TestAPIKeyService_RejectsV16AuthSnapshotFromEitherPreMergeLineage(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-v16", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 16},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v16 auth snapshot to be rejected: local and upstream v16 have different meanings, merged snapshot is past v16")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

// v17 是第三次同号异义：本地 v17（合并两条 v16 血脉的产物）与上游 v17（group Live gate）
// 含义不同，合并后升到 18，故 v17 条目同样必须整体拒绝。
func TestAPIKeyService_RejectsV17AuthSnapshotFromEitherLineage(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-v17", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 17},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v17 auth snapshot to be rejected: local v17 (merged v16 lineages) and upstream v17 (group Live gate) have different meanings")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

// v18 是第四次同号异义：本地 v18（合并两条 v17 血脉的产物：merchant 字段 + reasoning effort
// + AllowLive）与上游 v18（group profit control 三字段）含义不同，合并后升到 19，
// 故 v18 条目同样必须整体拒绝——否则任一条血脉的遗留条目会带着另一条血脉的零值字段生效。
func TestAPIKeyService_RejectsV18AuthSnapshotFromEitherLineage(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-v18", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 18},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v18 auth snapshot to be rejected: local v18 (merged v17 lineages) and upstream v18 (group profit control) have different meanings")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

// v19 是第五次同号异义：本地 v19（合并两条 v18 血脉的产物：merchant 字段 + reasoning effort
// + AllowLive + profit control）与上游 v19（分组级 search/audio/video_model_prices 计费字段，
// 迁移 217/218/219）含义不同，合并后升到 20，故 v19 条目同样必须整体拒绝——否则任一条血脉的
// 遗留条目会带着另一条血脉的零值字段生效（本地血脉命中会把分组级 search/audio/video 单价读成
// nil 而静默回落全局价计费）。
func TestAPIKeyService_RejectsV19AuthSnapshotFromEitherLineage(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-v19", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 19},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v19 auth snapshot to be rejected: local v19 (merged v18 lineages) and upstream v19 (group search/audio/video billing fields) have different meanings")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

// v20 是**第六次同号异义**，两条血脉都必须拒：
//   - 本地 v20 = 合并两条 v19 血脉的产物，但缺 LongContextPricingEnabled / ModelPricing
//     （上游 0.1.176 只把这两个分组计费字段加进了 repository 的鉴权投影、没加进快照，
//     本 fork 在 v21 补上）。反序列化后这两个字段是零值，长上下文阶梯静默关闭、
//     渠道区间定价塌到最便宜的第一档，属直接少收。
//   - 上游 v20（0.1.179 独立修了同一个 bug 后 bump 到 20）= 带分组计费字段，但缺本 fork
//     的 merchant/affiliate 字段（user.parent_merchant_id、group.affiliate_rebate_excluded），
//     命中后 merchant 停用守卫静默失效、返利排除读成 false。
//
// 两侧 v20 语义不同、字段集互有缺失，所以本轮合并**不能**因为「上游 v20 已经带分组计费
// 字段」就放行 v20：必须整体拒绝，由 v21 强制重建。
func TestAPIKeyService_RejectsV20AuthSnapshotMissingGroupPricingToggles(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-v20", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 20},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v20 auth snapshot to be rejected: local v20 predates LongContextPricingEnabled/ModelPricing (long-context ladders silently disabled, channel interval pricing collapsed to the cheapest tier) while upstream v20 lacks the fork's parent_merchant_id/affiliate_rebate_excluded (merchant suspended guard silently bypassed)")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

// v21 是**第七次同号异义**，两条血脉都必须拒：
//   - 本地 v21 = 本地 v20 + LongContextPricingEnabled / ModelPricing（本 fork 先于上游补上
//     分组计费字段），但**没有**上游 0.1.180+ 的三个分组开关。命中后 ForceOpenAIFast /
//     FreeOpenAIFast / MaxReasoningEffortOverLimit 读零值：分组强制 Fast 静默失效、
//     Fast 免费分组被照常计费（多收）、reasoning effort 超限策略从 deny 退化成 downgrade。
//   - 上游 v21（a44a1019f「Carry group Fast through auth snapshots」）= 上游 v20 +
//     group.force_openai_fast，但缺本 fork 的 merchant/affiliate 字段
//     （user.parent_merchant_id、group.affiliate_rebate_excluded）：命中后 MERCHANT-SYSTEM
//     停用商户拦截守卫静默失效、返利排除读成 false。
func TestAPIKeyService_RejectsV21AuthSnapshotFromEitherLineage(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-v21", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 21},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v21 auth snapshot to be rejected: local v21 (merged v20 lineages + long-context/model pricing) lacks upstream force_openai_fast/free_openai_fast/max_reasoning_effort_over_limit, while upstream v21 (group force_openai_fast) lacks the fork's parent_merchant_id/affiliate_rebate_excluded (merchant suspended guard silently bypassed)")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

// v22 只有上游血脉用过（e619ca386「Carry free Fast through auth snapshots」=
// 上游 v21 + group.free_openai_fast），但同样缺本 fork 的 merchant/affiliate 字段，
// 所以合并后必须整体拒绝。另外上游 v22 内部还有一次**未 bump** 的字段扩容
// （aa7a811e6 把 max_reasoning_effort_over_limit 加进快照却没抬版本），
// 因此上游 v22 条目本身就分「带/不带 over_limit」两种，更没有放行的余地。
func TestAPIKeyService_RejectsV22AuthSnapshotFromUpstreamLineage(t *testing.T) {
	svc := &APIKeyService{}

	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-v22", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 22},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v22 auth snapshot to be rejected: upstream v22 (group free_openai_fast) lacks the fork's parent_merchant_id/affiliate_rebate_excluded and is itself split by an un-bumped max_reasoning_effort_over_limit addition")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyService_RejectsV23AuthSnapshotFromEitherLineage(t *testing.T) {
	svc := &APIKeyService{}

	// 第八次撞号：本地 v23 = 上一轮两条血脉的合并产物（无 codex_models_manifest_config）；
	// 上游 v23（0.2.1 固定账号 Codex manifest）= 上游 v22 + codex_models_manifest_config，
	// 依旧没有本 fork 的 parent_merchant_id / affiliate_rebate_excluded。
	// 两条 v23 各缺对方的字段，任何一条被复用都会静默读零值，必须一律淘汰回源。
	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-v23", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 23},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v23 auth snapshot to be rejected: the local v23 lineage lacks codex_models_manifest_config while the upstream v23 lineage lacks the fork's parent_merchant_id/affiliate_rebate_excluded")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyService_RejectsV24AuthSnapshotFromEitherLineage(t *testing.T) {
	svc := &APIKeyService{}

	// 第九次撞号：本地 v24 = 上一轮两条血脉的合并产物（字段名还是 models_list_config）；
	// 上游 v24（0.2.4 cff3f8985「enforce model allowlists」）= 上游 v23 +
	// group.model_allowlist（models_list_config 更名且语义升级为准入白名单），
	// 依旧没有本 fork 的 parent_merchant_id / affiliate_rebate_excluded。
	//
	// 这一轮的零值方向尤其要留意：model_allowlist 读成零值即 Enabled=false，
	// 也就是「放行全部」——命中上游血脉的 v24 遗留条目时，
	// fork 的 merchant 停用守卫静默失效（要害），而白名单本身是**失效放行**，
	// 不会造成误拒，但同样等于准入约束静默消失。两个方向都不可接受，一律淘汰回源。
	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-v24", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 24},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v24 auth snapshot to be rejected: the local v24 lineage lacks group.model_allowlist while the upstream v24 lineage lacks the fork's parent_merchant_id/affiliate_rebate_excluded")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

func TestAPIKeyService_RejectsV25AuthSnapshotWithLegacyReasoningMultiplierKey(t *testing.T) {
	svc := &APIKeyService{}

	// v25 是本 fork 独有的号（上游 0.2.8 仍在 24），不是撞号；淘汰它是因为分组定价条目的
	// JSON 形状变了：v25 条目里 Group.ModelPricing 只带旧键 max_reasoning_effort_multiplier，
	// 上游 b8d52fad3 之后该字段已不存在，读出来 ReasoningEffortMultipliers=nil，
	// 分组级推理等级倍率静默按 1 倍计费。
	apiKey, ok, err := svc.applyAuthCacheEntry("k-legacy-v25", &APIKeyAuthCacheEntry{
		Snapshot: &APIKeyAuthSnapshot{Version: 25},
	})

	if err != nil {
		t.Fatalf("expected stale snapshot to be ignored without error, got %v", err)
	}
	if ok {
		t.Fatal("expected v25 auth snapshot to be rejected: its group model_pricing entries still carry the legacy max_reasoning_effort_multiplier key and would drop group reasoning effort multipliers")
	}
	if apiKey != nil {
		t.Fatalf("expected no API key from stale snapshot, got %#v", apiKey)
	}
}

// 版本号必须严格大于所有已知的撞号版本（v16…v24 各两条血脉，其中 v22 只有上游用过），
// 否则旧缓存会被误判有效。v24 是第九次撞号：上游 0.2.4 把 group.models_list_config
// 更名为 model_allowlist 并升级成准入白名单后 bump 到 24，而本 fork 上一轮已经在 24。
// 合并后的快照是两侧并集，所以下界抬到 24，本 fork 用 25 越过。
// 上游每给 group 加一个进快照的字段就 bump 一次（偶尔还会忘记 bump，见 aa7a811e6），
// 本 fork 也在加；下轮合并若上游再 bump 到 25，常量与这里的 lastCollidedVersion
// 必须一并继续抬高，不能沿用。
func TestAPIKeyAuthSnapshotVersion_IsPastAllCollidedLineages(t *testing.T) {
	const lastCollidedVersion = 24
	if apiKeyAuthSnapshotVersion <= lastCollidedVersion {
		t.Fatalf("apiKeyAuthSnapshotVersion must be > %d after merging the conflicting v16..v24 lineages, got %d", lastCollidedVersion, apiKeyAuthSnapshotVersion)
	}
}
