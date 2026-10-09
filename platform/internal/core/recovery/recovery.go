// Package recovery 实现恢复即停止（K-24；61 第 8.1 节，13 第 2 节）。
// 服务启动时调用 OnStartup：所有在途的执行申请和任务运行都停下，
// 执行中的申请记为结果未知，等人工核对。恢复不续跑、不重试、不重新执行任何东西，
// 所以 Deps 里没有执行器端口。
package recovery

import (
	"context"
	"fmt"
	"sort"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	creq "github.com/LeXwDeX/DevOps-Agent/platform/internal/core/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	mreq "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// StreamPrefix 是恢复事件流 ID 的前缀；恢复事件不伴随状态保存，经 EventSink 发出。
const StreamPrefix = "recovery:"

// Deps 是恢复依赖的端口。
type Deps struct {
	Requests ports.RequestStore
	Tasks    ports.TaskStore
	Events   ports.EventSink
	Clock    ports.Clock
	IDGen    ports.IDGen
}

// Report 是恢复报告，供页面展示。各列表已排序。
type Report struct {
	RecoveryID      string
	StoppedRequests []ids.CallID // 已停止（恢复中断）的申请
	UnknownRequests []ids.CallID // 恢复前正在执行，结果未知，需人工核对
	StoppedRuns     []string     // 已停止（恢复中断）的任务运行
	Errors          []string     // 没处理成的项和事件发送失败，其余项不受影响
}

// OnStartup 执行恢复：
//
//  1. 经 EventSink 发出 devops.recovery.started；
//  2. 非终态申请：executing → unknown（devops.execution.unknown），
//     其余 → stopped，原因 recovery_interrupted（devops.call.stopped）；
//  3. 非终态运行 → recovery_interrupted（devops.task.stopped）；
//  4. 发出 devops.recovery.completed，Data 带各项数量。
//
// 终态对象不碰（Version 不变）。某一项遇到 CAS 冲突时重读一次，仍是非终态就重试一次；
// 还不行记入 Errors 并继续处理其余项。事件发送失败同样只记入 Errors：
// 事件丢了不能让在途对象继续悬着。列出非终态对象失败时无法继续，返回已有的报告和存储端口的原错误。
// 重复调用结果相同：第二次没有非终态对象，报告为空。
func OnStartup(ctx context.Context, d Deps) (Report, error) {
	if d.Requests == nil || d.Tasks == nil || d.Events == nil || d.Clock == nil || d.IDGen == nil {
		return Report{}, errcode.New(errcode.InvalidArgument, "recovery: all deps are required")
	}
	r := &recoverer{d: d}
	r.rep.RecoveryID = d.IDGen.NewID("recovery")
	r.stream = StreamPrefix + r.rep.RecoveryID
	r.emit(ctx, events.RecoveryStarted, map[string]any{"recovery_id": r.rep.RecoveryID})

	reqs, err := d.Requests.ListNonTerminal(ctx)
	if err != nil {
		return r.finish(), err
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].CallID < reqs[j].CallID })
	for _, req := range reqs {
		r.request(ctx, req)
	}

	runs, err := d.Tasks.ListNonTerminalRuns(ctx)
	if err != nil {
		return r.finish(), err
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].RunID < runs[j].RunID })
	for _, run := range runs {
		r.run(ctx, run)
	}

	r.emit(ctx, events.RecoveryCompleted, map[string]any{
		"recovery_id":      r.rep.RecoveryID,
		"stopped_requests": len(r.rep.StoppedRequests),
		"unknown_requests": len(r.rep.UnknownRequests),
		"stopped_runs":     len(r.rep.StoppedRuns),
		"errors":           len(r.rep.Errors),
	})
	return r.finish(), nil
}

type recoverer struct {
	d      Deps
	rep    Report
	stream string
	seq    int64
	// outcome 记录本次恢复里申请的新状态，用来更新运行里对应的步骤记录。
	outcome map[ids.CallID]consts.RequestState
}

func (r *recoverer) finish() Report {
	sort.Slice(r.rep.StoppedRequests, func(i, j int) bool { return r.rep.StoppedRequests[i] < r.rep.StoppedRequests[j] })
	sort.Slice(r.rep.UnknownRequests, func(i, j int) bool { return r.rep.UnknownRequests[i] < r.rep.UnknownRequests[j] })
	sort.Strings(r.rep.StoppedRuns)
	sort.Strings(r.rep.Errors)
	return r.rep
}

func (r *recoverer) fail(format string, args ...any) {
	r.rep.Errors = append(r.rep.Errors, fmt.Sprintf(format, args...))
}

func (r *recoverer) eventID() string { return r.d.IDGen.NewID("event") }

