package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	// DefaultMaxTokens 是请求没写 MaxTokens 时用的值，避免推理占满额度后正文为空。
	DefaultMaxTokens = 4096
	// MaxResponseBytes 是回复体（流式为累计）的上限。
	MaxResponseBytes = 16 << 20
	// MaxRequestBytes 是编码后请求体的上限。
	MaxRequestBytes = 16 << 20

	maxToolCalls = 128

	chatAdapterName    = "chat-completions"
	chatAdapterVersion = "1"
)

// ChatAdapterBase 是标准 Chat Completions 适配器。
//
// 它只处理标准字段；不认识的字段放进 Response.Sidecar，从不解释 reasoning_content。
// 方法用值接收者，零值可用（扩展点就是它自己）。
type ChatAdapterBase struct {
	ext ChatExtension
}

var (
	_ ProtocolAdapter = ChatAdapterBase{}
	_ ChatExtension   = ChatAdapterBase{}
)

// NewChatAdapterBase 返回基础适配器。ext 应是最外层派生对象；nil 表示用基础实现本身。
func NewChatAdapterBase(ext ChatExtension) ChatAdapterBase {
	return ChatAdapterBase{ext: ext}
}

func (b ChatAdapterBase) extension() ChatExtension {
	if b.ext != nil {
		return b.ext
	}
	return b
}

func (ChatAdapterBase) Name() string    { return chatAdapterName }
func (ChatAdapterBase) Version() string { return chatAdapterVersion }

// ---------------------------------------------------------------- 编码

