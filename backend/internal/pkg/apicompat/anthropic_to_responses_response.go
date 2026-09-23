package apicompat

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

// ---------------------------------------------------------------------------
// Non-streaming: AnthropicResponse → ResponsesResponse
// ---------------------------------------------------------------------------

// AnthropicResponsesToolContext 汇总 /v1/responses → Anthropic 回退路径回程还原
// codex 0.14x 工具调用所需的请求侧上下文（fork 定制，对齐 chat 桥的
// CustomTools / ToolSearchDeclared / NamespaceTools 语义）：custom 工具的调用还原
// 为 custom_tool_call 项、tool_search 代理调用还原为 tool_search_call 项、namespace
// 子工具的摊平名调用还原为带 namespace 字段的 function_call 项。codex 只按这些项
// 类型路由，一律还原为 function_call 会报 unsupported call。
type AnthropicResponsesToolContext struct {
	CustomTools    map[string]bool
	ToolSearch     bool
	NamespaceTools map[string]NamespacedToolName
}

// NewAnthropicResponsesToolContext 从 Responses 请求的工具声明构造回程还原上下文，
// 复用 chat 桥的收集口径。没有任何需要还原的工具时返回 nil（nil 上下文按纯
// function_call 还原，与历史行为一致）。
func NewAnthropicResponsesToolContext(tools []ResponsesTool) *AnthropicResponsesToolContext {
	if len(tools) == 0 {
		return nil
	}
	ctx := &AnthropicResponsesToolContext{
		CustomTools:    CustomToolNames(tools),
		ToolSearch:     HasToolSearchTool(tools),
		NamespaceTools: NamespaceToolNames(tools),
	}
	if len(ctx.CustomTools) == 0 && !ctx.ToolSearch && len(ctx.NamespaceTools) == 0 {
		return nil
	}
	return ctx
}

// classifyToolUse 按请求声明判定 tool_use 应还原成的 Responses 项类型，返回
// (项类型, 还原后的工具名, namespace)。nil 上下文安全，等价于纯 function_call。
// 判定顺序与 chat 桥 announceChatToolItem 一致（请求方向的撞名拒绝保证无歧义）。
func (ctx *AnthropicResponsesToolContext) classifyToolUse(name string) (itemType, itemName, namespace string) {
	if ctx == nil {
		return "function_call", name, ""
	}
	if ctx.CustomTools[name] {
		return "custom_tool_call", name, ""
	}
	if ctx.ToolSearch && name == toolSearchProxyName {
		return "tool_search_call", name, ""
	}
	if ns, ok := ctx.NamespaceTools[name]; ok {
		return "function_call", ns.Name, ns.Namespace
	}
	return "function_call", name, ""
}

const anthropicThinkingEnvelopePrefix = "anthropic-thinking-v1:"

func encodeAnthropicThinking(block AnthropicContentBlock) string {
	// Only fields belonging to the signed thinking block are retained.
	payload, _ := json.Marshal(struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature,omitempty"`
		Data      string `json:"data,omitempty"`
	}{block.Type, block.Thinking, block.Signature, block.Data})
	return anthropicThinkingEnvelopePrefix + base64.RawStdEncoding.EncodeToString(payload)
}

// AnthropicToResponsesResponse converts an Anthropic Messages response into a
// Responses API response. This is the reverse of ResponsesToAnthropic and
// enables Anthropic upstream responses to be returned in OpenAI Responses format.
func AnthropicToResponsesResponse(resp *AnthropicResponse) *ResponsesResponse {
	return AnthropicToResponsesResponseWithTools(resp, nil)
}

