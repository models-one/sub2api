package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// billableDirectInput 复算 RecordUsage 的三桶互斥拆分
// （actualInput = input - cache_read - cache_creation，负值钳 0），
// 用于断言归一化后的 usage 经下游减法能还原真实 input。
func billableDirectInput(u OpenAIUsage) int {
	actual := u.InputTokens - u.CacheReadInputTokens - u.CacheCreationInputTokens
	if actual < 0 {
		actual = 0
	}
	return actual
}

// normalizeAnthropicDirectInputUsage 的口径契约：
//   - DeepSeek（语义已实测：input_tokens 只报缓存未命中数）无条件加回
//     cache_read + cache_creation；
//   - 其他平台（Kimi 等，语义未实测）仅在 input_tokens < cache_read + cache_creation
//     （明显为未命中口径）时加回，总量口径上游不得双重计费；
//   - cache_read 与 cache_creation 必须同口径一并加回：只加 cache_read 会被下游
//     三重减法多扣 cache_creation 一次，真实 input 被钳 0 少收钱。
func TestNormalizeAnthropicDirectInputUsage(t *testing.T) {
	t.Run("DeepSeek 未命中口径无条件加回", func(t *testing.T) {
		// 实测样例：3015 token prompt → input=71, cache_read=2944
		u := OpenAIUsage{InputTokens: 71, CacheReadInputTokens: 2944}
		normalizeAnthropicDirectInputUsage(PlatformDeepseek, &u)
		require.Equal(t, 3015, u.InputTokens)
	})

	t.Run("DeepSeek 新增内容超过缓存前缀也加回", func(t *testing.T) {
		u := OpenAIUsage{InputTokens: 5000, CacheReadInputTokens: 2944}
		normalizeAnthropicDirectInputUsage(PlatformDeepseek, &u)
		require.Equal(t, 7944, u.InputTokens, "条件判断 (input < cache) 会在此漏计")
	})

	t.Run("DeepSeek cache_creation 与 cache_read 同口径加回", func(t *testing.T) {
		// 回归：只加回 cache_read 时归一化=22000，下游减 28000 钳 0，真实 input 按 0 计费。
		u := OpenAIUsage{InputTokens: 2000, CacheReadInputTokens: 20000, CacheCreationInputTokens: 8000}
		normalizeAnthropicDirectInputUsage(PlatformDeepseek, &u)
		require.Equal(t, 30000, u.InputTokens)
		require.Equal(t, 2000, billableDirectInput(u), "三重扣减后必须还原真实 input")
	})

	t.Run("DeepSeek 纯 cache_creation 也加回", func(t *testing.T) {
		u := OpenAIUsage{InputTokens: 2000, CacheCreationInputTokens: 8000}
		normalizeAnthropicDirectInputUsage(PlatformDeepseek, &u)
		require.Equal(t, 10000, u.InputTokens)
		require.Equal(t, 2000, billableDirectInput(u))
	})

	t.Run("Moonshot 总量口径不得双重计费", func(t *testing.T) {
		// 若 Kimi 按 Anthropic 总量口径上报（input 已含全部输入），不应再加回
		u := OpenAIUsage{InputTokens: 3015, CacheReadInputTokens: 2944}
		normalizeAnthropicDirectInputUsage(PlatformKimi, &u)
		require.Equal(t, 3015, u.InputTokens)
	})

	t.Run("Moonshot 总量口径含 cache_creation 不得双重计费", func(t *testing.T) {
		u := OpenAIUsage{InputTokens: 30000, CacheReadInputTokens: 20000, CacheCreationInputTokens: 8000}
		normalizeAnthropicDirectInputUsage(PlatformKimi, &u)
		require.Equal(t, 30000, u.InputTokens)
		require.Equal(t, 2000, billableDirectInput(u))
	})

	t.Run("Moonshot 明显未命中口径仍加回", func(t *testing.T) {
		u := OpenAIUsage{InputTokens: 71, CacheReadInputTokens: 2944}
		normalizeAnthropicDirectInputUsage(PlatformKimi, &u)
		require.Equal(t, 3015, u.InputTokens)
	})

	t.Run("Moonshot 未命中口径 cache_creation 一并加回", func(t *testing.T) {
		u := OpenAIUsage{InputTokens: 2000, CacheReadInputTokens: 20000, CacheCreationInputTokens: 8000}
		normalizeAnthropicDirectInputUsage(PlatformKimi, &u)
		require.Equal(t, 30000, u.InputTokens)
		require.Equal(t, 2000, billableDirectInput(u))
	})

	t.Run("无缓存命中为 no-op", func(t *testing.T) {
		u := OpenAIUsage{InputTokens: 100}
		normalizeAnthropicDirectInputUsage(PlatformDeepseek, &u)
		require.Equal(t, 100, u.InputTokens)
	})
}

