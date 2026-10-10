// Package providers 放厂商派生适配器。每个厂商一个文件，必须嵌入对应的基础适配器类型。
package providers

import (
	"encoding/json"
	"strings"

	"github.com/LeXwDeX/DevOps-Agent/sdk/agent/protocol"
)

const reasoningField = "reasoning_content"

// DeepSeekAdapter 是 DeepSeek 的 Chat Completions 派生适配器。
//
// 它强制嵌入 protocol.ChatAdapterBase，只重写三个扩展点（读回复、读流增量、回传历史）。
// 必须用 NewDeepSeekAdapter 构造：构造时把最外层对象传给基础适配器，重写才会被基础流程分派到。
// 直接写 DeepSeekAdapter{} 得到的是未接线的对象，行为等同基础适配器。
type DeepSeekAdapter struct {
	protocol.ChatAdapterBase
}

var (
	_ protocol.ProtocolAdapter = DeepSeekAdapter{}
	_ protocol.ChatExtension   = DeepSeekAdapter{}
)

// NewDeepSeekAdapter 返回接好线的适配器（扩展接口指向 DeepSeekAdapter 自己）。
func NewDeepSeekAdapter() *DeepSeekAdapter {
	a := &DeepSeekAdapter{}
	a.ChatAdapterBase = protocol.NewChatAdapterBase(a)
	return a
}

func (DeepSeekAdapter) Name() string    { return "deepseek" }
func (DeepSeekAdapter) Version() string { return "1" }

// DecodeMessage：message.reasoning_content 变成 thinking 块，放在文本块之前，
// 并且不进 Sidecar（已被理解）。
func (a DeepSeekAdapter) DecodeMessage(msg map[string]json.RawMessage, out *protocol.Response) error {
	raw, present := msg[reasoningField]
	delete(msg, reasoningField)
	thinking, err := reasoningText(present, raw)
	if err != nil {
		return err
	}
	if err := a.ChatAdapterBase.DecodeMessage(msg, out); err != nil {
		return err
	}
	if thinking != "" {
		blocks := make([]protocol.Block, 0, len(out.Message.Blocks)+1)
		blocks = append(blocks, protocol.Block{Kind: protocol.BlockThinking, Text: thinking})
		out.Message.Blocks = append(blocks, out.Message.Blocks...)
	}
	return nil
}

// DecodeDelta：delta.reasoning_content 变成 thinking 增量，累积进 Final 的 thinking 块。
func (a DeepSeekAdapter) DecodeDelta(delta map[string]json.RawMessage, st *protocol.ChoiceState, ev *protocol.StreamEvent) error {
	raw, present := delta[reasoningField]
	delete(delta, reasoningField)
	thinking, err := reasoningText(present, raw)
	if err != nil {
		return err
	}
	if thinking != "" {
		st.AppendThinking(thinking)
		ev.ThinkingDelta += thinking
	}
	return a.ChatAdapterBase.DecodeDelta(delta, st, ev)
}

// EncodeAssistant：请求带工具（req.Tools 非空）时，历史里每条带 thinking 块的 assistant
// 消息都回传 reasoning_content（thinking 文本拼接），否则 API 返回 400。
// 请求不带工具时历史里一律不回传。
func (a DeepSeekAdapter) EncodeAssistant(req protocol.Request, m protocol.Message, wire map[string]any) error {
	if err := a.ChatAdapterBase.EncodeAssistant(req, m, wire); err != nil {
		return err
	}
	if len(req.Tools) == 0 {
		return nil
	}
	var sb strings.Builder
	for _, blk := range m.Blocks {
		if blk.Kind == protocol.BlockThinking {
			sb.WriteString(blk.Text)
		}
	}
	if sb.Len() > 0 {
		wire[reasoningField] = sb.String()
	}
	return nil
}

func reasoningText(present bool, raw json.RawMessage) (string, error) {
	if !present || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", protocol.NewError(protocol.CodeBadResponse, "%s is not a string", reasoningField)
	}
	return s, nil
}