// AnthropicToResponsesResponseWithTools 同 AnthropicToResponsesResponse，但按
// 请求侧工具上下文还原 codex 工具调用形态（fork 定制，见
// AnthropicResponsesToolContext）。toolCtx 为 nil 时行为与原函数一致。
func AnthropicToResponsesResponseWithTools(resp *AnthropicResponse, toolCtx *AnthropicResponsesToolContext) *ResponsesResponse {
	id := resp.ID
	if id == "" {
		id = generateResponsesID()
	}

	// Anthropic responses carry no creation timestamp, so stamp now — the same
	// synthesize-what-the-client-requires rule the generated id above follows.
	out := &ResponsesResponse{
		ID:        id,
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Model:     resp.Model,
	}

	var outputs []ResponsesOutput
	var msgParts []ResponsesContentPart

	for _, block := range resp.Content {
		switch block.Type {
		case "thinking", "redacted_thinking":
			if claude.IsOpus55(resp.Model) && (block.Signature != "" || block.Data != "") {
				item := ResponsesOutput{Type: "reasoning", ID: generateItemID(), EncryptedContent: encodeAnthropicThinking(block)}
				if block.Thinking != "" {
					item.Summary = []ResponsesSummary{{Type: "summary_text", Text: block.Thinking}}
				}
				outputs = append(outputs, item)
				continue
			}
			if block.Thinking != "" {
				outputs = append(outputs, ResponsesOutput{
					Type: "reasoning",
					ID:   generateItemID(),
					Summary: []ResponsesSummary{{
						Type: "summary_text",
						Text: block.Thinking,
					}},
				})
			}
		case "text":
			if claude.IsOpus55(resp.Model) && block.Text != "" {
				outputs = append(outputs, ResponsesOutput{Type: "message", ID: generateItemID(), Role: "assistant", Status: "completed", Content: []ResponsesContentPart{{Type: "output_text", Text: block.Text}}})
				continue
			}
			if block.Text != "" {
				msgParts = append(msgParts, ResponsesContentPart{
					Type: "output_text",
					Text: block.Text,
				})
			}
		case "tool_use":
			args := "{}"
			if len(block.Input) > 0 {
				args = string(block.Input)
			}
			outputs = append(outputs, responsesOutputForAnthropicToolUse(block, args, toolCtx))
		}
	}

	// Assemble message output item from text parts
	if len(msgParts) > 0 {
		outputs = append(outputs, ResponsesOutput{
			Type:    "message",
			ID:      generateItemID(),
			Role:    "assistant",
			Content: msgParts,
			Status:  "completed",
		})
	}

	if len(outputs) == 0 {
		outputs = append(outputs, ResponsesOutput{
			Type:    "message",
			ID:      generateItemID(),
			Role:    "assistant",
			Content: []ResponsesContentPart{{Type: "output_text", Text: ""}},
			Status:  "completed",
		})
	}
	out.Output = outputs

	// Map stop_reason → status
	out.Status = anthropicStopReasonToResponsesStatus(AnthropicStopReasonString(resp.StopReason), resp.Content)
	if out.Status == "incomplete" {
		out.IncompleteDetails = &ResponsesIncompleteDetails{Reason: "max_output_tokens"}
	}

	// Usage
	// Anthropic's input_tokens excludes cache_read/cache_creation, while OpenAI
	// Responses' input_tokens is the total including cached tokens. Add them back
	// when converting so downstream consumers see OpenAI semantics.
	totalInputTokens := resp.Usage.InputTokens +
		resp.Usage.CacheReadInputTokens +
		resp.Usage.CacheCreationInputTokens
	out.Usage = &ResponsesUsage{
		InputTokens:              totalInputTokens,
		OutputTokens:             resp.Usage.OutputTokens,
		TotalTokens:              totalInputTokens + resp.Usage.OutputTokens,
		CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
	}
	if resp.Usage.CacheReadInputTokens > 0 {
		out.Usage.InputTokensDetails = &ResponsesInputTokensDetails{
			CachedTokens: resp.Usage.CacheReadInputTokens,
		}
	}

	return out
}

// responsesOutputForAnthropicToolUse 把一个 tool_use 块按请求侧工具上下文还原为
// Responses 输出项（fork 定制，见 AnthropicResponsesToolContext）。
func responsesOutputForAnthropicToolUse(block AnthropicContentBlock, args string, toolCtx *AnthropicResponsesToolContext) ResponsesOutput {
	itemType, name, namespace := toolCtx.classifyToolUse(block.Name)
	out := ResponsesOutput{
		Type:   itemType,
		ID:     generateItemID(),
		CallID: toResponsesCallID(block.ID),
		Status: "completed",
	}
	switch itemType {
	case "custom_tool_call":
		// custom 调用的 arguments 是降级 schema 包裹的 {"input": ...}，还原为
		// 自由文本输入（见 extractCustomToolCallInput）。
		out.Name = name
		out.Input = extractCustomToolCallInput(args)
	case "tool_search_call":
		// codex 只在项类型为 tool_search_call 时执行 tool search，arguments 原样回传。
		out.Arguments = args
	default:
		out.Name = name
		out.Namespace = namespace
		out.Arguments = args
	}
	return out
}