// EncodePayload 校验请求并编码。输出确定（键按字典序）。
func (b ChatAdapterBase) EncodePayload(req Request) ([]byte, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	ext := b.extension()
	msgs := make([]any, 0, len(req.Messages))
	for i, m := range req.Messages {
		switch m.Role {
		case RoleSystem, RoleUser:
			var sb strings.Builder
			for _, blk := range m.Blocks {
				sb.WriteString(blk.Text)
			}
			msgs = append(msgs, map[string]any{"role": string(m.Role), "content": sb.String()})
		case RoleAssistant:
			wire := map[string]any{"role": "assistant"}
			if err := ext.EncodeAssistant(req, m, wire); err != nil {
				return nil, asError(err, CodeBadRequest)
			}
			wire["role"] = "assistant"
			msgs = append(msgs, wire)
		case RoleTool:
			for _, blk := range m.Blocks {
				msgs = append(msgs, map[string]any{
					"role": "tool", "tool_call_id": blk.ToolCallID, "content": blk.Text,
				})
			}
		default:
			return nil, NewError(CodeBadRequest, "message %d: invalid role", i)
		}
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultMaxTokens
	}
	wire := map[string]any{"model": req.Model, "messages": msgs, "max_tokens": maxTokens}
	if req.Stream {
		wire["stream"] = true
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			fn := map[string]any{"name": t.Name}
			if t.Description != "" {
				fn["description"] = t.Description
			}
			if len(t.Parameters) > 0 {
				fn["parameters"] = t.Parameters
			}
			tools = append(tools, map[string]any{"type": "function", "function": fn})
		}
		wire["tools"] = tools
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(wire); err != nil {
		return nil, NewError(CodeBadRequest, "cannot encode request")
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	if len(out) > MaxRequestBytes {
		return nil, NewError(CodeTooLarge, "request body exceeds %d bytes", MaxRequestBytes)
	}
	return out, nil
}

// EncodeAssistant 是默认实现：text 块拼成 content，tool_call 块写成 tool_calls。
// thinking 块在标准协议里没有位置，不回传。
func (ChatAdapterBase) EncodeAssistant(_ Request, m Message, wire map[string]any) error {
	var text strings.Builder
	var calls []any
	for _, blk := range m.Blocks {
		switch blk.Kind {
		case BlockText:
			text.WriteString(blk.Text)
		case BlockToolCall:
			calls = append(calls, map[string]any{
				"id":   blk.ToolCallID,
				"type": "function",
				"function": map[string]any{
					"name":      blk.ToolName,
					"arguments": string(blk.Arguments),
				},
			})
		case BlockThinking:
			// 不回传。
		default:
			return NewError(CodeBadRequest, "assistant message cannot hold %q block", blk.Kind)
		}
	}
	if text.Len() > 0 {
		wire["content"] = text.String()
	} else {
		wire["content"] = nil
	}
	if len(calls) > 0 {
		wire["tool_calls"] = calls
	}
	return nil
}

// validateRequest 是固定输入校验，不可重写。
func validateRequest(req Request) error {
	if strings.TrimSpace(req.Model) == "" {
		return NewError(CodeBadRequest, "model is empty")
	}
	if len(req.Messages) == 0 {
		return NewError(CodeBadRequest, "messages is empty")
	}
	if req.MaxTokens < 0 {
		return NewError(CodeBadRequest, "max_tokens is negative")
	}
	for i, m := range req.Messages {
		if !m.Role.valid() {
			return NewError(CodeBadRequest, "message %d: invalid role", i)
		}
		if len(m.Blocks) == 0 {
			return NewError(CodeBadRequest, "message %d: no blocks", i)
		}
		content := 0
		for j, blk := range m.Blocks {
			where := fmt.Sprintf("message %d block %d", i, j)
			switch blk.Kind {
			case BlockText:
				if m.Role == RoleTool {
					return NewError(CodeBadRequest, "%s: tool message needs tool_result blocks", where)
				}
				if blk.Text != "" {
					content++
				}
			case BlockThinking:
				if m.Role != RoleAssistant {
					return NewError(CodeBadRequest, "%s: thinking block only in assistant message", where)
				}
			case BlockToolCall:
				if m.Role != RoleAssistant {
					return NewError(CodeBadRequest, "%s: tool_call block only in assistant message", where)
				}
				if blk.ToolCallID == "" || blk.ToolName == "" {
					return NewError(CodeBadRequest, "%s: tool_call needs id and name", where)
				}
				if !json.Valid(blk.Arguments) {
					return NewError(CodeBadRequest, "%s: tool_call arguments are not valid JSON", where)
				}
				content++
			case BlockToolResult:
				if m.Role != RoleTool {
					return NewError(CodeBadRequest, "%s: tool_result block only in tool message", where)
				}
				if blk.ToolCallID == "" {
					return NewError(CodeBadRequest, "%s: tool_result needs tool call id", where)
				}
				content++
			default:
				return NewError(CodeBadRequest, "%s: unknown block kind", where)
			}
		}
		if content == 0 && m.Role != RoleSystem && m.Role != RoleUser {
			return NewError(CodeBadRequest, "message %d: no content", i)
		}
	}
	seen := make(map[string]bool, len(req.Tools))
	for i, t := range req.Tools {
		if t.Name == "" {
			return NewError(CodeBadRequest, "tool %d: name is empty", i)
		}
		if seen[t.Name] {
			return NewError(CodeBadRequest, "tool %d: duplicate name", i)
		}
		seen[t.Name] = true
		if len(t.Parameters) > 0 && !json.Valid(t.Parameters) {
			return NewError(CodeBadRequest, "tool %d: parameters are not valid JSON", i)
		}
	}
	return nil
}

// ---------------------------------------------------------------- 非流式解码

var knownTop = map[string]bool{"id": true, "object": true, "created": true, "model": true, "choices": true, "usage": true}

// NormalizeResponse 解析并校验完整回复体。
func (b ChatAdapterBase) NormalizeResponse(body []byte) (Response, error) {
	if len(body) > MaxResponseBytes {
		return Response{}, NewError(CodeTooLarge, "response body exceeds %d bytes", MaxResponseBytes)
	}
	top, err := parseObject(body)
	if err != nil {
		return Response{}, err
	}
	if e, ok := top["error"]; ok && !isNull(e) {
		return Response{}, NewError(CodeBadResponse, "response carries an error object")
	}
	var resp Response
	if resp.ID, err = optString(top, "id"); err != nil {
		return Response{}, err
	}
	if resp.Model, err = optString(top, "model"); err != nil {
		return Response{}, err
	}
	for k, v := range top {
		if !knownTop[k] && k != "error" && !isNull(v) {
			resp.AddSidecar("response."+k, v)
		}
	}
	var choices []json.RawMessage
	if raw, ok := top["choices"]; !ok || json.Unmarshal(raw, &choices) != nil {
		return Response{}, NewError(CodeBadResponse, "choices is missing or not an array")
	}
	if len(choices) != 1 {
		return Response{}, NewError(CodeBadResponse, "expected exactly one choice, got %d", len(choices))
	}
	choice, err := parseObject(choices[0])
	if err != nil {
		return Response{}, err
	}
	if idx, ok := choice["index"]; ok && !isNull(idx) {
		var n int
		if json.Unmarshal(idx, &n) != nil || n != 0 {
			return Response{}, NewError(CodeBadResponse, "unsupported choice index")
		}
	}
	rawFin, err := optString(choice, "finish_reason")
	if err != nil {
		return Response{}, err
	}
	resp.RawFinishReason = rawFin
	resp.FinishReason = ParseFinishReason(rawFin)
	for k, v := range choice {
		switch k {
		case "index", "message", "finish_reason":
		default:
			if !isNull(v) {
				resp.AddSidecar("choice."+k, v)
			}
		}
	}
	rawMsg, ok := choice["message"]
	if !ok {
		return Response{}, NewError(CodeBadResponse, "choice has no message")
	}
	msg, err := parseObject(rawMsg)
	if err != nil {
		return Response{}, err
	}
	if err := b.extension().DecodeMessage(msg, &resp); err != nil {
		return Response{}, asError(err, CodeBadResponse)
	}
	if raw, ok := top["usage"]; ok {
		u, side, err := parseUsage(raw)
		if err != nil {
			return Response{}, err
		}
		resp.Usage = u
		for k, v := range side {
			resp.AddSidecar(k, v)
		}
	}
	if err := validateOutput(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// DecodeMessage 是默认实现：读 role、content、tool_calls，其余放进 Sidecar。
func (ChatAdapterBase) DecodeMessage(msg map[string]json.RawMessage, out *Response) error {
	out.Message.Role = RoleAssistant
	if raw, ok := msg["role"]; ok && !isNull(raw) {
		var r string
		if json.Unmarshal(raw, &r) != nil || r != string(RoleAssistant) {
			return NewError(CodeBadResponse, "message role is not assistant")
		}
	}
	if raw, ok := msg["content"]; ok {
		parts, err := decodeContent(raw)
		if err != nil {
			return err
		}
		for _, p := range parts {
			if p != "" {
				out.Message.Blocks = append(out.Message.Blocks, Block{Kind: BlockText, Text: p})
			}
		}
	}
	if raw, ok := msg["tool_calls"]; ok && !isNull(raw) {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return NewError(CodeBadResponse, "tool_calls is not an array")
		}
		for i, it := range items {
			blk, err := decodeToolCall(i, it, out)
			if err != nil {
				return err
			}
			out.Message.Blocks = append(out.Message.Blocks, blk)
		}
	}
	for k, v := range msg {
		switch k {
		case "role", "content", "tool_calls":
		default:
			if !isNull(v) {
				out.AddSidecar("message."+k, v)
			}
		}
	}
	return nil
}

func decodeToolCall(i int, raw json.RawMessage, out *Response) (Block, error) {
	item, err := parseObject(raw)
	if err != nil {
		return Block{}, err
	}
	var blk Block
	blk.Kind = BlockToolCall
	if blk.ToolCallID, err = optString(item, "id"); err != nil {
		return Block{}, err
	}
	typ, err := optString(item, "type")
	if err != nil {
		return Block{}, err
	}
	if typ != "" && typ != "function" {
		return Block{}, NewError(CodeBadResponse, "tool call %d: unsupported type", i)
	}
	fn, err := parseObject(item["function"])
	if err != nil {
		return Block{}, NewError(CodeBadResponse, "tool call %d: function is missing or invalid", i)
	}
	if blk.ToolName, err = optString(fn, "name"); err != nil {
		return Block{}, err
	}
	args, err := optString(fn, "arguments")
	if err != nil {
		return Block{}, err
	}
	blk.Arguments = json.RawMessage(args)
	prefix := fmt.Sprintf("message.tool_calls.%d.", i)
	for k, v := range item {
		switch k {
		case "id", "type", "function":
		default:
			if !isNull(v) {
				out.AddSidecar(prefix+k, v)
			}
		}
	}
	for k, v := range fn {
		switch k {
		case "name", "arguments":
		default:
			if !isNull(v) {
				out.AddSidecar(prefix+"function."+k, v)
			}
		}
	}
	return blk, nil
}

// decodeContent 接受 null、字符串、或 text 片段数组。
func decodeContent(raw json.RawMessage) ([]string, error) {
	if isNull(raw) {
		return nil, nil
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, NewError(CodeBadResponse, "content is not valid")
		}
		return []string{s}, nil
	case '[':
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &parts); err != nil {
			return nil, NewError(CodeBadResponse, "content parts are not valid")
		}
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p.Type != "text" {
				return nil, NewError(CodeBadResponse, "unsupported content part type")
			}
			out = append(out, p.Text)
		}
		return out, nil
	}
	return nil, NewError(CodeBadResponse, "content has an unsupported type")
}

