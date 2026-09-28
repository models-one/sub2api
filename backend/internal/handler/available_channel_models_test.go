//go:build unit

package handler

import (
	"reflect"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func accountWithMapping(platform string, models ...string) service.Account {
	mapping := make(map[string]any, len(models))
	for _, m := range models {
		mapping[m] = m
	}
	return service.Account{
		Platform:    platform,
		Status:      service.StatusActive,
		Credentials: map[string]any{"model_mapping": mapping},
	}
}

// 三个账号里只有一个显式支持 claude-fable-5-1 时，模型必须出现在列表里。
// 历史实现取交集，会把它静默抹掉；网关 /v1/models 一直是并集，这里跟它对齐。
func TestResolveGroupModelsFromAccounts_UnionNotIntersection(t *testing.T) {
	accounts := []service.Account{
		accountWithMapping(service.PlatformAnthropic, "claude-opus-5", "claude-sonnet-5"),
		accountWithMapping(service.PlatformAnthropic, "claude-opus-5", "claude-fable-5-1"),
		accountWithMapping(service.PlatformAnthropic, "claude-opus-5"),
	}

	got := resolveGroupModelsFromAccounts(service.PlatformAnthropic, accounts, nil)
	want := []string{"claude-fable-5-1", "claude-opus-5", "claude-sonnet-5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected union %v, got %v", want, got)
	}
}

func TestResolveGroupModelsFromAccounts_SkipsInactiveOtherPlatformAndWildcard(t *testing.T) {
	inactive := accountWithMapping(service.PlatformAnthropic, "claude-inactive")
	inactive.Status = service.StatusDisabled

	otherPlatform := accountWithMapping(service.PlatformOpenAI, "gpt-5.1")

	wildcardOnly := accountWithMapping(service.PlatformAnthropic, "claude-*")

	accounts := []service.Account{
		inactive,
		otherPlatform,
		wildcardOnly,
		accountWithMapping(service.PlatformAnthropic, "claude-opus-5", "claude-*"),
	}

	got := resolveGroupModelsFromAccounts(service.PlatformAnthropic, accounts, nil)
	want := []string{"claude-opus-5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

// 没有账号 / 账号全透传（空 mapping）时回落到 LiteLLM 价目表。
func TestResolveGroupModelsFromAccounts_FallsBackWhenAllPassthrough(t *testing.T) {
	fallback := func() []string { return []string{"claude-opus-5"} }

	passthrough := service.Account{
		Platform: service.PlatformAnthropic,
		Status:   service.StatusActive,
		// 无 credentials → GetModelMapping 为空 → 透传
	}

	cases := map[string][]service.Account{
		"no accounts":     nil,
		"all passthrough": {passthrough},
	}
	for name, accounts := range cases {
		t.Run(name, func(t *testing.T) {
			got := resolveGroupModelsFromAccounts(service.PlatformAnthropic, accounts, fallback)
			if !reflect.DeepEqual(got, []string{"claude-opus-5"}) {
				t.Fatalf("expected litellm fallback, got %v", got)
			}
		})
	}
}

// 分组自定义模型列表优先于账号推断，且保留后台配置的顺序。
func TestGroupCustomModelsList_PreservesAdminOrderAndFiltersNoise(t *testing.T) {
	g := &service.Group{
		ID:       1,
		Platform: service.PlatformAnthropic,
		ModelAllowlist: service.GroupModelAllowlist{
			Enabled: true,
			Models:  []string{"claude-fable-5-1", " ", "claude-opus-5", "claude-fable-5-1", "claude-*", "claude-*-thinking", "*-haiku"},
		},
	}

	got := groupCustomModelsList(g)
	want := []string{"claude-fable-5-1", "claude-opus-5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestGroupCustomModelsList_DisabledOrEmpty(t *testing.T) {
	disabled := &service.Group{ModelAllowlist: service.GroupModelAllowlist{
		Enabled: false,
		Models:  []string{"claude-fable-5-1"},
	}}
	if got := groupCustomModelsList(disabled); got != nil {
		t.Fatalf("expected nil for disabled config, got %v", got)
	}

	// enabled 但条目全是噪声 → 返回空，调用方回落到账号推断
	noisy := &service.Group{ModelAllowlist: service.GroupModelAllowlist{
		Enabled: true,
		Models:  []string{"claude-*", "  ", "gpt-*-codex", "*-mini"},
	}}
	if got := groupCustomModelsList(noisy); len(got) != 0 {
		t.Fatalf("expected empty for wildcard-only config, got %v", got)
	}
}
