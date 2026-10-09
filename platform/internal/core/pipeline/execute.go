package pipeline

import (
	"context"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	corereq "github.com/LeXwDeX/DevOps-Agent/platform/internal/core/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/review"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// executorActor 是执行阶段审计记录的操作者。
const executorActor = "executor"

// 执行结果的固定原因文字。事件里只放这些固定文字，执行器的错误文本留在申请的 Execution 里（不进事件）。
const (
	reasonExecutorError = "executor returned no trustworthy result"
	reasonAuditDown     = "audit record unavailable, executor not called"
)

// 指纹不可用、人工已确认时，写进 execution.started 事件和审计的标记。
const (
	keyFingerprintConfirmed  = "fingerprint_unavailable_human_confirmed"
	noteFingerprintConfirmed = "指纹不可用，人工已确认"
)

// PreExecute 是执行前的最后核验（K-04），由执行器所在进程调用。只读不执行。
//
//   - 申请必须在 frozen，否则 core.invalid_transition / core.terminal_state。
//   - 重算冻结摘要，与 Frozen.Digest 不一致 → core.digest_mismatch，状态不变，不执行。
//   - 检验时采集到了指纹：重新采集；采集不到、出错或值与检验时不同 → 申请转 target_changed
//     （保存 devops.call.target_changed），返回 core.fingerprint_changed，不执行。
//   - 检验时指纹不可用（申请因此转了人工），且人工已对当前修订和摘要点 ▶ 执行：不做指纹比对，照常执行，
//     在 devops.execution.started 事件和审计里记"指纹不可用，人工已确认"。
//     检验时指纹不可用而没有人工批准的，以及指纹记录缺失或状态不合法的（不是检验写下的），按 target_changed 处理。
func (s *Service) PreExecute(ctx context.Context, id ids.CallID) error {
	t, err := s.begin(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.preExecute(ctx, t)
	return err
}

// preExecute 做执行前核验。confirmed 为真表示检验时指纹不可用、人工已确认，跳过了指纹比对。
func (s *Service) preExecute(ctx context.Context, t *txn) (confirmed bool, err error) {
	r := t.r
	if r.State != consts.RequestFrozen {
		return false, stateError(r, "be executed")
	}
	cur, err := r.FreezeDigest()
	if err != nil {
		return false, err
	}
	if r.Frozen == nil || cur != r.Frozen.Digest {
		return false, errcode.Newf(errcode.DigestMismatch, "call %s changed after freezing", r.CallID)
	}
	if r.Fingerprint == nil || r.Fingerprint.Status != consts.FingerprintCollected {
		// 只有检验时明确记为 unavailable、且人工已对当前修订和摘要点 ▶ 的申请才跳过比对。
		// 指纹缺失或状态不合法不是检验的结论（检验总会写下 collected 或 unavailable），按目标变化处理。
		if r.Fingerprint != nil && r.Fingerprint.Status == consts.FingerprintUnavailable && review.ApprovedFor(r) {
			return true, nil
		}
		// 没有人工批准，或指纹记录不是检验写下的：正常流程不会出现。按目标变化处理，不执行。
		return false, s.targetChanged(ctx, t, "no collected fingerprint at inspection")
	}
	fp, err := s.d.Fingerprint.Collect(ctx, r.Clone())
	switch {
	case err != nil:
		return false, s.targetChanged(ctx, t, "fingerprint collection failed")
	case fp.Status != consts.FingerprintCollected || fp.Value == "":
		return false, s.targetChanged(ctx, t, "fingerprint unavailable")
	case fp.Value != r.Fingerprint.Value:
		return false, s.targetChanged(ctx, t, "fingerprint differs from inspection")
	}
	return false, nil
}

// targetChanged 保存 frozen → target_changed 和事件，返回 core.fingerprint_changed。
func (s *Service) targetChanged(ctx context.Context, t *txn, reason string) error {
	if err := corereq.Transition(t.r, consts.RequestTargetChanged, s.d.Clock.Now()); err != nil {
		return err
	}
	data := revData(t.r)
	data["code"] = string(errcode.FingerprintChanged)
	data["reason"] = reason
	t.emit(events.CallTargetChanged, data)
	if _, err := t.save(ctx); err != nil {
		return err
	}
	return errcode.New(errcode.FingerprintChanged, reason)
}

// Execute 执行已冻结的申请：PreExecute → 保存 executing（含 devops.execution.started）→ 调用执行器 → Compare → 保存终态。
//
//   - "开始执行"记录先于执行器调用持久保存（13 第 3 节）；保存失败（含 CAS 冲突）则不调用执行器。
//   - 执行器返回 Go 错误（没有可信结果）→ unknown，永不记成功（K-05）；结果带 Err → failed；其余按 Compare。
//   - 终态保存和审计用不随调用方取消的 context，避免执行器已经跑完而结果丢失。
//   - 目标已变化时返回已保存的 target_changed 申请和 core.fingerprint_changed；其他核验失败返回 nil 和错误。
//   - 终态保存成功后审计失败时，返回已保存的申请和审计错误。
func (s *Service) Execute(ctx context.Context, id ids.CallID) (*request.ExecutionRequest, error) {
	t, err := s.begin(ctx, id)
	if err != nil {
		return nil, err
	}
	confirmed, err := s.preExecute(ctx, t)
	if err != nil {
		if hasCode(err, errcode.FingerprintChanged) {
			return t.r, err
		}
		return nil, err
	}
	r := t.r
	now := s.d.Clock.Now()
	if err := corereq.Transition(r, consts.RequestExecuting, now); err != nil {
		return nil, err
	}
	r.Execution = &request.Execution{StartedAt: now}
	startData := s.frozenData(r)
	if confirmed {
		startData[keyFingerprintConfirmed] = true
		startData["note"] = noteFingerprintConfirmed
	}
	t.emit(events.ExecutionStarted, startData)
	if _, err := t.save(ctx); err != nil {
		return nil, err
	}
	endCtx := context.WithoutCancel(ctx)

	// 开始记录审计失败时不调用执行器：申请直接结束为 failed，不会卡在 executing。
	startDetail := map[string]any{"revision": int(r.Revision), "freeze_digest": string(r.Frozen.Digest)}
	if confirmed {
		startDetail[keyFingerprintConfirmed] = true
		startDetail["note"] = noteFingerprintConfirmed
	}
	startRec := s.audit(r, executorActor, "execution.started", startDetail)
	if err := s.d.Audit.Append(ctx, startRec); err != nil {
		r.Execution.Detail = reasonAuditDown + ": " + err.Error()
		out, serr := s.finishExecution(endCtx, t, consts.RequestFailed, reasonAuditDown, nil)
		if serr != nil {
			return nil, serr
		}
		return out, err
	}

	res, xerr := s.d.Executor.Execute(ctx, r.Clone())
	var outcome consts.RequestState
	var reason string
	var result *request.ExecutionResult
	if xerr != nil {
		outcome, reason = consts.RequestUnknown, reasonExecutorError
		r.Execution.Detail = reasonExecutorError + ": " + xerr.Error()
	} else {
		outcome, reason = Compare(r, res)
		result = res.Clone()
		r.Execution.Detail = reason
	}
	return s.finishExecution(endCtx, t, outcome, reason, result)
}

// finishExecution 保存终态和对应事件，然后追加结束审计记录。
func (s *Service) finishExecution(ctx context.Context, t *txn, outcome consts.RequestState, reason string, result *request.ExecutionResult) (*request.ExecutionRequest, error) {
	r := t.r
	now := s.d.Clock.Now()
	if err := corereq.Transition(r, outcome, now); err != nil {
		return nil, err
	}
	r.Execution.EndedAt = now
	if result != nil && !result.EndedAt.IsZero() {
		r.Execution.EndedAt = result.EndedAt
	}
	r.Execution.Result = result
	r.Execution.Outcome = outcome
	if r.Execution.Detail == "" {
		r.Execution.Detail = reason
	}

	var typ events.Type
	switch outcome {
	case consts.RequestSucceeded:
		typ = events.ExecutionCompleted
	case consts.RequestMismatched:
		typ = events.ExecutionMismatched
	case consts.RequestFailed:
		typ = events.ExecutionFailed
	default:
		typ = events.ExecutionUnknown
	}
	data := revData(r)
	data["outcome"] = string(outcome)
	data["reason"] = reason
	if result != nil {
		if result.ExitCode != nil {
			data["exit_code"] = *result.ExitCode
		}
		if result.OutputRef != "" {
			data["output_ref"] = result.OutputRef
		}
	}
	t.emit(typ, data)
	saved, err := t.save(ctx)
	if err != nil {
		return nil, err
	}
	endRec := s.audit(saved, executorActor, "execution.finished", map[string]any{
		"revision": int(saved.Revision), "outcome": string(outcome), "reason": reason,
	})
	if err := s.d.Audit.Append(ctx, endRec); err != nil {
		return saved, err
	}
	return saved, nil
}