// anthropicStopReasonToResponsesStatus maps Anthropic stop_reason to Responses status.
func anthropicStopReasonToResponsesStatus(stopReason string, blocks []AnthropicContentBlock) string {
	switch stopReason {
	case "max_tokens":
		return "incomplete"
	case "end_turn", "tool_use", "stop_sequence":
		return "completed"
	default:
		return "completed"
	}
}

// ---------------------------------------------------------------------------
// Streaming: AnthropicStreamEvent → []ResponsesStreamEvent (stateful converter)
// ---------------------------------------------------------------------------

// AnthropicEventToResponsesState tracks state for converting a sequence of
// Anthropic SSE events into Responses SSE events.
type AnthropicEventToResponsesState struct {
	ResponseID     string
	Model          string
	Created        int64
	SequenceNumber int

	// CreatedSent tracks whether response.created has been emitted.
	CreatedSent bool
	// CompletedSent tracks whether the terminal event has been emitted.
	CompletedSent bool

	// Current output tracking
	OutputIndex     int
	CurrentItemID   string
	CurrentItemType string // "message" | "function_call" | "custom_tool_call" | "tool_search_call" | "reasoning"

	// For message output: accumulate text parts
	ContentIndex int
	// TextAccum accumulates the current text part so that output_text.done and
	// content_part.done can carry the full text (deltas carry increments only).
	TextAccum string

	// For function_call: track per-output info
	CurrentCallID string
	CurrentName   string
	// fork 定制：namespace 子工具调用还原出的归属命名空间（见 ToolContext）。
	CurrentNamespace string
	// fork 定制：累积当前工具调用的 input_json_delta。custom 调用的 input 与
	// tool_search 调用的 arguments 无法增量还原，收尾时一次性下发；function_call
	// 的 output_item.done 也需带全量 arguments 供 codex 物化调用。
	CurrentArgs strings.Builder

	// ToolContext 是请求侧工具上下文（fork 定制，见 AnthropicResponsesToolContext）。
	// nil 时所有 tool_use 按 function_call 还原，与历史行为一致。
	ToolContext *AnthropicResponsesToolContext

	// Content of the currently open item, folded into Outputs when it closes.
	// function_call 的参数累积复用上面 fork 的 CurrentArgs（strings.Builder），
	// 上游同名 string 字段不再单独保留，避免双重累积。
	CurrentContent             []ResponsesContentPart // message
	CurrentSummary             string                 // reasoning
	CurrentThinking            AnthropicContentBlock
	PreserveThinkingSignatures bool

	// Outputs accumulates every closed output item so that response.completed
	// can carry the full output list. The OpenAI SDK's get_final_response()
	// parses the terminal event's response directly; without this, clients see
	// an empty output_text.
	Outputs []ResponsesOutput

	// Usage from message_start / message_delta. InputTokens here follows
	// Anthropic semantics (excludes cached tokens); they are added back when
	// emitting the OpenAI Responses usage.
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int

	StopReason string
}

// NewAnthropicEventToResponsesState returns an initialised stream state.
func NewAnthropicEventToResponsesState() *AnthropicEventToResponsesState {
	return &AnthropicEventToResponsesState{
		Created: time.Now().Unix(),
	}
}

