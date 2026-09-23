package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestAccountTestService_TestClaudeOAuth_MimicsOfficialClient 验证 Claude OAuth 账号的
// 测试请求会完整模拟官方 Claude Code 客户端（与生产网关 buildUpstreamRequest 的 mimic
// 路径一致）。否则上游"客户端限制"会把测试请求判为第三方而返回 403，进而在 403 分支被
// SetError 误封账号——这正是本次修复要避免的问题。
func TestAccountTestService_TestClaudeOAuth_MimicsOfficialClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	account := Account{
		ID:          10,
		Name:        "claude-oauth",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "oauth-access-token",
		},
	}

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n")),
	}}

	svc := &AccountTestService{
		accountRepo:  &stubOpenAIAccountRepo{accounts: []Account{account}},
		httpUpstream: upstream,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/10/test", bytes.NewReader(nil))

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)

	// 目标 URL 与生产 OAuth 路径一致
	require.Equal(t, testClaudeAPIURL, upstream.lastReq.URL.String())

	// 认证头
	require.Equal(t, "Bearer oauth-access-token", getHeaderRaw(upstream.lastReq.Header, "authorization"))

	// 官方 Claude Code 客户端指纹头（由 applyClaudeCodeMimicHeaders 注入）
	require.Equal(t, claude.DefaultHeaders()["User-Agent"], getHeaderRaw(upstream.lastReq.Header, "User-Agent"))
	require.Equal(t, "cli", getHeaderRaw(upstream.lastReq.Header, "x-app"))
	require.Equal(t, "application/json", getHeaderRaw(upstream.lastReq.Header, "Accept"))
	require.Equal(t, "stream", getHeaderRaw(upstream.lastReq.Header, "x-stainless-helper-method"))
	require.NotEmpty(t, getHeaderRaw(upstream.lastReq.Header, "x-client-request-id"))

	// anthropic-beta 必须为完整 mimic 集合（含 prompt-caching-scope 等），
	// 且不含仅 DefaultBetaHeader 才有的 fine-grained-tool-streaming。
	betaHeader := getHeaderRaw(upstream.lastReq.Header, "anthropic-beta")
	require.Equal(t, strings.Join(claude.FullClaudeCodeMimicryBetas(), ","), betaHeader)
	require.Contains(t, betaHeader, claude.BetaPromptCachingScope)
	require.NotContains(t, betaHeader, claude.BetaFineGrainedToolStreaming)

	// body：system 被还原为真实 CLI 的 2-block 形态——[billing block, CC prompt]，
	// 且 billing block 不再携带 cch 字段（新版 CLI 已取消 cch 签名）。
	system := gjson.GetBytes(upstream.lastBody, "system")
	require.True(t, system.IsArray())
	billingText := system.Get("0.text").String()
	require.True(t, strings.HasPrefix(billingText, "x-anthropic-billing-header"),
		"first system block should be billing header, got: %s", billingText)
	require.NotContains(t, billingText, "cch=")
	require.Equal(t, claudeCodeSystemPrompt, system.Get("1.text").String())
}