// validateOutput 是固定输出校验，不可重写：工具调用完整性、调用 ID 唯一、截断检查。
func validateOutput(resp *Response) error {
	hasText, hasCall := false, false
	seen := map[string]bool{}
	for i, blk := range resp.Message.Blocks {
		switch blk.Kind {
		case BlockText:
			if blk.Text != "" {
				hasText = true
			}
		case BlockThinking:
		case BlockToolCall:
			if blk.ToolCallID == "" || blk.ToolName == "" {
				return NewError(CodeBadResponse, "tool call %d lacks id or name", i)
			}
			if len(blk.Arguments) == 0 || !json.Valid(blk.Arguments) {
				if resp.FinishReason == FinishLength {
					return NewError(CodeOutputTruncated, "tool call arguments cut off by length limit")
				}
				return NewError(CodeBadResponse, "tool call %d arguments are not complete JSON", i)
			}
			if seen[blk.ToolCallID] {
				return NewError(CodeBadResponse, "duplicate tool call id")
			}
			seen[blk.ToolCallID] = true
			hasCall = true
		default:
			return NewError(CodeBadResponse, "response holds a %q block", blk.Kind)
		}
	}
	if resp.FinishReason == FinishLength && !hasText && !hasCall {
		return NewError(CodeOutputTruncated, "output cut off by length limit before any text or tool call")
	}
	return nil
}