// AnthropicEventToResponsesEvents converts a single Anthropic SSE event into
// zero or more Responses SSE events, updating state as it goes.
func AnthropicEventToResponsesEvents(
	evt *AnthropicStreamEvent,
	state *AnthropicEventToResponsesState,
) []ResponsesStreamEvent {
	switch evt.Type {
	case "message_start":
		return anthToResHandleMessageStart(evt, state)
	case "content_block_start":
		return anthToResHandleContentBlockStart(evt, state)
	case "content_block_delta":
		return anthToResHandleContentBlockDelta(evt, state)
	case "content_block_stop":
		return anthToResHandleContentBlockStop(evt, state)
	case "message_delta":
		return anthToResHandleMessageDelta(evt, state)
	case "message_stop":
		return anthToResHandleMessageStop(state)
	default:
		return nil
	}
}

// FinalizeAnthropicResponsesStream emits synthetic termination events if the
// stream ended without a proper message_stop.
func FinalizeAnthropicResponsesStream(state *AnthropicEventToResponsesState) []ResponsesStreamEvent {
	if !state.CreatedSent || state.CompletedSent {
		return nil
	}

	var events []ResponsesStreamEvent

	// Close any open item
	events = append(events, closeCurrentResponsesItem(state)...)

	status, incompleteDetails := anthropicResponsesStreamTerminalState(state.StopReason)
	events = append(events, makeResponsesCompletedEvent(state, status, incompleteDetails))
	state.CompletedSent = true
	return events
}

// ResponsesEventToSSE formats a ResponsesStreamEvent as an SSE data line.
func ResponsesEventToSSE(evt ResponsesStreamEvent) (string, error) {
	data, err := json.Marshal(evt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", evt.Type, data), nil
}

// --- internal handlers ---

func anthToResHandleMessageStart(evt *AnthropicStreamEvent, state *AnthropicEventToResponsesState) []ResponsesStreamEvent {
	if evt.Message != nil {
		state.ResponseID = evt.Message.ID
		state.PreserveThinkingSignatures = state.PreserveThinkingSignatures || claude.IsOpus55(evt.Message.Model)
		if state.Model == "" {
			state.Model = evt.Message.Model
		}
		if evt.Message.Usage.InputTokens > 0 {
			state.InputTokens = evt.Message.Usage.InputTokens
		}
		if evt.Message.Usage.CacheReadInputTokens > 0 {
			state.CacheReadInputTokens = evt.Message.Usage.CacheReadInputTokens
		}
		if evt.Message.Usage.CacheCreationInputTokens > 0 {
			state.CacheCreationInputTokens = evt.Message.Usage.CacheCreationInputTokens
		}
	}

	if state.CreatedSent {
		return nil
	}
	state.CreatedSent = true

	// Emit response.created
	return []ResponsesStreamEvent{makeResponsesCreatedEvent(state)}
}

func anthToResHandleContentBlockStart(evt *AnthropicStreamEvent, state *AnthropicEventToResponsesState) []ResponsesStreamEvent {
	if evt.ContentBlock == nil {
		return nil
	}

	var events []ResponsesStreamEvent

	switch evt.ContentBlock.Type {
	case "thinking", "redacted_thinking":
		// 开新 item 前必须先关掉在开的那个，与下面的 tool_use 分支一致。
		// 一个 message item 在它的 text 块 content_block_stop 时是刻意保持打开的
		// （同一 item 里可能还有后续 text 块），所以 thinking 块到来时它仍然开着：
		// 不关就直接被 CurrentItemType/CurrentItemID 覆盖，累积在 CurrentContent
		// 里的助手文本既不会进 state.Outputs，也拿不到 output_item.done，
		// response.completed 于是只带 reasoning——客户端看到的是「成功但无输出」。
		// 交错思考（interleaved-thinking，本仓库在 anthropic-beta 透传里明确支持）
		// 会稳定产生 text → thinking 这个顺序。
		events = append(events, closeCurrentResponsesItem(state)...)

		state.CurrentItemID = generateItemID()
		state.CurrentItemType = "reasoning"
		state.CurrentThinking = *evt.ContentBlock
		state.CurrentSummary = evt.ContentBlock.Thinking
		state.ContentIndex = 0

		events = append(events, makeResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
			OutputIndex: state.OutputIndex,
			Item: &ResponsesOutput{
				Type: "reasoning",
				ID:   state.CurrentItemID,
			},
		}))

	case "text":
		// If we don't have an open message item, open one
		if state.CurrentItemType != "message" {
			// 走到这里时 CurrentItemType 只可能是 ""（前一个 reasoning/function_call
			// 已在自己的 content_block_stop 里关闭）；保留这次关闭是为了让三个分支
			// 的「开新 item 前先关旧的」保持同一条不变式，而不是留一个仅 text 例外。
			events = append(events, closeCurrentResponsesItem(state)...)

			state.CurrentItemID = generateItemID()
			state.CurrentItemType = "message"
			state.ContentIndex = 0

			events = append(events, makeResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
				OutputIndex: state.OutputIndex,
				Item: &ResponsesOutput{
					Type:   "message",
					ID:     state.CurrentItemID,
					Role:   "assistant",
					Status: "in_progress",
				},
			}))
		}

		// response.content_part.added must precede the output_text.delta events
		// for that part. The message item is added with content: [], and the
		// OpenAI SDK's accumulating stream helper (client.responses.stream) only
		// appends a content part when it sees content_part.added. Without it the
		// following output_text.delta indexes output.content[content_index] and
		// raises IndexError. Raw event iteration
		// (responses.create(stream=True)) does not accumulate, which is why this
		// went unnoticed.
		events = append(events, makeResponsesEvent(state, "response.content_part.added", &ResponsesStreamEvent{
			OutputIndex:  state.OutputIndex,
			ContentIndex: state.ContentIndex,
			ItemID:       state.CurrentItemID,
			Part:         &ResponsesContentPart{Type: "output_text", Text: ""},
		}))
		state.TextAccum = ""

	case "tool_use":
		// Close previous item if any
		events = append(events, closeCurrentResponsesItem(state)...)

		// fork 定制：按请求声明的工具类型还原 codex 工具调用形态（对齐 chat 桥
		// announceChatToolItem，见 AnthropicResponsesToolContext）。
		itemType, name, namespace := state.ToolContext.classifyToolUse(evt.ContentBlock.Name)
		state.CurrentItemID = generateItemID()
		state.CurrentItemType = itemType
		state.CurrentCallID = toResponsesCallID(evt.ContentBlock.ID)
		state.CurrentName = name
		state.CurrentNamespace = namespace
		state.CurrentArgs.Reset()

		events = append(events, makeResponsesEvent(state, "response.output_item.added", &ResponsesStreamEvent{
			OutputIndex: state.OutputIndex,
			Item: &ResponsesOutput{
				Type:      itemType,
				ID:        state.CurrentItemID,
				CallID:    state.CurrentCallID,
				Name:      state.CurrentName,
				Namespace: state.CurrentNamespace,
				Status:    "in_progress",
			},
		}))
	}

	return events
}

