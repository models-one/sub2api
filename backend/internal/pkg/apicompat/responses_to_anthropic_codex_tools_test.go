package apicompat

// fork 定制回归测试：/v1/responses → Anthropic 回退路径的 codex 0.14x 工具链，
// 对齐 chat 桥（chatcompletions_responses_bridge.go）语义：
//   - namespace 子工具摊平为 "<namespace>__<name>" + 撞名显式拒绝 + 回程还原；
//   - custom/freeform 工具降级为 {"input": string} 参数 + 回程还原为 custom_tool_call；
//   - tool_search 降级为同名代理 function + 回程还原为 tool_search_call。
// 修复前 default 分支把 type=namespace/tool_search 原样塞进 AnthropicTool.Type
// （非法 type，严格上游 400），回程一律还原为 function_call（codex 报 unsupported call）。

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 请求方向：工具声明转换
// ---------------------------------------------------------------------------

func TestResponsesToAnthropic_NamespaceToolsFlatten(t *testing.T) {
	tools, err := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "namespace", Name: "repo", Tools: []ResponsesTool{
			{Type: "function", Name: "search", Description: "Search the repo", Parameters: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)},
			{Type: "function", Name: "read"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, tools, 2)

	assert.Empty(t, tools[0].Type, "摊平后必须是普通 function 工具，绝不透传 type=namespace")
	assert.Equal(t, "repo__search", tools[0].Name)
	assert.Equal(t, "Search the repo", tools[0].Description)
	schema := requireObjectInputSchema(t, tools[0].InputSchema)
	assert.JSONEq(t, `{"q":{"type":"string"}}`, string(schema["properties"]))

	assert.Empty(t, tools[1].Type)
	assert.Equal(t, "repo__read", tools[1].Name)
}

func TestResponsesToAnthropic_NamespaceChildrenFieldEquivalent(t *testing.T) {
	// tools 与 children 二选一，语义相同。
	tools, err := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "namespace", Name: "repo", Children: []ResponsesTool{
			{Type: "function", Name: "search"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "repo__search", tools[0].Name)
}

func TestResponsesToAnthropic_NamespaceFlatNameConflictWithTopLevelRejected(t *testing.T) {
	_, err := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "function", Name: "repo__search"},
		{Type: "namespace", Name: "repo", Tools: []ResponsesTool{
			{Type: "function", Name: "search"},
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo__search")
}

func TestResponsesToAnthropic_NamespaceCrossNamespaceConflictRejected(t *testing.T) {
	_, err := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "namespace", Name: "a", Tools: []ResponsesTool{{Type: "function", Name: "b__c"}}},
		{Type: "namespace", Name: "a__b", Tools: []ResponsesTool{{Type: "function", Name: "c"}}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a__b__c")
}

func TestResponsesToAnthropic_NamespaceDuplicateChildDeduped(t *testing.T) {
	tools, err := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "namespace", Name: "repo", Tools: []ResponsesTool{{Type: "function", Name: "search"}}},
		{Type: "namespace", Name: "repo", Tools: []ResponsesTool{{Type: "function", Name: "search"}}},
	})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "repo__search", tools[0].Name)
}

func TestResponsesToAnthropic_ToolSearchDegradesToProxyFunction(t *testing.T) {
	tools, err := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "tool_search"},
		{Type: "tool_search"}, // 重复声明去重
	})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Empty(t, tools[0].Type, "绝不透传 type=tool_search")
	assert.Equal(t, toolSearchProxyName, tools[0].Name)
	assert.JSONEq(t, toolSearchProxySchema, string(tools[0].InputSchema))
}

