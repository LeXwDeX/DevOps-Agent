package protocol_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/sdk/agent/protocol"
)

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sseData 取出 SSE 文件里每个 data 行的内容。
func sseData(t testing.TB, name string) [][]byte {
	t.Helper()
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(readFixture(t, name)))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
			out = append(out, []byte(strings.TrimSpace(v)))
		}
	}
	return out
}

func runStream(t testing.TB, a protocol.ProtocolAdapter, name string) ([]protocol.StreamEvent, protocol.Response, error) {
	t.Helper()
	st := protocol.NewStreamState()
	var all []protocol.StreamEvent
	for _, d := range sseData(t, name) {
		evs, err := a.DecodeEvent(st, d)
		if err != nil {
			return all, protocol.Response{}, err
		}
		all = append(all, evs...)
	}
	resp, err := st.Final()
	return all, resp, err
}

func wantCode(t *testing.T, err error, target *protocol.Error) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error %s, got nil", target.Code)
	}
	if !errors.Is(err, target) {
		t.Fatalf("want error %s, got %v", target.Code, err)
	}
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error is not *protocol.Error: %T", err)
	}
}

func sidecarKeys(r protocol.Response) []string {
	var ks []string
	for k := range r.Sidecar {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func TestNormalizeStandard(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	resp, err := a.NormalizeResponse(readFixture(t, "resp_standard.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := protocol.Response{
		ID: "chatcmpl-1", Model: "gpt-x",
		Message:         protocol.Message{Role: protocol.RoleAssistant, Blocks: []protocol.Block{{Kind: protocol.BlockText, Text: "hello world"}}},
		FinishReason:    protocol.FinishStop,
		RawFinishReason: "stop",
		Usage:           &protocol.Usage{PromptTokens: 10, CompletionTokens: 3, TotalTokens: 13},
	}
	if !reflect.DeepEqual(resp, want) {
		t.Fatalf("got %+v\nwant %+v", resp, want)
	}
	if a.Name() != "chat-completions" || a.Version() != "1" {
		t.Fatal("name/version")
	}
}

func TestNormalizeToolCalls(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	resp, err := a.NormalizeResponse(readFixture(t, "resp_tool_calls.json"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != protocol.FinishToolCalls || len(resp.Message.Blocks) != 2 {
		t.Fatalf("%+v", resp)
	}
	b0, b1 := resp.Message.Blocks[0], resp.Message.Blocks[1]
	if b0.Kind != protocol.BlockToolCall || b0.ToolCallID != "call_a" || b0.ToolName != "get_weather" || string(b0.Arguments) != `{"city":"Paris"}` {
		t.Fatalf("%+v", b0)
	}
	if b1.ToolCallID != "call_b" || string(b1.Arguments) != "{}" {
		t.Fatalf("%+v", b1)
	}
}

func TestNormalizeUnknownFieldsGoToSidecar(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	resp, err := a.NormalizeResponse(readFixture(t, "resp_unknown_fields.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"choice.logprobs", "choice.vendor_note", "message.annotations",
		"response.service_tier", "response.system_fingerprint", "usage.completion_tokens_details",
	}
	if got := sidecarKeys(resp); !reflect.DeepEqual(got, want) {
		t.Fatalf("sidecar keys %v want %v", got, want)
	}
	if string(resp.Sidecar["message.annotations"]) != `[{"k":1}]` {
		t.Fatalf("raw not preserved: %s", resp.Sidecar["message.annotations"])
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 2 {
		t.Fatalf("usage %+v", resp.Usage)
	}
}

func TestNormalizeMissingUsageAndOddFinish(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	resp, err := a.NormalizeResponse(readFixture(t, "resp_missing_usage_odd_finish.json"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage != nil {
		t.Fatal("usage should be nil (unknown, not zero)")
	}
	if resp.FinishReason != protocol.FinishUnknown || resp.RawFinishReason != "function_call" {
		t.Fatalf("%q %q", resp.FinishReason, resp.RawFinishReason)
	}
}

func TestBaseNeverInterpretsReasoning(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	resp, err := a.NormalizeResponse(readFixture(t, "resp_reasoning.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.Blocks) != 1 || resp.Message.Blocks[0].Kind != protocol.BlockText {
		t.Fatalf("%+v", resp.Message.Blocks)
	}
	if string(resp.Sidecar["message.reasoning_content"]) != `"2+2 is 4."` {
		t.Fatalf("sidecar %v", resp.Sidecar)
	}
}

func TestNormalizeErrors(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	cases := []struct {
		file string
		want *protocol.Error
	}{
		{"resp_length_empty.json", protocol.ErrOutputTruncated},
		{"resp_reasoning_length_empty.json", protocol.ErrOutputTruncated}, // 基础适配器：推理在 Sidecar，不算正文
		{"resp_bad_tool_args.json", protocol.ErrBadResponse},
		{"resp_bad_tool_args_length.json", protocol.ErrOutputTruncated},
		{"resp_tool_call_no_id.json", protocol.ErrBadResponse},
		{"resp_dup_tool_ids.json", protocol.ErrBadResponse},
		{"resp_malformed.json", protocol.ErrBadResponse},
		{"resp_no_choices.json", protocol.ErrBadResponse},
		{"resp_error_object.json", protocol.ErrBadResponse},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			_, err := a.NormalizeResponse(readFixture(t, c.file))
			wantCode(t, err, c.want)
		})
	}
	for _, body := range []string{``, `null`, `[]`, `"x"`, `{} {}`, `{"choices":"x"}`, `{"choices":[{"message":{"role":"user","content":"x"}}]}`,
		`{"choices":[{"message":{"content":5}}]}`, `{"choices":[{"message":{"content":"x"},"index":1}]}`} {
		_, err := a.NormalizeResponse([]byte(body))
		wantCode(t, err, protocol.ErrBadResponse)
	}
}

func TestLengthWithTextIsNotTruncated(t *testing.T) {
	resp, err := protocol.NewChatAdapterBase(nil).NormalizeResponse(readFixture(t, "resp_length_with_text.json"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != protocol.FinishLength || len(resp.Message.Blocks) != 1 {
		t.Fatalf("%+v", resp)
	}
}

func TestEmptyStopIsNotTruncated(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`
	resp, err := protocol.NewChatAdapterBase(nil).NormalizeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.Blocks) != 0 {
		t.Fatal("empty content should not make a text block")
	}
}

func TestNormalizeTooLarge(t *testing.T) {
	big := make([]byte, protocol.MaxResponseBytes+1)
	_, err := protocol.NewChatAdapterBase(nil).NormalizeResponse(big)
	wantCode(t, err, protocol.ErrTooLarge)
}

func TestContentPartsArray(t *testing.T) {
	body := `{"choices":[{"message":{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},"finish_reason":"stop"}]}`
	resp, err := protocol.NewChatAdapterBase(nil).NormalizeResponse([]byte(body))
	if err != nil || len(resp.Message.Blocks) != 2 {
		t.Fatalf("%v %+v", err, resp)
	}
	body = `{"choices":[{"message":{"content":[{"type":"image_url"}]},"finish_reason":"stop"}]}`
	_, err = protocol.NewChatAdapterBase(nil).NormalizeResponse([]byte(body))
	wantCode(t, err, protocol.ErrBadResponse)
}

// ---------------------------------------------------------------- 流式

func TestStreamStandard(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	evs, resp, err := runStream(t, a, "stream_standard.sse")
	if err != nil {
		t.Fatal(err)
	}
	wantEv := []protocol.StreamEvent{{TextDelta: "Hel"}, {TextDelta: "lo"}, {Done: true}}
	if !reflect.DeepEqual(evs, wantEv) {
		t.Fatalf("events %+v", evs)
	}
	if resp.ID != "c1" || resp.Model != "gpt-x" || resp.FinishReason != protocol.FinishStop {
		t.Fatalf("%+v", resp)
	}
	if len(resp.Message.Blocks) != 1 || resp.Message.Blocks[0].Text != "Hello" {
		t.Fatalf("%+v", resp.Message.Blocks)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 6 {
		t.Fatalf("usage %+v", resp.Usage)
	}
}

func TestStreamToolCallFragments(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	st := protocol.NewStreamState()
	var final protocol.Response
	data := sseData(t, "stream_tool_calls.sse")
	for i, d := range data {
		evs, err := a.DecodeEvent(st, d)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if ev.TextDelta != "" || ev.ThinkingDelta != "" {
				t.Fatalf("chunk %d: tool fragments must not surface as deltas: %+v", i, ev)
			}
		}
		if i == len(data)-2 {
			// 终态之前不能拿到工具调用。
			if _, err := st.Final(); !errors.Is(err, protocol.ErrStreamIncomplete) {
				t.Fatalf("Final before [DONE]: %v", err)
			}
		}
	}
	final, err := st.Final()
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Message.Blocks) != 2 {
		t.Fatalf("%+v", final.Message.Blocks)
	}
	b0 := final.Message.Blocks[0]
	if b0.ToolCallID != "call_a" || b0.ToolName != "get_weather" || string(b0.Arguments) != `{"city":"Paris"}` {
		t.Fatalf("%+v", b0)
	}
	if final.Message.Blocks[1].ToolCallID != "call_b" || final.FinishReason != protocol.FinishToolCalls {
		t.Fatalf("%+v", final)
	}
}

func TestStreamErrors(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	cases := []struct {
		file string
		want *protocol.Error
	}{
		{"stream_cut_before_finish.sse", protocol.ErrStreamIncomplete},
		{"stream_finish_no_done.sse", protocol.ErrStreamIncomplete},
		{"stream_done_no_finish.sse", protocol.ErrStreamIncomplete},
		{"stream_truncated_args.sse", protocol.ErrBadResponse},
		{"stream_tool_call_no_id.sse", protocol.ErrBadResponse},
		{"stream_malformed.sse", protocol.ErrBadResponse},
		{"stream_content_after_finish.sse", protocol.ErrBadResponse},
		{"stream_reasoning_length.sse", protocol.ErrOutputTruncated}, // 基础适配器：没有正文
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			_, _, err := runStream(t, a, c.file)
			wantCode(t, err, c.want)
		})
	}
}

func TestStreamStateIsPoisonedAfterError(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	st := protocol.NewStreamState()
	_, err := a.DecodeEvent(st, []byte(`{bad`))
	wantCode(t, err, protocol.ErrBadResponse)
	_, err = a.DecodeEvent(st, []byte(`[DONE]`))
	wantCode(t, err, protocol.ErrBadResponse)
	_, err = st.Final()
	wantCode(t, err, protocol.ErrBadResponse)
	_, err = a.DecodeEvent(nil, []byte(`[DONE]`))
	wantCode(t, err, protocol.ErrBadRequest)
}

func TestStreamDataAfterDoneAndKeepalive(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	st := protocol.NewStreamState()
	if evs, err := a.DecodeEvent(st, []byte("  ")); err != nil || len(evs) != 0 {
		t.Fatal(evs, err)
	}
	if _, err := a.DecodeEvent(st, []byte(`[DONE]`)); err != nil {
		t.Fatal(err)
	}
	_, err := a.DecodeEvent(st, []byte(`{"choices":[]}`))
	wantCode(t, err, protocol.ErrBadResponse)
}

func TestStreamErrorChunkAndConflicts(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	for _, chunk := range []string{
		`{"error":{"message":"x"}}`,
		`{"choices":[{"index":1,"delta":{"content":"x"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"a"}]}}]}`, // 缺 index
		`{"choices":[{"delta":{"tool_calls":[{"index":-1,"id":"a"}]}}]}`,
		`{"choices":[{"delta":{"role":"user"}}]}`,
		`{"choices":[{"delta":{"content":5}}]}`,
		`[1]`,
	} {
		_, err := a.DecodeEvent(protocol.NewStreamState(), []byte(chunk))
		wantCode(t, err, protocol.ErrBadResponse)
	}
	st := protocol.NewStreamState()
	if _, err := a.DecodeEvent(st, []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"f"}}]}}]}`)); err != nil {
		t.Fatal(err)
	}
	_, err := a.DecodeEvent(st, []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"b"}]}}]}`))
	wantCode(t, err, protocol.ErrBadResponse)
}

func TestStreamTooLarge(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	_, err := a.DecodeEvent(protocol.NewStreamState(), make([]byte, protocol.MaxResponseBytes+1))
	wantCode(t, err, protocol.ErrTooLarge)

	st := protocol.NewStreamState()
	chunk := []byte(`{"choices":[{"delta":{"content":"` + strings.Repeat("x", 4<<20) + `"}}]}`)
	var last error
	for i := 0; i < 5 && last == nil; i++ {
		_, last = a.DecodeEvent(st, chunk)
	}
	wantCode(t, last, protocol.ErrTooLarge)
}

func TestStreamUnknownFieldsGoToSidecar(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	_, resp, err := runStream(t, a, "stream_unknown_fields.sse")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.Blocks) != 1 || resp.Message.Blocks[0].Text != "ab" {
		t.Fatalf("%+v", resp.Message.Blocks)
	}
	if string(resp.Sidecar["message.reasoning_content"]) != `"r1r2"` {
		t.Fatalf("sidecar %v", resp.Sidecar)
	}
	if string(resp.Sidecar["response.system_fingerprint"]) != `"fp_1"` {
		t.Fatalf("sidecar %v", resp.Sidecar)
	}
	if _, ok := resp.Sidecar["message.refusal"]; ok {
		t.Fatal("null fields are not recorded")
	}
}

// ---------------------------------------------------------------- 编码

func userMsg(s string) protocol.Message {
	return protocol.Message{Role: protocol.RoleUser, Blocks: []protocol.Block{{Kind: protocol.BlockText, Text: s}}}
}

func TestEncodeGolden(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	req := protocol.Request{
		Model: "m",
		Messages: []protocol.Message{
			{Role: protocol.RoleSystem, Blocks: []protocol.Block{{Kind: protocol.BlockText, Text: "be brief & kind"}}},
			userMsg("hi"),
			{Role: protocol.RoleAssistant, Blocks: []protocol.Block{
				{Kind: protocol.BlockThinking, Text: "secret thoughts"},
				{Kind: protocol.BlockToolCall, ToolCallID: "c1", ToolName: "f", Arguments: json.RawMessage(`{"a":1}`)},
			}},
			{Role: protocol.RoleTool, Blocks: []protocol.Block{
				{Kind: protocol.BlockToolResult, ToolCallID: "c1", Text: "ok"},
			}},
		},
		Tools: []protocol.Tool{{Name: "f", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	got, err := a.EncodePayload(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"max_tokens":4096,"messages":[` +
		`{"content":"be brief & kind","role":"system"},` +
		`{"content":"hi","role":"user"},` +
		`{"content":null,"role":"assistant","tool_calls":[{"function":{"arguments":"{\"a\":1}","name":"f"},"id":"c1","type":"function"}]},` +
		`{"content":"ok","role":"tool","tool_call_id":"c1"}],` +
		`"model":"m","tools":[{"function":{"description":"d","name":"f","parameters":{"type":"object"}},"type":"function"}]}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if strings.Contains(string(got), "reasoning_content") || strings.Contains(string(got), "secret") {
		t.Fatal("base must never emit reasoning_content")
	}
}

func TestEncodeMaxTokensAndStream(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	b, err := a.EncodePayload(protocol.Request{Model: "m", Messages: []protocol.Message{userMsg("x")}, MaxTokens: 77, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["max_tokens"] != float64(77) || m["stream"] != true {
		t.Fatalf("%s", b)
	}
}

func TestEncodeValidation(t *testing.T) {
	a := protocol.NewChatAdapterBase(nil)
	call := func(id, name, args string) protocol.Message {
		return protocol.Message{Role: protocol.RoleAssistant, Blocks: []protocol.Block{
			{Kind: protocol.BlockToolCall, ToolCallID: id, ToolName: name, Arguments: json.RawMessage(args)}}}
	}
	cases := map[string]protocol.Request{
		"no model":        {Messages: []protocol.Message{userMsg("x")}},
		"no messages":     {Model: "m"},
		"neg max":         {Model: "m", Messages: []protocol.Message{userMsg("x")}, MaxTokens: -1},
		"bad role":        {Model: "m", Messages: []protocol.Message{{Role: "boss", Blocks: []protocol.Block{{Kind: protocol.BlockText, Text: "x"}}}}},
		"no blocks":       {Model: "m", Messages: []protocol.Message{{Role: protocol.RoleUser}}},
		"call no id":      {Model: "m", Messages: []protocol.Message{call("", "f", "{}")}},
		"call no name":    {Model: "m", Messages: []protocol.Message{call("c", "", "{}")}},
		"call bad args":   {Model: "m", Messages: []protocol.Message{call("c", "f", `{"a":`)}},
		"call empty args": {Model: "m", Messages: []protocol.Message{call("c", "f", ``)}},
		"result no id": {Model: "m", Messages: []protocol.Message{{Role: protocol.RoleTool, Blocks: []protocol.Block{
			{Kind: protocol.BlockToolResult, Text: "x"}}}}},
		"result in user": {Model: "m", Messages: []protocol.Message{{Role: protocol.RoleUser, Blocks: []protocol.Block{
			{Kind: protocol.BlockToolResult, ToolCallID: "c"}}}}},
		"text in tool": {Model: "m", Messages: []protocol.Message{{Role: protocol.RoleTool, Blocks: []protocol.Block{
			{Kind: protocol.BlockText, Text: "x"}}}}},
		"thinking in user": {Model: "m", Messages: []protocol.Message{{Role: protocol.RoleUser, Blocks: []protocol.Block{
			{Kind: protocol.BlockThinking, Text: "x"}}}}},
		"thinking only assistant": {Model: "m", Messages: []protocol.Message{{Role: protocol.RoleAssistant, Blocks: []protocol.Block{
			{Kind: protocol.BlockThinking, Text: "x"}}}}},
		"unknown kind": {Model: "m", Messages: []protocol.Message{{Role: protocol.RoleUser, Blocks: []protocol.Block{{Kind: "image"}}}}},
		"tool no name": {Model: "m", Messages: []protocol.Message{userMsg("x")}, Tools: []protocol.Tool{{}}},
		"tool dup":     {Model: "m", Messages: []protocol.Message{userMsg("x")}, Tools: []protocol.Tool{{Name: "a"}, {Name: "a"}}},
		"tool bad params": {Model: "m", Messages: []protocol.Message{userMsg("x")},
			Tools: []protocol.Tool{{Name: "a", Parameters: json.RawMessage(`{`)}}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := a.EncodePayload(req)
			wantCode(t, err, protocol.ErrBadRequest)
		})
	}
}

// ---------------------------------------------------------------- 扩展接口分派

// markerExt 经 NewChatAdapterBase 接入，验证基础流程经扩展接口分派。
type markerExt struct {
	protocol.ChatAdapterBase
	decodeMsgCalls, decodeDeltaCalls, encodeCalls int
}

func (m *markerExt) DecodeMessage(msg map[string]json.RawMessage, out *protocol.Response) error {
	m.decodeMsgCalls++
	return m.ChatAdapterBase.DecodeMessage(msg, out)
}

func (m *markerExt) DecodeDelta(d map[string]json.RawMessage, st *protocol.ChoiceState, ev *protocol.StreamEvent) error {
	m.decodeDeltaCalls++
	return m.ChatAdapterBase.DecodeDelta(d, st, ev)
}

func (m *markerExt) EncodeAssistant(req protocol.Request, msg protocol.Message, wire map[string]any) error {
	m.encodeCalls++
	return m.ChatAdapterBase.EncodeAssistant(req, msg, wire)
}

func TestExtensionIsDispatchedFromOutermost(t *testing.T) {
	m := &markerExt{}
	m.ChatAdapterBase = protocol.NewChatAdapterBase(m)
	if _, err := m.NormalizeResponse(readFixture(t, "resp_standard.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runStream(t, m, "stream_standard.sse"); err != nil {
		t.Fatal(err)
	}
	asst := protocol.Message{Role: protocol.RoleAssistant, Blocks: []protocol.Block{{Kind: protocol.BlockText, Text: "x"}}}
	if _, err := m.EncodePayload(protocol.Request{Model: "m", Messages: []protocol.Message{userMsg("q"), asst}}); err != nil {
		t.Fatal(err)
	}
	if m.decodeMsgCalls != 1 || m.decodeDeltaCalls == 0 || m.encodeCalls != 1 {
		t.Fatalf("hooks not dispatched: %+v", m)
	}
	// 同样的基础适配器，ext 为 nil 时用自己，不调用外层对象。
	n := &markerExt{}
	n.ChatAdapterBase = protocol.NewChatAdapterBase(nil)
	if _, err := n.NormalizeResponse(readFixture(t, "resp_standard.json")); err != nil {
		t.Fatal(err)
	}
	if n.decodeMsgCalls != 0 {
		t.Fatal("nil ext must not dispatch to the outer object")
	}
}

type failingExt struct{ protocol.ChatAdapterBase }

func (failingExt) DecodeMessage(map[string]json.RawMessage, *protocol.Response) error {
	return errors.New("secret detail")
}
func (failingExt) EncodeAssistant(protocol.Request, protocol.Message, map[string]any) error {
	return errors.New("secret detail")
}

func TestHookErrorsBecomeSDKErrors(t *testing.T) {
	f := &failingExt{}
	f.ChatAdapterBase = protocol.NewChatAdapterBase(f)
	_, err := f.NormalizeResponse(readFixture(t, "resp_standard.json"))
	wantCode(t, err, protocol.ErrBadResponse)
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("foreign error text leaked")
	}
	asst := protocol.Message{Role: protocol.RoleAssistant, Blocks: []protocol.Block{{Kind: protocol.BlockText, Text: "x"}}}
	_, err = f.EncodePayload(protocol.Request{Model: "m", Messages: []protocol.Message{asst}})
	wantCode(t, err, protocol.ErrBadRequest)
}

func TestErrorIs(t *testing.T) {
	e := protocol.NewError(protocol.CodeBadResponse, "x %d", 1)
	if !errors.Is(e, protocol.ErrBadResponse) || errors.Is(e, protocol.ErrTooLarge) {
		t.Fatal("Is by code")
	}
	if e.Error() != "goagent.model.bad_response: x 1" {
		t.Fatal(e.Error())
	}
}

// ---------------------------------------------------------------- fuzz

func FuzzDecodeEvent(f *testing.F) {
	for _, n := range []string{"stream_standard.sse", "stream_tool_calls.sse", "stream_reasoning.sse",
		"stream_unknown_fields.sse", "stream_truncated_args.sse", "stream_content_after_finish.sse"} {
		for _, d := range sseData(f, n) {
			f.Add(d)
		}
	}
	for _, s := range []string{``, `[DONE]`, `null`, `{}`, `{"choices":null}`, `{"choices":[{}]}`, `{"choices":[{"delta":null}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":9999999999999}]}}]}`, `{"usage":{"prompt_tokens":-1}}`, "\xff\xfe"} {
		f.Add([]byte(s))
	}
	a := protocol.NewChatAdapterBase(nil)
	f.Fuzz(func(t *testing.T, data []byte) {
		st := protocol.NewStreamState()
		// 同一块喂两次，再喂 [DONE]，覆盖累积路径。
		for _, d := range [][]byte{data, data, []byte("[DONE]")} {
			if _, err := a.DecodeEvent(st, d); err != nil {
				var pe *protocol.Error
				if !errors.As(err, &pe) {
					t.Fatalf("non-SDK error %T: %v", err, err)
				}
			}
		}
		if _, err := st.Final(); err != nil {
			var pe *protocol.Error
			if !errors.As(err, &pe) {
				t.Fatalf("non-SDK error from Final %T: %v", err, err)
			}
		}
	})
}

func FuzzNormalizeResponse(f *testing.F) {
	for _, n := range []string{"resp_standard.json", "resp_tool_calls.json", "resp_unknown_fields.json", "resp_reasoning.json", "resp_malformed.json"} {
		f.Add(readFixture(f, n))
	}
	a := protocol.NewChatAdapterBase(nil)
	f.Fuzz(func(t *testing.T, body []byte) {
		if _, err := a.NormalizeResponse(body); err != nil {
			var pe *protocol.Error
			if !errors.As(err, &pe) {
				t.Fatalf("non-SDK error %T: %v", err, err)
			}
		}
	})
}
