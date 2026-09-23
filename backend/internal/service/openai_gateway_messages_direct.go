package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// buildAnthropicDirectMessagesURL returns the upstream Anthropic Messages API
// endpoint for platforms that natively support the Anthropic protocol.
//
//   - Deepseek: https://api.deepseek.com  →  https://api.deepseek.com/anthropic/v1/messages
//   - Kimi: https://api.kimi.com/coding/v1  →  https://api.kimi.com/coding/v1/messages
//   - Zhipu 官方: https://open.bigmodel.cn  →  https://open.bigmodel.cn/api/anthropic/v1/messages
//   - Zhipu 中转: https://relay.orbitai.cc  →  https://relay.orbitai.cc/v1/messages
func buildAnthropicDirectMessagesURL(account *Account) string {
	switch account.Platform {
	case PlatformDeepseek:
		baseURL := account.GetDeepseekBaseURL()
		// Strip /v1 suffix — the Anthropic-compatible path lives at /anthropic/v1/messages
		baseURL = strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
		return baseURL + "/anthropic/v1/messages"
	case PlatformKimi:
		baseURL := account.GetKimiBaseURL()
		return strings.TrimRight(baseURL, "/") + "/messages"
	case PlatformZhipu:
		// Zhipu(GLM) 原生 Anthropic 端点的根因上游而异：
		//   - 智谱官方 open.bigmodel.cn / api.z.ai：端点在 /api/anthropic 下
		//     （官方 ANTHROPIC_BASE_URL=https://open.bigmodel.cn/api/anthropic）。
		//   - NewAPI 类中转（如 relay.orbitai.cc）：直接在根暴露 /v1/messages。
		baseURL := strings.TrimRight(account.GetZhipuBaseURL(), "/")
		if u, err := url.Parse(baseURL); err == nil &&
			(u.Host == "open.bigmodel.cn" || u.Host == "api.z.ai") &&
			!strings.Contains(u.Path, "/api/anthropic") {
			return u.Scheme + "://" + u.Host + "/api/anthropic/v1/messages"
		}
		baseURL = strings.TrimSuffix(baseURL, "/v1")
		return baseURL + "/v1/messages"
	case PlatformMiniMax:
		// 上游 0.2.4 把 minimax 加进 IsCNProvider，于是「APIKey 账号 + 没写 api_protocol」
		// 会命中本 fork 的 usesLegacyCNAnthropicDirect 直通分支走到这里；
		// 不补这个 case 就落到 default 返回空串，forwardAnthropicDirect 直接报
		// unsupported platform。默认根是 DefaultMiniMaxAnthropicBaseURL
		// （https://api.minimaxi.com/anthropic），与 deepseek 同型再拼 /v1/messages。
		baseURL := strings.TrimRight(account.GetCNProtocolBaseURL(APIProtocolAnthropic), "/")
		baseURL = strings.TrimSuffix(baseURL, "/v1")
		return baseURL + "/v1/messages"
	default:
		return ""
	}
}

// normalizeAnthropicDirectInputUsage 把直连上游的 usage 归一为主路径计费口径
// （input_tokens 为总输入，下游 actualInput = input_tokens - cache_read - cache_creation，
// 见 RecordUsage 的三桶互斥拆分）。cache_read 与 cache_creation 必须同口径一并加回，
// 只加 cache_read 会在 cache_creation 非零时被下游减法多扣一次，真实 input 被钳 0 少收钱。
//
//   - Deepseek（已实测 api.deepseek.com：input=71、cache_read=2944 对应 3015 token
//     prompt）：input_tokens 永远只报缓存未命中数，必须无条件加回 cache_read 与
//     cache_creation；条件判断 (input < cache) 会在新增内容超过缓存前缀时漏计。
//   - 其他平台（Kimi / Zhipu）：usage 语义未实测（仓库内 Kimi fixture 显示 input_tokens
//     疑似总量口径），仅在 input_tokens < cache_read + cache_creation（明显为未命中
//     口径）时加回，避免总量口径上游把缓存前缀按全价+缓存价双重计费。
func normalizeAnthropicDirectInputUsage(platform string, usage *OpenAIUsage) {
	cacheTokens := usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	if platform == PlatformDeepseek || usage.InputTokens < cacheTokens {
		usage.InputTokens += cacheTokens
	}
}