// ---------------------------------------------------------------- 流式解码

// DecodeEvent 解一块流数据。data 是 SSE 的 data 内容。
func (b ChatAdapterBase) DecodeEvent(state *StreamState, data []byte) ([]StreamEvent, error) {
	if state == nil {
		return nil, NewError(CodeBadRequest, "stream state is nil")
	}
	if state.err != nil {
		return nil, state.err
	}
	evs, err := b.decodeEvent(state, data)
	if err != nil {
		e := asError(err, CodeBadResponse)
		state.err = e
		return nil, e
	}
	return evs, nil
}

func (b ChatAdapterBase) decodeEvent(st *StreamState, data []byte) ([]StreamEvent, error) {
	if len(data) > MaxResponseBytes {
		return nil, NewError(CodeTooLarge, "stream chunk exceeds %d bytes", MaxResponseBytes)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	if st.done {
		return nil, NewError(CodeBadResponse, "data after [DONE]")
	}
	if st.bytes += len(data); st.bytes > MaxResponseBytes {
		return nil, NewError(CodeTooLarge, "stream exceeds %d bytes", MaxResponseBytes)
	}
	if string(data) == "[DONE]" {
		st.done = true
		return []StreamEvent{{Done: true}}, nil
	}
	top, err := parseObject(data)
	if err != nil {
		return nil, err
	}
	if e, ok := top["error"]; ok && !isNull(e) {
		return nil, NewError(CodeBadResponse, "stream chunk carries an error object")
	}
	if v, err := optString(top, "id"); err != nil {
		return nil, err
	} else if v != "" && st.id == "" {
		st.id = v
	}
	if v, err := optString(top, "model"); err != nil {
		return nil, err
	} else if v != "" && st.model == "" {
		st.model = v
	}
	for k, v := range top {
		if !knownTop[k] && k != "error" && !isNull(v) {
			if st.side == nil {
				st.side = make(map[string]json.RawMessage)
			}
			st.side["response."+k] = v
		}
	}
	if raw, ok := top["usage"]; ok {
		u, side, err := parseUsage(raw)
		if err != nil {
			return nil, err
		}
		if u != nil {
			st.usage = u
		}
		for k, v := range side {
			if st.side == nil {
				st.side = make(map[string]json.RawMessage)
			}
			st.side[k] = v
		}
	}
	var choices []json.RawMessage
	if raw, ok := top["choices"]; ok && !isNull(raw) {
		if json.Unmarshal(raw, &choices) != nil {
			return nil, NewError(CodeBadResponse, "choices is not an array")
		}
	}
	var events []StreamEvent
	for _, craw := range choices {
		ev, err := b.decodeChoice(st, craw)
		if err != nil {
			return nil, err
		}
		if ev.TextDelta != "" || ev.ThinkingDelta != "" {
			events = append(events, ev)
		}
	}
	return events, nil
}

func (b ChatAdapterBase) decodeChoice(st *StreamState, raw json.RawMessage) (StreamEvent, error) {
	var ev StreamEvent
	choice, err := parseObject(raw)
	if err != nil {
		return ev, err
	}
	if idx, ok := choice["index"]; ok && !isNull(idx) {
		var n int
		if json.Unmarshal(idx, &n) != nil || n != 0 {
			return ev, NewError(CodeBadResponse, "unsupported choice index")
		}
	}
	if st.choice == nil {
		st.choice = &ChoiceState{}
	}
	cs := st.choice
	before := cs.mutations
	for k, v := range choice {
		switch k {
		case "index", "delta", "finish_reason":
		default:
			cs.AddSidecar("choice."+k, v)
		}
	}
	if rawDelta, ok := choice["delta"]; ok && !isNull(rawDelta) {
		delta, err := parseObject(rawDelta)
		if err != nil {
			return ev, err
		}
		if err := b.extension().DecodeDelta(delta, cs, &ev); err != nil {
			return ev, asError(err, CodeBadResponse)
		}
	}
	if cs.finished && cs.mutations != before {
		return ev, NewError(CodeBadResponse, "content after finish_reason")
	}
	fin, err := optString(choice, "finish_reason")
	if err != nil {
		return ev, err
	}
	if fin != "" {
		cs.rawFin = fin
		cs.finished = true
	}
	return ev, nil
}

// DecodeDelta 是默认实现：读 role、content、tool_calls 片段，其余放进 Sidecar。
func (ChatAdapterBase) DecodeDelta(delta map[string]json.RawMessage, st *ChoiceState, ev *StreamEvent) error {
	if raw, ok := delta["role"]; ok && !isNull(raw) {
		var r string
		if json.Unmarshal(raw, &r) != nil || r != string(RoleAssistant) {
			return NewError(CodeBadResponse, "delta role is not assistant")
		}
		st.role = RoleAssistant
	}
	if raw, ok := delta["content"]; ok {
		parts, err := decodeContent(raw)
		if err != nil {
			return err
		}
		for _, p := range parts {
			st.AppendText(p)
			ev.TextDelta += p
		}
	}
	if raw, ok := delta["tool_calls"]; ok && !isNull(raw) {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return NewError(CodeBadResponse, "delta tool_calls is not an array")
		}
		for _, it := range items {
			if err := decodeToolFragment(it, st); err != nil {
				return err
			}
		}
	}
	for k, v := range delta {
		switch k {
		case "role", "content", "tool_calls":
		default:
			st.AddSidecar("message."+k, v)
		}
	}
	return nil
}

