package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/orchestrator/expr"
	creq "github.com/LeXwDeX/DevOps-Agent/platform/internal/core/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	mreq "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// stepStopReason 把步骤的非成功终态映射为停止原因（61 第 7.4 节）。
var stepStopReason = map[consts.RequestState]consts.StopReason{
	consts.RequestMismatched: consts.StopMismatched,
	consts.RequestUnknown:    consts.StopUnknown,
	consts.RequestFailed:     consts.StopFailed,
	consts.RequestRejected:   consts.StopRejected,
}

// Advance 在当前步骤的申请状态变化后调用，推进运行（61 第 7.4 节）：
//
//   - draft / checking / frozen / executing：运行为 running；
//   - awaiting_review / awaiting_revision：运行为 awaiting_review。
//     这两类只在运行状态变化时保存；
//   - succeeded：记录步骤输出，按无条件 Next 或按顺序求值分支条件选下一步，
//     没有后续则 completed，否则创建下一步的草稿申请并返回它；
//   - mismatched / unknown / failed / rejected / stopped：运行 stopped，原因同步骤；
//   - target_changed：运行 stopped，并把步骤申请也停止（登记过的步骤不由 Agent 修订）。
//
// 没有分支匹配，或分支条件求值出错（例如缺字段），运行 stopped，原因 no_branch_matched，
// 错误码写在 Detail 里：缺字段是停止，不是"假"。
// 运行已在终态返回 core.run_invalid_state；申请不是当前步骤的当前申请返回 core.invalid_argument。
// 调用方传入的申请只是副本：引擎按 CallID 重读存储里的记录，Version 落后返回 core.cas_conflict
// （重读后重试），状态或修订号对不上返回 core.invalid_argument；推进只依据存储里的状态和输出，
// 不依据副本的说法。返回的申请只在创建了下一步时非 nil，由调用方送进流水线。
func (e *Engine) Advance(ctx context.Context, runID string, step *mreq.ExecutionRequest) (*task.Run, *mreq.ExecutionRequest, error) {
	if step == nil {
		return nil, nil, errcode.New(errcode.InvalidArgument, "step request is required")
	}
	if !step.State.Valid() {
		return nil, nil, errcode.Newf(errcode.InvalidArgument, "unknown request state %q", step.State)
	}
	run, err := e.d.Tasks.GetRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	if run.State.IsTerminal() || run.Current == "" {
		return nil, nil, errcode.Newf(errcode.RunInvalidState, "run %s is %s", runID, run.State)
	}
	idx, err := currentRecord(run, step)
	if err != nil {
		return nil, nil, err
	}
	step, err = e.storedStep(ctx, step)
	if err != nil {
		return nil, nil, err
	}
	def, progs, err := e.loadForRun(ctx, run)
	if err != nil {
		return nil, nil, err
	}
	now := e.d.Clock.Now()
	expected := run.Version

	switch step.State {
	case consts.RequestDraft, consts.RequestChecking, consts.RequestFrozen, consts.RequestExecuting:
		return e.followStep(ctx, run, idx, step.State, consts.RunRunning, now)
	case consts.RequestAwaitingReview, consts.RequestAwaitingRevision:
		return e.followStep(ctx, run, idx, step.State, consts.RunAwaitingReview, now)

	case consts.RequestSucceeded:
		return e.stepSucceeded(ctx, def, progs, run, idx, step, now)

	case consts.RequestStopped:
		if step.Stop == nil {
			return nil, nil, errcode.Newf(errcode.InvalidArgument, "call %s is stopped without a stop reason", step.CallID)
		}
		return e.stopRun(ctx, run, idx, step.State, step.Stop.Reason, step.Stop.Detail, expected, now)

	case consts.RequestTargetChanged:
		// 先停步骤申请再停运行：运行保存失败时，重试会走 stopped 分支收尾。
		if err := e.stopTargetChanged(ctx, run, step, now); err != nil {
			return nil, nil, err
		}
		return e.stopRun(ctx, run, idx, step.State, consts.StopTargetChanged,
			"target changed before execution; registered steps are not revised", expected, now)
	}

	reason, ok := stepStopReason[step.State]
	if !ok {
		return nil, nil, errcode.Newf(errcode.InvalidArgument, "unhandled request state %q", step.State)
	}
	return e.stopRun(ctx, run, idx, step.State, reason, stepDetail(step), expected, now)
}

// storedStep 按 CallID 重读存储里的申请，并核对调用方的副本与它一致：
// Version 落后（或超前）返回 core.cas_conflict；同一 Version 下状态或修订号不同，副本就不是
// 存储里的记录，返回 core.invalid_argument。返回存储里的记录，之后的推进都以它为准。
func (e *Engine) storedStep(ctx context.Context, step *mreq.ExecutionRequest) (*mreq.ExecutionRequest, error) {
	stored, err := e.d.Requests.Get(ctx, step.CallID)
	if err != nil {
		return nil, err
	}
	if stored.Version != step.Version {
		return nil, errcode.Newf(errcode.CASConflict, "call %s: stored version %d, caller holds %d; re-read and retry", step.CallID, stored.Version, step.Version)
	}
	if stored.State != step.State || stored.Revision != step.Revision {
		return nil, errcode.Newf(errcode.InvalidArgument, "call %s: caller's copy (%s, revision %d) differs from the stored record (%s, revision %d)",
			step.CallID, step.State, step.Revision, stored.State, stored.Revision)
	}
	return stored, nil
}

