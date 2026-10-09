//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// fork：自适应国产账号连通性测试未指定模型时，三个子探测（Chat / Anthropic / Responses）
// 都必须用国产默认测试模型，不能把 OpenAI 的 gpt-5.x 发给国产上游。
// 上游 0.2.15 起该路径先取 providerDefaultTestModel()，国产 profile 未配时曾直接回落 gpt-5.x。
func TestAccountTestService_AdaptiveCNEmptyModelUsesCNDefaultTestModel(t *testing.T) {
	for _, tc := range []struct {
		platform string
		model    string
	}{
		{PlatformDeepseek, "deepseek-chat"},
		{PlatformKimi, "kimi-k2"},
		{PlatformZhipu, "GLM-5.1"},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			account := adaptiveCNAccountTestAccount(310, tc.platform)
			svc, upstream := adaptiveCNAccountTestService(
				account,
				adaptiveCNChatTestResponse(),
				adaptiveCNAnthropicTestResponse(),
				adaptiveCNResponsesTestResponse(),
			)
			c, _ := newTestContext()

			require.NoError(t, svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault))
			require.NotEmpty(t, upstream.bodies)
			for i, body := range upstream.bodies {
				require.Equal(t, tc.model, gjson.GetBytes(body, "model").String(), "probe %d (%s)", i, upstream.requests[i].URL)
			}
		})
	}
}