func decodeToolFragment(raw json.RawMessage, st *ChoiceState) error {
	item, err := parseObject(raw)
	if err != nil {
		return err
	}
	idxRaw, ok := item["index"]
	if !ok || isNull(idxRaw) {
		return NewError(CodeBadResponse, "tool call fragment has no index")
	}
	var idx int
	if json.Unmarshal(idxRaw, &idx) != nil {
		return NewError(CodeBadResponse, "tool call fragment index is not an integer")
	}
	id, err := optString(item, "id")
	if err != nil {
		return err
	}
	typ, err := optString(item, "type")
	if err != nil {
		return err
	}
	if typ != "" && typ != "function" {
		return NewError(CodeBadResponse, "unsupported tool call type")
	}
	var name, args string
	var fn map[string]json.RawMessage
	if fraw, ok := item["function"]; ok && !isNull(fraw) {
		if fn, err = parseObject(fraw); err != nil {
			return err
		}
		if name, err = optString(fn, "name"); err != nil {
			return err
		}
		if args, err = optString(fn, "arguments"); err != nil {
			return err
		}
	}
	if err := st.AppendToolCall(idx, id, name, args); err != nil {
		return err
	}
	prefix := fmt.Sprintf("message.tool_calls.%d.", idx)
	for k, v := range item {
		switch k {
		case "index", "id", "type", "function":
		default:
			st.AddSidecar(prefix+k, v)
		}
	}
	for k, v := range fn {
		switch k {
		case "name", "arguments":
		default:
			st.AddSidecar(prefix+"function."+k, v)
		}
	}
	return nil
}