// forwardAnthropicDirect forwards an Anthropic Messages request directly to
// upstream platforms that expose a native Anthropic-compatible endpoint
// (Deepseek /anthropic, Kimi /coding, Zhipu /api/anthropic). Unlike the normal ForwardAsAnthropic
// path, this skips the Anthropic→Responses format conversion and pipes the
// upstream Anthropic SSE/JSON response through unchanged.
func (s *OpenAIGatewayService) forwardAnthropicDirect(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	originalModel, billingModel, upstreamModel string,
	clientStream bool,
	startTime time.Time,
) (*OpenAIForwardResult, error) {

	// 1. Replace model in the Anthropic request body.
	body = ReplaceModelInBody(body, upstreamModel)

	// 1b. 计费用的推理等级：从最终转发体取（上游实际收到什么档位就按什么档位计），
	// 提取与兜底规则同上游原生直通 forwardAnthropicViaNativeAnthropicEndpoint：
	// output_config.effort 优先；缺失且 thinking 已启用时按国产 passback-required 模型
	// 兜底为 high，DeepSeek 除外。唯一差别：原生直通会先用 NormalizeGLM53AnthropicThinking
	// 改写 glm-5.3 的请求体（medium→high、xhigh→max、disabled→low 等），本路径不改写
	// 转发体，所以 glm-5.3 按客户端原值计费——口径跟随转发体，而不是跟随原生直通。
	// 本路径此前从不设 ReasoningEffort，RecordUsage 拿到空串即按 1 倍计，渠道/分组的
	// reasoning_effort_multipliers 对存量国产账号整体失效，商户加价、返利与账号成本
	// 也跟着按未乘倍率的金额算。下游每个结果（含断开/读错误的部分结果）都必须带上。
	reasoningEffort := ApplyThinkingEnabledFallback(
		NormalizeClaudeOutputEffort(gjson.GetBytes(body, "output_config.effort").String()),
		body,
		upstreamModel,
	)

	// 2. Build upstream URL.
	targetURL := buildAnthropicDirectMessagesURL(account)
	if targetURL == "" {
		return nil, fmt.Errorf("unsupported platform for direct Anthropic forwarding: %s", account.Platform)
	}

	// 3. Get access token.
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	// 4. Build HTTP request.
	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build anthropic direct request: %w", err)
	}
	// Deepseek / Kimi / Zhipu 的原生 Anthropic 端点用标准 x-api-key。
	req.Header.Set("x-api-key", token)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("accept", "application/json")

	// Kimi For Coding 对客户端做白名单校验，需为 Coding Agent UA（前缀 claude-cli/）。
	if account.Platform == PlatformKimi {
		baseURL := account.GetKimiBaseURL()
		if strings.Contains(baseURL, "api.kimi.com") {
			req.Header.Set("user-agent", kimiCodingUserAgent)
		}
	}

	// Passthrough anthropic-beta header from client if present.
	if beta := c.GetHeader("anthropic-beta"); beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}

	logger.L().Debug("anthropic_direct: forwarding request",
		zap.Int64("account_id", account.ID),
		zap.String("platform", string(account.Platform)),
		zap.String("target_url", targetURL),
		zap.String("original_model", originalModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	// 5. Send request via httpUpstream (respects proxy settings).
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		writeAnthropicError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
		return nil, fmt.Errorf("upstream request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	// 6. Handle error responses — support failover.
	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		_ = resp.Body.Close()

		upstreamMsg := strings.TrimSpace(string(respBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)

		if s.shouldFailoverOpenAIUpstreamResponse(account, resp.StatusCode, upstreamMsg, respBody) {
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				Kind:               "failover",
				Message:            upstreamMsg,
			})
			s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, upstreamModel)
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
			}
		}
		// Non-failover error: pass through the upstream Anthropic error to client.
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	// 7. Handle successful response.
	if clientStream {
		return s.handleAnthropicDirectStreamingResponse(resp, c, account.Platform, originalModel, billingModel, upstreamModel, reasoningEffort, startTime)
	}
	return s.handleAnthropicDirectBufferedResponse(resp, c, account.Platform, originalModel, billingModel, upstreamModel, reasoningEffort, startTime)
}