func anthToResHandleContentBlockDelta(evt *AnthropicStreamEvent, state *AnthropicEventToResponsesState) []ResponsesStreamEvent {
	if evt.Delta == nil {
		return nil
	}

	switch evt.Delta.Type {
	case "text_delta":
		if evt.Delta.Text == "" {
			return nil
		}
		state.TextAccum += evt.Delta.Text
		return []ResponsesStreamEvent{makeResponsesEvent(state, "response.output_text.delta", &ResponsesStreamEvent{
			OutputIndex:  state.OutputIndex,
			ContentIndex: state.ContentIndex,
			Delta:        evt.Delta.Text,
			ItemID:       state.CurrentItemID,
		})}

	case "thinking_delta":
		if evt.Delta.Thinking == "" {
			return nil
		}
		state.CurrentSummary += evt.Delta.Thinking
		return []ResponsesStreamEvent{makeResponsesEvent(state, "response.reasoning_summary_text.delta", &ResponsesStreamEvent{
			OutputIndex:  state.OutputIndex,
			SummaryIndex: 0,
			Delta:        evt.Delta.Thinking,
			ItemID:       state.CurrentItemID,
		})}

	case "input_json_delta":
		if evt.Delta.PartialJSON == "" {
			return nil
		}
		// fork 定制：累积全量参数（收尾项需要）；custom 调用的 input 与
		// tool_search 的 arguments 无法增量还原，流中不产出增量事件，收尾时
		// 一次性下发（见 anthToResHandleContentBlockStop）。
		_, _ = state.CurrentArgs.WriteString(evt.Delta.PartialJSON)
		if state.CurrentItemType == "custom_tool_call" || state.CurrentItemType == "tool_search_call" {
			return nil
		}
		return []ResponsesStreamEvent{makeResponsesEvent(state, "response.function_call_arguments.delta", &ResponsesStreamEvent{
			OutputIndex: state.OutputIndex,
			Delta:       evt.Delta.PartialJSON,
			ItemID:      state.CurrentItemID,
			CallID:      state.CurrentCallID,
			Name:        state.CurrentName,
		})}

	case "signature_delta":
		// Keep signatures in the opaque bridge envelope, never in visible text.
		if state.PreserveThinkingSignatures {
			state.CurrentThinking.Signature += evt.Delta.Signature
		}
		return nil
	}

	return nil
}

