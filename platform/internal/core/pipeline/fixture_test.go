package pipeline_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/coretest"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/pipeline"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

var bg = context.Background()

// 测试里用的敏感字面量：它们绝不能出现在任何事件里。
const (
	secretRendered = "RENDERED-COMMAND-TEXT"
	secretParam    = "PARAM-VALUE-TEXT"
)

// batch 记录一次 Create/Save 带了多少条事件。
type batch struct {
	op   string
	evts []events.Event
}

// recStore 包住内存存储，记录每次保存的事件批次。
type recStore struct {
	ports.RequestStore
	mu      sync.Mutex
	batches []batch
}

func (s *recStore) Create(ctx context.Context, r *request.ExecutionRequest, evts []events.Event) error {
	err := s.RequestStore.Create(ctx, r, evts)
	if err == nil {
		s.mu.Lock()
		s.batches = append(s.batches, batch{"create", evts})
		s.mu.Unlock()
	}
	return err
}

func (s *recStore) Save(ctx context.Context, r *request.ExecutionRequest, expected ids.Version, evts []events.Event) error {
	err := s.RequestStore.Save(ctx, r, expected, evts)
	if err == nil {
		s.mu.Lock()
		s.batches = append(s.batches, batch{"save", evts})
		s.mu.Unlock()
	}
	return err
}

func (s *recStore) types() [][]events.Type {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]events.Type
	for _, b := range s.batches {
		var ts []events.Type
		for _, e := range b.evts {
			ts = append(ts, e.Type)
		}
		out = append(out, ts)
	}
	return out
}

type fixture struct {
	t     *testing.T
	env   *coretest.Env
	store *recStore
	svc   *pipeline.Service
	deps  pipeline.Deps
}

func unitDigest() ids.Digest { return ids.Digest(strings.Repeat("a", 64)) }

// newFixture 装配一套默认能走到"自动冻结"的环境：
// 自动会话、global 层 auto 策略、一个声明了预演且可逆的已登记单元 unit-1。
func newFixture(t *testing.T, mods ...func(*pipeline.Deps)) *fixture {
	t.Helper()
	env := coretest.NewEnv()
	env.Scope.AddSession("s1", ports.SessionInfo{Project: "proj", Environment: "prod", Mode: consts.SessionAuto})
	env.Scope.AddTargets("proj", "prod", "host-a")
	f := &fixture{t: t, env: env, store: &recStore{RequestStore: env.Requests}}
	f.setPolicy(consts.PolicyAuto)
	f.setUnit(func(*ports.UnitInfo) {})
	f.deps = pipeline.Deps{
		Requests: f.store, Revisions: env.Revisions, Policy: env.Policy, Registry: env.Registry,
		Scope: env.Scope, Preview: env.Preview, Fingerprint: env.Fingerprint, DCG: env.DCG,
		Executor: env.Executor, Audit: env.Audit, Clock: env.Clock, IDs: env.IDs,
	}
	for _, m := range mods {
		m(&f.deps)
	}
	svc, err := pipeline.New(f.deps)
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	return f
}

// setPolicy 设置 svc 和 ad_hoc 两个主体的 global 层模式。
func (f *fixture) setPolicy(mode consts.PolicyMode) {
	f.env.Policy.Set(policy.Set{Version: "p1", Rules: []policy.Rule{
		{Layer: consts.LayerGlobal, Subject: "svc", Mode: policy.ModePtr(mode)},
		{Layer: consts.LayerGlobal, Subject: "ad_hoc", Mode: policy.ModePtr(mode)},
	}})
}

// setUnit 改写（重新登记）unit-1。
func (f *fixture) setUnit(mod func(*ports.UnitInfo)) {
	u := ports.UnitInfo{
		Ref:     request.UnitRef{ID: "unit-1", Version: 1, Digest: unitDigest()},
		Subject: "svc", Action: "restart", Zone: consts.ZoneWrite,
		Category: consts.CategoryWrite, Reversible: consts.Reversible, HasPreview: true,
		FingerprintMethod: "fake", FingerprintMethodVersion: "v1",
	}
	mod(&u)
	f.env.Registry.AddUnit(u)
}

