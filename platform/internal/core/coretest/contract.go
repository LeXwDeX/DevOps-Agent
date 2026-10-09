package coretest

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// 端口契约测试：每个函数接收一个工厂，每个子测试用工厂新建一个干净的实现。
// 内存替身和以后的 provider 跑同一套，保证行为一致。
// 实现了 EventRecorder 的存储会额外检查"状态和事件一起保存"。

var bg = context.Background()

func wantCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if got := codeOf(err); got != want {
		t.Fatalf("error code = %q (err %v), want %q", got, err, want)
	}
}

func sortedIDs(rs []*request.ExecutionRequest) []string {
	var out []string
	for _, r := range rs {
		out = append(out, string(r.CallID))
	}
	sort.Strings(out)
	return out
}

func eventsOf(s any) ([]events.Event, bool) {
	if r, ok := s.(EventRecorder); ok {
		return r.Events(), true
	}
	return nil, false
}

// RequestStoreContract 检查 ports.RequestStore 的行为。
func RequestStoreContract(t *testing.T, newStore func() ports.RequestStore) {
	t.Run("get missing is not_found", func(t *testing.T) {
		_, err := newStore().Get(bg, "nope")
		wantCode(t, err, errcode.NotFound)
	})

	t.Run("create then get returns equal copy", func(t *testing.T) {
		s := newStore()
		r := SampleRequest("c1")
		if err := s.Create(bg, r, []events.Event{SampleEvent("call:c1", 1, events.CallProposed)}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(bg, "c1")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, r) {
			t.Fatalf("got %+v, want %+v", got, r)
		}
	})

	t.Run("create duplicate is already_exists and records nothing", func(t *testing.T) {
		s := newStore()
		r := SampleRequest("c1")
		if err := s.Create(bg, r, nil); err != nil {
			t.Fatal(err)
		}
		err := s.Create(bg, r, []events.Event{SampleEvent("call:c1", 1, events.CallProposed)})
		wantCode(t, err, errcode.AlreadyExists)
		if evts, ok := eventsOf(s); ok && len(evts) != 0 {
			t.Fatalf("failed Create recorded events: %v", evts)
		}
	})

	t.Run("create rejects invalid input", func(t *testing.T) {
		s := newStore()
		wantCode(t, s.Create(bg, nil, nil), errcode.InvalidArgument)
		wantCode(t, s.Create(bg, &request.ExecutionRequest{}, nil), errcode.InvalidArgument)
	})

	t.Run("no aliasing in either direction", func(t *testing.T) {
		s := newStore()
		r := SampleRequest("c1")
		if err := s.Create(bg, r, nil); err != nil {
			t.Fatal(err)
		}
		// 改入参不影响已存的
		r.Content.Params["svc"] = "changed"
		r.Scope.Targets[0] = "changed"
		got, _ := s.Get(bg, "c1")
		if got.Content.Params["svc"] != "nginx" || got.Scope.Targets[0] != "host-a" {
			t.Fatal("store aliases the request passed to Create")
		}
		// 改返回值不影响已存的
		got.Content.Params["svc"] = "changed2"
		got.Content.Params["opts"].([]any)[1].(map[string]any)["k"] = "changed2"
		got.Expectation.SuccessCriteria[0].Expr = "changed2"
		got.Submission.SelfReview.Verdict = consts.VerdictRisk
		again, _ := s.Get(bg, "c1")
		if !reflect.DeepEqual(again, SampleRequest("c1")) {
			t.Fatal("store returns aliased request from Get")
		}
		// Save 同样隔离
		next := again.Clone()
		next.Version = 2
		next.State = consts.RequestChecking
		if err := s.Save(bg, next, 1, nil); err != nil {
			t.Fatal(err)
		}
		next.Content.Params["svc"] = "changed3"
		saved, _ := s.Get(bg, "c1")
		if saved.Content.Params["svc"] != "nginx" {
			t.Fatal("store aliases the request passed to Save")
		}
		list, _ := s.ListNonTerminal(bg)
		list[0].Content.Params["svc"] = "changed4"
		saved, _ = s.Get(bg, "c1")
		if saved.Content.Params["svc"] != "nginx" {
			t.Fatal("ListNonTerminal returns aliased requests")
		}
	})

	t.Run("save with matching version succeeds and stores events in order", func(t *testing.T) {
		s := newStore()
		r := SampleRequest("c1")
		if err := s.Create(bg, r, []events.Event{SampleEvent("call:c1", 1, events.CallProposed)}); err != nil {
			t.Fatal(err)
		}
		next := r.Clone()
		next.State = consts.RequestChecking
		next.Version = r.Version.Next()
		next.EventSeq = 3
		evts := []events.Event{
			SampleEvent("call:c1", 2, events.InspectionStarted),
			SampleEvent("call:c1", 3, events.InspectionPassed),
		}
		if err := s.Save(bg, next, r.Version, evts); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Get(bg, "c1")
		if got.State != consts.RequestChecking || got.Version != 2 || got.EventSeq != 3 {
			t.Fatalf("got %+v", got)
		}
		if all, ok := eventsOf(s); ok {
			want := []events.Event{SampleEvent("call:c1", 1, events.CallProposed), evts[0], evts[1]}
			if !reflect.DeepEqual(all, want) {
				t.Fatalf("events = %+v, want %+v", all, want)
			}
		}
	})

	t.Run("stale save is cas_conflict and changes nothing", func(t *testing.T) {
		s := newStore()
		r := SampleRequest("c1")
		if err := s.Create(bg, r, nil); err != nil {
			t.Fatal(err)
		}
		a := r.Clone()
		a.State, a.Version = consts.RequestChecking, 2
		if err := s.Save(bg, a, 1, nil); err != nil {
			t.Fatal(err)
		}
		b := r.Clone() // 基于旧版本
		b.State, b.Version = consts.RequestStopped, 2
		err := s.Save(bg, b, 1, []events.Event{SampleEvent("call:c1", 9, events.CallStopped)})
		wantCode(t, err, errcode.CASConflict)
		// 版本超前也算冲突
		c := r.Clone()
		c.Version = 6
		wantCode(t, s.Save(bg, c, 5, nil), errcode.CASConflict)
		got, _ := s.Get(bg, "c1")
		if got.State != consts.RequestChecking || got.Version != 2 {
			t.Fatalf("state changed by failed Save: %+v", got)
		}
		if evts, ok := eventsOf(s); ok && len(evts) != 0 {
			t.Fatalf("failed Save recorded events: %v", evts)
		}
	})

	t.Run("save must set version to expected+1", func(t *testing.T) {
		s := newStore()
		r := SampleRequest("c1")
		if err := s.Create(bg, r, nil); err != nil {
			t.Fatal(err)
		}
		for _, v := range []ids.Version{1, 3} {
			bad := r.Clone()
			bad.Version = v
			wantCode(t, s.Save(bg, bad, 1, []events.Event{SampleEvent("call:c1", 2, events.CallRevised)}), errcode.InvalidArgument)
		}
		if evts, ok := eventsOf(s); ok && len(evts) != 0 {
			t.Fatalf("failed Save recorded events: %v", evts)
		}
	})

	t.Run("save missing is not_found", func(t *testing.T) {
		s := newStore()
		r := SampleRequest("ghost")
		r.Version = 2
		wantCode(t, s.Save(bg, r, 1, nil), errcode.NotFound)
		wantCode(t, s.Save(bg, nil, 1, nil), errcode.InvalidArgument)
	})

	t.Run("list non-terminal", func(t *testing.T) {
		s := newStore()
		states := map[ids.CallID]consts.RequestState{
			"a-draft": consts.RequestDraft, "b-exec": consts.RequestExecuting, "c-done": consts.RequestSucceeded,
			"d-stop": consts.RequestStopped, "e-frozen": consts.RequestFrozen, "f-unknown": consts.RequestUnknown,
		}
		for id, st := range states {
			r := SampleRequest(id)
			r.State = st
			if err := s.Create(bg, r, nil); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.ListNonTerminal(bg)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"a-draft", "b-exec", "e-frozen"}; !reflect.DeepEqual(sortedIDs(got), want) {
			t.Fatalf("ids = %v, want %v", sortedIDs(got), want)
		}
		// 转到终态后不再出现
		r, _ := s.Get(bg, "a-draft")
		r.State, r.Version = consts.RequestStopped, 2
		if err := s.Save(bg, r, 1, nil); err != nil {
			t.Fatal(err)
		}
		got, _ = s.ListNonTerminal(bg)
		if want := []string{"b-exec", "e-frozen"}; !reflect.DeepEqual(sortedIDs(got), want) {
			t.Fatalf("ids after stop = %v, want %v", sortedIDs(got), want)
		}
		// 没有数据时返回空
		empty, err := newStore().ListNonTerminal(bg)
		if err != nil || len(empty) != 0 {
			t.Fatalf("empty store: %v %v", empty, err)
		}
	})

	t.Run("concurrent saves: exactly one wins", func(t *testing.T) {
		s := newStore()
		if err := s.Create(bg, SampleRequest("c1"), nil); err != nil {
			t.Fatal(err)
		}
		const n = 16
		results := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r := SampleRequest("c1")
				r.Version = 2
				r.Revision = ids.Revision(i + 1)
				results[i] = s.Save(bg, r, 1, []events.Event{SampleEvent("call:c1", int64(i+1), events.CallRevised)})
			}(i)
		}
		wg.Wait()
		wins, conflicts := 0, 0
		winner := ids.Revision(0)
		for i, err := range results {
			switch {
			case err == nil:
				wins++
				winner = ids.Revision(i + 1)
			case codeOf(err) == errcode.CASConflict:
				conflicts++
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if wins != 1 || conflicts != n-1 {
			t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
		}
		got, _ := s.Get(bg, "c1")
		if got.Version != 2 || got.Revision != winner {
			t.Fatalf("stored %+v, winner revision %d", got, winner)
		}
		if evts, ok := eventsOf(s); ok && len(evts) != 1 {
			t.Fatalf("events recorded = %d, want 1 (only the winner's)", len(evts))
		}
	})

	t.Run("canceled context fails", func(t *testing.T) {
		s := newStore()
		c, cancel := context.WithCancel(bg)
		cancel()
		if _, err := s.Get(c, "c1"); err == nil {
			t.Fatal("Get with canceled context succeeded")
		}
		if err := s.Create(c, SampleRequest("c1"), nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("Create err = %v", err)
		}
		if _, err := s.Get(bg, "c1"); codeOf(err) != errcode.NotFound {
			t.Fatal("canceled Create stored data")
		}
	})
}