// emit 经 EventSink 发出恢复事件；它不伴随状态保存。
func (r *recoverer) emit(ctx context.Context, t events.Type, data map[string]any) {
	r.seq++
	e := events.Event{
		SchemaVersion: core.EventV1,
		EventID:       r.eventID(),
		Type:          t,
		StreamID:      r.stream,
		Sequence:      r.seq,
		OccurredAt:    r.d.Clock.Now(),
		SourceKind:    events.SourceHostConfirmed,
		Data:          data,
	}
	if err := r.d.Events.Emit(ctx, e); err != nil {
		r.fail("emit %s: %v", t, err)
	}
}

func isCAS(err error) bool {
	c, ok := errcode.CodeOf(err)
	return ok && c == errcode.CASConflict
}

// request 处理一个申请。
func (r *recoverer) request(ctx context.Context, req *mreq.ExecutionRequest) {
	for attempt := 0; ; attempt++ {
		if req.State.IsTerminal() {
			return
		}
		to, err := r.rewriteRequest(ctx, req)
		if err == nil {
			if r.outcome == nil {
				r.outcome = map[ids.CallID]consts.RequestState{}
			}
			r.outcome[req.CallID] = to
			if to == consts.RequestUnknown {
				r.rep.UnknownRequests = append(r.rep.UnknownRequests, req.CallID)
			} else {
				r.rep.StoppedRequests = append(r.rep.StoppedRequests, req.CallID)
			}
			return
		}
		if !isCAS(err) || attempt > 0 {
			r.fail("request %s: %v", req.CallID, err)
			return
		}
		fresh, gerr := r.d.Requests.Get(ctx, req.CallID)
		if gerr != nil {
			r.fail("request %s: re-read after conflict: %v", req.CallID, gerr)
			return
		}
		req = fresh
	}
}

// rewriteRequest 按规则改写并保存申请，返回新状态。req 在失败时不应再使用。
func (r *recoverer) rewriteRequest(ctx context.Context, req *mreq.ExecutionRequest) (consts.RequestState, error) {
	now := r.d.Clock.Now()
	w := req.Clone()
	expected := w.Version
	var et events.Type
	data := map[string]any{"reason": string(consts.StopRecoveryInterrupted)}
	if w.State == consts.RequestExecuting {
		// 执行中 → 结果未知：状态机里有 executing → unknown 这条边。
		if err := creq.Transition(w, consts.RequestUnknown, now); err != nil {
			return "", err
		}
		if w.Execution == nil {
			w.Execution = &mreq.Execution{}
		}
		w.Execution.Outcome = consts.RequestUnknown
		w.Execution.Detail = string(consts.StopRecoveryInterrupted)
		et = events.ExecutionUnknown
	} else {
		if err := creq.Stop(w, consts.StopRecoveryInterrupted, "service restarted or restored", now); err != nil {
			return "", err
		}
		et = events.CallStopped
	}
	w.Version = expected.Next()
	ev := events.ForRequest(w, et, data, now, r.eventID())
	if err := r.d.Requests.Save(ctx, w, expected, []events.Event{ev}); err != nil {
		return "", err
	}
	return w.State, nil
}

// run 处理一个任务运行。
func (r *recoverer) run(ctx context.Context, run *task.Run) {
	for attempt := 0; ; attempt++ {
		if run.State.IsTerminal() {
			return
		}
		err := r.rewriteRun(ctx, run)
		if err == nil {
			r.rep.StoppedRuns = append(r.rep.StoppedRuns, run.RunID)
			return
		}
		if !isCAS(err) || attempt > 0 {
			r.fail("run %s: %v", run.RunID, err)
			return
		}
		fresh, gerr := r.d.Tasks.GetRun(ctx, run.RunID)
		if gerr != nil {
			r.fail("run %s: re-read after conflict: %v", run.RunID, gerr)
			return
		}
		run = fresh
	}
}

func (r *recoverer) rewriteRun(ctx context.Context, run *task.Run) error {
	now := r.d.Clock.Now()
	w := run.Clone()
	expected := w.Version
	w.State = consts.RunRecoveryInterrupted
	w.MissingInputs = nil
	w.Stop = &task.RunStop{Reason: consts.StopRecoveryInterrupted, StepID: w.Current, At: now}
	// 当前步骤的申请在本次恢复里被改写过，运行里的记录跟着更新。
	if n := len(w.Steps); n > 0 && w.Current != "" && w.Steps[n-1].StepID == w.Current {
		if st, ok := r.outcome[w.Steps[n-1].CallID]; ok {
			w.Steps[n-1].State = st
		}
	}
	w.Version = expected.Next()
	w.UpdatedAt = now
	data := map[string]any{"reason": string(consts.StopRecoveryInterrupted)}
	if w.Current != "" {
		data["step_id"] = w.Current
	}
	ev := events.ForRun(w, events.TaskStopped, data, now, r.eventID())
	return r.d.Tasks.SaveRun(ctx, w, expected, []events.Event{ev})
}
