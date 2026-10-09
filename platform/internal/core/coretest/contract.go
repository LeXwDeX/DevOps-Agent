package coretest

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
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