func anthToResHandleContentBlockStop(evt *AnthropicStreamEvent, state *AnthropicEventToResponsesState) []ResponsesStreamEvent {
	switch state.CurrentItemType {
	case "reasoning":
		// Emit reasoning summary done + output item done
		events := []ResponsesStreamEvent{
			makeResponsesEvent(state, "response.reasoning_summary_text.done", &ResponsesStreamEvent{
				OutputIndex:  state.OutputIndex,
				SummaryIndex: 0,
				ItemID:       state.CurrentItemID,
			}),
		}
		events = append(events, closeCurrentResponsesItem(state)...)
		return events

	case "function_call":
		// Emit function_call_arguments.done + output item done.
		// arguments must repeat exactly what the deltas already streamed for this
		// item: clients reconcile the done event against the accumulated
		// function_call_arguments.delta payloads and reject the call as
		// inconsistent_tool_call when the two disagree. Omitting the field left it
		// empty while the deltas carried the whole JSON.
		events := []ResponsesStreamEvent{
			makeResponsesEvent(state, "response.function_call_arguments.done", &ResponsesStreamEvent{
				OutputIndex: state.OutputIndex,
				ItemID:      state.CurrentItemID,
				CallID:      state.CurrentCallID,
				Name:        state.CurrentName,
				Arguments:   currentResponsesToolArguments(state),
			}),
		}
		events = append(events, closeCurrentResponsesItem(state)...)
		return events

	case "custom_tool_call":
		// fork 定制：custom 调用按 custom_tool_call 生命周期收尾，input 在此处
		// 一次性下发（流中不产出增量，对齐 chat 桥 closeChatToolItems）。
		input := extractCustomToolCallInput(currentResponsesToolArguments(state))
		var events []ResponsesStreamEvent
		if input != "" {
			events = append(events, makeResponsesEvent(state, "response.custom_tool_call_input.delta", &ResponsesStreamEvent{
				OutputIndex: state.OutputIndex,
				ItemID:      state.CurrentItemID,
				Delta:       input,
			}))
		}
		events = append(events, makeResponsesEvent(state, "response.custom_tool_call_input.done", &ResponsesStreamEvent{
			OutputIndex: state.OutputIndex,
			ItemID:      state.CurrentItemID,
			CallID:      state.CurrentCallID,
			Name:        state.CurrentName,
			Input:       input,
		}))
		events = append(events, closeCurrentResponsesItem(state)...)
		return events

	case "tool_search_call":
		// fork 定制：tool_search 调用按 tool_search_call 项收尾，codex 从
		// output_item.done 物化该调用（无参数增量事件，对齐 chat 桥）。
		return closeCurrentResponsesItem(state)

	case "message":
		// Text block is done: emit output_text.done then content_part.done (the
		// order OpenAI uses), both carrying the part's full text. The message
		// item itself stays open since more blocks may follow.
		text := state.TextAccum
		state.TextAccum = ""
		contentIndex := state.ContentIndex
		state.CurrentContent = append(state.CurrentContent, ResponsesContentPart{Type: "output_text", Text: text})
		// 关掉一个 part 就推进 content_index：上面那句注释说的「item 保持打开，
		// 因为后面可能还有块」正是这里的触发条件。不推进的话，同一 item 里第二个
		// text 块会再发一次 content_part.added(content_index=0)，与第一个 part 撞在
		// 同一下标上——SDK 的累积式 stream helper 按 content[content_index] 写入，
		// 后一个 part 直接覆盖前一个，可见文本丢失。
		// 只在这里推进：新 item 的 content_index 由 closeCurrentResponsesItem 归 0。
		state.ContentIndex++
		return []ResponsesStreamEvent{
			makeResponsesEvent(state, "response.output_text.done", &ResponsesStreamEvent{
				OutputIndex:  state.OutputIndex,
				ContentIndex: contentIndex,
				ItemID:       state.CurrentItemID,
				Text:         text,
			}),
			makeResponsesEvent(state, "response.content_part.done", &ResponsesStreamEvent{
				OutputIndex:  state.OutputIndex,
				ContentIndex: contentIndex,
				ItemID:       state.CurrentItemID,
				Part:         &ResponsesContentPart{Type: "output_text", Text: text},
			}),
		}
	}

	return nil
}

