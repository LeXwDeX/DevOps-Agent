package pipeline

import (
	"context"
	"strings"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	corereq "github.com/LeXwDeX/DevOps-Agent/platform/internal/core/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/review"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// SubmitInput 是提交执行申请的输入。
type SubmitInput struct {
	Source      consts.RequestSource
	SessionID   string
	Task        *request.TaskRef // Source 为 task_step 时必填
	Scope       request.Scope
	Content     request.Content
	Expectation request.Expectation
	Submission  request.Submission
}

// supportedSource 判断本块流水线是否接受该来源（61 第 11 节：预授权不在本块；人工终端不经流水线）。
func supportedSource(src consts.RequestSource) error {
	switch src {
	case consts.SourcePreAuthorized, consts.SourceHumanTerminal:
		return errcode.Newf(errcode.SourceNotSupported, "source %s is not supported by the pipeline", src)
	}
	return nil
}

// Submit 登记一个新的执行申请：分配 CallID，创建 draft，同一次保存里带 devops.call.proposed。
// 不做检验；调用方接着调用 Inspect。
func (s *Service) Submit(ctx context.Context, in SubmitInput) (*request.ExecutionRequest, error) {
	if !in.Source.Valid() {
		return nil, errcode.Newf(errcode.InvalidArgument, "unknown request source %q", in.Source)
	}
	if err := supportedSource(in.Source); err != nil {
		return nil, err
	}
	now := s.d.Clock.Now()
	r, err := corereq.New(corereq.NewParams{
		CallID:      ids.CallID(s.d.IDs.NewID("call")),
		Source:      in.Source,
		SessionID:   in.SessionID,
		Task:        in.Task,
		Scope:       in.Scope,
		Content:     in.Content,
		Expectation: in.Expectation,
		Submission:  in.Submission,
		Now:         now,
	})
	if err != nil {
		return nil, err
	}
	data := revData(r)
	data["source"] = string(r.Source)
	evt := events.ForRequest(r, events.CallProposed, data, now, s.d.IDs.NewID("event"))
	if err := s.d.Requests.Create(ctx, r, []events.Event{evt}); err != nil {
		return nil, err
	}
	return r, nil
}

// ReviseInput 是受控改写的输入（K-14）。Source 只能是 self_review 或 human_comment。
type ReviseInput struct {
	Content     request.Content
	Expectation request.Expectation
	Submission  request.Submission
	Source      consts.RevisionSource
}

// Revise 写入新修订：旧修订先追加到修订日志，再 CAS 保存新修订和 devops.call.revised。
// 允许的状态由 request.Revise 决定（checking、awaiting_revision、target_changed）；
// frozen 及之后返回 core.frozen_immutable（K-15）。成功后状态为 checking，调用方接着调用 Inspect。
//
// 日志先于保存写入，所以保存因 CAS 冲突失败时，日志里可能留下一份没有被取代的旧修订副本。
// 日志只追加；读取方按 (Revision, Digest) 对同一调用的记录去重。这比"先保存后写日志"安全：
// 后者在两步之间崩溃会永久丢掉旧修订。
func (s *Service) Revise(ctx context.Context, id ids.CallID, in ReviseInput) (*request.ExecutionRequest, error) {
	t, err := s.begin(ctx, id)
	if err != nil {
		return nil, err
	}
	old, err := corereq.Revise(t.r, in.Content, in.Expectation, in.Source, s.d.Clock.Now())
	if err != nil {
		return nil, err
	}
	t.r.Submission = in.Submission.Clone()
	if err := s.d.Revisions.Append(ctx, old); err != nil {
		return nil, err
	}
	data := revData(t.r)
	data["source"] = string(in.Source)
	data["previous_revision"] = int(old.Revision)
	t.emit(events.CallRevised, data)
	return t.save(ctx)
}

// ReviewInput 是人工审核动作。Revision 和 Digest 是人工看到的修订，必须等于当前值（K-09、K-13）。
type ReviewInput struct {
	Action   consts.HumanAction
	Revision ids.Revision
	Digest   ids.Digest
	Actor    string
	Comment  string
}

// Review 记录一次人工动作（▶ 执行、✕ 拒绝、点评）。
// 保存成功后追加审计记录；审计失败时申请已经保存，返回已保存的申请和审计错误，调用方不要当作动作失败重试。
func (s *Service) Review(ctx context.Context, id ids.CallID, in ReviewInput) (*request.ExecutionRequest, error) {
	t, err := s.begin(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := review.Act(t.r, in.Action, in.Revision, in.Digest, in.Actor, in.Comment, s.d.Clock.Now()); err != nil {
		return nil, err
	}
	data := revData(t.r)
	data["actor"] = in.Actor
	switch in.Action {
	case consts.ActionExecute:
		t.emit(events.ReviewApproved, data)
		t.emit(events.CallFrozen, s.frozenData(t.r))
	case consts.ActionReject:
		t.emit(events.ReviewRejected, data)
	case consts.ActionComment:
		t.emit(events.ReviewCommented, data)
	}
	saved, err := t.save(ctx)
	if err != nil {
		return nil, err
	}
	rec := s.audit(saved, in.Actor, "review."+string(in.Action), map[string]any{
		"revision": int(in.Revision), "digest": string(in.Digest), "comment": in.Comment, "state": string(saved.State),
	})
	if err := s.d.Audit.Append(ctx, rec); err != nil {
		return saved, err
	}
	return saved, nil
}

// Cancel 人工取消：非终态且状态机允许的状态转 stopped（原因 cancelled_by_human）。
// executing 不能取消（状态机没有这条边），返回 core.invalid_transition，结果必须来自执行器；终态返回 core.terminal_state。
// 保存成功后追加审计记录，审计失败的语义同 Review。
func (s *Service) Cancel(ctx context.Context, id ids.CallID, actor string) (*request.ExecutionRequest, error) {
	if strings.TrimSpace(actor) == "" {
		return nil, errcode.New(errcode.InvalidArgument, "actor is required")
	}
	t, err := s.begin(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := corereq.Stop(t.r, consts.StopCancelledByHuman, "cancelled by "+actor, s.d.Clock.Now()); err != nil {
		return nil, err
	}
	data := revData(t.r)
	data["reason"] = string(consts.StopCancelledByHuman)
	data["actor"] = actor
	t.emit(events.CallStopped, data)
	saved, err := t.save(ctx)
	if err != nil {
		return nil, err
	}
	rec := s.audit(saved, actor, "call.cancel", map[string]any{"revision": int(saved.Revision), "reason": string(consts.StopCancelledByHuman)})
	if err := s.d.Audit.Append(ctx, rec); err != nil {
		return saved, err
	}
	return saved, nil
}

// frozenData 是 devops.call.frozen 的数据：只有摘要。
func (s *Service) frozenData(r *request.ExecutionRequest) map[string]any {
	d := revData(r)
	if r.Frozen != nil {
		d["freeze_digest"] = string(r.Frozen.Digest)
	}
	return d
}

// audit 构造一条审计记录。Detail 由调用方保证不含 V。
func (s *Service) audit(r *request.ExecutionRequest, actor, action string, detail map[string]any) ports.AuditRecord {
	rec := ports.AuditRecord{
		At: s.d.Clock.Now(), Actor: actor, Action: action, CallID: r.CallID, Revision: r.Revision, Detail: detail,
	}
	if r.Task != nil {
		rec.RunID = r.Task.RunID
	}
	return rec
}
