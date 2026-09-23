//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// geminiBrokenStreamBody 先交付一段带 usage 的 SSE，再以传输错误中断，模拟流式中途断开。
type geminiBrokenStreamBody struct {
	r *strings.Reader
}

func (b *geminiBrokenStreamBody) Read(p []byte) (int, error) {
	if b.r.Len() > 0 {
		return b.r.Read(p)
	}
	return 0, errors.New("upstream connection reset")
}

func (b *geminiBrokenStreamBody) Close() error { return nil }

// fork 的 PartialError 部分计费分支必须与正常完成路径同样携带 ReasoningEffort
// （上游 e47255715 只加在正常路径上），否则中断流会跳过渠道 reasoning_effort_multipliers。
func TestGeminiPartialStreamCarriesReasoningEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, provider := range []string{"api_key", "antigravity"} {
		t.Run(provider, func(t *testing.T) {
			const model = "gemini-3.1-pro-high"
			body, err := json.Marshal(map[string]any{
				"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": "hello"}}}},
				"generationConfig": map[string]any{"thinkingConfig": map[string]any{"thinkingLevel": "high"}},
			})
			require.NoError(t, err)

			chunk := `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`
			if provider == "antigravity" {
				chunk = `{"response":` + chunk + `}`
			}
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       &geminiBrokenStreamBody{r: strings.NewReader("data: " + chunk + "\n\n")},
			}}}
			c, _ := newAntigravityCompatContext(http.MethodPost, "/v1beta/models/"+model+":streamGenerateContent", body)

			var result *ForwardResult
			if provider == "antigravity" {
				svc := newAntigravityCompatService(config.GatewayConfig{}, upstream)
				result, err = svc.ForwardGemini(context.Background(), c, newAntigravityCompatAccount(AccountTypeOAuth), model, "streamGenerateContent", true, body, false)
			} else {
				svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}, tokenProvider: &GeminiTokenProvider{}}
				result, err = svc.ForwardNative(context.Background(), c, geminiSignalTestAccount(), model, "streamGenerateContent", true, body)
			}

			require.Error(t, err)
			require.NotNil(t, result, "a stream that already delivered tokens must return a partial result for billing")
			require.True(t, result.PartialError)
			require.Equal(t, "high", optionalStringValue(result.ReasoningEffort))
		})
	}
}
