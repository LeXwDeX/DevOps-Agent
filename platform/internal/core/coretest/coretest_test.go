package coretest

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func TestRequestStoreContract(t *testing.T) {
	RequestStoreContract(t, func() ports.RequestStore { return NewMemRequestStore() })
}

func TestRevisionLogContract(t *testing.T) {
	RevisionLogContract(t, func() ports.RevisionLog { return NewMemRevisionLog() })
}

func TestTaskStoreContract(t *testing.T) {
	TaskStoreContract(t, func() ports.TaskStore { return NewMemTaskStore() })
}

func TestSecretKeysContract(t *testing.T) {
	SecretKeysContract(t, func() ports.SecretKeys { return NewFakeSecretKeys() })
}

func TestEventSinkContract(t *testing.T) {
	EventSinkContract(t, func() ports.EventSink { return NewMemEventSink() })
}

func TestIDGenContract(t *testing.T) {
	IDGenContract(t, func() ports.IDGen { return NewSeqIDGen() })
}

func TestFaultInjection(t *testing.T) {
	s := NewMemRequestStore()
	if err := s.Create(bg, SampleRequest("c1"), nil); err != nil {
		t.Fatal(err)
	}

	// FailWith：一直失败，状态不变
	s.FailWith("Get", context.DeadlineExceeded)
	for i := 0; i < 3; i++ {
		if _, err := s.Get(bg, "c1"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
	}
	s.ClearFaults()
	if _, err := s.Get(bg, "c1"); err != nil {
		t.Fatal(err)
	}
	if s.CallCount("Get") != 4 {
		t.Fatalf("calls = %d", s.CallCount("Get"))
	}

	// FailNext：只失败 n 次；失败的 Save 什么都不写，事件也不记
	s.FailNext("Save", 2, ErrInjected)
	next := SampleRequest("c1")
	next.Version = 2
	for i := 0; i < 2; i++ {
		if err := s.Save(bg, next, 1, []events.Event{SampleEvent("call:c1", 1, events.CallRevised)}); !errors.Is(err, ErrInjected) {
			t.Fatalf("err = %v", err)
		}
	}
	if got, _ := s.Get(bg, "c1"); got.Version != 1 || len(s.Events()) != 0 {
		t.Fatal("failed Save left data behind")
	}
	if err := s.Save(bg, next, 1, nil); err != nil {
		t.Fatalf("third Save: %v", err)
	}

	// 其他方法不受影响
	s.FailWith("Create", ErrInjected)
	if _, err := s.Get(bg, "c1"); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorScriptAndCalls(t *testing.T) {
	e := NewFakeExecutor()
	if e.Calls() != 0 {
		t.Fatal("calls not zero")
	}
	one := 1
	e.Push(request.ExecutionResult{ExitCode: &one, Err: "boom"}, nil)
	e.Push(request.ExecutionResult{}, ErrInjected)
	e.PushOutput(map[string]any{"rows": 3})

	r := SampleRequest("c1")
	res, err := e.Execute(bg, r)
	if err != nil || *res.ExitCode != 1 || res.Err != "boom" || res.StartedAt.IsZero() {
		t.Fatalf("first: %+v %v", res, err)
	}
	if _, err := e.Execute(bg, r); !errors.Is(err, ErrInjected) {
		t.Fatalf("second err = %v", err)
	}
	res, err = e.Execute(bg, r)
	if err != nil || *res.ExitCode != 0 || res.Output["rows"] != 3 {
		t.Fatalf("third: %+v %v", res, err)
	}
	// 队列取完后用默认值
	res, _ = e.Execute(bg, r)
	if *res.ExitCode != 0 || len(res.Output) != 0 {
		t.Fatalf("default: %+v", res)
	}
	if e.Calls() != 4 || len(e.Requests()) != 4 {
		t.Fatalf("calls = %d", e.Calls())
	}
	// 记录的申请是副本
	r.Content.Rendered = "changed"
	if e.Requests()[0].Content.Rendered == "changed" {
		t.Fatal("executor recorded alias")
	}
	// 注入故障也计数
	e.FailWith("Execute", ErrInjected)
	_, _ = e.Execute(bg, r)
	if e.Calls() != 5 {
		t.Fatalf("calls = %d", e.Calls())
	}
}

func TestFingerprintScript(t *testing.T) {
	f := NewFakeFingerprint()
	f.PushValue("fp-A")
	f.PushUnavailable()
	f.Push(request.Fingerprint{}, ErrInjected)
	r := SampleRequest("c1")

	fp, err := f.Collect(bg, r)
	if err != nil || fp.Value != "fp-A" || fp.Status != consts.FingerprintCollected || fp.CollectedAt.IsZero() {
		t.Fatalf("1: %+v %v", fp, err)
	}
	fp, err = f.Collect(bg, r)
	if err != nil || fp.Status != consts.FingerprintUnavailable {
		t.Fatalf("2: %+v %v", fp, err)
	}
	if _, err = f.Collect(bg, r); !errors.Is(err, ErrInjected) {
		t.Fatalf("3: %v", err)
	}
	fp, _ = f.Collect(bg, r)
	if fp.Value != DefaultFingerprintValue {
		t.Fatalf("default: %+v", fp)
	}
	if f.Calls() != 4 {
		t.Fatalf("calls = %d", f.Calls())
	}
}

func TestPreviewDCGScript(t *testing.T) {
	p := NewFakePreview()
	p.Push(request.PreviewResult{Status: consts.PreviewFailed, Output: "bad"}, nil)
	r := SampleRequest("c1")
	if res, _ := p.Preview(bg, r); res.Status != consts.PreviewFailed || res.At.IsZero() {
		t.Fatalf("%+v", res)
	}
	if res, _ := p.Preview(bg, r); res.Status != consts.PreviewPassed {
		t.Fatalf("default %+v", res)
	}
	if p.Calls() != 2 {
		t.Fatal("preview calls")
	}

	d := NewFakeDCG()
	d.PushResult(consts.DCGHit, "rule-1")
	d.Push(request.DCGCheck{}, ErrInjected)
	if res, _ := d.Check(bg, r); res.Result != consts.DCGHit || !reflect.DeepEqual(res.RuleIDs, []string{"rule-1"}) {
		t.Fatalf("%+v", res)
	}
	if _, err := d.Check(bg, r); !errors.Is(err, ErrInjected) {
		t.Fatalf("err = %v", err)
	}
	if res, _ := d.Check(bg, r); res.Result != consts.DCGPass {
		t.Fatalf("default %+v", res)
	}
	if d.Calls() != 3 {
		t.Fatal("dcg calls")
	}
}

func TestRegistryAndScope(t *testing.T) {
	reg := NewFakeRegistry()
	reg.AddUnit(ports.UnitInfo{Ref: request.UnitRef{ID: "u", Version: 1}, Subject: "ssh", Action: "restart", Zone: consts.ZoneWrite})
	u, err := reg.Unit(bg, "u", 1)
	if err != nil || u.Subject != "ssh" {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := reg.Unit(bg, "u", 2); codeOf(err) != errcode.NotFound {
		t.Fatalf("err = %v", err)
	}

	def := SampleDefinition("t", 1)
	step := def.Steps[0]
	if _, _, err := reg.RenderStep(bg, def, step, nil, nil); codeOf(err) != errcode.NotFound {
		t.Fatalf("unset step: %v", err)
	}
	c := SampleRequest("x").Content
	e := SampleRequest("x").Expectation
	reg.SetStep("A", c, e)
	c.Rendered = "changed"
	gc, ge, err := reg.RenderStep(bg, def, step, nil, nil)
	if err != nil || gc.Rendered != "systemctl restart nginx" || ge.Intent != "restart nginx" {
		t.Fatalf("%+v %+v %v", gc, ge, err)
	}
	reg.RenderFn = func(context.Context, *task.Definition, task.Step, map[string]any, map[string]map[string]any) (request.Content, request.Expectation, error) {
		return request.Content{Rendered: "custom"}, request.Expectation{}, nil
	}
	if gc, _, _ := reg.RenderStep(bg, def, step, nil, nil); gc.Rendered != "custom" {
		t.Fatal("RenderFn ignored")
	}

	sc := NewFakeScopeSource()
	sc.AddSession("s1", ports.SessionInfo{Project: "p", Environment: "prod", Mode: consts.SessionAuto})
	sc.AddTargets("p", "prod", "a", "b")
	if info, err := sc.Session(bg, "s1"); err != nil || info.Mode != consts.SessionAuto {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := sc.Session(bg, "s2"); codeOf(err) != errcode.NotFound {
		t.Fatal("missing session")
	}
	out, _ := sc.OutsideScope(bg, "p", "prod", []string{"a", "z"})
	if !reflect.DeepEqual(out, []string{"z"}) {
		t.Fatalf("outside = %v", out)
	}
	out, _ = sc.OutsideScope(bg, "p", "dev", []string{"a"})
	if !reflect.DeepEqual(out, []string{"a"}) {
		t.Fatalf("other env outside = %v", out)
	}
}

func TestPolicySource(t *testing.T) {
	m := policy.ModePtr(consts.PolicyAuto)
	set := policy.Set{Version: "v1", Rules: []policy.Rule{{Layer: consts.LayerSystem, Mode: m}}}
	ps := NewFakePolicySource(set)
	*m = consts.PolicyForbid // 改入参不影响
	got, err := ps.Load(bg)
	if err != nil || *got.Rules[0].Mode != consts.PolicyAuto {
		t.Fatalf("%+v %v", got, err)
	}
	*got.Rules[0].Mode = consts.PolicyForbid // 改返回值不影响
	again, _ := ps.Load(bg)
	if *again.Rules[0].Mode != consts.PolicyAuto {
		t.Fatal("Load aliases")
	}
	ps.Set(policy.Set{Version: "v2"})
	if g, _ := ps.Load(bg); g.Version != "v2" {
		t.Fatal("Set ignored")
	}
	ps.FailWith("Load", ErrInjected)
	if _, err := ps.Load(bg); !errors.Is(err, ErrInjected) {
		t.Fatal("fault ignored")
	}
}

func TestClockAndIDs(t *testing.T) {
	c := NewFixedClock(time.Time{})
	if !c.Now().Equal(Epoch) {
		t.Fatal("default start")
	}
	c.Advance(time.Hour)
	if !c.Now().Equal(Epoch.Add(time.Hour)) {
		t.Fatal("Advance")
	}
	c.Set(Epoch)
	if !c.Now().Equal(Epoch) {
		t.Fatal("Set")
	}
	g := NewSeqIDGen()
	if g.NewID("call") != "call-1" || g.NewID("call") != "call-2" || g.NewID("event") != "event-1" {
		t.Fatal("sequence")
	}
}

func TestAuditSink(t *testing.T) {
	a := NewMemAuditSink()
	rec := ports.AuditRecord{Actor: "u", Action: "approve", CallID: "c1", Detail: map[string]any{"k": []any{"v"}}}
	if err := a.Append(bg, rec); err != nil {
		t.Fatal(err)
	}
	rec.Detail["k"].([]any)[0] = "changed"
	got := a.Records()
	if len(got) != 1 || got[0].Detail["k"].([]any)[0] != "v" {
		t.Fatalf("%+v", got)
	}
	got[0].Detail["k"].([]any)[0] = "changed2"
	if a.Records()[0].Detail["k"].([]any)[0] != "v" {
		t.Fatal("Records aliases")
	}
}

func TestEnvSharedLogOrder(t *testing.T) {
	env := NewEnv()
	e1 := SampleEvent("call:c1", 1, events.CallProposed)
	e2 := SampleEvent("recovery:1", 1, events.RecoveryStarted)
	e3 := SampleEvent("run:r1", 1, events.TaskStarted)
	_ = env.Requests.Create(bg, SampleRequest("c1"), []events.Event{e1})
	_ = env.Sink.Emit(bg, e2)
	_ = env.Tasks.CreateRun(bg, SampleRun("r1"), []events.Event{e3})
	if got := env.Log.All(); !reflect.DeepEqual(got, []events.Event{e1, e2, e3}) {
		t.Fatalf("log = %+v", got)
	}
	// 失败的保存不进共享日志
	_ = env.Requests.Create(bg, SampleRequest("c1"), []events.Event{e1})
	if len(env.Log.All()) != 3 {
		t.Fatal("failed Create logged events")
	}
	// 替身用环境的时钟打时间
	env.Clock.Advance(time.Minute)
	fp, _ := env.Fingerprint.Collect(bg, SampleRequest("c1"))
	if !fp.CollectedAt.Equal(Epoch.Add(time.Minute)) {
		t.Fatalf("collected at %v", fp.CollectedAt)
	}
}

func TestSamplesAreValid(t *testing.T) {
	r := SampleRequest("c1")
	if !r.Digest.Valid() {
		t.Error("digest")
	}
	if err := r.Expectation.Validate(r.Scope); err != nil {
		t.Errorf("sample expectation: %v", err)
	}
	if err := SampleDefinition("t", 1).Validate(); err != nil {
		t.Errorf("sample definition: %v", err)
	}
	if err := SampleEvent("call:c1", 1, events.CallProposed).Validate(); err != nil {
		t.Errorf("sample event: %v", err)
	}
}

// 并发使用同一个替身，配合 -race 检查锁。
func TestConcurrentUse(t *testing.T) {
	env := NewEnv()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = env.Executor.Execute(bg, SampleRequest("c"))
				_, _ = env.Fingerprint.Collect(bg, SampleRequest("c"))
				_, _ = env.DCG.Check(bg, SampleRequest("c"))
				_, _ = env.Preview.Preview(bg, SampleRequest("c"))
				_ = env.Audit.Append(bg, ports.AuditRecord{Action: "x"})
				_ = env.Sink.Emit(bg, SampleEvent("call:c", int64(j+1), events.CallProposed))
				env.Clock.Advance(time.Second)
				env.IDs.NewID("x")
				env.Executor.FailNext("Nope", 1, ErrInjected)
			}
		}()
	}
	wg.Wait()
	if env.Executor.Calls() != 160 {
		t.Fatalf("calls = %d", env.Executor.Calls())
	}
}

func TestFakeSecretKeysRemoveAndFaults(t *testing.T) {
	s := NewFakeSecretKeys()
	s.AddKeys("p", "prod", "A", "B")
	s.RemoveKeys("p", "prod", "B")
	got, err := s.Missing(bg, "p", "prod", []string{"A", "B"})
	if err != nil || !reflect.DeepEqual(got, []string{"B"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	s.FailNext("Missing", 1, ErrInjected)
	if _, err := s.Missing(bg, "p", "prod", []string{"A"}); !errors.Is(err, ErrInjected) {
		t.Fatalf("err = %v", err)
	}
	if s.Calls() != 2 {
		t.Fatalf("calls = %d", s.Calls())
	}
}