// RevisionLogContract 检查 ports.RevisionLog 的行为。
func RevisionLogContract(t *testing.T, newLog func() ports.RevisionLog) {
	t.Run("list unknown call is empty", func(t *testing.T) {
		got, err := newLog().List(bg, "nope")
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("append keeps order and full copies", func(t *testing.T) {
		l := newLog()
		var want []*request.ExecutionRequest
		for rev := 1; rev <= 3; rev++ {
			r := SampleRequest("c1")
			r.Revision = ids.Revision(rev)
			r.Content.Rendered = "rev " + string(rune('0'+rev))
			if err := l.Append(bg, r); err != nil {
				t.Fatal(err)
			}
			want = append(want, r.Clone())
		}
		got, err := l.List(bg, "c1")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %d entries, differs from appended", len(got))
		}
	})

	t.Run("append is not overwritten by same revision", func(t *testing.T) {
		l := newLog()
		r := SampleRequest("c1")
		if err := l.Append(bg, r); err != nil {
			t.Fatal(err)
		}
		if err := l.Append(bg, r); err != nil {
			t.Fatal(err)
		}
		got, _ := l.List(bg, "c1")
		if len(got) != 2 {
			t.Fatalf("entries = %d, want 2 (append-only)", len(got))
		}
	})

	t.Run("calls are separate", func(t *testing.T) {
		l := newLog()
		_ = l.Append(bg, SampleRequest("c1"))
		_ = l.Append(bg, SampleRequest("c2"))
		a, _ := l.List(bg, "c1")
		b, _ := l.List(bg, "c2")
		if len(a) != 1 || len(b) != 1 || a[0].CallID != "c1" || b[0].CallID != "c2" {
			t.Fatalf("a=%v b=%v", a, b)
		}
	})

	t.Run("no aliasing", func(t *testing.T) {
		l := newLog()
		r := SampleRequest("c1")
		_ = l.Append(bg, r)
		r.Content.Params["svc"] = "changed"
		got, _ := l.List(bg, "c1")
		if got[0].Content.Params["svc"] != "nginx" {
			t.Fatal("Append aliases input")
		}
		got[0].Content.Params["svc"] = "changed2"
		again, _ := l.List(bg, "c1")
		if again[0].Content.Params["svc"] != "nginx" {
			t.Fatal("List returns aliased data")
		}
	})

	t.Run("append rejects invalid input", func(t *testing.T) {
		l := newLog()
		wantCode(t, l.Append(bg, nil), errcode.InvalidArgument)
		wantCode(t, l.Append(bg, &request.ExecutionRequest{}), errcode.InvalidArgument)
	})

	t.Run("concurrent appends are all kept", func(t *testing.T) {
		l := newLog()
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _ = l.Append(bg, SampleRequest("c1")) }()
		}
		wg.Wait()
		got, _ := l.List(bg, "c1")
		if len(got) != 16 {
			t.Fatalf("entries = %d", len(got))
		}
	})
}

