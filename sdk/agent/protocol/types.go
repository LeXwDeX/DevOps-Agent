// Package protocol 定义 SDK 的公共模型类型、协议适配器接口和 Chat Completions 基础适配器。
//
// 本包只依赖标准库，不导入平台的任何包。
package protocol

import (
	"encoding/json"
	"strings"
)

// Role 是消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

func (r Role) valid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		return true
	}
	return false
}

// BlockKind 是内容块种类。
type BlockKind string

const (
	BlockText       BlockKind = "text"
	BlockThinking   BlockKind = "thinking"
	BlockToolCall   BlockKind = "tool_call"
	BlockToolResult BlockKind = "tool_result"
)

// Block 是有序内容块。
//
//	text:        Text
//	thinking:    Text
//	tool_call:   ToolCallID, ToolName, Arguments（完整的 JSON）
//	tool_result: ToolCallID, Text（结果内容）
type Block struct {
	Kind       BlockKind       `json:"kind"`
	Text       string          `json:"text,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
}

// Message 是一条消息。块的顺序有意义。
type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
}

// Tool 是工具声明。Parameters 是 JSON Schema（可空）。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Request 是协议无关的模型请求。MaxTokens 为 0 时由适配器用默认值。
type Request struct {
	Model     string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
	Stream    bool
}

// FinishReason 是归一化的结束原因。
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"
	FinishToolCalls     FinishReason = "tool_calls"
	FinishContentFilter FinishReason = "content_filter"
	FinishUnknown       FinishReason = "unknown"
)

// ParseFinishReason 把线上字符串映射到 FinishReason。不认识的值映射为 unknown，
// 原始值由 Response.RawFinishReason 保留。
func ParseFinishReason(raw string) FinishReason {
	switch FinishReason(raw) {
	case FinishStop, FinishLength, FinishToolCalls, FinishContentFilter:
		return FinishReason(raw)
	}
	return FinishUnknown
}

// Usage 是用量。厂商没返回时 Response.Usage 为 nil（未知不是 0，K-05）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response 是归一化后的完整回复。
//
// Sidecar 保存适配器没有理解的字段的原始 JSON（只读，不获得执行含义，42 第 5 节）。
// 键带位置前缀：
//
//	response.<k>   回复顶层字段
//	choice.<k>     choices[0] 的字段
//	message.<k>    message（流式为 delta）的字段，如 message.reasoning_content
//	message.tool_calls.<i>.<k>  第 i 个工具调用的字段
//	usage.<k>      usage 的字段
//
// JSON null 的未知字段不记录。
type Response struct {
	ID              string
	Model           string
	Message         Message
	FinishReason    FinishReason
	RawFinishReason string
	Usage           *Usage
	Sidecar         map[string]json.RawMessage
}

// AddSidecar 记录一个未理解的字段。派生适配器的钩子可用。
func (r *Response) AddSidecar(key string, raw json.RawMessage) {
	if r.Sidecar == nil {
		r.Sidecar = make(map[string]json.RawMessage)
	}
	r.Sidecar[key] = raw
}

// StreamEvent 是一块流数据解出的进度。它只表示部分内容，不能产生可执行工具。
type StreamEvent struct {
	TextDelta     string
	ThinkingDelta string
	Done          bool
}

// ChoiceState 累积单个 choice 的增量。工具调用片段只缓冲，Final 之前不会成为工具调用。
type ChoiceState struct {
	role     Role
	text     strings.Builder
	thinking strings.Builder
	rawFin   string
	finished bool

	calls map[int]*toolFragment

	side    map[string]json.RawMessage  // 非字符串的未知字段，后到覆盖先到
	sideStr map[string]*strings.Builder // 字符串的未知字段，按到达顺序拼接

	mutations int // 内容变化计数，用来发现结束之后还有内容
}

type toolFragment struct {
	id, name string
	args     strings.Builder
}

// AppendText 追加正文增量。
func (c *ChoiceState) AppendText(s string) {
	if s == "" {
		return
	}
	c.text.WriteString(s)
	c.mutations++
}

// AppendThinking 追加思考增量。基础适配器从不调用它；只有派生适配器的钩子会调用。
func (c *ChoiceState) AppendThinking(s string) {
	if s == "" {
		return
	}
	c.thinking.WriteString(s)
	c.mutations++
}

// AppendToolCall 缓冲一个工具调用片段。id、name 可以为空；args 是参数文本片段。
func (c *ChoiceState) AppendToolCall(index int, id, name, args string) error {
	if index < 0 || index >= 1<<20 {
		return NewError(CodeBadResponse, "tool call index out of range")
	}
	f := c.calls[index]
	if f == nil {
		if len(c.calls) >= maxToolCalls {
			return NewError(CodeBadResponse, "too many tool calls")
		}
		if c.calls == nil {
			c.calls = make(map[int]*toolFragment)
		}
		f = &toolFragment{}
		c.calls[index] = f
	}
	if id != "" {
		if f.id != "" && f.id != id {
			return NewError(CodeBadResponse, "conflicting tool call id at index %d", index)
		}
		f.id = id
	}
	if name != "" {
		if f.name != "" && f.name != name {
			return NewError(CodeBadResponse, "conflicting tool call name at index %d", index)
		}
		f.name = name
	}
	f.args.WriteString(args)
	c.mutations++
	return nil
}

// AddSidecar 记录一个未理解的 delta 字段。字符串值跨块拼接（reasoning 之类的文本增量
// 不会丢），其他类型后到覆盖先到。JSON null 不记录。
func (c *ChoiceState) AddSidecar(key string, raw json.RawMessage) {
	if isNull(raw) {
		return
	}
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		if _, isOther := c.side[key]; !isOther {
			b := c.sideStr[key]
			if b == nil {
				if c.sideStr == nil {
					c.sideStr = make(map[string]*strings.Builder)
				}
				b = &strings.Builder{}
				c.sideStr[key] = b
			}
			b.WriteString(s)
			return
		}
	}
	delete(c.sideStr, key)
	if c.side == nil {
		c.side = make(map[string]json.RawMessage)
	}
	c.side[key] = append(json.RawMessage(nil), raw...)
}

// StreamState 累积一次流式回复。零值可用。DecodeEvent 出错后状态作废，后续调用返回同一个错误。
type StreamState struct {
	id, model string
	usage     *Usage
	side      map[string]json.RawMessage
	choice    *ChoiceState
	bytes     int
	done      bool
	err       *Error
}

// NewStreamState 返回空状态。
func NewStreamState() *StreamState { return &StreamState{} }

// Done 报告是否已收到 [DONE]。
func (s *StreamState) Done() bool { return s.done }