// Final 在收到 [DONE] 之后返回完整回复，并做固定输出校验。
// 流没结束、或没有 finish_reason，返回 goagent.model.stream_incomplete。
func (s *StreamState) Final() (Response, error) {
	if s.err != nil {
		return Response{}, s.err
	}
	if !s.done {
		return Response{}, NewError(CodeStreamIncomplete, "stream ended before [DONE]")
	}
	c := s.choice
	if c == nil {
		return Response{}, NewError(CodeBadResponse, "stream carried no choice")
	}
	if !c.finished {
		return Response{}, NewError(CodeStreamIncomplete, "stream ended without finish_reason")
	}
	resp := Response{
		ID:              s.id,
		Model:           s.model,
		Message:         Message{Role: RoleAssistant},
		RawFinishReason: c.rawFin,
		FinishReason:    ParseFinishReason(c.rawFin),
		Usage:           s.usage,
	}
	if c.thinking.Len() > 0 {
		resp.Message.Blocks = append(resp.Message.Blocks, Block{Kind: BlockThinking, Text: c.thinking.String()})
	}
	if c.text.Len() > 0 {
		resp.Message.Blocks = append(resp.Message.Blocks, Block{Kind: BlockText, Text: c.text.String()})
	}
	idxs := make([]int, 0, len(c.calls))
	for i := range c.calls {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		f := c.calls[i]
		resp.Message.Blocks = append(resp.Message.Blocks, Block{
			Kind: BlockToolCall, ToolCallID: f.id, ToolName: f.name,
			Arguments: json.RawMessage(f.args.String()),
		})
	}
	for k, v := range s.side {
		resp.AddSidecar(k, v)
	}
	for k, v := range c.side {
		resp.AddSidecar(k, v)
	}
	for k, b := range c.sideStr {
		enc, _ := json.Marshal(b.String())
		resp.AddSidecar(k, enc)
	}
	if err := validateOutput(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// ---------------------------------------------------------------- 辅助

func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

// parseObject 解一个 JSON 对象。错误不回显内容。
func parseObject(raw []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return nil, NewError(CodeBadResponse, "invalid JSON at offset %d", se.Offset)
		}
		return nil, NewError(CodeBadResponse, "JSON value is not an object")
	}
	if m == nil {
		return nil, NewError(CodeBadResponse, "JSON value is not an object")
	}
	return m, nil
}

// optString 读可空字符串字段。缺失或 null 返回 ""。
func optString(m map[string]json.RawMessage, key string) (string, error) {
	raw, ok := m[key]
	if !ok || isNull(raw) {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", NewError(CodeBadResponse, "field %q is not a string", key)
	}
	return s, nil
}

// parseUsage 返回用量和 usage 里不认识的字段（带 "usage." 前缀）。null 返回 nil。
func parseUsage(raw json.RawMessage) (*Usage, map[string]json.RawMessage, error) {
	if isNull(raw) {
		return nil, nil, nil
	}
	m, err := parseObject(raw)
	if err != nil {
		return nil, nil, NewError(CodeBadResponse, "usage is not an object")
	}
	var u Usage
	var haveTotal bool
	side := map[string]json.RawMessage{}
	for k, v := range m {
		var dst *int
		switch k {
		case "prompt_tokens":
			dst = &u.PromptTokens
		case "completion_tokens":
			dst = &u.CompletionTokens
		case "total_tokens":
			dst, haveTotal = &u.TotalTokens, !isNull(v)
		default:
			if !isNull(v) {
				side["usage."+k] = v
			}
			continue
		}
		if isNull(v) {
			continue
		}
		if json.Unmarshal(v, dst) != nil || *dst < 0 {
			return nil, nil, NewError(CodeBadResponse, "usage.%s is not a non-negative integer", k)
		}
	}
	if !haveTotal {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return &u, side, nil
}