// handleAnthropicDirectStreamingResponse pipes an upstream Anthropic SSE stream
// directly to the client while extracting usage information for billing.
func (s *OpenAIGatewayService) handleAnthropicDirectStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	platform string,
	originalModel, billingModel, upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.WriteHeader(http.StatusOK)

	var usage OpenAIUsage
	var requestID, responseID string
	var firstTokenMs *int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// Write every line to client immediately.
		_, writeErr := c.Writer.WriteString(line + "\n")
		if writeErr != nil {
			return &OpenAIForwardResult{
				Model:            originalModel,
				BillingModel:     billingModel,
				UpstreamModel:    upstreamModel,
				ReasoningEffort:  reasoningEffort,
				Usage:            usage,
				Stream:           true,
				Duration:         time.Since(startTime),
				ClientDisconnect: true,
			}, fmt.Errorf("client write error: %w", writeErr)
		}
		c.Writer.Flush()

		// Parse "data: {...}" or "data:{...}" lines to extract usage.
		// Standard Anthropic uses "data: " (with space), Kimi uses "data:" (no space).
		var data string
		if strings.HasPrefix(line, "data: ") {
			data = line[6:]
		} else if strings.HasPrefix(line, "data:") {
			data = line[5:]
		} else {
			continue
		}
		if data == "[DONE]" {
			continue
		}

		eventType := gjson.Get(data, "type").String()

		switch eventType {
		case "message_start":
			// First content event — record TTFT.
			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			// Extract input tokens and request/response IDs.
			msg := gjson.Get(data, "message")
			responseID = msg.Get("id").String()
			usage.InputTokens = int(msg.Get("usage.input_tokens").Int())
			usage.CacheCreationInputTokens = int(msg.Get("usage.cache_creation_input_tokens").Int())
			usage.CacheReadInputTokens = int(msg.Get("usage.cache_read_input_tokens").Int())

			// 归一化口径见 normalizeAnthropicDirectInputUsage（DeepSeek 无条件、其他平台条件加回）。
			normalizeAnthropicDirectInputUsage(platform, &usage)

		case "content_block_start", "content_block_delta":
			if firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}

		case "message_delta":
			// Extract output tokens.
			usage.OutputTokens = int(gjson.Get(data, "usage.output_tokens").Int())
		}
	}

	if err := scanner.Err(); err != nil {
		return &OpenAIForwardResult{
			Model:            originalModel,
			BillingModel:     billingModel,
			UpstreamModel:    upstreamModel,
			ReasoningEffort:  reasoningEffort,
			Usage:            usage,
			Stream:           true,
			Duration:         time.Since(startTime),
			ClientDisconnect: true,
		}, fmt.Errorf("upstream read error: %w", err)
	}

	// Extract request ID from response headers.
	if rid := resp.Header.Get("request-id"); rid != "" {
		requestID = rid
	} else if rid := resp.Header.Get("x-request-id"); rid != "" {
		requestID = rid
	}

	return &OpenAIForwardResult{
		RequestID:       requestID,
		ResponseID:      responseID,
		Usage:           usage,
		Model:           originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
		ReasoningEffort: reasoningEffort,
		Stream:          true,
		Duration:        time.Since(startTime),
		FirstTokenMs:    firstTokenMs,
	}, nil
}

// handleAnthropicDirectBufferedResponse reads the full upstream Anthropic JSON
// response, writes it to the client, and extracts usage for billing.
func (s *OpenAIGatewayService) handleAnthropicDirectBufferedResponse(
	resp *http.Response,
	c *gin.Context,
	platform string,
	originalModel, billingModel, upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	// Even when stream=false, some Anthropic-compatible upstreams may still
	// return SSE. Detect by Content-Type and delegate to the streaming handler.
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return s.handleAnthropicDirectBufferedSSE(resp, c, platform, originalModel, billingModel, upstreamModel, reasoningEffort, startTime)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeAnthropicError(c, http.StatusBadGateway, "api_error", "Failed to read upstream response")
		return nil, fmt.Errorf("read upstream response: %w", err)
	}

	// Extract usage from the JSON response.
	var usage OpenAIUsage
	usage.InputTokens = int(gjson.GetBytes(respBody, "usage.input_tokens").Int())
	usage.OutputTokens = int(gjson.GetBytes(respBody, "usage.output_tokens").Int())
	usage.CacheCreationInputTokens = int(gjson.GetBytes(respBody, "usage.cache_creation_input_tokens").Int())
	usage.CacheReadInputTokens = int(gjson.GetBytes(respBody, "usage.cache_read_input_tokens").Int())

	// 归一化口径见 normalizeAnthropicDirectInputUsage。
	normalizeAnthropicDirectInputUsage(platform, &usage)

	responseID := gjson.GetBytes(respBody, "id").String()
	requestID := resp.Header.Get("x-request-id")

	// Write the JSON response to client.
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(respBody)

	return &OpenAIForwardResult{
		RequestID:       requestID,
		ResponseID:      responseID,
		Usage:           usage,
		Model:           originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
		ReasoningEffort: reasoningEffort,
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}

