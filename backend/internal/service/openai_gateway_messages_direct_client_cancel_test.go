//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 存量国产直通 forwardAnthropicDirect 对「客户端已断开」的传输错误须与上游 0.2.9
// （f4f8ff04d）的统一口径一致：不记 ops 上游事件、不写 502、不触发换号；
// 真实的上游传输错误则照旧记 request_error 并回写 502。
func TestLegacyCNAnthropicDirect_TransportErrorClientCanceled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"glm-5.2","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)

	t.Run("client canceled", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body)).WithContext(cancelCtx)
		upstream := &httpUpstreamRecorder{err: fmt.Errorf("Post upstream: %w", context.Canceled)}
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

		result, err := svc.ForwardAsAnthropic(cancelCtx, c, legacyCNDirectAccount(PlatformZhipu), body, "", "")
		require.Nil(t, result)
		require.ErrorIs(t, err, context.Canceled)
		var failoverErr *UpstreamFailoverError
		require.False(t, errors.As(err, &failoverErr))
		require.False(t, c.Writer.Written(), "handler owns the response for a client that is already gone")
		_, hasEvents := c.Get(OpsUpstreamErrorsKey)
		require.False(t, hasEvents, "client disconnect must not be recorded as an upstream error")
	})

	t.Run("real transport error", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		upstream := &httpUpstreamRecorder{err: errors.New("dial tcp: connection refused")}
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

		result, err := svc.ForwardAsAnthropic(context.Background(), c, legacyCNDirectAccount(PlatformZhipu), body, "", "")
		require.Nil(t, result)
		require.Error(t, err)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		events, ok := c.Get(OpsUpstreamErrorsKey)
		require.True(t, ok)
		require.Len(t, events.([]*OpsUpstreamErrorEvent), 1)
		require.Equal(t, "request_error", events.([]*OpsUpstreamErrorEvent)[0].Kind)
	})
}