func TestResponsesToAnthropic_ToolSearchConflictsWithDeclaredToolRejected(t *testing.T) {
	_, err := convertResponsesToAnthropicTools([]ResponsesTool{
		{Type: "function", Name: toolSearchProxyName},
		{Type: "tool_search"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), toolSearchProxyName)
}

func TestResponsesToAnthropic_ToolChoiceCustomAndToolSearch(t *testing.T) {
	custom, err := convertResponsesToAnthropicToolChoice(json.RawMessage(`{"type":"custom","name":"apply_patch"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"tool","name":"apply_patch"}`, string(custom))

	search, err := convertResponsesToAnthropicToolChoice(json.RawMessage(`{"type":"tool_search"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"tool","name":"tool_search"}`, string(search))
}

// ---------------------------------------------------------------------------
// 请求方向：codex 历史项还原为 tool_use / tool_result
// ---------------------------------------------------------------------------

func TestResponsesToAnthropic_CodexToolCallHistoryRoundTrip(t *testing.T) {
	input := json.RawMessage(`[
		{"type":"custom_tool_call","call_id":"call_c1","name":"exec","input":"ls -la"},
		{"type":"custom_tool_call_output","call_id":"call_c1","output":"total 0"},
		{"type":"tool_search_call","call_id":"call_s1","arguments":"{\"query\":\"github\"}"},
		{"type":"tool_search_output","call_id":"call_s1","output":"[]"},
		{"type":"function_call","call_id":"call_n1","name":"search","namespace":"repo","arguments":"{\"q\":\"x\"}"},
		{"type":"function_call_output","call_id":"call_n1","output":"hit"}
	]`)

	_, messages, err := convertResponsesInputToAnthropic("", input, false)
	require.NoError(t, err)
	require.Len(t, messages, 6, "三对 tool_use/tool_result，assistant/user 交替")

	// custom_tool_call → tool_use，自由文本 input 包进降级 schema 的 {"input": ...}
	assert.Equal(t, "assistant", messages[0].Role)
	blocks := parseContentBlocks(messages[0].Content)
	require.Len(t, blocks, 1)
	assert.Equal(t, "tool_use", blocks[0].Type)
	assert.Equal(t, "exec", blocks[0].Name)
	assert.Equal(t, "call_c1", blocks[0].ID)
	assert.JSONEq(t, `{"input":"ls -la"}`, string(blocks[0].Input))

	assert.Equal(t, "user", messages[1].Role)
	results := parseContentBlocks(messages[1].Content)
	require.Len(t, results, 1)
	assert.Equal(t, "tool_result", results[0].Type)
	assert.Equal(t, "call_c1", results[0].ToolUseID)

	// tool_search_call → 代理 function 的 tool_use
	blocks = parseContentBlocks(messages[2].Content)
	require.Len(t, blocks, 1)
	assert.Equal(t, "tool_use", blocks[0].Type)
	assert.Equal(t, toolSearchProxyName, blocks[0].Name)
	assert.JSONEq(t, `{"query":"github"}`, string(blocks[0].Input))

	// namespace 子工具历史调用 → 摊平名 tool_use
	blocks = parseContentBlocks(messages[4].Content)
	require.Len(t, blocks, 1)
	assert.Equal(t, "tool_use", blocks[0].Type)
	assert.Equal(t, "repo__search", blocks[0].Name)
	assert.JSONEq(t, `{"q":"x"}`, string(blocks[0].Input))
}

// ---------------------------------------------------------------------------
// 回程（非流式）：tool_use 按请求声明的工具类型还原
// ---------------------------------------------------------------------------

func codexToolContextForTest(t *testing.T) *AnthropicResponsesToolContext {
	t.Helper()
	ctx := NewAnthropicResponsesToolContext([]ResponsesTool{
		{Type: "custom", Name: "exec"},
		{Type: "tool_search"},
		{Type: "namespace", Name: "repo", Tools: []ResponsesTool{{Type: "function", Name: "search"}}},
		{Type: "function", Name: "get_weather"},
	})
	require.NotNil(t, ctx)
	return ctx
}

func TestAnthropicToResponsesResponseWithTools_RestoresCodexToolCalls(t *testing.T) {
	toolCtx := codexToolContextForTest(t)

	resp := &AnthropicResponse{
		ID:         "msg_codex",
		Model:      "claude-sonnet-4-5",
		StopReason: AnthropicStopReasonPtr("tool_use"),
		Content: []AnthropicContentBlock{
			{Type: "tool_use", ID: "toolu_1", Name: "exec", Input: json.RawMessage(`{"input":"ls -la"}`)},
			{Type: "tool_use", ID: "toolu_2", Name: "tool_search", Input: json.RawMessage(`{"query":"github"}`)},
			{Type: "tool_use", ID: "toolu_3", Name: "repo__search", Input: json.RawMessage(`{"q":"x"}`)},
			{Type: "tool_use", ID: "toolu_4", Name: "get_weather", Input: json.RawMessage(`{"city":"NYC"}`)},
		},
	}

	out := AnthropicToResponsesResponseWithTools(resp, toolCtx)
	require.Len(t, out.Output, 4)

	custom := out.Output[0]
	assert.Equal(t, "custom_tool_call", custom.Type)
	assert.Equal(t, "exec", custom.Name)
	assert.Equal(t, "ls -la", custom.Input, "input 从降级 schema 的 {\"input\": ...} 还原为自由文本")
	assert.Equal(t, toResponsesCallID("toolu_1"), custom.CallID)
	assert.Empty(t, custom.Arguments)

	search := out.Output[1]
	assert.Equal(t, "tool_search_call", search.Type)
	assert.JSONEq(t, `{"query":"github"}`, search.Arguments)

	nsCall := out.Output[2]
	assert.Equal(t, "function_call", nsCall.Type)
	assert.Equal(t, "search", nsCall.Name, "摊平名还原为裸子工具名")
	assert.Equal(t, "repo", nsCall.Namespace)
	assert.JSONEq(t, `{"q":"x"}`, nsCall.Arguments)

	plain := out.Output[3]
	assert.Equal(t, "function_call", plain.Type)
	assert.Equal(t, "get_weather", plain.Name)
	assert.Empty(t, plain.Namespace)
}

func TestAnthropicToResponsesResponse_NilContextKeepsLegacyFunctionCall(t *testing.T) {
	resp := &AnthropicResponse{
		ID:         "msg_legacy",
		Model:      "claude-sonnet-4-5",
		StopReason: AnthropicStopReasonPtr("tool_use"),
		Content: []AnthropicContentBlock{
			{Type: "tool_use", ID: "toolu_1", Name: "exec", Input: json.RawMessage(`{"input":"ls"}`)},
		},
	}

	out := AnthropicToResponsesResponse(resp)
	require.Len(t, out.Output, 1)
	assert.Equal(t, "function_call", out.Output[0].Type)
	assert.Equal(t, "exec", out.Output[0].Name)
	assert.JSONEq(t, `{"input":"ls"}`, out.Output[0].Arguments)
}

// ---------------------------------------------------------------------------
// 回程（流式）：tool_use 生命周期按工具类型下发
// ---------------------------------------------------------------------------

// feedAnthropicToolUseStream 按 message_start → tool_use 块 → message_stop 的顺序
// 灌一个单工具调用流，返回全部下发事件。
func feedAnthropicToolUseStream(state *AnthropicEventToResponsesState, toolName string, argDeltas []string) []ResponsesStreamEvent {
	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(evt, state)...)
	}

	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_stream", Model: "claude-sonnet-4-5"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: "toolu_1", Name: toolName}})
	for _, delta := range argDeltas {
		feed(&AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: delta}})
	}
	feed(&AnthropicStreamEvent{Type: "content_block_stop"})
	feed(&AnthropicStreamEvent{Type: "message_stop"})
	return events
}

