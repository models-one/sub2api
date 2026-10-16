//go:build unit

package service

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 网关鉴权热路径 100% 走 APIKeyService.GetByKey → applyAuthCacheEntry → snapshotToAPIKey，
// 返回的是**快照重建**出来的 apiKey，而且被标记 Hydrated: true（IsGroupContextValid 因此认它可信）。
// 只有 snapshotFromAPIKey 返回 nil 时才会掉到直连 DB 的兜底分支。
//
// 后果：只要上游给 Group 加了新字段、却只加进 repository 的 Select 投影而没加进
// APIKeyAuthGroupSnapshot，热路径上那个字段就恒为 Go 零值。计费字段上的零值几乎总是
// "少收"方向（bool 零值 = 关闭加价、slice 零值 = 没有定价覆盖），而且**所有单测都会照常通过**，
// 因为测试都直接构造 Group{...} 而不经过快照往返。
//
// 0.1.176 就踩了这颗雷：上游新增 long_context_pricing_enabled / model_pricing 只进了
// api_key_repo.go 的投影，快照没跟，导致 resolver 里 longContextPricingEnabled 恒 false ——
// 全平台长上下文加价静默关闭，且渠道区间定价被 applyFirstTokenTier 塌到最便宜的第一档。
//
// 这条守卫用反射把 Group 的每个导出字段填成非零值，跑一遍真实的
// snapshotFromAPIKey → snapshotToAPIKey，断言没有字段被往返吃成零值。
// 故意不进快照的字段放进 allowlist 并写明理由——加字段的人必须显式选一边。
func TestAPIKeyAuthSnapshot_GroupFieldParity_NoSilentDrop(t *testing.T) {
	// 故意不进快照的 Group 字段。新增字段时要么补进快照，要么在这里登记理由。
	intentionallyNotSnapshotted := map[string]string{
		"CreatedAt":   "审计时间戳，网关热路径不读",
		"UpdatedAt":   "审计时间戳，网关热路径不读",
		"DeletedAt":   "软删标记；能取到 key 就说明分组没被删",
		"Description": "纯展示文案，网关热路径不读",
		// 上游 0.2.16（9a5133310）新增：分组名称 / 描述的界面语言译文，只在从库加载分组的
		// 展示接口（DTO 本地化、订阅、可用渠道、模型广场、支付套餐）里使用；上游快照同样不带。
		"I18n":      "纯展示译文，网关热路径不读",
		"SortOrder": "管理端列表排序，网关热路径不读",

		"AccountGroups":           "关联实体，仅管理端详情页需要",
		"AccountCount":            "管理端聚合统计，非持久化字段",
		"ActiveAccountCount":      "管理端聚合统计，非持久化字段",
		"RateLimitedAccountCount": "管理端聚合统计，非持久化字段",

		"DuplicateOperationID": "复制分组的幂等键，仅 admin 写路径使用",
		"DefaultValidityDays":  "建 key 时的默认有效期，仅 admin/用户建 key 路径使用",
		"RequireOAuthOnly":     "分组准入策略，由 admin 侧与调度层读取，不在计费热路径",
		"RequirePrivacySet":    "同上",

		// 下面三个是计费字段，但走 openai_gateway_usage.apiKeyWithFreshGroupMediaPricing
		// 的按需回源（groupMediaPricingLooksIncomplete 命中时用 GetByIDLite 重查分组），
		// 属上游既有设计。注意该回源只挂在图片/视频成本函数上、且启发式会在
		// ImageRateMultiplier 非零时提前判「完整」，所以**只有媒体计费能靠它兜底**；
		// 任何 token 计费相关的新字段都必须直接进快照，不能指望这条路。
		"VideoModelPrices":             "媒体计费，按需回源（见 groupMediaPricingLooksIncomplete）",
		"BatchImageDiscountMultiplier": "批量图片计费，随媒体价一并按需回源",
		"BatchImageHoldMultiplier":     "批量图片预扣，随媒体价一并按需回源",
	}

	groupType := reflect.TypeOf(Group{})
	filled := &Group{}
	fillNonZeroStruct(t, reflect.ValueOf(filled).Elem())

	svc := NewAPIKeyService(nil, nil, nil, nil, nil, nil, &config.Config{})
	apiKey := &APIKey{
		ID:      1,
		UserID:  2,
		Key:     "k-group-field-parity",
		Name:    "parity",
		Status:  StatusActive,
		GroupID: &filled.ID,
		User: &User{
			ID:     2,
			Status: StatusActive,
			Role:   RoleUser,
		},
		Group: filled,
	}

	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	require.NotNil(t, snapshot, "snapshotFromAPIKey 返回 nil 会让热路径掉到 DB 兜底，本用例前提不成立")
	roundTrip := svc.snapshotToAPIKey(apiKey.Key, snapshot)
	require.NotNil(t, roundTrip)
	require.NotNil(t, roundTrip.Group)

	var dropped []string
	after := reflect.ValueOf(roundTrip.Group).Elem()
	for i := 0; i < groupType.NumField(); i++ {
		field := groupType.Field(i)
		if field.PkgPath != "" { // 未导出
			continue
		}
		if _, ok := intentionallyNotSnapshotted[field.Name]; ok {
			continue
		}
		if after.Field(i).IsZero() {
			dropped = append(dropped, field.Name)
		}
	}

	require.Emptyf(t, dropped, "这些 Group 字段经鉴权快照往返后变成了零值，网关热路径永远读不到真实值："+
		"%v —— 请补进 APIKeyAuthGroupSnapshot 的字段定义 + snapshotFromAPIKey + snapshotToAPIKey 三处，"+
		"并把 apiKeyAuthSnapshotVersion 抬一级作废存量快照；若确属无需快照的字段，请登记进本用例的 "+
		"intentionallyNotSnapshotted 并写明理由", dropped)
}

