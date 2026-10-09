package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/coretest"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	mreq "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

var ctx = context.Background()

type renderCall struct {
	step    string
	params  map[string]any
	outputs map[string]map[string]any
}

type fixture struct {
	t   *testing.T
	env *coretest.Env
	eng *Engine

	mu      sync.Mutex
	renders []renderCall
}

func str() task.FieldType { return task.FieldType{Kind: task.KindString} }
func num() task.FieldType { return task.FieldType{Kind: task.KindNumber} }

// diamondDef 是 A→B→C→D，再按 D.status 走 E→F→G 或 H→L→M。
func diamondDef(version int64) *task.Definition {
	step := func(id, next string) task.Step {
		return task.Step{ID: id, Unit: mreq.UnitRef{ID: "u" + strings.ToLower(id), Version: 1}, Next: next}
	}
	c := step("C", "")
	c.Branches = []task.Branch{{When: `params.svc != ""`, To: "D"}}
	d := step("D", "")
	d.Outputs = []task.FieldSpec{{Name: "status", Type: str()}, {Name: "exit_code", Type: num()}}
	d.Branches = []task.Branch{
		{When: `D.status == "ok" && D.exit_code == 0`, To: "E"},
		{When: `D.status == "degraded"`, To: "H"},
	}
	a := step("A", "B")
	a.Outputs = []task.FieldSpec{{Name: "n", Type: num()}}
	return &task.Definition{
		ID: "deploy", Version: version, Digest: ids.Digest(strings.Repeat("0", 63) + string(rune('0'+version))),
		Params: []task.FieldSpec{
			{Name: "svc", Type: str(), Required: true},
			{Name: "hosts", Type: task.FieldType{Kind: task.KindList, Elem: task.KindString}},
		},
		Entry: "A",
		Steps: []task.Step{a, step("B", "C"), c, d,
			step("E", "F"), step("F", "G"), step("G", ""),
			step("H", "L"), step("L", "M"), step("M", "")},
	}
}

func newFixture(t *testing.T, defs ...*task.Definition) *fixture {
	t.Helper()
	f := &fixture{t: t, env: coretest.NewEnv()}
	if len(defs) == 0 {
		defs = []*task.Definition{diamondDef(1)}
	}
	for i, d := range defs {
		f.env.Tasks.SeedDefinition(d, i == 0)
	}
	f.env.Registry.RenderFn = f.render
	eng, err := New(Deps{Tasks: f.env.Tasks, Requests: f.env.Requests, Registry: f.env.Registry, Secrets: f.env.Secrets, Clock: f.env.Clock, IDGen: f.env.IDs})
	if err != nil {
		t.Fatal(err)
	}
	f.eng = eng
	return f
}

func (f *fixture) render(_ context.Context, _ *task.Definition, step task.Step, params map[string]any, outputs map[string]map[string]any) (mreq.Content, mreq.Expectation, error) {
	f.mu.Lock()
	f.renders = append(f.renders, renderCall{step: step.ID, params: params, outputs: outputs})
	f.mu.Unlock()
	return mreq.Content{Rendered: "run " + step.ID, Params: map[string]any{"svc": params["svc"]}},
		mreq.Expectation{
			Intent: "step " + step.ID, Targets: []string{"host-a"}, Category: consts.CategoryReadOnly,
			ExpectedChange: mreq.ExpectedChange{Text: "none"}, ExpectedOutput: "ok",
			SuccessCriteria: []mreq.Criterion{{Expr: "exit_code == 0"}}, Reversible: consts.Reversible, Rollback: "n/a",
		}, nil
}

func (f *fixture) lastRender() renderCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renders[len(f.renders)-1]
}

func (f *fixture) renderCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.renders)
}

func input() StartInput {
	return StartInput{
		TaskID: "deploy", SessionID: "s1",
		Scope:  mreq.Scope{Project: "p", Environment: "prod", Targets: []string{"host-a"}},
		Params: map[string]any{"svc": "nginx"},
	}
}

func (f *fixture) start() (*task.Run, *mreq.ExecutionRequest) {
	f.t.Helper()
	run, req, err := f.eng.Start(ctx, input())
	if err != nil {
		f.t.Fatalf("Start: %v", err)
	}
	return run, req
}

// setState 模拟流水线：把申请直接改到某个状态并写回存储（版本加一），返回新的申请。
func (f *fixture) setState(req *mreq.ExecutionRequest, st consts.RequestState, mut func(*mreq.ExecutionRequest)) *mreq.ExecutionRequest {
	f.t.Helper()
	r, err := f.env.Requests.Get(ctx, req.CallID)
	if err != nil {
		f.t.Fatal(err)
	}
	r.State = st
	r.Version = r.Version.Next()
	if mut != nil {
		mut(r)
	}
	f.env.Requests.Seed(r)
	return r
}

// succeed 让步骤成功并带结构化输出，然后推进。
func (f *fixture) succeed(run *task.Run, req *mreq.ExecutionRequest, out map[string]any) (*task.Run, *mreq.ExecutionRequest) {
	f.t.Helper()
	r := f.setState(req, consts.RequestSucceeded, func(r *mreq.ExecutionRequest) {
		r.Execution = &mreq.Execution{Outcome: consts.RequestSucceeded, Result: &mreq.ExecutionResult{Output: out}}
	})
	run, next, err := f.eng.Advance(ctx, run.RunID, r)
	if err != nil {
		f.t.Fatalf("Advance(%s): %v", req.Task.StepID, err)
	}
	return run, next
}

// walk 依次让每个步骤成功，outs 按步骤 ID 给输出，返回走过的步骤 ID 和最终运行。
func (f *fixture) walk(run *task.Run, req *mreq.ExecutionRequest, outs map[string]map[string]any) (*task.Run, []string) {
	f.t.Helper()
	var path []string
	for req != nil {
		path = append(path, req.Task.StepID)
		run, req = f.succeed(run, req, outs[req.Task.StepID])
	}
	return run, path
}

func codeOf(err error) errcode.Code {
	c, _ := errcode.CodeOf(err)
	return c
}

func mustCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if err == nil || codeOf(err) != want {
		t.Fatalf("err = %v, want code %s", err, want)
	}
}

func types(evts []events.Event, stream string) []events.Type {
	var out []events.Type
	for _, e := range evts {
		if e.StreamID == stream {
			out = append(out, e.Type)
		}
	}
	return out
}

func sameTypes(got, want []events.Type) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func (f *fixture) storedRun(id string) *task.Run {
	f.t.Helper()
	r, err := f.env.Tasks.GetRun(ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) storedReq(id ids.CallID) *mreq.ExecutionRequest {
	f.t.Helper()
	r, err := f.env.Requests.Get(ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}