// currentRecord 核对申请就是运行当前步骤的当前申请，返回它在 run.Steps 里的下标。
func currentRecord(run *task.Run, step *mreq.ExecutionRequest) (int, error) {
	if step.Task == nil || step.Task.RunID != run.RunID || step.Task.StepID != run.Current {
		return -1, errcode.Newf(errcode.InvalidArgument, "call %s is not the current step (%s) of run %s", step.CallID, run.Current, run.RunID)
	}
	for i := len(run.Steps) - 1; i >= 0; i-- {
		if run.Steps[i].StepID == run.Current {
			if run.Steps[i].CallID != step.CallID {
				return -1, errcode.Newf(errcode.InvalidArgument, "call %s is not the recorded call of step %s in run %s", step.CallID, run.Current, run.RunID)
			}
			return i, nil
		}
	}
	return -1, errcode.Newf(errcode.InvalidArgument, "run %s has no record of step %s", run.RunID, run.Current)
}

// followStep 处理运行中和待人工两类状态：运行状态不变就什么都不写。
func (e *Engine) followStep(ctx context.Context, run *task.Run, idx int, st consts.RequestState, target consts.RunState, now time.Time) (*task.Run, *mreq.ExecutionRequest, error) {
	if run.State == target {
		return run, nil, nil
	}
	expected := run.Version
	run.State = target
	run.Steps[idx].State = st
	if err := e.saveRun(ctx, run, expected, now, nil); err != nil {
		return nil, nil, err
	}
	return run, nil, nil
}

// stopRun 把运行停在当前步骤并产生 devops.task.stopped。
func (e *Engine) stopRun(ctx context.Context, run *task.Run, idx int, st consts.RequestState, reason consts.StopReason, detail string, expected ids.Version, now time.Time) (*task.Run, *mreq.ExecutionRequest, error) {
	run.Steps[idx].State = st
	setStopped(run, consts.RunStopped, reason, run.Current, detail, now)
	ev := e.stoppedEvent(run, "", now)
	if err := e.saveRun(ctx, run, expected, now, []events.Event{ev}); err != nil {
		return nil, nil, err
	}
	return run, nil, nil
}

// stopTargetChanged 停止进入 target_changed 的步骤申请，原因 target_changed。
// 申请已被别人停止（重试）时什么都不做。
func (e *Engine) stopTargetChanged(ctx context.Context, run *task.Run, step *mreq.ExecutionRequest, now time.Time) error {
	r := step.Clone()
	expected := r.Version
	if err := creq.Stop(r, consts.StopTargetChanged, "registered step: target changed, not revised by the agent", now); err != nil {
		return err
	}
	r.Version = expected.Next()
	ev := events.ForRequest(r, events.CallStopped, map[string]any{
		"reason":  string(consts.StopTargetChanged),
		"run_id":  run.RunID,
		"step_id": run.Current,
	}, now, e.eventID())
	return e.d.Requests.Save(ctx, r, expected, []events.Event{ev})
}

// stepSucceeded 记录输出、选下一步，并创建下一步申请或结束运行。
func (e *Engine) stepSucceeded(ctx context.Context, def *task.Definition, progs map[string][]*expr.Program, run *task.Run, idx int, step *mreq.ExecutionRequest, now time.Time) (*task.Run, *mreq.ExecutionRequest, error) {
	expected := run.Version
	rec := &run.Steps[idx]
	rec.State = consts.RequestSucceeded
	rec.Output = nil
	if step.Execution != nil && step.Execution.Result != nil {
		rec.Output = mreq.CloneMap(step.Execution.Result.Output)
	}
	stepDef, ok := def.Step(run.Current)
	if !ok {
		return nil, nil, errcode.Newf(errcode.TaskInvalid, "run %s: step %q is not in the definition", run.RunID, run.Current)
	}

	next, noMatch := stepDef.Next, ""
	if len(stepDef.Branches) > 0 {
		next, noMatch = chooseBranch(stepDef, progs[stepDef.ID], branchEnv(run))
	}
	rec.Next = next

	// 先备好下一步申请：生成失败时什么都没写，调用方可以原样重试。
	var nextReq *mreq.ExecutionRequest
	var nextStep task.Step
	if noMatch == "" && next != "" {
		nextStep, ok = def.Step(next)
		if !ok {
			return nil, nil, errcode.Newf(errcode.TaskInvalid, "run %s: next step %q is not in the definition", run.RunID, next)
		}
		var err error
		if nextReq, err = e.newStepRequest(ctx, def, run, nextStep, now); err != nil {
			return nil, nil, err
		}
	}

	run.State = consts.RunRunning
	completed := events.ForRun(run, events.TaskStepCompleted, map[string]any{
		"step_id":       run.Current,
		"state":         string(consts.RequestSucceeded),
		"next":          next,
		"output_fields": sortedKeys(rec.Output),
	}, now, e.eventID())
	evts := []events.Event{completed}

	switch {
	case noMatch != "":
		setStopped(run, consts.RunStopped, consts.StopNoBranchMatched, run.Current, noMatch, now)
		evts = append(evts, e.stoppedEvent(run, "", now))
	case next == "":
		run.State = consts.RunCompleted
		evts = append(evts, events.ForRun(run, events.TaskCompleted, map[string]any{
			"task_id":      def.ID,
			"task_version": def.Version,
			"steps":        len(run.Steps),
		}, now, e.eventID()))
	default:
		attach(run, nextStep.ID, nextReq)
	}
	if err := e.saveRun(ctx, run, expected, now, evts); err != nil {
		return nil, nil, err
	}
	if nextReq != nil {
		if err := e.createRequest(ctx, run, nextReq, now); err != nil {
			return nil, nil, err
		}
	}
	return run, nextReq, nil
}