func anthToResHandleMessageDelta(evt *AnthropicStreamEvent, state *AnthropicEventToResponsesState) []ResponsesStreamEvent {
	if evt.Usage != nil {
		state.OutputTokens = evt.Usage.OutputTokens
		if evt.Usage.InputTokens > 0 {
			state.InputTokens = evt.Usage.InputTokens
		}
		if evt.Usage.CacheReadInputTokens > 0 {
			state.CacheReadInputTokens = evt.Usage.CacheReadInputTokens
		}
		if evt.Usage.CacheCreationInputTokens > 0 {
			state.CacheCreationInputTokens = evt.Usage.CacheCreationInputTokens
		}
	}
	if evt.Delta != nil && evt.Delta.StopReason != "" {
		state.StopReason = evt.Delta.StopReason
	}

	return nil
}

func anthToResHandleMessageStop(state *AnthropicEventToResponsesState) []ResponsesStreamEvent {
	if state.CompletedSent {
		return nil
	}

	var events []ResponsesStreamEvent
	events = append(events, closeCurrentResponsesItem(state)...)

	status, incompleteDetails := anthropicResponsesStreamTerminalState(state.StopReason)
	events = append(events, makeResponsesCompletedEvent(state, status, incompleteDetails))
	state.CompletedSent = true
	return events
}

// --- helper functions ---

func anthropicResponsesStreamTerminalState(stopReason string) (string, *ResponsesIncompleteDetails) {
	if stopReason == "max_tokens" {
		return "incomplete", &ResponsesIncompleteDetails{Reason: "max_output_tokens"}
	}
	return "completed", nil
}

func closeCurrentResponsesItem(state *AnthropicEventToResponsesState) []ResponsesStreamEvent {
	if state.CurrentItemType == "" {
		return nil
	}

	// Assemble the full item: both output_item.done and response.completed must
	// carry its content. Emitting only {type,id,status} makes SDK-side
	// accumulation produce an empty output.
	item := ResponsesOutput{
		Type:   state.CurrentItemType,
		ID:     state.CurrentItemID,
		Status: "completed",
	}
	// fork 定制：工具调用项的 output_item.done 带完整 call 载荷，codex 从该事件
	// 物化调用（custom/tool_search 流中不产出增量，全量只在这里下发）。上游新增
	// message/reasoning 的完整内容装配，供 output_item.done 与 response.completed 携带。
	switch state.CurrentItemType {
	case "message":
		item.Role = "assistant"
		item.Content = state.CurrentContent
	case "function_call":
		item.CallID = state.CurrentCallID
		item.Name = state.CurrentName
		item.Namespace = state.CurrentNamespace
		item.Arguments = currentResponsesToolArguments(state)
	case "custom_tool_call":
		item.CallID = state.CurrentCallID
		item.Name = state.CurrentName
		item.Input = extractCustomToolCallInput(currentResponsesToolArguments(state))
	case "tool_search_call":
		item.CallID = state.CurrentCallID
		item.Arguments = currentResponsesToolArguments(state)
	case "reasoning":
		if state.PreserveThinkingSignatures && (state.CurrentThinking.Signature != "" || state.CurrentThinking.Data != "") {
			state.CurrentThinking.Thinking = state.CurrentSummary
			item.EncryptedContent = encodeAnthropicThinking(state.CurrentThinking)
		}
		if state.CurrentSummary != "" {
			item.Summary = []ResponsesSummary{{Type: "summary_text", Text: state.CurrentSummary}}
		}
	}
	state.Outputs = append(state.Outputs, item)

	// Reset
	state.CurrentItemType = ""
	state.CurrentItemID = ""
	state.CurrentCallID = ""
	state.CurrentName = ""
	state.CurrentNamespace = ""
	state.CurrentArgs.Reset()
	state.CurrentContent = nil
	state.CurrentSummary = ""
	state.CurrentThinking = AnthropicContentBlock{}
	state.TextAccum = ""
	state.OutputIndex++
	state.ContentIndex = 0

	return []ResponsesStreamEvent{makeResponsesEvent(state, "response.output_item.done", &ResponsesStreamEvent{
		OutputIndex: state.OutputIndex - 1, // Use the index before increment
		Item:        &item,
	})}
}