func (f *fixture) setSessionMode(m consts.SessionMode) {
	f.env.Scope.AddSession("s1", ports.SessionInfo{Project: "proj", Environment: "prod", Mode: m})
}

// input 是默认的合格提交：已登记工具、自审无风险、预期齐全。
func input() pipeline.SubmitInput {
	return pipeline.SubmitInput{
		Source:    consts.SourceRegisteredTool,
		SessionID: "s1",
		Scope:     request.Scope{Project: "proj", Environment: "prod", Targets: []string{"host-a"}},
		Content: request.Content{
			Unit:       &request.UnitRef{ID: "unit-1", Version: 1, Digest: unitDigest()},
			Rendered:   secretRendered,
			Params:     map[string]any{"svc": secretParam},
			SecretKeys: []string{"TOKEN"},
		},
		Expectation: request.Expectation{
			Intent: "restart service", Targets: []string{"host-a"}, Category: consts.CategoryWrite,
			ExpectedChange:  request.ExpectedChange{Text: "service restarted"},
			ExpectedOutput:  "active",
			SuccessCriteria: []request.Criterion{{Expr: "exit_code == 0"}},
			Reversible:      consts.Reversible, Rollback: "restart again", HasPreview: true,
		},
		Submission: request.Submission{SelfReview: &request.SelfReview{Verdict: consts.VerdictNoRisk}},
	}
}

// adHocInput 是独立命令的合格提交（没有单元）。
func adHocInput() pipeline.SubmitInput {
	in := input()
	in.Source = consts.SourceAdHoc
	in.Content.Unit = nil
	in.Expectation.HasPreview = false
	in.Expectation.Reversible = consts.Reversible
	return in
}

func (f *fixture) submit(in pipeline.SubmitInput) *request.ExecutionRequest {
	f.t.Helper()
	r, err := f.svc.Submit(bg, in)
	if err != nil {
		f.t.Fatalf("Submit: %v", err)
	}
	return r
}

// inspect 提交并检验，返回检验后的申请。
func (f *fixture) inspect(in pipeline.SubmitInput) *request.ExecutionRequest {
	f.t.Helper()
	r := f.submit(in)
	out, err := f.svc.Inspect(bg, r.CallID)
	if err != nil {
		f.t.Fatalf("Inspect: %v", err)
	}
	return out
}

func (f *fixture) get(id ids.CallID) *request.ExecutionRequest {
	f.t.Helper()
	r, err := f.env.Requests.Get(bg, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func wantState(t *testing.T, r *request.ExecutionRequest, want consts.RequestState) {
	t.Helper()
	if r.State != want {
		t.Fatalf("state = %s, want %s (rejection=%+v escalations=%v)", r.State, want, r.Rejection, r.Checks.Escalations)
	}
}

func wantCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	got, ok := errcode.CodeOf(err)
	if !ok || got != want {
		t.Fatalf("error = %v (code %q), want %s", err, got, want)
	}
}

func typesOf(evts []events.Event) []events.Type {
	out := make([]events.Type, len(evts))
	for i, e := range evts {
		out[i] = e.Type
	}
	return out
}

func eventTypes(f *fixture) []events.Type { return typesOf(f.env.Requests.Events()) }

func lastEvent(t *testing.T, f *fixture) events.Event {
	t.Helper()
	all := f.env.Requests.Events()
	if len(all) == 0 {
		t.Fatal("no events")
	}
	return all[len(all)-1]
}

func hasType(evts []events.Event, typ events.Type) (events.Event, bool) {
	for _, e := range evts {
		if e.Type == typ {
			return e, true
		}
	}
	return events.Event{}, false
}

func sameTypes(a, b []events.Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// assertNoLeak 保证没有任何事件带出内容原文或参数值。
func assertNoLeak(t *testing.T, evts []events.Event, extra ...string) {
	t.Helper()
	b, err := json.Marshal(evts)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range append([]string{secretRendered, secretParam}, extra...) {
		if strings.Contains(string(b), s) {
			t.Errorf("events leak %q", s)
		}
	}
}

func intp(v int) *int { return &v }

type (
	policySet  = policy.Set
	policyRule = policy.Rule
)

func policyModePtr(m consts.PolicyMode) *consts.PolicyMode { return policy.ModePtr(m) }