func findResponsesEvents(events []ResponsesStreamEvent, eventType string) []ResponsesStreamEvent {
	var out []ResponsesStreamEvent
	for _, evt := range events {
		if evt.Type == eventType {
			out = append(out, evt)
		}
	}
	return out
}

func TestAnthropicEventToResponses_CustomToolCallStreamLifecycle(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.ToolContext = codexToolContextForTest(t)

	events := feedAnthropicToolUseStream(state, "exec", []string{`{"input":`, `"ls -la"}`})

	added := findResponsesEvents(events, "response.output_item.added")
	require.Len(t, added, 1)
	assert.Equal(t, "custom_tool_call", added[0].Item.Type)
	assert.Equal(t, "exec", added[0].Item.Name)
	assert.Equal(t, toResponsesCallID("toolu_1"), added[0].Item.CallID)

	// custom 调用的 input 无法增量还原，流中不产出 function_call 参数增量。
	assert.Empty(t, findResponsesEvents(events, "response.function_call_arguments.delta"))
	assert.Empty(t, findResponsesEvents(events, "response.function_call_arguments.done"))

	inputDelta := findResponsesEvents(events, "response.custom_tool_call_input.delta")
	require.Len(t, inputDelta, 1)
	assert.Equal(t, "ls -la", inputDelta[0].Delta)

	inputDone := findResponsesEvents(events, "response.custom_tool_call_input.done")
	require.Len(t, inputDone, 1)
	assert.Equal(t, "ls -la", inputDone[0].Input)
	assert.Equal(t, "exec", inputDone[0].Name)

	done := findResponsesEvents(events, "response.output_item.done")
	require.Len(t, done, 1)
	assert.Equal(t, "custom_tool_call", done[0].Item.Type)
	assert.Equal(t, "exec", done[0].Item.Name)
	assert.Equal(t, "ls -la", done[0].Item.Input)
	assert.Equal(t, toResponsesCallID("toolu_1"), done[0].Item.CallID)
}