// currentResponsesToolArguments 返回当前工具调用累积的全量 arguments，空时兜底 "{}"。
func currentResponsesToolArguments(state *AnthropicEventToResponsesState) string {
	args := strings.TrimSpace(state.CurrentArgs.String())
	if args == "" {
		return "{}"
	}
	return args
}

func makeResponsesCreatedEvent(state *AnthropicEventToResponsesState) ResponsesStreamEvent {
	seq := state.SequenceNumber
	state.SequenceNumber++
	return ResponsesStreamEvent{
		Type:           "response.created",
		SequenceNumber: seq,
		Response: &ResponsesResponse{
			ID:        state.ResponseID,
			Object:    "response",
			CreatedAt: state.Created,
			Model:     state.Model,
			Status:    "in_progress",
			Output:    []ResponsesOutput{},
		},
	}
}

func makeResponsesCompletedEvent(
	state *AnthropicEventToResponsesState,
	status string,
	incompleteDetails *ResponsesIncompleteDetails,
) ResponsesStreamEvent {
	seq := state.SequenceNumber
	state.SequenceNumber++

	// Anthropic's input_tokens excludes cache_read/cache_creation; add them
	// back to match OpenAI Responses semantics where input_tokens is the total.
	totalInputTokens := state.InputTokens + state.CacheReadInputTokens + state.CacheCreationInputTokens
	usage := &ResponsesUsage{
		InputTokens:              totalInputTokens,
		OutputTokens:             state.OutputTokens,
		TotalTokens:              totalInputTokens + state.OutputTokens,
		CacheCreationInputTokens: state.CacheCreationInputTokens,
	}
	if state.CacheReadInputTokens > 0 {
		usage.InputTokensDetails = &ResponsesInputTokensDetails{
			CachedTokens: state.CacheReadInputTokens,
		}
	}

	eventType := "response.completed"
	if status == "incomplete" {
		eventType = "response.incomplete"
	}

	// Carry the output items accumulated over the stream. The SDK's
	// get_final_response() reads them straight from the terminal event, so an
	// empty list leaves clients with an empty result.
	outputs := state.Outputs
	if outputs == nil {
		outputs = []ResponsesOutput{}
	}

	return ResponsesStreamEvent{
		Type:           eventType,
		SequenceNumber: seq,
		Response: &ResponsesResponse{
			ID:                state.ResponseID,
			Object:            "response",
			CreatedAt:         state.Created,
			Model:             state.Model,
			Status:            status,
			Output:            outputs,
			Usage:             usage,
			IncompleteDetails: incompleteDetails,
		},
	}
}

func makeResponsesEvent(state *AnthropicEventToResponsesState, eventType string, template *ResponsesStreamEvent) ResponsesStreamEvent {
	seq := state.SequenceNumber
	state.SequenceNumber++

	evt := *template
	evt.Type = eventType
	evt.SequenceNumber = seq
	return evt
}

func generateResponsesID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "resp_" + hex.EncodeToString(b)
}

func generateItemID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "item_" + hex.EncodeToString(b)
}