// chooseBranch 按顺序求值，第一条为真的生效。返回去向；没有匹配或求值出错时
// 去向为空，第二个返回值说明原因（出错时带错误码）。出错就停，不再看后面的分支。
func chooseBranch(step task.Step, progs []*expr.Program, env expr.Env) (string, string) {
	for i, p := range progs {
		ok, err := p.Eval(env)
		if err != nil {
			return "", fmt.Sprintf("branch #%d %q: %v", i, step.Branches[i].When, err)
		}
		if ok {
			return step.Branches[i].To, ""
		}
	}
	return "", "no branch condition matched"
}

// stepDetail 摘出步骤停止的说明：拒绝原因、对比结论或执行器错误。
func stepDetail(step *mreq.ExecutionRequest) string {
	if r := step.Rejection; r != nil {
		d := string(r.Source)
		if r.Code != "" {
			d += " " + string(r.Code)
		}
		if r.Reason != "" {
			d += ": " + r.Reason
		}
		return d
	}
	if x := step.Execution; x != nil {
		if x.Detail != "" {
			return x.Detail
		}
		if x.Result != nil {
			return x.Result.Err
		}
	}
	return ""
}

// Cancel 取消运行（61 第 7.4 节）：运行 cancelled，原因 cancelled_by_human。
// 当前步骤的申请不在终态且不在 executing 时一并停止；正在 executing 的不动，
// 由执行器报告结果，Detail 里注明。已在终态的运行返回 core.run_invalid_state。
func (e *Engine) Cancel(ctx context.Context, runID, actor string) (*task.Run, error) {
	run, err := e.d.Tasks.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.State.IsTerminal() {
		return nil, errcode.Newf(errcode.RunInvalidState, "run %s is already %s", runID, run.State)
	}
	now := e.d.Clock.Now()
	expected := run.Version
	detail := "cancelled by " + actor
	stepID := run.Current
	if n := len(run.Steps); n > 0 && stepID != "" {
		rec := &run.Steps[n-1]
		req, err := e.d.Requests.Get(ctx, rec.CallID)
		switch {
		case isCode(err, errcode.NotFound):
			// 申请没建成（遗留的运行），没有东西可停。
		case err != nil:
			return nil, err
		case req.State == consts.RequestExecuting:
			rec.State = req.State
			detail += "; step " + stepID + " is executing, its result will be reported by the executor"
		case !req.State.IsTerminal():
			if err := e.stopStepCancelled(ctx, run, req, detail, now); err != nil {
				return nil, err
			}
			rec.State = consts.RequestStopped
		default:
			rec.State = req.State
		}
	}
	setStopped(run, consts.RunCancelled, consts.StopCancelledByHuman, stepID, detail, now)
	ev := e.stoppedEvent(run, actor, now)
	if err := e.saveRun(ctx, run, expected, now, []events.Event{ev}); err != nil {
		return nil, err
	}
	return run, nil
}

// stopStepCancelled 停止步骤申请，原因 cancelled_by_human。
func (e *Engine) stopStepCancelled(ctx context.Context, run *task.Run, req *mreq.ExecutionRequest, detail string, now time.Time) error {
	expected := req.Version
	if err := creq.Stop(req, consts.StopCancelledByHuman, detail, now); err != nil {
		return err
	}
	req.Version = expected.Next()
	ev := events.ForRequest(req, events.CallStopped, map[string]any{
		"reason":  string(consts.StopCancelledByHuman),
		"run_id":  run.RunID,
		"step_id": run.Current,
	}, now, e.eventID())
	return e.d.Requests.Save(ctx, req, expected, []events.Event{ev})
}

func isCode(err error, c errcode.Code) bool {
	got, ok := errcode.CodeOf(err)
	return ok && got == c
}