// handleAnthropicDirectBufferedSSE handles the case where the upstream returns
// SSE even though the client requested stream=false. It buffers all events,
// assembles the final message, and returns it as a single JSON response.
func (s *OpenAIGatewayService) handleAnthropicDirectBufferedSSE(
	resp *http.Response,
	c *gin.Context,
	platform string,
	originalModel, billingModel, upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	var usage OpenAIUsage
	var lastMessageData []byte
	var responseID, requestID string
	var stopReason string

	// 累积各 content block 的文本/思考增量，用于在非流式响应里重建 content 数组。
	type sseContentBlock struct {
		typ string
		sb  strings.Builder
	}
	contentBlocks := map[int]*sseContentBlock{}
	var blockOrder []int
	blockAt := func(idx int) *sseContentBlock {
		b := contentBlocks[idx]
		if b == nil {
			b = &sseContentBlock{}
			contentBlocks[idx] = b
			blockOrder = append(blockOrder, idx)
		}
		return b
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		var data string
		if strings.HasPrefix(line, "data: ") {
			data = line[6:]
		} else if strings.HasPrefix(line, "data:") {
			data = line[5:]
		} else {
			continue
		}
		if data == "[DONE]" {
			continue
		}

		eventType := gjson.Get(data, "type").String()
		switch eventType {
		case "message_start":
			msg := gjson.Get(data, "message")
			responseID = msg.Get("id").String()
			usage.InputTokens = int(msg.Get("usage.input_tokens").Int())
			usage.CacheCreationInputTokens = int(msg.Get("usage.cache_creation_input_tokens").Int())
			usage.CacheReadInputTokens = int(msg.Get("usage.cache_read_input_tokens").Int())
			// 归一化口径见 normalizeAnthropicDirectInputUsage。
			normalizeAnthropicDirectInputUsage(platform, &usage)
			// Store the initial message object as the base for the final response.
			lastMessageData = []byte(msg.Raw)

		case "content_block_start":
			idx := int(gjson.Get(data, "index").Int())
			blockAt(idx).typ = gjson.Get(data, "content_block.type").String()

		case "content_block_delta":
			// 重建 content：累积 text / thinking 增量（按 block index 归位）。
			idx := int(gjson.Get(data, "index").Int())
			b := blockAt(idx)
			switch gjson.Get(data, "delta.type").String() {
			case "thinking_delta":
				if b.typ == "" {
					b.typ = "thinking"
				}
				_, _ = b.sb.WriteString(gjson.Get(data, "delta.thinking").String())
			case "text_delta", "":
				if b.typ == "" {
					b.typ = "text"
				}
				_, _ = b.sb.WriteString(gjson.Get(data, "delta.text").String())
			}

		case "message_delta":
			usage.OutputTokens = int(gjson.Get(data, "usage.output_tokens").Int())
			if sr := gjson.Get(data, "delta.stop_reason").String(); sr != "" {
				stopReason = sr
			}
		}
	}

	requestID = resp.Header.Get("x-request-id")

	// Build a minimal but correct JSON response.
	// Reconstruct from the message_start base + accumulated content.
	// For simplicity, re-read the SSE stream result. Since we already have
	// lastMessageData from message_start, inject final usage and write it.
	if lastMessageData != nil {
		// Update usage in the response.
		type anthropicUsage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
		}
		var msgResp map[string]any
		if err := json.Unmarshal(lastMessageData, &msgResp); err == nil {
			msgResp["usage"] = anthropicUsage{
				InputTokens:              usage.InputTokens,
				OutputTokens:             usage.OutputTokens,
				CacheCreationInputTokens: usage.CacheCreationInputTokens,
				CacheReadInputTokens:     usage.CacheReadInputTokens,
			}
			// 用累积的增量重建 content 数组（text / thinking 块）。message_start 的
			// content 为空，必须在此回填，否则非流式客户端拿到空响应。
			// tool_use 等块本路径不重建（流式路径已正确处理）。
			if len(blockOrder) > 0 {
				blocks := make([]map[string]any, 0, len(blockOrder))
				for _, idx := range blockOrder {
					b := contentBlocks[idx]
					switch b.typ {
					case "thinking":
						blocks = append(blocks, map[string]any{"type": "thinking", "thinking": b.sb.String()})
					case "text", "":
						blocks = append(blocks, map[string]any{"type": "text", "text": b.sb.String()})
					}
				}
				if len(blocks) > 0 {
					msgResp["content"] = blocks
				}
			}
			if stopReason != "" {
				msgResp["stop_reason"] = stopReason
			}
			finalBody, _ := json.Marshal(msgResp)
			c.Writer.Header().Set("Content-Type", "application/json")
			c.Writer.WriteHeader(http.StatusOK)
			_, _ = c.Writer.Write(finalBody)
		}
	}

	return &OpenAIForwardResult{
		RequestID:       requestID,
		ResponseID:      responseID,
		Usage:           usage,
		Model:           originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
		ReasoningEffort: reasoningEffort,
		Stream:          false,
		Duration:        time.Since(startTime),
	}, nil
}