// 定向回归：0.1.176 上游新增的两个分组计费字段必须经快照往返存活。
// LongContextPricingEnabled 丢失 ⇒ 长上下文加价全平台静默关闭 + 渠道区间定价塌到第一档；
// ModelPricing 丢失 ⇒ 分组逐模型定价完全不生效。
func TestAPIKeyAuthSnapshot_PreservesGroupPricingToggles(t *testing.T) {
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, nil, &config.Config{})
	inputPrice := 1.25
	group := &Group{
		ID:                        7,
		Name:                      "long-ctx",
		Platform:                  PlatformOpenAI,
		Status:                    StatusActive,
		SubscriptionType:          SubscriptionTypeStandard,
		RateMultiplier:            1,
		LongContextPricingEnabled: true,
		ModelPricing: []ChannelModelPricing{
			{Models: []string{"gpt-5.6"}, InputPrice: &inputPrice},
		},
	}
	apiKey := &APIKey{
		ID:      1,
		UserID:  2,
		Key:     "k-group-pricing-toggles",
		Name:    "toggles",
		Status:  StatusActive,
		GroupID: &group.ID,
		User:    &User{ID: 2, Status: StatusActive, Role: RoleUser},
		Group:   group,
	}

	roundTrip := svc.snapshotToAPIKey(apiKey.Key, svc.snapshotFromAPIKey(context.Background(), apiKey))
	require.NotNil(t, roundTrip)
	require.NotNil(t, roundTrip.Group)

	require.True(t, roundTrip.Group.LongContextPricingEnabled,
		"LongContextPricingEnabled 经快照往返丢失：热路径恒读到 false，长上下文加价全线关闭")
	require.Len(t, roundTrip.Group.ModelPricing, 1,
		"ModelPricing 经快照往返丢失：分组逐模型定价在网关侧完全不生效")
	require.Equal(t, []string{"gpt-5.6"}, roundTrip.Group.ModelPricing[0].Models)
	require.NotNil(t, roundTrip.Group.ModelPricing[0].InputPrice)
	require.InDelta(t, inputPrice, *roundTrip.Group.ModelPricing[0].InputPrice, 1e-9)
}

// fillNonZeroStruct 把结构体的每个导出字段填成可辨识的非零值，用于「往返后是否被吃成零值」的检测。
// 只需要顶层字段非零即可判定丢失，故限制递归深度，避免自引用类型（如带 map[string]any 的
// 配置结构）把栈撑爆。
func fillNonZeroStruct(t *testing.T, v reflect.Value) {
	t.Helper()
	fillNonZeroStructDepth(t, v, 0)
}

const fillMaxDepth = 4

func fillNonZeroStructDepth(t *testing.T, v reflect.Value, depth int) {
	t.Helper()
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).PkgPath != "" {
			continue
		}
		fillNonZeroValue(t, v.Field(i), depth)
	}
}

func fillNonZeroValue(t *testing.T, v reflect.Value, depth int) {
	t.Helper()
	if !v.CanSet() || depth > fillMaxDepth {
		return
	}
	// time.Time 有未导出字段，按值整体赋一个非零时刻。
	if v.Type() == reflect.TypeOf(time.Time{}) {
		v.Set(reflect.ValueOf(time.Unix(1735689600, 0).UTC()))
		return
	}
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.String:
		v.SetString("x")
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		fillNonZeroValue(t, v.Elem(), depth+1)
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		fillNonZeroValue(t, elem, depth+1)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), elem))
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		fillNonZeroValue(t, key, depth+1)
		val := reflect.New(v.Type().Elem()).Elem()
		fillNonZeroValue(t, val, depth+1)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.Interface:
		if v.NumMethod() == 0 { // any
			v.Set(reflect.ValueOf("x"))
		}
	case reflect.Struct:
		fillNonZeroStructDepth(t, v, depth+1)
	}
}