// TaskStoreContract 检查 ports.TaskStore 的行为。
// 实现了 DefinitionSeeder 的存储会额外检查任务定义和启用版本。
func TaskStoreContract(t *testing.T, newStore func() ports.TaskStore) {
	t.Run("definitions and active version", func(t *testing.T) {
		s := newStore()
		seeder, ok := s.(DefinitionSeeder)
		if !ok {
			t.Skip("store does not implement DefinitionSeeder")
		}
		_, err := s.Definition(bg, "deploy", 1)
		wantCode(t, err, errcode.NotFound)
		_, err = s.ActiveVersion(bg, "deploy")
		wantCode(t, err, errcode.NotFound)

		seeder.SeedDefinition(SampleDefinition("deploy", 1), true)
		seeder.SeedDefinition(SampleDefinition("deploy", 2), false)
		d, err := s.Definition(bg, "deploy", 2)
		if err != nil || !reflect.DeepEqual(d, SampleDefinition("deploy", 2)) {
			t.Fatalf("definition: %+v, %v", d, err)
		}
		if v, err := s.ActiveVersion(bg, "deploy"); err != nil || v != 1 {
			t.Fatalf("active = %d, %v", v, err)
		}
		_, err = s.Definition(bg, "deploy", 3)
		wantCode(t, err, errcode.NotFound)
		// 返回的定义是副本
		d.Steps[0].ID = "changed"
		d.Params[0].Name = "changed"
		again, _ := s.Definition(bg, "deploy", 2)
		if !reflect.DeepEqual(again, SampleDefinition("deploy", 2)) {
			t.Fatal("Definition returns aliased data")
		}
	})

	t.Run("get missing run is not_found", func(t *testing.T) {
		_, err := newStore().GetRun(bg, "nope")
		wantCode(t, err, errcode.NotFound)
	})

	t.Run("create then get returns equal copy", func(t *testing.T) {
		s := newStore()
		run := SampleRun("r1")
		if err := s.CreateRun(bg, run, []events.Event{SampleEvent("run:r1", 1, events.TaskStarted)}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRun(bg, "r1")
		if err != nil || !reflect.DeepEqual(got, run) {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("create duplicate is already_exists and records nothing", func(t *testing.T) {
		s := newStore()
		if err := s.CreateRun(bg, SampleRun("r1"), nil); err != nil {
			t.Fatal(err)
		}
		err := s.CreateRun(bg, SampleRun("r1"), []events.Event{SampleEvent("run:r1", 1, events.TaskStarted)})
		wantCode(t, err, errcode.AlreadyExists)
		if evts, ok := eventsOf(s); ok && len(evts) != 0 {
			t.Fatalf("failed CreateRun recorded events: %v", evts)
		}
		wantCode(t, s.CreateRun(bg, nil, nil), errcode.InvalidArgument)
		wantCode(t, s.CreateRun(bg, &task.Run{}, nil), errcode.InvalidArgument)
	})

	t.Run("no aliasing", func(t *testing.T) {
		s := newStore()
		run := SampleRun("r1")
		_ = s.CreateRun(bg, run, nil)
		run.Params["svc"] = "changed"
		run.Steps[0].Output["n"].([]any)[0] = 99
		got, _ := s.GetRun(bg, "r1")
		if got.Params["svc"] != "nginx" || got.Steps[0].Output["n"].([]any)[0] != 1 {
			t.Fatal("CreateRun aliases input")
		}
		got.Params["svc"] = "changed2"
		got.Scope.Targets[0] = "changed2"
		again, _ := s.GetRun(bg, "r1")
		if !reflect.DeepEqual(again, SampleRun("r1")) {
			t.Fatal("GetRun returns aliased data")
		}
		list, _ := s.ListNonTerminalRuns(bg)
		list[0].Params["svc"] = "changed3"
		again, _ = s.GetRun(bg, "r1")
		if again.Params["svc"] != "nginx" {
			t.Fatal("ListNonTerminalRuns returns aliased data")
		}
	})

	t.Run("save run: cas, version rule and events", func(t *testing.T) {
		s := newStore()
		run := SampleRun("r1")
		_ = s.CreateRun(bg, run, []events.Event{SampleEvent("run:r1", 1, events.TaskStarted)})

		next := run.Clone()
		next.State, next.Version, next.EventSeq = consts.RunRunning, 2, 2
		ev := SampleEvent("run:r1", 2, events.TaskStepCompleted)
		if err := s.SaveRun(bg, next, 1, []events.Event{ev}); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetRun(bg, "r1")
		if got.State != consts.RunRunning || got.Version != 2 {
			t.Fatalf("got %+v", got)
		}
		if all, ok := eventsOf(s); ok {
			want := []events.Event{SampleEvent("run:r1", 1, events.TaskStarted), ev}
			if !reflect.DeepEqual(all, want) {
				t.Fatalf("events = %+v", all)
			}
		}

		stale := run.Clone()
		stale.State, stale.Version = consts.RunStopped, 2
		err := s.SaveRun(bg, stale, 1, []events.Event{SampleEvent("run:r1", 3, events.TaskStopped)})
		wantCode(t, err, errcode.CASConflict)
		bad := got.Clone()
		bad.Version = 7
		wantCode(t, s.SaveRun(bg, bad, 2, nil), errcode.InvalidArgument)
		ghost := SampleRun("ghost")
		ghost.Version = 2
		wantCode(t, s.SaveRun(bg, ghost, 1, nil), errcode.NotFound)
		wantCode(t, s.SaveRun(bg, nil, 1, nil), errcode.InvalidArgument)

		got, _ = s.GetRun(bg, "r1")
		if got.State != consts.RunRunning || got.Version != 2 {
			t.Fatalf("failed saves changed run: %+v", got)
		}
		if all, ok := eventsOf(s); ok && len(all) != 2 {
			t.Fatalf("failed saves recorded events: %d", len(all))
		}
	})

	t.Run("list non-terminal runs", func(t *testing.T) {
		s := newStore()
		states := map[string]consts.RunState{
			"r-pending": consts.RunPending, "r-running": consts.RunRunning, "r-review": consts.RunAwaitingReview,
			"r-input": consts.RunAwaitingInput, "r-done": consts.RunCompleted, "r-stopped": consts.RunStopped,
			"r-cancel": consts.RunCancelled, "r-recovery": consts.RunRecoveryInterrupted,
		}
		for id, st := range states {
			run := SampleRun(id)
			run.State = st
			if err := s.CreateRun(bg, run, nil); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.ListNonTerminalRuns(bg)
		if err != nil {
			t.Fatal(err)
		}
		var runIDs []string
		for _, r := range got {
			runIDs = append(runIDs, r.RunID)
		}
		sort.Strings(runIDs)
		if want := []string{"r-input", "r-pending", "r-review", "r-running"}; !reflect.DeepEqual(runIDs, want) {
			t.Fatalf("ids = %v, want %v", runIDs, want)
		}
	})

	t.Run("concurrent save runs: exactly one wins", func(t *testing.T) {
		s := newStore()
		_ = s.CreateRun(bg, SampleRun("r1"), nil)
		const n = 16
		results := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				r := SampleRun("r1")
				r.Version = 2
				r.Current = string(rune('a' + i))
				results[i] = s.SaveRun(bg, r, 1, []events.Event{SampleEvent("run:r1", int64(i+1), events.TaskStepCompleted)})
			}(i)
		}
		wg.Wait()
		wins := 0
		for _, err := range results {
			switch {
			case err == nil:
				wins++
			case codeOf(err) != errcode.CASConflict:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		if wins != 1 {
			t.Fatalf("wins = %d", wins)
		}
		if evts, ok := eventsOf(s); ok && len(evts) != 1 {
			t.Fatalf("events recorded = %d, want 1", len(evts))
		}
	})
}

// EventSinkContract 检查 ports.EventSink 的行为。
func EventSinkContract(t *testing.T, newSink func() ports.EventSink) {
	t.Run("emit nothing is fine", func(t *testing.T) {
		if err := newSink().Emit(bg); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("emit keeps order across calls", func(t *testing.T) {
		s := newSink()
		e1 := SampleEvent("recovery:1", 1, events.RecoveryStarted)
		e2 := SampleEvent("recovery:1", 2, events.RecoveryCompleted)
		e3 := SampleEvent("call:c1", 1, events.ExecutionUnknown)
		if err := s.Emit(bg, e1, e2); err != nil {
			t.Fatal(err)
		}
		if err := s.Emit(bg, e3); err != nil {
			t.Fatal(err)
		}
		if all, ok := eventsOf(s); ok && !reflect.DeepEqual(all, []events.Event{e1, e2, e3}) {
			t.Fatalf("events = %+v", all)
		}
	})

	t.Run("no aliasing", func(t *testing.T) {
		s := newSink()
		e := SampleEvent("call:c1", 1, events.CallProposed)
		if err := s.Emit(bg, e); err != nil {
			t.Fatal(err)
		}
		e.Data["digest"] = "changed"
		e.Targets[0] = "changed"
		if all, ok := eventsOf(s); ok {
			if all[0].Data["digest"] != "d" || all[0].Targets[0] != "host-a" {
				t.Fatal("sink aliases emitted event")
			}
			all[0].Data["digest"] = "changed2"
			again, _ := eventsOf(s)
			if again[0].Data["digest"] != "d" {
				t.Fatal("Events returns aliased data")
			}
		}
	})

	t.Run("canceled context fails and records nothing", func(t *testing.T) {
		s := newSink()
		c, cancel := context.WithCancel(bg)
		cancel()
		if err := s.Emit(c, SampleEvent("call:c1", 1, events.CallProposed)); err == nil {
			t.Fatal("Emit with canceled context succeeded")
		}
		if all, ok := eventsOf(s); ok && len(all) != 0 {
			t.Fatalf("recorded %d events", len(all))
		}
	})

	t.Run("concurrent emits are all kept", func(t *testing.T) {
		s := newSink()
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_ = s.Emit(bg, SampleEvent("call:c1", int64(i+1), events.CallProposed))
			}(i)
		}
		wg.Wait()
		if all, ok := eventsOf(s); ok && len(all) != 16 {
			t.Fatalf("recorded %d events, want 16", len(all))
		}
	})
}

// IDGenContract 检查 ports.IDGen：非空、同一 kind 内唯一、并发安全。
func IDGenContract(t *testing.T, newGen func() ports.IDGen) {
	t.Run("unique within kind", func(t *testing.T) {
		g := newGen()
		seen := map[string]bool{}
		for i := 0; i < 100; i++ {
			id := g.NewID("call")
			if id == "" || seen[id] {
				t.Fatalf("empty or duplicate id %q", id)
			}
			seen[id] = true
		}
	})
	t.Run("concurrent ids are unique", func(t *testing.T) {
		g := newGen()
		var mu sync.Mutex
		seen := map[string]bool{}
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					id := g.NewID("event")
					mu.Lock()
					if seen[id] {
						t.Errorf("duplicate id %q", id)
					}
					seen[id] = true
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
	})
}

// SecretKeySeeder 让契约测试能给凭证库登记 K。只登记 K 名，没有 V。
type SecretKeySeeder interface {
	AddKeys(project, env string, keys ...string)
}

// SecretKeysContract 检查 ports.SecretKeys 的行为。
// 实现了 SecretKeySeeder 才能登记 K；没有实现的实现只检查"没有登记过就全部缺失"。
func SecretKeysContract(t *testing.T, newPort func() ports.SecretKeys) {
	t.Run("nothing registered means everything is missing", func(t *testing.T) {
		got, err := newPort().Missing(bg, "p", "prod", []string{"TOKEN", "DB_PASS"})
		if err != nil || !reflect.DeepEqual(got, []string{"TOKEN", "DB_PASS"}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("no keys asked means nothing missing", func(t *testing.T) {
		got, err := newPort().Missing(bg, "p", "prod", nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("only missing keys come back, in the asked order, without duplicates", func(t *testing.T) {
		p := newPort()
		seeder, ok := p.(SecretKeySeeder)
		if !ok {
			t.Skip("port does not implement SecretKeySeeder")
		}
		seeder.AddKeys("p", "prod", "TOKEN", "API")
		asked := []string{"ZED", "TOKEN", "ALPHA", "ZED", "API"}
		got, err := p.Missing(bg, "p", "prod", asked)
		if err != nil || !reflect.DeepEqual(got, []string{"ZED", "ALPHA"}) {
			t.Fatalf("got %v, %v", got, err)
		}
		if !reflect.DeepEqual(asked, []string{"ZED", "TOKEN", "ALPHA", "ZED", "API"}) {
			t.Fatalf("input was modified: %v", asked)
		}
		got, err = p.Missing(bg, "p", "prod", []string{"TOKEN", "API"})
		if err != nil || len(got) != 0 {
			t.Fatalf("all registered: got %v, %v", got, err)
		}
	})

	t.Run("keys are scoped to project and environment", func(t *testing.T) {
		p := newPort()
		seeder, ok := p.(SecretKeySeeder)
		if !ok {
			t.Skip("port does not implement SecretKeySeeder")
		}
		seeder.AddKeys("p", "prod", "TOKEN")
		for _, scope := range [][2]string{{"p", "dev"}, {"q", "prod"}} {
			got, err := p.Missing(bg, scope[0], scope[1], []string{"TOKEN"})
			if err != nil || !reflect.DeepEqual(got, []string{"TOKEN"}) {
				t.Fatalf("%v: got %v, %v", scope, got, err)
			}
		}
	})

	t.Run("a cancelled context is an error, not an empty answer", func(t *testing.T) {
		ctx, cancel := context.WithCancel(bg)
		cancel()
		got, err := newPort().Missing(ctx, "p", "prod", []string{"TOKEN"})
		if err == nil && len(got) == 0 {
			t.Fatal("cancelled context answered \"nothing missing\"")
		}
	})
}

// ---- 其余端口的契约测试 ----
//
// 需要预置数据的端口，工厂接收一份由契约定义的种子（Sample...Seed 或参数），
// provider 的测试把种子写进真实存储后返回端口。

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(bg)
	cancel()
	return ctx
}

// samplePolicySet 返回覆盖四层、带指针字段的合法策略集。
func samplePolicySet() policy.Set {
	return policy.Set{Version: "p-v1", Rules: []policy.Rule{
		{Layer: consts.LayerAction, Subject: "svc", Action: "restart", Environment: "prod", Mode: policy.ModePtr(consts.PolicyManual), AllowAuto: policy.BoolPtr(false)},
		{Layer: consts.LayerZone, Subject: "svc", Zone: consts.ZoneWrite, Mode: policy.ModePtr(consts.PolicyAuto)},
		{Layer: consts.LayerGlobal, Subject: "svc", Mode: policy.ModePtr(consts.PolicyManual)},
		{Layer: consts.LayerSystem, Mode: policy.ModePtr(consts.PolicyManual), AllowAuto: policy.BoolPtr(true)},
	}}
}

// PolicySourceContract 检查 ports.PolicySource。newPort 收到种子策略集，返回 Load 能读到它的端口。
// 检查：读到的与种子相等；改种子、改 Load 的返回值都不影响后面的 Load；空策略集可读；已取消的 ctx 返回错误。
func PolicySourceContract(t *testing.T, newPort func(seed policy.Set) ports.PolicySource) {
	t.Run("load returns the seeded set", func(t *testing.T) {
		got, err := newPort(samplePolicySet()).Load(bg)
		if err != nil || !reflect.DeepEqual(got, samplePolicySet()) {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("changing the seed afterwards changes nothing", func(t *testing.T) {
		seed := samplePolicySet()
		p := newPort(seed)
		*seed.Rules[0].Mode = consts.PolicyForbid
		*seed.Rules[0].AllowAuto = true
		seed.Rules[1].Subject = "changed"
		seed.Version = "changed"
		got, err := p.Load(bg)
		if err != nil || !reflect.DeepEqual(got, samplePolicySet()) {
			t.Fatalf("source aliases the seed: %+v, %v", got, err)
		}
	})

	t.Run("results share no memory with each other or later calls", func(t *testing.T) {
		p := newPort(samplePolicySet())
		a, err := p.Load(bg)
		if err != nil {
			t.Fatal(err)
		}
		b, err := p.Load(bg)
		if err != nil {
			t.Fatal(err)
		}
		*a.Rules[0].Mode = consts.PolicyForbid
		*a.Rules[0].AllowAuto = true
		a.Rules[1].Subject = "changed"
		a.Rules[2] = policy.Rule{}
		a.Rules = append(a.Rules[:1], a.Rules[2:]...)
		a.Version = "changed"
		if !reflect.DeepEqual(b, samplePolicySet()) {
			t.Fatalf("two results share memory: %+v", b)
		}
		again, err := p.Load(bg)
		if err != nil || !reflect.DeepEqual(again, samplePolicySet()) {
			t.Fatalf("later Load changed: %+v, %v", again, err)
		}
	})

	t.Run("an empty set loads", func(t *testing.T) {
		got, err := newPort(policy.Set{}).Load(bg)
		if err != nil || len(got.Rules) != 0 {
			t.Fatalf("got %+v, %v", got, err)
		}
	})

	t.Run("a cancelled context is an error", func(t *testing.T) {
		if _, err := newPort(samplePolicySet()).Load(canceledCtx()); err == nil {
			t.Fatal("Load with cancelled context succeeded")
		}
	})
}

// RegistrySeed 是 RegistryContract 的种子：已登记单元，以及一次 RenderStep 的输入和应得结果。
// provider 的测试要让 RenderStep(Def, Step, Params, Outputs) 返回 Content 和 Expectation。
type RegistrySeed struct {
	Units       []ports.UnitInfo
	Def         *task.Definition
	Step        task.Step
	Params      map[string]any
	Outputs     map[string]map[string]any
	Content     request.Content
	Expectation request.Expectation
}

// SampleRegistrySeed 返回契约使用的种子。
func SampleRegistrySeed() RegistrySeed {
	def := SampleDefinition("deploy", 2)
	sample := SampleRequest("seed")
	return RegistrySeed{
		Units: []ports.UnitInfo{
			{
				Ref:     request.UnitRef{ID: "unit-1", Version: 1, Digest: ids.Digest("1111111111111111111111111111111111111111111111111111111111111111")},
				Subject: "svc", Action: "restart", Zone: consts.ZoneWrite, Category: consts.CategoryStartStop,
				Reversible: consts.Reversible, HasPreview: true, FingerprintMethod: "fake", FingerprintMethodVersion: "v1",
			},
			{
				Ref:     request.UnitRef{ID: "unit-1", Version: 2, Digest: ids.Digest("2222222222222222222222222222222222222222222222222222222222222222")},
				Subject: "svc", Action: "stop", Zone: consts.ZoneWrite, Category: consts.CategoryStartStop,
				Reversible: consts.Irreversible,
			},
		},
		Def:         def,
		Step:        def.Steps[1],
		Params:      map[string]any{"svc": "nginx", "hosts": []any{"a", map[string]any{"k": "v"}}},
		Outputs:     map[string]map[string]any{"A": {"exit_code": 0, "items": []any{"x", map[string]any{"n": 1}}}},
		Content:     sample.Content,
		Expectation: sample.Expectation,
	}
}

func cloneOutputs(o map[string]map[string]any) map[string]map[string]any {
	if o == nil {
		return nil
	}
	out := make(map[string]map[string]any, len(o))
	for k, v := range o {
		out[k] = request.CloneMap(v)
	}
	return out
}

// scrambleContent 改动 c 里每一处引用类字段。
func scrambleContent(c *request.Content) {
	c.Rendered = "changed"
	c.Params["svc"] = "changed"
	c.Params["opts"].([]any)[1].(map[string]any)["k"] = "changed"
	c.SecretKeys[0] = "changed"
	c.Unit.ID = "changed"
}

func scrambleExpectation(e *request.Expectation) {
	e.Intent = "changed"
	e.Targets[0] = "changed"
	e.SuccessCriteria[0].Expr = "changed"
	*e.ExpectedChange.Ranges[0].Min = 99
}

// RegistryContract 检查 ports.Registry。newPort 收到种子，返回已登记这些单元、能渲染种子步骤的端口。
// 检查：登记信息一致；不存在返回 core.not_found；RenderStep 返回种子里的结果，结果不与后面的调用共享内存，
// 不修改入参；已取消的 ctx 返回错误。
func RegistryContract(t *testing.T, newPort func(seed RegistrySeed) ports.Registry) {
	seed := SampleRegistrySeed()

	t.Run("registered units come back as registered", func(t *testing.T) {
		p := newPort(seed)
		for _, want := range seed.Units {
			got, err := p.Unit(bg, want.Ref.ID, want.Ref.Version)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("unit %s@%d: got %+v, %v", want.Ref.ID, want.Ref.Version, got, err)
			}
		}
	})

	t.Run("unknown unit or version is not_found", func(t *testing.T) {
		p := newPort(seed)
		_, err := p.Unit(bg, "nope", 1)
		wantCode(t, err, errcode.NotFound)
		_, err = p.Unit(bg, "unit-1", 99)
		wantCode(t, err, errcode.NotFound)
	})

	t.Run("render step returns the seeded content and expectation", func(t *testing.T) {
		c, e, err := newPort(seed).RenderStep(bg, seed.Def, seed.Step, seed.Params, seed.Outputs)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(c, seed.Content) || !reflect.DeepEqual(e, seed.Expectation) {
			t.Fatalf("got %+v / %+v", c, e)
		}
	})

	t.Run("render step results share no memory with later calls", func(t *testing.T) {
		p := newPort(seed)
		c1, e1, err := p.RenderStep(bg, seed.Def, seed.Step, seed.Params, seed.Outputs)
		if err != nil {
			t.Fatal(err)
		}
		c2, e2, err := p.RenderStep(bg, seed.Def, seed.Step, seed.Params, seed.Outputs)
		if err != nil {
			t.Fatal(err)
		}
		scrambleContent(&c1)
		scrambleExpectation(&e1)
		if !reflect.DeepEqual(c2, seed.Content) || !reflect.DeepEqual(e2, seed.Expectation) {
			t.Fatal("two results share memory")
		}
		c3, e3, err := p.RenderStep(bg, seed.Def, seed.Step, seed.Params, seed.Outputs)
		if err != nil || !reflect.DeepEqual(c3, seed.Content) || !reflect.DeepEqual(e3, seed.Expectation) {
			t.Fatalf("later call changed: %+v / %+v, %v", c3, e3, err)
		}
	})

	t.Run("render step does not modify its inputs", func(t *testing.T) {
		def, params, outputs := seed.Def.Clone(), request.CloneMap(seed.Params), cloneOutputs(seed.Outputs)
		step := def.Steps[1]
		if _, _, err := newPort(seed).RenderStep(bg, def, step, params, outputs); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(def, seed.Def) || !reflect.DeepEqual(params, seed.Params) || !reflect.DeepEqual(outputs, seed.Outputs) {
			t.Fatal("RenderStep modified its inputs")
		}
	})

	t.Run("a cancelled context is an error", func(t *testing.T) {
		p := newPort(seed)
		if _, err := p.Unit(canceledCtx(), "unit-1", 1); err == nil {
			t.Fatal("Unit with cancelled context succeeded")
		}
		if _, _, err := p.RenderStep(canceledCtx(), seed.Def, seed.Step, seed.Params, seed.Outputs); err == nil {
			t.Fatal("RenderStep with cancelled context succeeded")
		}
	})
}

// ScopeTargets 是属于某个项目和环境的目标（稳定 ID）。
type ScopeTargets struct {
	Project, Environment string
	Targets              []string
}

// ScopeSeed 是 ScopeSourceContract 的种子。
type ScopeSeed struct {
	Sessions map[string]ports.SessionInfo
	Targets  []ScopeTargets
}

// SampleScopeSeed 返回契约使用的种子：session-1 属于 proj/prod，session-2 属于 proj/dev。
func SampleScopeSeed() ScopeSeed {
	return ScopeSeed{
		Sessions: map[string]ports.SessionInfo{
			"session-1": {Project: "proj", Environment: "prod", Mode: consts.SessionAuto},
			"session-2": {Project: "proj", Environment: "dev", Mode: consts.SessionManual},
		},
		Targets: []ScopeTargets{
			{Project: "proj", Environment: "prod", Targets: []string{"host-a", "host-b"}},
			{Project: "proj", Environment: "dev", Targets: []string{"host-dev"}},
		},
	}
}

// ScopeSourceContract 检查 ports.ScopeSource。newPort 收到种子，返回已登记这些会话和目标的端口。
// 检查：会话信息一致；不存在返回 core.not_found；范围外的目标按传入顺序返回，项目或环境不同都算范围外；
// 不修改入参，返回值不与入参共享；已取消的 ctx 返回错误。
func ScopeSourceContract(t *testing.T, newPort func(seed ScopeSeed) ports.ScopeSource) {
	seed := SampleScopeSeed()

	t.Run("session info comes back as registered", func(t *testing.T) {
		p := newPort(seed)
		for id, want := range seed.Sessions {
			got, err := p.Session(bg, id)
			if err != nil || got != want {
				t.Fatalf("session %s: got %+v, %v", id, got, err)
			}
		}
	})

	t.Run("unknown session is not_found", func(t *testing.T) {
		_, err := newPort(seed).Session(bg, "nope")
		wantCode(t, err, errcode.NotFound)
	})

	t.Run("targets in scope give an empty answer", func(t *testing.T) {
		got, err := newPort(seed).OutsideScope(bg, "proj", "prod", []string{"host-b", "host-a"})
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
		got, err = newPort(seed).OutsideScope(bg, "proj", "prod", nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("no targets: got %v, %v", got, err)
		}
	})

	t.Run("targets outside come back in the asked order", func(t *testing.T) {
		got, err := newPort(seed).OutsideScope(bg, "proj", "prod", []string{"zed", "host-a", "alpha", "host-dev"})
		if err != nil || !reflect.DeepEqual(got, []string{"zed", "alpha", "host-dev"}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("another environment, project or an unknown scope is outside", func(t *testing.T) {
		p := newPort(seed)
		for _, sc := range [][2]string{{"proj", "dev"}, {"other", "prod"}, {"proj", "nowhere"}} {
			got, err := p.OutsideScope(bg, sc[0], sc[1], []string{"host-a"})
			if err != nil || !reflect.DeepEqual(got, []string{"host-a"}) {
				t.Fatalf("%v: got %v, %v", sc, got, err)
			}
		}
		got, err := p.OutsideScope(bg, "proj", "dev", []string{"host-dev"})
		if err != nil || len(got) != 0 {
			t.Fatalf("own target in dev: got %v, %v", got, err)
		}
	})

	t.Run("input is not modified and the result is not shared", func(t *testing.T) {
		p := newPort(seed)
		asked := []string{"zed", "host-a", "alpha"}
		got, err := p.OutsideScope(bg, "proj", "prod", asked)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(asked, []string{"zed", "host-a", "alpha"}) {
			t.Fatalf("input was modified: %v", asked)
		}
		for i := range got {
			got[i] = "changed"
		}
		if !reflect.DeepEqual(asked, []string{"zed", "host-a", "alpha"}) {
			t.Fatalf("result shares memory with the input: %v", asked)
		}
		again, err := p.OutsideScope(bg, "proj", "prod", asked)
		if err != nil || !reflect.DeepEqual(again, []string{"zed", "alpha"}) {
			t.Fatalf("later call: got %v, %v", again, err)
		}
	})

	t.Run("a cancelled context is an error", func(t *testing.T) {
		p := newPort(seed)
		if _, err := p.Session(canceledCtx(), "session-1"); err == nil {
			t.Fatal("Session with cancelled context succeeded")
		}
		if _, err := p.OutsideScope(canceledCtx(), "proj", "prod", []string{"zed"}); err == nil {
			t.Fatal("OutsideScope with cancelled context succeeded")
		}
	})
}

// requestUntouched 检查调用没有改动传入的申请：深比较和两个摘要都不变。
func requestUntouched(t *testing.T, r, before *request.ExecutionRequest, cd, fd ids.Digest) {
	t.Helper()
	if !reflect.DeepEqual(r, before) {
		t.Fatalf("the port modified the request:\n got %+v\nwant %+v", r, before)
	}
	cd2, err1 := r.ContentDigest()
	fd2, err2 := r.FreezeDigest()
	if err1 != nil || err2 != nil || cd2 != cd || fd2 != fd {
		t.Fatalf("the port changed a digest: content %s -> %s, freeze %s -> %s (%v, %v)", cd, cd2, fd, fd2, err1, err2)
	}
}

// checkingSample 是检验中的申请，用于预演、指纹和 dcg。
func checkingSample() *request.ExecutionRequest {
	r := SampleRequest("c1")
	r.State = consts.RequestChecking
	return r
}

// frozenSample 是已冻结的申请，用于执行器。
func frozenSample() *request.ExecutionRequest {
	r := SampleRequest("c1")
	r.State = consts.RequestFrozen
	fd, _ := r.FreezeDigest()
	r.Frozen = &request.Frozen{Digest: fd, At: Epoch}
	return r
}

// requestPortContract 是四个"收申请、回结果"的端口共用的检查：不改申请；已取消的 ctx 返回错误。
func requestPortContract(t *testing.T, mk func() *request.ExecutionRequest, call func(ctx context.Context, r *request.ExecutionRequest) error) {
	t.Run("the request is not modified", func(t *testing.T) {
		r := mk()
		before := r.Clone()
		cd, _ := r.ContentDigest()
		fd, _ := r.FreezeDigest()
		if err := call(bg, r); err != nil {
			t.Fatal(err)
		}
		requestUntouched(t, r, before, cd, fd)
	})
	t.Run("a cancelled context is an error and the request stays untouched", func(t *testing.T) {
		r := mk()
		before := r.Clone()
		cd, _ := r.ContentDigest()
		fd, _ := r.FreezeDigest()
		if err := call(canceledCtx(), r); err == nil {
			t.Fatal("call with cancelled context succeeded")
		}
		requestUntouched(t, r, before, cd, fd)
	})
}

// PreviewPortContract 检查 ports.PreviewPort：结果状态合法；不改申请；已取消的 ctx 返回错误。
func PreviewPortContract(t *testing.T, newPort func() ports.PreviewPort) {
	t.Run("the result status is valid", func(t *testing.T) {
		res, err := newPort().Preview(bg, checkingSample())
		if err != nil || !res.Status.Valid() {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
	p := newPort()
	requestPortContract(t, checkingSample, func(ctx context.Context, r *request.ExecutionRequest) error {
		_, err := p.Preview(ctx, r)
		return err
	})
}

// FingerprintPortContract 检查 ports.FingerprintPort：状态合法；状态为 collected 时值非空；
// 目标没变时连续采集的值相同；不改申请；已取消的 ctx 返回错误。
func FingerprintPortContract(t *testing.T, newPort func() ports.FingerprintPort) {
	t.Run("the result is valid; collected has a value", func(t *testing.T) {
		fp, err := newPort().Collect(bg, checkingSample())
		if err != nil || !fp.Status.Valid() {
			t.Fatalf("got %+v, %v", fp, err)
		}
		if fp.Status == consts.FingerprintCollected && fp.Value == "" {
			t.Fatalf("collected fingerprint has no value: %+v", fp)
		}
	})
	t.Run("an unchanged target gives the same value", func(t *testing.T) {
		p := newPort()
		a, err := p.Collect(bg, checkingSample())
		if err != nil {
			t.Fatal(err)
		}
		b, err := p.Collect(bg, checkingSample())
		if err != nil {
			t.Fatal(err)
		}
		if a.Status == consts.FingerprintCollected && b.Status == consts.FingerprintCollected && a.Value != b.Value {
			t.Fatalf("values differ: %q vs %q", a.Value, b.Value)
		}
	})
	p := newPort()
	requestPortContract(t, checkingSample, func(ctx context.Context, r *request.ExecutionRequest) error {
		_, err := p.Collect(ctx, r)
		return err
	})
}

// DCGPortContract 检查 ports.DCGPort：结论合法；结果之间不共享内存；不改申请；已取消的 ctx 返回错误。
// "不共享内存"只在结果带规则 ID 时有内容可检查。
func DCGPortContract(t *testing.T, newPort func() ports.DCGPort) {
	t.Run("the result is valid", func(t *testing.T) {
		res, err := newPort().Check(bg, checkingSample())
		if err != nil || !res.Result.Valid() {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
	t.Run("results share no memory", func(t *testing.T) {
		p := newPort()
		a, err := p.Check(bg, checkingSample())
		if err != nil {
			t.Fatal(err)
		}
		b, err := p.Check(bg, checkingSample())
		if err != nil {
			t.Fatal(err)
		}
		snapshot := append([]string(nil), b.RuleIDs...)
		for i := range a.RuleIDs {
			a.RuleIDs[i] = "changed"
		}
		if !reflect.DeepEqual(append([]string(nil), b.RuleIDs...), snapshot) {
			t.Fatalf("two results share memory: %v", b.RuleIDs)
		}
	})
	p := newPort()
	requestPortContract(t, checkingSample, func(ctx context.Context, r *request.ExecutionRequest) error {
		_, err := p.Check(ctx, r)
		return err
	})
}

// ExecutorPortContract 检查 ports.ExecutorPort：结果的结束时间不早于开始时间；结果之间不共享内存；
// 不改申请；已取消的 ctx 返回错误。申请是已冻结的样例，实现应能无害地执行它。
func ExecutorPortContract(t *testing.T, newPort func() ports.ExecutorPort) {
	t.Run("the result times are in order", func(t *testing.T) {
		res, err := newPort().Execute(bg, frozenSample())
		if err != nil {
			t.Fatal(err)
		}
		if !res.StartedAt.IsZero() && !res.EndedAt.IsZero() && res.EndedAt.Before(res.StartedAt) {
			t.Fatalf("ended %v before started %v", res.EndedAt, res.StartedAt)
		}
	})
	t.Run("results share no memory", func(t *testing.T) {
		p := newPort()
		a, err := p.Execute(bg, frozenSample())
		if err != nil {
			t.Fatal(err)
		}
		b, err := p.Execute(bg, frozenSample())
		if err != nil {
			t.Fatal(err)
		}
		snapshot := b.Clone()
		if a.ExitCode != nil {
			*a.ExitCode = 99
		}
		for k := range a.Output {
			a.Output[k] = "changed"
		}
		if a.Output != nil {
			a.Output["added"] = "changed"
		}
		if !reflect.DeepEqual(&b, snapshot) {
			t.Fatalf("two results share memory: %+v", b)
		}
	})
	p := newPort()
	requestPortContract(t, frozenSample, func(ctx context.Context, r *request.ExecutionRequest) error {
		_, err := p.Execute(ctx, r)
		return err
	})
}

// AuditSinkContract 检查 ports.AuditSink。records 读出该端口已记录的全部审计记录（按记录顺序）；
// 内存替身用 MemAuditSink.Records，provider 用自己的读取方式。
// 检查：按追加顺序保存且内容一致；不与入参、读出的记录共享内存；已取消的 ctx 返回错误且什么都不记；并发追加不丢。
func AuditSinkContract(t *testing.T, newPort func() ports.AuditSink, records func(ports.AuditSink) []ports.AuditRecord) {
	rec := func(i int) ports.AuditRecord {
		return ports.AuditRecord{
			At: Epoch.Add(time.Duration(i) * time.Second), Actor: "u", Action: "act-" + string(rune('a'+i)),
			CallID: "c1", Revision: ids.FirstRevision, RunID: "run-1",
			Detail: map[string]any{"k": []any{"v", map[string]any{"n": i}}},
		}
	}

	t.Run("records keep the append order", func(t *testing.T) {
		s := newPort()
		var want []ports.AuditRecord
		for i := 0; i < 6; i++ {
			want = append(want, rec(i))
			if err := s.Append(bg, rec(i)); err != nil {
				t.Fatal(err)
			}
		}
		if got := records(s); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v\nwant %+v", got, want)
		}
	})

	t.Run("no aliasing in either direction", func(t *testing.T) {
		s := newPort()
		r := rec(0)
		if err := s.Append(bg, r); err != nil {
			t.Fatal(err)
		}
		r.Detail["k"].([]any)[0] = "changed"
		r.Detail["added"] = true
		got := records(s)
		if len(got) != 1 || !reflect.DeepEqual(got[0], rec(0)) {
			t.Fatalf("sink aliases the appended record: %+v", got)
		}
		got[0].Detail["k"].([]any)[1].(map[string]any)["n"] = 99
		if again := records(s); !reflect.DeepEqual(again, []ports.AuditRecord{rec(0)}) {
			t.Fatalf("records returns aliased data: %+v", again)
		}
	})

	t.Run("a cancelled context is an error and records nothing", func(t *testing.T) {
		s := newPort()
		if err := s.Append(canceledCtx(), rec(0)); err == nil {
			t.Fatal("Append with cancelled context succeeded")
		}
		if got := records(s); len(got) != 0 {
			t.Fatalf("recorded %d records", len(got))
		}
	})

	t.Run("concurrent appends are all kept", func(t *testing.T) {
		s := newPort()
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_ = s.Append(bg, rec(i%6))
			}(i)
		}
		wg.Wait()
		if got := records(s); len(got) != 16 {
			t.Fatalf("recorded %d records, want 16", len(got))
		}
	})
}

// ClockContract 检查 ports.Clock：时间非零；连续读取不倒退；并发读取安全。
func ClockContract(t *testing.T, newClock func() ports.Clock) {
	t.Run("now is not zero", func(t *testing.T) {
		if now := newClock().Now(); now.IsZero() {
			t.Fatal("Now returned the zero time")
		}
	})
	t.Run("time does not go backwards", func(t *testing.T) {
		c := newClock()
		prev := c.Now()
		for i := 0; i < 100; i++ {
			now := c.Now()
			if now.Before(prev) {
				t.Fatalf("time went backwards: %v after %v", now, prev)
			}
			prev = now
		}
	})
	t.Run("concurrent reads are safe", func(t *testing.T) {
		c := newClock()
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					if c.Now().IsZero() {
						t.Error("Now returned the zero time")
					}
				}
			}()
		}
		wg.Wait()
	})
}
