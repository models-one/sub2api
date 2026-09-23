package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const defaultClaudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

// 默认 User-Agent，复用项目统一的 Claude Code 真值源，与 identity_service 默认指纹、
// 计费同步保持一致。旧值 "claude-code/2.1.7" 是过时离群值（错前缀 claude-code/ + 错版本），
// 打的同样是 api.anthropic.com，版本不一致会被 Anthropic 判第三方。
// 上游 0.2.8 起版本号改为运行期生效值（管理员手填 → 后台自动同步 → 内置基线），
// identity_service 已改用 claude.DefaultUserAgent()，这里必须同样按调用时取值，
// 不能再用编译期常量 CLICurrentVersion，否则同步到新版本后两边 UA 分叉。
func defaultUsageUserAgent() string {
	return claude.DefaultUserAgent()
}

type claudeUsageService struct {
	usageURL          string
	allowPrivateHosts bool
	httpUpstream      service.HTTPUpstream
}

// NewClaudeUsageFetcher 创建 Claude 用量获取服务
// httpUpstream: 可选，如果提供则支持 TLS 指纹伪装
func NewClaudeUsageFetcher(httpUpstream service.HTTPUpstream) service.ClaudeUsageFetcher {
	return &claudeUsageService{
		usageURL:     defaultClaudeUsageURL,
		httpUpstream: httpUpstream,
	}
}

// FetchUsage 简单版本，不支持 TLS 指纹（向后兼容）
func (s *claudeUsageService) FetchUsage(ctx context.Context, accessToken, proxyURL string) (*service.ClaudeUsageResponse, error) {
	return s.FetchUsageWithOptions(ctx, &service.ClaudeUsageFetchOptions{
		AccessToken: accessToken,
		ProxyURL:    proxyURL,
	})
}

// FetchUsageWithOptions 完整版本，支持 TLS 指纹和自定义 User-Agent
func (s *claudeUsageService) FetchUsageWithOptions(ctx context.Context, opts *service.ClaudeUsageFetchOptions) (*service.ClaudeUsageResponse, error) {
	if opts == nil {
		return nil, fmt.Errorf("options is nil")
	}

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "GET", s.usageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	// 设置请求头（与抓包一致，但不设置 Accept-Encoding，让 Go 自动处理压缩）
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opts.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	// 设置 User-Agent（优先使用缓存的 Fingerprint，否则使用默认值）
	userAgent := defaultUsageUserAgent()
	if opts.Fingerprint != nil && opts.Fingerprint.UserAgent != "" {
		userAgent = opts.Fingerprint.UserAgent
	}
	req.Header.Set("User-Agent", userAgent)

	var resp *http.Response

	// 如果有 TLS Profile 且有 HTTPUpstream，使用 DoWithTLS
	if opts.TLSProfile != nil && s.httpUpstream != nil {
		resp, err = s.httpUpstream.DoWithTLS(req, opts.ProxyURL, opts.AccountID, 0, opts.TLSProfile)
		if err != nil {
			return nil, fmt.Errorf("request with TLS fingerprint failed: %w", err)
		}
	} else {
		// 不启用 TLS 指纹，使用普通 HTTP 客户端
		client, err := httpclient.GetClient(httpclient.Options{
			ProxyURL:           opts.ProxyURL,
			Timeout:            30 * time.Second,
			ValidateResolvedIP: true,
			AllowPrivateHosts:  s.allowPrivateHosts,
		})
		if err != nil {
			return nil, fmt.Errorf("create http client failed: %w", err)
		}

		resp, err = client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		msg := fmt.Sprintf("API returned status %d: %s", resp.StatusCode, string(body))
		return nil, infraerrors.New(http.StatusInternalServerError, "UPSTREAM_ERROR", msg)
	}

	var usageResp service.ClaudeUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usageResp); err != nil {
		return nil, fmt.Errorf("decode response failed: %w", err)
	}

	return &usageResp, nil
}