// 直连路径流式/非流式 usage 解析的端到端回归：上游 Anthropic 原生口径
// （input_tokens 只报未命中）带非零 cache_creation 时，归一化后的 usage
// 必须能经下游三重扣减还原真实 input（历史 bug：cache_creation 未加回，
// 归一化 22000 - 28000 钳 0，真实 input 按 0 计费）。
func TestAnthropicDirectUsageWithCacheCreation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}

	t.Run("流式 SSE", func(t *testing.T) {
		sse := strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_01","usage":{"input_tokens":2000,"cache_creation_input_tokens":8000,"cache_read_input_tokens":20000,"output_tokens":1}}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}`,
			``,
		}, "\n")
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)

		result, err := svc.handleAnthropicDirectStreamingResponse(resp, c, PlatformDeepseek, "deepseek-chat", "deepseek-chat", "deepseek-chat", nil, time.Now())
		require.NoError(t, err)
		require.Equal(t, 30000, result.Usage.InputTokens, "归一化后应为总量口径")
		require.Equal(t, 20000, result.Usage.CacheReadInputTokens)
		require.Equal(t, 8000, result.Usage.CacheCreationInputTokens)
		require.Equal(t, 50, result.Usage.OutputTokens)
		require.Equal(t, 2000, billableDirectInput(result.Usage), "三重扣减后必须还原真实 input")
	})

	t.Run("非流式 JSON", func(t *testing.T) {
		body := `{"id":"msg_02","type":"message","content":[{"type":"text","text":"hi"}],` +
			`"usage":{"input_tokens":2000,"cache_creation_input_tokens":8000,"cache_read_input_tokens":20000,"output_tokens":50}}`
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)

		result, err := svc.handleAnthropicDirectBufferedResponse(resp, c, PlatformDeepseek, "deepseek-chat", "deepseek-chat", "deepseek-chat", nil, time.Now())
		require.NoError(t, err)
		require.Equal(t, 30000, result.Usage.InputTokens, "归一化后应为总量口径")
		require.Equal(t, 20000, result.Usage.CacheReadInputTokens)
		require.Equal(t, 8000, result.Usage.CacheCreationInputTokens)
		require.Equal(t, 50, result.Usage.OutputTokens)
		require.Equal(t, 2000, billableDirectInput(result.Usage), "三重扣减后必须还原真实 input")
	})

	t.Run("非流式但上游返回 SSE", func(t *testing.T) {
		// stream=false 但上游仍回 SSE 时走 handleAnthropicDirectBufferedSSE，口径必须一致。
		sse := strings.Join([]string{
			`data: {"type":"message_start","message":{"id":"msg_03","model":"deepseek-chat","usage":{"input_tokens":2000,"cache_creation_input_tokens":8000,"cache_read_input_tokens":20000}}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}`,
			``,
		}, "\n")
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)

		result, err := svc.handleAnthropicDirectBufferedResponse(resp, c, PlatformDeepseek, "deepseek-chat", "deepseek-chat", "deepseek-chat", nil, time.Now())
		require.NoError(t, err)
		require.Equal(t, 30000, result.Usage.InputTokens, "归一化后应为总量口径")
		require.Equal(t, 2000, billableDirectInput(result.Usage), "三重扣减后必须还原真实 input")
	})
}

// buildAnthropicDirectMessagesURL 的逐平台 URL 约定契约。
// Zhipu 端点根因上游而异：智谱官方/z.ai 在 /api/anthropic 下，NewAPI 中转在根。
func TestBuildAnthropicDirectMessagesURL(t *testing.T) {
	apikey := func(platform, baseURL string) *Account {
		return &Account{Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": baseURL}}
	}
	cases := []struct {
		name    string
		account *Account
		want    string
	}{
		{"Deepseek 默认", &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey}, "https://api.deepseek.com/anthropic/v1/messages"},
		{"Kimi 默认", &Account{Platform: PlatformKimi, Type: AccountTypeAPIKey}, "https://api.kimi.com/coding/v1/messages"},
		{"Zhipu 官方默认 base 补 /api/anthropic", &Account{Platform: PlatformZhipu, Type: AccountTypeAPIKey}, "https://open.bigmodel.cn/api/anthropic/v1/messages"},
		{"Zhipu 官方 paas 根也归一到 /api/anthropic", apikey(PlatformZhipu, "https://open.bigmodel.cn/api/paas/v4"), "https://open.bigmodel.cn/api/anthropic/v1/messages"},
		{"Zhipu 官方已含 /api/anthropic 不重复", apikey(PlatformZhipu, "https://open.bigmodel.cn/api/anthropic"), "https://open.bigmodel.cn/api/anthropic/v1/messages"},
		{"Zhipu z.ai 补 /api/anthropic", apikey(PlatformZhipu, "https://api.z.ai"), "https://api.z.ai/api/anthropic/v1/messages"},
		{"Zhipu NewAPI 中转根直挂 /v1/messages", apikey(PlatformZhipu, "https://relay.orbitai.cc"), "https://relay.orbitai.cc/v1/messages"},
		{"Zhipu 中转 base 带 /v1 归一", apikey(PlatformZhipu, "https://relay.orbitai.cc/v1"), "https://relay.orbitai.cc/v1/messages"},
		{"Zhipu 中转 base 带末尾斜杠", apikey(PlatformZhipu, "https://relay.orbitai.cc/"), "https://relay.orbitai.cc/v1/messages"},
		{"未支持平台返回空", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, buildAnthropicDirectMessagesURL(tc.account))
		})
	}
}
