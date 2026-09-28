package apicompat

// fork 定制回归测试：上游 97bdde313（content_block_start 携带的工具参数种子）
// 与 fork 的 codex 工具还原（custom_tool_call / tool_search_call / namespace）
// 合流后的行为。参数唯一来源仍是 state.CurrentArgs；种子只在全程无 delta 时
// 于 content_block_stop 被采用。

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func feedAnthropicSeededToolUseStream(state *AnthropicEventToResponsesState, toolName string, input json.RawMessage, argDeltas []string, blockStop bool) []ResponsesStreamEvent {
	var events []ResponsesStreamEvent
	feed := func(evt *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(evt, state)...)
	}

	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_seed", Model: "claude-sonnet-4-5"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: "toolu_seed", Name: toolName, Input: input}})
	for _, delta := range argDeltas {
		feed(&AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: delta}})
	}
	if blockStop {
		feed(&AnthropicStreamEvent{Type: "content_block_stop"})
	}
	feed(&AnthropicStreamEvent{Type: "message_stop"})
	return events
}

func TestAnthropicEventToResponses_CustomToolCallUsesStartSeed(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.ToolContext = codexToolContextForTest(t)

	events := feedAnthropicSeededToolUseStream(state, "exec", json.RawMessage(`{"input":"ls -la"}`), nil, true)

	assert.Empty(t, findResponsesEvents(events, "response.function_call_arguments.delta"))
	inputDelta := findResponsesEvents(events, "response.custom_tool_call_input.delta")
	require.Len(t, inputDelta, 1)
	assert.Equal(t, "ls -la", inputDelta[0].Delta)
	inputDone := findResponsesEvents(events, "response.custom_tool_call_input.done")
	require.Len(t, inputDone, 1)
	assert.Equal(t, "ls -la", inputDone[0].Input)

	done := findResponsesEvents(events, "response.output_item.done")
	require.Len(t, done, 1)
	assert.Equal(t, "custom_tool_call", done[0].Item.Type)
	assert.Equal(t, "ls -la", done[0].Item.Input)

	completed := findResponsesEvents(events, "response.completed")
	require.Len(t, completed, 1)
	require.Len(t, completed[0].Response.Output, 1)
	assert.Equal(t, "ls -la", completed[0].Response.Output[0].Input)
}

func TestAnthropicEventToResponses_CustomToolCallDeltaSupersedesSeed(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.ToolContext = codexToolContextForTest(t)

	events := feedAnthropicSeededToolUseStream(state, "exec", json.RawMessage(`{"input":"stale"}`),
		[]string{`{"input":`, `"ls -la"}`}, true)

	inputDone := findResponsesEvents(events, "response.custom_tool_call_input.done")
	require.Len(t, inputDone, 1)
	assert.Equal(t, "ls -la", inputDone[0].Input, "真实 delta 覆盖种子，不拼接两份 JSON")
}

func TestAnthropicEventToResponses_ToolSearchCallUsesStartSeed(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.ToolContext = codexToolContextForTest(t)

	events := feedAnthropicSeededToolUseStream(state, "tool_search", json.RawMessage(`{"query":"github"}`), nil, true)

	assert.Empty(t, findResponsesEvents(events, "response.function_call_arguments.delta"))
	done := findResponsesEvents(events, "response.output_item.done")
	require.Len(t, done, 1)
	assert.Equal(t, "tool_search_call", done[0].Item.Type)
	assert.JSONEq(t, `{"query":"github"}`, done[0].Item.Arguments)
}

func TestAnthropicEventToResponses_NamespaceFunctionCallUsesStartSeed(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.ToolContext = codexToolContextForTest(t)

	events := feedAnthropicSeededToolUseStream(state, "repo__search", json.RawMessage(`{"q":"x"}`), nil, true)

	deltas := findResponsesEvents(events, "response.function_call_arguments.delta")
	require.Len(t, deltas, 1)
	assert.Equal(t, `{"q":"x"}`, deltas[0].Delta)
	argsDone := findResponsesEvents(events, "response.function_call_arguments.done")
	require.Len(t, argsDone, 1)
	assert.Equal(t, `{"q":"x"}`, argsDone[0].Arguments, "sum(deltas) == done")
	assert.Equal(t, "search", argsDone[0].Name)

	done := findResponsesEvents(events, "response.output_item.done")
	require.Len(t, done, 1)
	assert.Equal(t, "search", done[0].Item.Name)
	assert.Equal(t, "repo", done[0].Item.Namespace)
	assert.Equal(t, `{"q":"x"}`, done[0].Item.Arguments)
}

// 缺失 content_block_stop（异常截断，由 message_stop 兜底关闭）时，收尾项仍带种子参数。
func TestAnthropicEventToResponses_SeedSurvivesCloseWithoutBlockStop(t *testing.T) {
	state := NewAnthropicEventToResponsesState()

	events := feedAnthropicSeededToolUseStream(state, "get_weather", json.RawMessage(`{"city":"NYC"}`), nil, false)

	done := findResponsesEvents(events, "response.output_item.done")
	require.Len(t, done, 1)
	assert.Equal(t, `{"city":"NYC"}`, done[0].Item.Arguments)
	assert.Empty(t, state.PendingToolInput, "关闭后种子须清空，不串到下一个块")
}
