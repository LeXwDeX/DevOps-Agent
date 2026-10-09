package protocol

import "encoding/json"

// ProtocolAdapter 是统一分派接口。分派器始终通过最外层派生对象调用它。
type ProtocolAdapter interface {
	// EncodePayload 校验请求并编码成线上请求体。
	EncodePayload(req Request) ([]byte, error)
	// NormalizeResponse 把完整的非流式回复体归一化为 Response。
	NormalizeResponse(body []byte) (Response, error)
	// DecodeEvent 解一个流事件（SSE 的 data 部分，如 `{...}` 或 `[DONE]`），累积进 state。
	DecodeEvent(state *StreamState, data []byte) ([]StreamEvent, error)
	Name() string
	Version() string
}

// ChatExtension 是 Chat Completions 基础流程回调的可重写扩展点（42 第 3.2、4 节）。
//
// ChatAdapterBase 提供全部默认实现。派生适配器嵌入 ChatAdapterBase，按需重写其中几个，
// 并在构造时把自己（最外层对象）传给 NewChatAdapterBase。基础流程经这个接口调用钩子，
// 所以重写会生效。重写时可以调用被嵌入的 ChatAdapterBase 的同名方法复用标准行为。
//
// 输入校验、大小限制、截断检查、工具调用完整性等固定校验在基础流程里，钩子改变不了它们。
type ChatExtension interface {
	// EncodeAssistant 把一条 assistant 消息写进 wire（已含 "role"）。req 是当前请求，
	// 钩子可据此决定是否回传思考内容。默认实现写 content 和 tool_calls，不写思考。
	EncodeAssistant(req Request, m Message, wire map[string]any) error

	// DecodeMessage 读 choices[0].message。msg 归钩子所有，钩子应删掉自己已理解的键，
	// 剩下的键由默认实现放进 out.Sidecar。默认实现填 out.Message 的角色和块。
	DecodeMessage(msg map[string]json.RawMessage, out *Response) error

	// DecodeDelta 读流式 choices[0].delta。delta 归钩子所有（同上）。钩子把增量累积进 st，
	// 并把要展示的增量写进 ev。
	DecodeDelta(delta map[string]json.RawMessage, st *ChoiceState, ev *StreamEvent) error
}