func TestAnthropicEventToResponses_ToolSearchCallStreamLifecycle(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.ToolContext = codexToolContextForTest(t)

	events := feedAnthropicToolUseStream(state, "tool_search", []string{`{"query":"github"}`})

	added := findResponsesEvents(events, "response.output_item.added")
	require.Len(t, added, 1)
	assert.Equal(t, "tool_search_call", added[0].Item.Type)

	// tool_search 调用无参数增量事件，codex 从 output_item.done 物化。
	assert.Empty(t, findResponsesEvents(events, "response.function_call_arguments.delta"))
	assert.Empty(t, findResponsesEvents(events, "response.function_call_arguments.done"))

	done := findResponsesEvents(events, "response.output_item.done")
	require.Len(t, done, 1)
	assert.Equal(t, "tool_search_call", done[0].Item.Type)
	assert.Equal(t, toResponsesCallID("toolu_1"), done[0].Item.CallID)
	assert.JSONEq(t, `{"query":"github"}`, done[0].Item.Arguments)
}

func TestAnthropicEventToResponses_NamespaceToolCallStreamRestoresOwnership(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.ToolContext = codexToolContextForTest(t)

	events := feedAnthropicToolUseStream(state, "repo__search", []string{`{"q":"x"}`})

	added := findResponsesEvents(events, "response.output_item.added")
	require.Len(t, added, 1)
	assert.Equal(t, "function_call", added[0].Item.Type)
	assert.Equal(t, "search", added[0].Item.Name)
	assert.Equal(t, "repo", added[0].Item.Namespace)

	// namespace 子工具仍按 function_call 生命周期下发参数增量。
	deltas := findResponsesEvents(events, "response.function_call_arguments.delta")
	require.Len(t, deltas, 1)
	assert.Equal(t, `{"q":"x"}`, deltas[0].Delta)

	argsDone := findResponsesEvents(events, "response.function_call_arguments.done")
	require.Len(t, argsDone, 1)
	assert.Equal(t, "search", argsDone[0].Name)
	assert.JSONEq(t, `{"q":"x"}`, argsDone[0].Arguments)

	done := findResponsesEvents(events, "response.output_item.done")
	require.Len(t, done, 1)
	assert.Equal(t, "function_call", done[0].Item.Type)
	assert.Equal(t, "search", done[0].Item.Name)
	assert.Equal(t, "repo", done[0].Item.Namespace)
	assert.JSONEq(t, `{"q":"x"}`, done[0].Item.Arguments)
}

func TestAnthropicEventToResponses_PlainFunctionCallDoneCarriesFullPayload(t *testing.T) {
	// nil 上下文（或未命中任何 codex 工具）时保持 function_call 生命周期，
	// 且 output_item.done 带全量 call 载荷供 codex 物化调用。
	state := NewAnthropicEventToResponsesState()

	events := feedAnthropicToolUseStream(state, "get_weather", []string{`{"city":`, `"NYC"}`})

	added := findResponsesEvents(events, "response.output_item.added")
	require.Len(t, added, 1)
	assert.Equal(t, "function_call", added[0].Item.Type)
	assert.Equal(t, "get_weather", added[0].Item.Name)

	deltas := findResponsesEvents(events, "response.function_call_arguments.delta")
	require.Len(t, deltas, 2)

	done := findResponsesEvents(events, "response.output_item.done")
	require.Len(t, done, 1)
	assert.Equal(t, "function_call", done[0].Item.Type)
	assert.Equal(t, "get_weather", done[0].Item.Name)
	assert.Equal(t, toResponsesCallID("toolu_1"), done[0].Item.CallID)
	assert.JSONEq(t, `{"city":"NYC"}`, done[0].Item.Arguments)
}
