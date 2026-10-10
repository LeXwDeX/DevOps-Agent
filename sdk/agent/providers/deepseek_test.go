package providers_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/sdk/agent/protocol"
	"github.com/LeXwDeX/DevOps-Agent/sdk/agent/providers"
)

// 语料与基础适配器共用。
const corpusDir = "../protocol/testdata"

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(corpusDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

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
	if !errors.Is(err, target) {
		t.Fatalf("want %s, got %v", target.Code, err)
	}
}

func text(s string) protocol.Block { return protocol.Block{Kind: protocol.BlockText, Text: s} }
func thinking(s string) protocol.Block {
	return protocol.Block{Kind: protocol.BlockThinking, Text: s}
}
func call(id, name, args string) protocol.Block {
	return protocol.Block{Kind: protocol.BlockToolCall, ToolCallID: id, ToolName: name, Arguments: json.RawMessage(args)}
}
func msg(r protocol.Role, bs ...protocol.Block) protocol.Message {
	return protocol.Message{Role: r, Blocks: bs}
}

// ---------------------------------------------------------------- 读回复

func TestDeepSeekReasoningBecomesThinkingBeforeText(t *testing.T) {
	a := providers.NewDeepSeekAdapter()
	resp, err := a.NormalizeResponse(readFixture(t, "resp_reasoning.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.Block{thinking("2+2 is 4."), text("The answer is 4.")}
	if !reflect.DeepEqual(resp.Message.Blocks, want) {
		t.Fatalf("%+v", resp.Message.Blocks)
	}
	if _, ok := resp.Sidecar["message.reasoning_content"]; ok || len(resp.Sidecar) != 0 {
		t.Fatalf("understood field must leave the sidecar: %v", resp.Sidecar)
	}
	if a.Name() != "deepseek" || a.Version() != "1" {
		t.Fatal("name/version")
	}
}

func TestDeepSeekReasoningWithToolCalls(t *testing.T) {
	resp, err := providers.NewDeepSeekAdapter().NormalizeResponse(readFixture(t, "resp_reasoning_tool_calls.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.Block{thinking("I should call the weather tool."), call("call_w", "get_weather", `{"city":"Paris"}`)}
	if !reflect.DeepEqual(resp.Message.Blocks, want) || resp.FinishReason != protocol.FinishToolCalls {
		t.Fatalf("%+v", resp)
	}
}

func TestDeepSeekTruncatedWhenOnlyReasoning(t *testing.T) {
	a := providers.NewDeepSeekAdapter()
	_, err := a.NormalizeResponse(readFixture(t, "resp_reasoning_length_empty.json"))
	wantCode(t, err, protocol.ErrOutputTruncated)
	// 只有思考、finish_reason 正常结束：不是截断，保留 thinking 块。
	body := `{"choices":[{"message":{"role":"assistant","content":null,"reasoning_content":"hmm"},"finish_reason":"stop"}]}`
	resp, err := a.NormalizeResponse([]byte(body))
	if err != nil || len(resp.Message.Blocks) != 1 || resp.Message.Blocks[0].Kind != protocol.BlockThinking {
		t.Fatalf("%v %+v", err, resp)
	}
}

func TestDeepSeekEmptyReasoningMakesNoBlock(t *testing.T) {
	resp, err := providers.NewDeepSeekAdapter().NormalizeResponse(readFixture(t, "resp_reasoning_empty_content_stop.json"))
	if err != nil || len(resp.Message.Blocks) != 0 || len(resp.Sidecar) != 0 {
		t.Fatalf("%v %+v", err, resp)
	}
}

func TestDeepSeekReasoningNotString(t *testing.T) {
	_, err := providers.NewDeepSeekAdapter().NormalizeResponse(readFixture(t, "resp_reasoning_not_string.json"))
	wantCode(t, err, protocol.ErrBadResponse)
}

func TestDeepSeekKeepsFixedValidation(t *testing.T) {
	a := providers.NewDeepSeekAdapter()
	for _, f := range []string{"resp_bad_tool_args.json", "resp_tool_call_no_id.json", "resp_dup_tool_ids.json", "resp_malformed.json", "resp_no_choices.json"} {
		_, err := a.NormalizeResponse(readFixture(t, f))
		wantCode(t, err, protocol.ErrBadResponse)
	}
	_, err := a.NormalizeResponse(readFixture(t, "resp_bad_tool_args_length.json"))
	wantCode(t, err, protocol.ErrOutputTruncated)
	_, err = a.NormalizeResponse(make([]byte, protocol.MaxResponseBytes+1))
	wantCode(t, err, protocol.ErrTooLarge)
}

func TestDeepSeekUnknownFieldsStillGoToSidecar(t *testing.T) {
	resp, err := providers.NewDeepSeekAdapter().NormalizeResponse(readFixture(t, "resp_unknown_fields.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.Sidecar["message.annotations"]; !ok || len(resp.Sidecar) != 6 {
		t.Fatalf("%v", resp.Sidecar)
	}
}

// ---------------------------------------------------------------- 流式

func TestDeepSeekStreamReasoning(t *testing.T) {
	a := providers.NewDeepSeekAdapter()
	evs, resp, err := runStream(t, a, "stream_reasoning.sse")
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.StreamEvent{
		{ThinkingDelta: "2+2 "}, {ThinkingDelta: "is 4."}, {TextDelta: "The answer "}, {TextDelta: "is 4."}, {Done: true},
	}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("%+v", evs)
	}
	wantBlocks := []protocol.Block{thinking("2+2 is 4."), text("The answer is 4.")}
	if !reflect.DeepEqual(resp.Message.Blocks, wantBlocks) {
		t.Fatalf("%+v", resp.Message.Blocks)
	}
	if len(resp.Sidecar) != 0 || resp.Usage == nil || resp.Usage.TotalTokens != 28 {
		t.Fatalf("%+v", resp)
	}
}

func TestDeepSeekStreamReasoningToolCalls(t *testing.T) {
	a := providers.NewDeepSeekAdapter()
	evs, resp, err := runStream(t, a, "stream_reasoning_tool_calls.sse")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(evs, []protocol.StreamEvent{{ThinkingDelta: "Need weather."}, {Done: true}}) {
		t.Fatalf("%+v", evs)
	}
	want := []protocol.Block{thinking("Need weather."), call("call_w", "get_weather", `{"city":"Paris"}`)}
	if !reflect.DeepEqual(resp.Message.Blocks, want) {
		t.Fatalf("%+v", resp.Message.Blocks)
	}
}

func TestDeepSeekStreamErrors(t *testing.T) {
	a := providers.NewDeepSeekAdapter()
	_, _, err := runStream(t, a, "stream_reasoning_length.sse")
	wantCode(t, err, protocol.ErrOutputTruncated)
	cases := map[string]*protocol.Error{
		"stream_cut_before_finish.sse":    protocol.ErrStreamIncomplete,
		"stream_finish_no_done.sse":       protocol.ErrStreamIncomplete,
		"stream_truncated_args.sse":       protocol.ErrBadResponse,
		"stream_malformed.sse":            protocol.ErrBadResponse,
		"stream_content_after_finish.sse": protocol.ErrBadResponse,
	}
	for f, want := range cases {
		_, _, err := runStream(t, a, f)
		wantCode(t, err, want)
	}
	st := protocol.NewStreamState()
	_, err = a.DecodeEvent(st, []byte(`{"choices":[{"delta":{"reasoning_content":7}}]}`))
	wantCode(t, err, protocol.ErrBadResponse)
}

// ---------------------------------------------------------------- 回传历史

func history() []protocol.Message {
	return []protocol.Message{
		msg(protocol.RoleUser, text("weather?")),
		msg(protocol.RoleAssistant, thinking("need tool"), call("c1", "get_weather", `{"city":"Paris"}`)),
		msg(protocol.RoleTool, protocol.Block{Kind: protocol.BlockToolResult, ToolCallID: "c1", Text: "sunny"}),
		msg(protocol.RoleAssistant, thinking("summarise"), text("It is sunny.")),
		msg(protocol.RoleUser, text("thanks")),
	}
}

func encodedMessages(t *testing.T, a protocol.ProtocolAdapter, req protocol.Request) []map[string]any {
	t.Helper()
	b, err := a.EncodePayload(req)
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	return w.Messages
}

func TestDeepSeekEncodeWithToolsPassesAllThinkingBack(t *testing.T) {
	a := providers.NewDeepSeekAdapter()
	req := protocol.Request{Model: "deepseek", Messages: history(), Tools: []protocol.Tool{{Name: "get_weather"}}}
	ms := encodedMessages(t, a, req)
	// 带工具调用的 assistant 和不带工具调用的 assistant，都回传 reasoning_content。
	if ms[1]["reasoning_content"] != "need tool" {
		t.Fatalf("%v", ms[1])
	}
	if ms[3]["reasoning_content"] != "summarise" || ms[3]["content"] != "It is sunny." {
		t.Fatalf("%v", ms[3])
	}
	for _, i := range []int{0, 2, 4} {
		if _, ok := ms[i]["reasoning_content"]; ok {
			t.Fatalf("message %d must not carry reasoning_content", i)
		}
	}
}

func TestDeepSeekEncodeWithoutToolsOmitsAllThinking(t *testing.T) {
	a := providers.NewDeepSeekAdapter()
	b, err := a.EncodePayload(protocol.Request{Model: "deepseek", Messages: history()[:1:1]})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "reasoning_content") {
		t.Fatal(string(b))
	}
	// 没有工具声明时，历史里的 tool_call/thinking 照常编码，但一律不回传 reasoning_content。
	req := protocol.Request{Model: "deepseek", Messages: history()}
	for i, m := range encodedMessages(t, a, req) {
		if _, ok := m["reasoning_content"]; ok {
			t.Fatalf("message %d carries reasoning_content without tools", i)
		}
	}
}

func TestDeepSeekEncodeAssistantWithoutThinkingAddsNothing(t *testing.T) {
	a := providers.NewDeepSeekAdapter()
	req := protocol.Request{Model: "deepseek", Tools: []protocol.Tool{{Name: "f"}}, Messages: []protocol.Message{
		msg(protocol.RoleUser, text("q")),
		msg(protocol.RoleAssistant, call("c", "f", `{}`)),
	}}
	ms := encodedMessages(t, a, req)
	if _, ok := ms[1]["reasoning_content"]; ok {
		t.Fatalf("%v", ms[1])
	}
}

func TestBaseNeverEmitsReasoningContent(t *testing.T) {
	base := protocol.NewChatAdapterBase(nil)
	req := protocol.Request{Model: "m", Messages: history(), Tools: []protocol.Tool{{Name: "get_weather"}}}
	b, err := base.EncodePayload(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "reasoning_content") || strings.Contains(string(b), "need tool") {
		t.Fatal(string(b))
	}
}

func TestDeepSeekRoundTrip(t *testing.T) {
	a := providers.NewDeepSeekAdapter()
	resp, err := a.NormalizeResponse(readFixture(t, "resp_reasoning_tool_calls.json"))
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.Request{Model: "deepseek", Tools: []protocol.Tool{{Name: "get_weather"}}, Messages: []protocol.Message{
		msg(protocol.RoleUser, text("weather in Paris?")),
		resp.Message,
		msg(protocol.RoleTool, protocol.Block{Kind: protocol.BlockToolResult, ToolCallID: "call_w", Text: "sunny"}),
	}}
	ms := encodedMessages(t, a, req)
	if ms[1]["reasoning_content"] != "I should call the weather tool." {
		t.Fatalf("%v", ms[1])
	}
	// 同一个请求经基础适配器编码：没有 reasoning_content。
	b, _ := protocol.NewChatAdapterBase(nil).EncodePayload(req)
	if strings.Contains(string(b), "reasoning_content") {
		t.Fatal("base leaked reasoning_content")
	}
	// 流式回复的 Final 也能原样回传。
	_, sresp, err := runStream(t, a, "stream_reasoning_tool_calls.sse")
	if err != nil {
		t.Fatal(err)
	}
	req.Messages[1] = sresp.Message
	if encodedMessages(t, a, req)[1]["reasoning_content"] != "Need weather." {
		t.Fatal("stream round trip")
	}
}

// ---------------------------------------------------------------- 派生准入（42 第 3.3 节）

func TestAdmissionEmbedsParentType(t *testing.T) {
	typ := reflect.TypeOf(providers.DeepSeekAdapter{})
	baseType := reflect.TypeOf(protocol.ChatAdapterBase{})
	found := false
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Anonymous && f.Type == baseType {
			found = true
		}
	}
	if !found {
		t.Fatal("DeepSeekAdapter must embed protocol.ChatAdapterBase as an anonymous field")
	}
	var _ protocol.ProtocolAdapter = providers.NewDeepSeekAdapter()
}

func TestAdmissionUnoverriddenBehaviourEqualsBase(t *testing.T) {
	base := protocol.NewChatAdapterBase(nil)
	ds := providers.NewDeepSeekAdapter()

	// 请求：没有 thinking、没有带 thinking 的历史。
	reqs := []protocol.Request{
		{Model: "m", Messages: []protocol.Message{msg(protocol.RoleSystem, text("s")), msg(protocol.RoleUser, text("hi"))}},
		{Model: "m", MaxTokens: 10, Stream: true, Messages: []protocol.Message{msg(protocol.RoleUser, text("hi"))}},
		{Model: "m", Tools: []protocol.Tool{{Name: "f", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}}, Messages: []protocol.Message{
			msg(protocol.RoleUser, text("hi")),
			msg(protocol.RoleAssistant, text("calling"), call("c", "f", `{"a":1}`)),
			msg(protocol.RoleTool, protocol.Block{Kind: protocol.BlockToolResult, ToolCallID: "c", Text: "r"}),
			msg(protocol.RoleAssistant, text("done")),
		}},
	}
	for i, r := range reqs {
		want, err1 := base.EncodePayload(r)
		got, err2 := ds.EncodePayload(r)
		if err1 != nil || err2 != nil || !bytes.Equal(want, got) {
			t.Fatalf("request %d differs:\nbase %s (%v)\nds   %s (%v)", i, want, err1, got, err2)
		}
	}
	// 非法请求：同样的错误码。
	_, e1 := base.EncodePayload(protocol.Request{})
	_, e2 := ds.EncodePayload(protocol.Request{})
	if e1 == nil || e2 == nil || e1.Error() != e2.Error() {
		t.Fatal(e1, e2)
	}

	// 回复：没有 reasoning_content 的语料，结果与错误码一致。
	for _, f := range []string{"resp_standard.json", "resp_tool_calls.json", "resp_unknown_fields.json", "resp_missing_usage_odd_finish.json",
		"resp_length_empty.json", "resp_length_with_text.json", "resp_bad_tool_args.json", "resp_bad_tool_args_length.json",
		"resp_tool_call_no_id.json", "resp_dup_tool_ids.json", "resp_malformed.json", "resp_no_choices.json", "resp_error_object.json"} {
		want, e1 := base.NormalizeResponse(readFixture(t, f))
		got, e2 := ds.NormalizeResponse(readFixture(t, f))
		if !reflect.DeepEqual(want, got) || fmtErr(e1) != fmtErr(e2) {
			t.Fatalf("%s differs:\nbase %+v %v\nds   %+v %v", f, want, e1, got, e2)
		}
	}
	for _, f := range []string{"stream_standard.sse", "stream_tool_calls.sse", "stream_cut_before_finish.sse", "stream_finish_no_done.sse",
		"stream_done_no_finish.sse", "stream_truncated_args.sse", "stream_tool_call_no_id.sse", "stream_malformed.sse", "stream_content_after_finish.sse"} {
		wEv, wResp, e1 := runStream(t, base, f)
		gEv, gResp, e2 := runStream(t, ds, f)
		if !reflect.DeepEqual(wEv, gEv) || !reflect.DeepEqual(wResp, gResp) || fmtErr(e1) != fmtErr(e2) {
			t.Fatalf("%s differs:\nbase %+v %+v %v\nds   %+v %+v %v", f, wEv, wResp, e1, gEv, gResp, e2)
		}
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestAdmissionOverridesAreDispatchedFromOutermost(t *testing.T) {
	ds := providers.NewDeepSeekAdapter()
	base := protocol.NewChatAdapterBase(nil)
	body := readFixture(t, "resp_reasoning.json")

	// 经提升的基础流程（NormalizeResponse 定义在 ChatAdapterBase 上）调用到 DeepSeek 的重写。
	got, err := ds.ChatAdapterBase.NormalizeResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Message.Blocks) != 2 || got.Message.Blocks[0].Kind != protocol.BlockThinking {
		t.Fatalf("override not dispatched: %+v", got.Message.Blocks)
	}
	// 以值方式使用（复制）也一样。
	cp := *ds
	if r, _ := cp.NormalizeResponse(body); len(r.Message.Blocks) != 2 {
		t.Fatal("copy lost dispatch")
	}
	// 裸基础适配器：推理进 Sidecar。
	bare, err := base.NormalizeResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.Message.Blocks) != 1 || bare.Sidecar["message.reasoning_content"] == nil {
		t.Fatalf("bare base: %+v", bare)
	}
	// 流式：同理。
	_, sr, err := runStream(t, ds, "stream_reasoning.sse")
	if err != nil || sr.Message.Blocks[0].Kind != protocol.BlockThinking {
		t.Fatalf("%v %+v", err, sr)
	}
	_, br, err := runStream(t, base, "stream_reasoning.sse")
	if err != nil || len(br.Message.Blocks) != 1 || string(br.Sidecar["message.reasoning_content"]) != `"2+2 is 4."` {
		t.Fatalf("%v %+v", err, br)
	}
	// 编码：经基础流程的 EncodePayload 调到重写。
	req := protocol.Request{Model: "m", Tools: []protocol.Tool{{Name: "f"}}, Messages: history()}
	b, err := ds.ChatAdapterBase.EncodePayload(req)
	if err != nil || !strings.Contains(string(b), `"reasoning_content":"need tool"`) {
		t.Fatalf("%v %s", err, b)
	}
}

// 没经构造函数接线的 DeepSeekAdapter 退化为基础行为，这是文档里写明的使用约束。
func TestUnwiredAdapterFallsBackToBase(t *testing.T) {
	var ds providers.DeepSeekAdapter
	r, err := ds.NormalizeResponse(readFixture(t, "resp_reasoning.json"))
	if err != nil || len(r.Message.Blocks) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
}

// ---------------------------------------------------------------- fuzz

func FuzzDecodeEvent(f *testing.F) {
	for _, n := range []string{"stream_standard.sse", "stream_tool_calls.sse", "stream_reasoning.sse", "stream_reasoning_tool_calls.sse",
		"stream_reasoning_length.sse", "stream_unknown_fields.sse"} {
		for _, d := range sseData(f, n) {
			f.Add(d)
		}
	}
	for _, s := range []string{``, `[DONE]`, `{"choices":[{"delta":{"reasoning_content":null}}]}`, `{"choices":[{"delta":{"reasoning_content":["x"]}}]}`, "\xff"} {
		f.Add([]byte(s))
	}
	a := providers.NewDeepSeekAdapter()
	f.Fuzz(func(t *testing.T, data []byte) {
		st := protocol.NewStreamState()
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
