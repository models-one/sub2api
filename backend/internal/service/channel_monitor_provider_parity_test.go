//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 渠道监控的 provider 白名单散在五处（monitorProviders / probeCapableProviders /
// providerAdapters / bodyMergeKeyDenyList / isOpenAICompatibleChatProvider），
// 而迁移的 CHECK、ent 校验器、前端 PROVIDERS 各自又是一份。
// 上游每加一个平台就只改自己那几处：0.2.4 加 minimax 时本 fork 手工补齐了五处，
// 0.2.5 加 opencode_go 时五处一处都没补——迁移/ent/handler/前端全放行，
// 唯独 validateProvider 拒死，整条监控链路对新平台不可用且报错指向「provider 非法」。
//
// 这条守卫把「能被调度的平台」与「监控白名单」钉在一起，新增平台时自动变红。
func TestMonitorProvidersCoverSchedulablePlatforms(t *testing.T) {
	// 已拍板「暂不支持渠道监控」的平台，显式登记原因；守卫对其余（含今后新增）平台照常生效。
	// 要为其中某个平台补监控时，删掉对应条目即可让守卫重新兜底。
	monitorUnsupported := map[string]string{
		// 上游 0.2.12 新增的 TypeSafe/Jev：上游渠道监控同样未纳入。System One 只有非流式原生端点，
		// 不兼容 Chat Completions / Responses，现有探活 adapter 都打不了；
		// 要支持需同步改迁移 226 的 CHECK、ent 校验器、前端 PROVIDERS 并新写 adapter。
		PlatformTypeSafe: "System One 无可复用的探活 adapter",
		// 上游 0.2.15 新增的多协议聚合平台：上游渠道监控同样未纳入；按模型分流、端点与鉴权
		// 随 profile 变化，现有 adapter 不能直接复用。补监控同样需要迁移 CHECK / ent / 前端 / adapter。
		PlatformCommandCode: "按模型分流的多协议聚合平台，无对应探活 adapter",
		PlatformCline:       "按模型分流的多协议聚合平台，无对应探活 adapter",
	}
	// 监控面向的是有上游可打的具体供应商；composite 是路由聚合、不直接对应上游。
	for _, platform := range AllowedQuotaPlatforms {
		if _, skip := monitorUnsupported[platform]; skip {
			require.NotContains(t, monitorProviders, platform,
				"平台 %s 已进 monitorProviders，请从 monitorUnsupported 豁免表里删掉它", platform)
			continue
		}
		if platform == PlatformAntigravity {
			// antigravity 只支持配额模式，probeCapableProviders 有意不含它。
			require.Contains(t, monitorProviders, platform,
				"平台 %s 不在 monitorProviders 里，validateProvider 会拒绝为它建监控", platform)
			continue
		}
		require.Contains(t, monitorProviders, platform,
			"平台 %s 不在 monitorProviders 里，validateProvider 会拒绝为它建监控", platform)
		require.Contains(t, probeCapableProviders, platform,
			"平台 %s 不在 probeCapableProviders 里，probe / quota_probe 模式会被拒", platform)
		require.Contains(t, providerAdapters, platform,
			"平台 %s 没有探活 adapter，检测会拿不到 buildPath/buildBody", platform)
	}
}

// 错误提示串必须列全 monitorProviders，否则运维照着提示排查会被误导。
func TestChannelMonitorInvalidProviderMessageListsEveryProvider(t *testing.T) {
	msg := ErrChannelMonitorInvalidProvider.Error()
	for provider := range monitorProviders {
		require.True(t, strings.Contains(msg, provider),
			"ErrChannelMonitorInvalidProvider 的提示串漏了 %s：%s", provider, msg)
	}
}

// OpenAI 兼容判定与 bodyMergeKeyDenyList 必须同集：
// 前者决定走不走 OpenAI 兼容探活，后者是这条路径上的请求体合并白名单，
// 少一个平台会让自定义请求体模板对它静默失效。
func TestOpenAICompatibleProvidersHaveBodyMergeKeys(t *testing.T) {
	for provider := range monitorProviders {
		if !isOpenAICompatibleChatProvider(provider) {
			continue
		}
		// openai 在这张表里按 API 模式分键（openai:chat_completions / openai:responses），
		// 其余兼容供应商用平台名本身作键。
		if provider == MonitorProviderOpenAI {
			require.Contains(t, bodyMergeKeyDenyList, MonitorProviderOpenAI+":"+MonitorAPIModeChatCompletions,
				"openai 缺 chat_completions 模式的 bodyMergeKeyDenyList 条目")
			continue
		}
		require.Contains(t, bodyMergeKeyDenyList, provider,
			"OpenAI 兼容 provider %s 缺 bodyMergeKeyDenyList 条目", provider)
	}
}
