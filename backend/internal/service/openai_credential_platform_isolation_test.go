//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 账号密钥只能发往该账号所属平台的主机。以下覆盖两处曾把密钥发错主机的 fork 回归：
// 国产平台非 APIKey 类型账号的 GetAccessToken，以及 Grok APIKey 账号的 CC 回退。

var cnProviderPlatformsForIsolation = []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax}

func TestGetAccessTokenCNProviderRejectsNonAPIKeyTypes(t *testing.T) {
	const cnKey = "sk-cn-secret"
	for _, platform := range cnProviderPlatformsForIsolation {
		t.Run(platform, func(t *testing.T) {
			newAccount := func(accountType string) *Account {
				return &Account{
					ID: 1, Platform: platform, Type: accountType,
					Credentials: map[string]any{"api_key": cnKey},
				}
			}
			svc := &OpenAIGatewayService{}

			token, tokenType, err := svc.GetAccessToken(context.Background(), newAccount(AccountTypeAPIKey))
			require.NoError(t, err)
			require.Equal(t, cnKey, token)
			require.Equal(t, "apikey", tokenType)

			for accountType, wantErr := range map[string]string{
				AccountTypeUpstream:   "unsupported account type: upstream",
				AccountTypeSetupToken: "unsupported account type: setup-token",
				AccountTypeOAuth:      "access_token not found in credentials",
			} {
				token, tokenType, err := svc.GetAccessToken(context.Background(), newAccount(accountType))
				require.EqualError(t, err, wantErr, accountType)
				require.Empty(t, token, accountType)
				require.Empty(t, tokenType, accountType)
			}

			// 生产环境配有 OpenAI TokenProvider：它同样拒绝非 openai 平台的 OAuth 账号。
			withProvider := &OpenAIGatewayService{openAITokenProvider: &OpenAITokenProvider{}}
			token, _, err = withProvider.GetAccessToken(context.Background(), newAccount(AccountTypeOAuth))
			require.EqualError(t, err, "not an openai oauth account")
			require.Empty(t, token)
		})
	}
}

func TestCNProviderNonAPIKeyAccountsNeverReachOpenAIHosts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const cnKey = "sk-cn-secret"
	for _, platform := range cnProviderPlatformsForIsolation {
		for accountType, wantErr := range map[string]string{
			AccountTypeOAuth:    "access_token not found in credentials",
			AccountTypeUpstream: "unsupported account type: upstream",
		} {
			t.Run(platform+"/"+accountType, func(t *testing.T) {
				upstream := &httpUpstreamRecorder{err: errors.New("must not be called")}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				account := &Account{
					ID: 2, Name: "cn-misconfigured", Platform: platform, Type: accountType, Concurrency: 1,
					Credentials: map[string]any{"api_key": cnKey},
				}
				body := []byte(`{"model":"some-model","input":"hello","max_output_tokens":32,"stream":false}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")

				_, err := svc.Forward(context.Background(), c, account, body)
				require.EqualError(t, err, wantErr)
				require.Empty(t, upstream.requests, "credential of a misconfigured %s account was sent upstream", platform)
			})
		}
	}
}

func TestGetOpenAIApiKeyOnlyReturnsOpenAIPlatformKeys(t *testing.T) {
	newAccount := func(platform, accountType string) *Account {
		return &Account{Platform: platform, Type: accountType, Credentials: map[string]any{"api_key": "sk-test"}}
	}

	require.Equal(t, "sk-test", newAccount(PlatformOpenAI, AccountTypeAPIKey).GetOpenAIApiKey())
	require.Equal(t, "sk-test", newAccount(PlatformOpenAI, AccountTypeAPIKey).GetOpenAIProtocolAPIKey())
	require.Empty(t, newAccount(PlatformOpenAI, AccountTypeUpstream).GetOpenAIApiKey())

	// grok 有自己的 base_url（GetGrokBaseURL），OpenAI 协议族的取密钥入口不得返回它的密钥。
	grok := newAccount(PlatformGrok, AccountTypeAPIKey)
	require.Empty(t, grok.GetOpenAIApiKey())
	require.Empty(t, grok.GetOpenAIProtocolAPIKey())

	// 多协议供应商的密钥只经 GetOpenAIProtocolAPIKey 取得，base_url 由 provider profile 决定。
	for _, platform := range append(append([]string{}, cnProviderPlatformsForIsolation...), PlatformOpenCodeGo) {
		account := newAccount(platform, AccountTypeAPIKey)
		require.Empty(t, account.GetOpenAIApiKey(), platform)
		require.Equal(t, "sk-test", account.GetOpenAIProtocolAPIKey(), platform)
		require.Empty(t, newAccount(platform, AccountTypeUpstream).GetOpenAIProtocolAPIKey(), platform)
	}
}

func TestGrokAPIKeyMessagesCCFallbackDoesNotSendKeyToOpenAI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, baseURL := range []string{"", "https://grok-relay.example"} {
		t.Run("base_url="+baseURL, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{err: errors.New("must not be called")}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			credentials := map[string]any{"api_key": "xai-secret"}
			if baseURL != "" {
				credentials["base_url"] = baseURL
			}
			account := &Account{
				ID: 3, Name: "grok-apikey", Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: credentials,
				Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: false},
			}
			body := []byte(`{"model":"grok-4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
			require.EqualError(t, err, "account 3 missing api_key")
			require.Empty(t, upstream.requests, "grok api key was sent through the OpenAI chat completions fallback")
		})
	}
}

// 固定 responses 协议的多协议账号测试图片模型时，仍用自己的密钥与 base_url。
func TestAccountTestServiceImageAPIKeyUsesProtocolKeyForMultiProtocolAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aGVsbG8="}]}`)),
	}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{
		ID: 4, Name: "deepseek-responses", Platform: PlatformDeepseek, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":      "sk-deepseek",
			"api_protocol": APIProtocolResponses,
			"base_url":     "https://deepseek-relay.example/v1",
		},
	}

	require.NoError(t, svc.testOpenAIImageAPIKey(c, context.Background(), account, "gpt-image-2", "draw a cat"))
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://deepseek-relay.example/v1/images/generations", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer sk-deepseek", upstream.lastReq.Header.Get("Authorization"))

	// grok 不走这条测试路径；即便误入，也只能报缺密钥，不能把 xAI 密钥发给 api.openai.com。
	grokRec := httptest.NewRecorder()
	grokCtx, _ := gin.CreateTestContext(grokRec)
	grokCtx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
	grokUpstream := &httpUpstreamRecorder{err: errors.New("must not be called")}
	grokSvc := &AccountTestService{httpUpstream: grokUpstream, cfg: &config.Config{}}
	grok := &Account{ID: 5, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "xai-secret"}}
	_ = grokSvc.testOpenAIImageAPIKey(grokCtx, context.Background(), grok, "gpt-image-2", "draw a cat")
	require.Empty(t, grokUpstream.requests)
	require.Contains(t, grokRec.Body.String(), "No API key available")
}
