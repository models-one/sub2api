//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 存量国产 APIKey 账号（没配 api_protocol）走 fork 的原生 Anthropic 直通
// forwardAnthropicDirect。该路径此前从不设 ReasoningEffort，渠道/分组配置的
// reasoning_effort_multipliers 对这批账号整体失效（恒按 1 倍）。
// 这里从 ForwardAsAnthropic 入口打到直通分支，再经 OpenAIGatewayService.RecordUsage
// 真实入账，断言用量日志的 reasoning_effort 与扣费金额都吃到了倍率。

const cnDirectReasoningInputTokens = 1_000_000

func legacyCNDirectAccount(platform string) *Account {
	baseURL := "https://open.bigmodel.cn"
	if platform == PlatformDeepseek {
		baseURL = "https://api.deepseek.com"
	}
	return &Account{
		ID:          9101,
		Name:        "legacy-cn-direct",
		Platform:    platform,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-cn-test", "base_url": baseURL},
	}
}

func cnDirectAnthropicSSE(model string) string {
	return strings.Join([]string{
		"event: message_start",
		fmt.Sprintf(`data: {"type":"message_start","message":{"id":"msg_cn","type":"message","role":"assistant","model":%q,"content":[],"usage":{"input_tokens":%d,"output_tokens":0}}}`, model, cnDirectReasoningInputTokens),
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
}

func cnDirectAnthropicJSON(model string) string {
	return fmt.Sprintf(`{"id":"msg_cn","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":%d,"output_tokens":0}}`, model, cnDirectReasoningInputTokens)
}

// cnDirectBrokenBody 先交付 message_start（带 usage）与一段文本，再以传输错误中断。
type cnDirectBrokenBody struct{ r *strings.Reader }

func (b *cnDirectBrokenBody) Read(p []byte) (int, error) {
	if b.r.Len() > 0 {
		return b.r.Read(p)
	}
	return 0, errors.New("upstream connection reset")
}

func (b *cnDirectBrokenBody) Close() error { return nil }

func cnDirectBrokenStream(model string) io.ReadCloser {
	partial := strings.Join([]string{
		"event: message_start",
		fmt.Sprintf(`data: {"type":"message_start","message":{"id":"msg_cn","type":"message","role":"assistant","model":%q,"content":[],"usage":{"input_tokens":%d,"output_tokens":0}}}`, model, cnDirectReasoningInputTokens),
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"par"}}`,
		"",
	}, "\n")
	return &cnDirectBrokenBody{r: strings.NewReader(partial)}
}

// recordCNDirectUsage 按 handler 的 submitMessagesUsage 口径把转发结果入账，
// 返回写入的用量日志与实际扣减的余额。
func recordCNDirectUsage(t *testing.T, result *OpenAIForwardResult, account *Account) (*UsageLog, float64) {
	t.Helper()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	groupID := int64(9201)
	inputPrice, outputPrice := 1e-6, 0.0
	group := &Group{
		ID: groupID, Platform: account.Platform, Status: StatusActive, Hydrated: true, RateMultiplier: 1,
		ModelPricing: []ChannelModelPricing{{
			Models: []string{result.BillingModel}, BillingMode: BillingModeToken,
			InputPrice: &inputPrice, OutputPrice: &outputPrice,
			ReasoningEffortMultipliers: map[string]float64{"high": 2, "max": 3},
		}},
	}
	require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result:  result,
		APIKey:  &APIKey{ID: 9301, GroupID: &groupID, Group: group},
		User:    &User{ID: 9401},
		Account: account,
	}))
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 1, userRepo.deductCalls)
	return usageRepo.lastLog, userRepo.lastAmount
}

func TestLegacyCNAnthropicDirect_ReasoningEffortMultiplierBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)

	type responseKind string
	const (
		respStream       responseKind = "stream"
		respJSON         responseKind = "json"
		respBufferedSSE  responseKind = "buffered-sse" // stream=false 但上游仍回 SSE
		respPartialBreak responseKind = "partial-stream"
	)

	for _, tc := range []struct {
		name       string
		platform   string
		model      string
		extra      string // 追加到请求体的字段（effort / thinking）
		wantEffort string
		wantCost   float64
		// 转发体里 output_config.effort 的期望值（空串=不存在）：计费档位必须与上游实际
		// 收到的一致，兜底档位（thinking 启用、未带 effort）只进计费、不写进转发体。
		wantForwardedEffort string
	}{
		{"explicit high", PlatformZhipu, "glm-5.2", `,"output_config":{"effort":"high"}`, "high", 2, "high"},
		{"explicit max", PlatformZhipu, "glm-5.2", `,"output_config":{"effort":"max"}`, "max", 3, "max"},
		// thinking 已启用、没带 effort：GLM 属 passback-required，兜底为 high。
		{"thinking enabled fallback", PlatformZhipu, "glm-5.2", `,"thinking":{"type":"enabled","budget_tokens":1024}`, "high", 2, ""},
		// DeepSeek 原生支持 effort 档位，不注入默认值——保持 1 倍，与原生直通口径一致。
		{"deepseek thinking no fallback", PlatformDeepseek, "deepseek-v4-pro", `,"thinking":{"type":"enabled","budget_tokens":1024}`, "", 1, ""},
		{"no effort", PlatformZhipu, "glm-5.2", ``, "", 1, ""},
		// glm-5.3：本路径不做上游原生直通的 NormalizeGLM53AnthropicThinking 改写，
		// 上游收到的就是 medium，计费也按 medium（未配置 → 1 倍），不按原生直通会改成的 high。
		{"glm-5.3 forwarded as-is", PlatformZhipu, "glm-5.3", `,"output_config":{"effort":"medium"}`, "medium", 1, "medium"},
	} {
		for _, kind := range []responseKind{respStream, respJSON, respBufferedSSE, respPartialBreak} {
			t.Run(tc.name+"/"+string(kind), func(t *testing.T) {
				clientStream := kind == respStream || kind == respPartialBreak
				body := []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"max_tokens":32,"messages":[{"role":"user","content":"hello"}]%s}`,
					tc.model, clientStream, tc.extra))

				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
				switch kind {
				case respStream, respBufferedSSE:
					resp.Header.Set("Content-Type", "text/event-stream")
					resp.Body = io.NopCloser(strings.NewReader(cnDirectAnthropicSSE(tc.model)))
				case respJSON:
					resp.Header.Set("Content-Type", "application/json")
					resp.Body = io.NopCloser(strings.NewReader(cnDirectAnthropicJSON(tc.model)))
				case respPartialBreak:
					resp.Header.Set("Content-Type", "text/event-stream")
					resp.Body = cnDirectBrokenStream(tc.model)
				}
				upstream := &httpUpstreamRecorder{resp: resp}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				account := legacyCNDirectAccount(tc.platform)
				require.True(t, usesLegacyCNAnthropicDirect(account), "fixture must hit the legacy direct branch")

				result, err := svc.ForwardAsAnthropic(context.Background(), adaptiveProtocolTestContext("/v1/messages", body), account, body, "", "")
				if kind == respPartialBreak {
					require.Error(t, err)
					require.NotNil(t, result, "a stream that already delivered tokens must return a billable partial result")
					require.True(t, result.ClientDisconnect)
				} else {
					require.NoError(t, err)
					require.NotNil(t, result)
				}

				// 确认走的是原生 Anthropic 直通，而不是 CC/Responses 转换。
				require.NotNil(t, upstream.lastReq)
				require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, "/v1/messages"), upstream.lastReq.URL.String())
				require.Equal(t, tc.model, gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, tc.wantForwardedEffort, gjson.GetBytes(upstream.lastBody, "output_config.effort").String())

				require.Equal(t, tc.wantEffort, optionalStringValue(result.ReasoningEffort))
				require.Equal(t, cnDirectReasoningInputTokens, result.Usage.InputTokens)

				log, charged := recordCNDirectUsage(t, result, account)
				require.Equal(t, tc.wantEffort, optionalStringValue(log.ReasoningEffort))
				require.InDelta(t, tc.wantCost, log.TotalCost, 1e-9)
				require.InDelta(t, tc.wantCost, log.ActualCost, 1e-9)
				require.InDelta(t, tc.wantCost, charged, 1e-9)
			})
		}
	}
}

// cnDirectFailingWriter 模拟客户端已断开：任何写入都失败。
type cnDirectFailingWriter struct{ header http.Header }

func (w *cnDirectFailingWriter) Header() http.Header       { return w.header }
func (w *cnDirectFailingWriter) WriteHeader(int)           {}
func (w *cnDirectFailingWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }

// 客户端写失败的提前返回点同样要带 ReasoningEffort（此时 usage 尚为零，只断言字段本身，
// 防止以后 message_start 之后才断开的场景漏掉倍率）。
func TestLegacyCNAnthropicDirect_ClientWriteFailureKeepsReasoningEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"glm-5.2","stream":true,"max_tokens":32,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hello"}]}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(cnDirectAnthropicSSE("glm-5.2"))),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	c, _ := gin.CreateTestContext(&cnDirectFailingWriter{header: http.Header{}})
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	result, err := svc.ForwardAsAnthropic(context.Background(), c, legacyCNDirectAccount(PlatformZhipu), body, "", "")
	require.Error(t, err)
	require.NotNil(t, result)
	require.True(t, result.ClientDisconnect)
	require.Equal(t, "high", optionalStringValue(result.ReasoningEffort))
}
