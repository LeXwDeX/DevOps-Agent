package events

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func TestAll(t *testing.T) {
	a := All()
	if len(a) != 44 {
		t.Fatalf("len = %d, want 44", len(a))
	}
	seen := map[Type]bool{}
	for _, v := range a {
		if !strings.HasPrefix(string(v), "devops.") || seen[v] || !v.Valid() {
			t.Errorf("bad or duplicate %q", v)
		}
		seen[v] = true
	}
	if Type("devops.nope").Valid() || Type("").Valid() {
		t.Error("unknown type valid")
	}
	a[0] = "tampered"
	if All()[0] == "tampered" {
		t.Error("All exposes internal slice")
	}
}

const eventsDoc = "../../../../docs/40-sdk/45-events-and-hooks.md"

func TestMatchesEventsDoc(t *testing.T) {
	raw, err := os.ReadFile(eventsDoc)
	if err != nil {
		t.Skipf("docs not available: %v", err)
	}
	doc := string(raw)
	start := strings.Index(doc, "## 6. devops.* 平台事件总表")
	end := strings.Index(doc, "## 7. ")
	if start < 0 || end < start {
		t.Fatal("section 6 not found")
	}
	re := regexp.MustCompile("^`(devops\\.[a-z_.]+)`$")
	got := map[string]bool{}
	for _, line := range strings.Split(doc[start:end], "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		if m := re.FindStringSubmatch(strings.TrimSpace(cells[1])); m != nil {
			got[m[1]] = true
		}
	}
	want := map[string]bool{}
	for _, v := range All() {
		want[string(v)] = true
	}
	g, w := keys(got), keys(want)
	if strings.Join(g, ",") != strings.Join(w, ",") {
		t.Errorf("doc = %v\ncode = %v", g, w)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestForRequest(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := &request.ExecutionRequest{
		CallID: "c1", Revision: 3, SessionID: "s1", State: consts.RequestChecking, EventSeq: 4,
		Task:    &request.TaskRef{RunID: "run1", StepID: "A"},
		Scope:   request.Scope{Project: "p", Environment: "prod", Targets: []string{"a", "b"}},
		Content: request.Content{Rendered: "rm -rf /secret-command"},
	}
	data := map[string]any{"digest": "abc", "list": []any{"x"}}
	e := ForRequest(r, CallRevised, data, now, "ev-1")
	if r.EventSeq != 5 || e.Sequence != 5 {
		t.Errorf("seq: r=%d e=%d", r.EventSeq, e.Sequence)
	}
	if e.StreamID != "call:c1" || e.Status != "checking" || e.CallID != "c1" || e.Revision != 3 ||
		e.RunID != "run1" || e.Project != "p" || e.Environment != "prod" || e.SessionID != "s1" ||
		e.SourceKind != "HostConfirmed" || e.SchemaVersion != core.EventV1 || e.EventID != "ev-1" ||
		e.Type != CallRevised || !e.OccurredAt.Equal(now) {
		t.Errorf("event = %+v", e)
	}
	if err := e.Validate(); err != nil {
		t.Error(err)
	}
	// 不夹带内容原文
	if strings.Contains(eventString(e), "secret-command") {
		t.Error("rendered content leaked")
	}
	// 与输入隔离
	data["list"].([]any)[0] = "y"
	r.Scope.Targets[0] = "z"
	if e.Data["list"].([]any)[0] != "x" || e.Targets[0] != "a" {
		t.Error("event aliases inputs")
	}
	e2 := ForRequest(r, CallFrozen, nil, now, "ev-2")
	if e2.Sequence != 6 {
		t.Errorf("second seq = %d", e2.Sequence)
	}
	// 无 Task 时 RunID 为空
	r.Task = nil
	if ForRequest(r, CallFrozen, nil, now, "ev-3").RunID != "" {
		t.Error("run id without task")
	}
}

func eventString(e Event) string {
	var b strings.Builder
	b.WriteString(string(e.Type) + e.StreamID + e.Status)
	for k, v := range e.Data {
		b.WriteString(k)
		if s, ok := v.(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

func TestForRun(t *testing.T) {
	run := &task.Run{RunID: "run1", SessionID: "s", State: consts.RunRunning, EventSeq: 0,
		Scope: request.Scope{Project: "p", Environment: "e", Targets: []string{"t"}}}
	e := ForRun(run, TaskStarted, map[string]any{"task": "t1"}, time.Unix(1, 0).UTC(), "ev-9")
	if run.EventSeq != 1 || e.Sequence != 1 || e.StreamID != "run:run1" || e.Status != "running" || e.RunID != "run1" {
		t.Errorf("event = %+v", e)
	}
	if err := e.Validate(); err != nil {
		t.Error(err)
	}
}

func TestValidateAndClone(t *testing.T) {
	good := Event{Type: CallProposed, EventID: "e", StreamID: "call:c", Sequence: 1}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]Event{
		"type":   {Type: "x", EventID: "e", StreamID: "s", Sequence: 1},
		"id":     {Type: CallProposed, StreamID: "s", Sequence: 1},
		"stream": {Type: CallProposed, EventID: "e", Sequence: 1},
		"seq":    {Type: CallProposed, EventID: "e", StreamID: "s"},
	} {
		if code, _ := errcode.CodeOf(e.Validate()); code != errcode.InvalidArgument {
			t.Errorf("%s not rejected", name)
		}
	}
	e := Event{Targets: []string{"a"}, Data: map[string]any{"k": []any{"v"}}}
	c := e.Clone()
	c.Targets[0] = "z"
	c.Data["k"].([]any)[0] = "z"
	if e.Targets[0] != "a" || e.Data["k"].([]any)[0] != "v" {
		t.Error("clone aliasing")
	}
}
