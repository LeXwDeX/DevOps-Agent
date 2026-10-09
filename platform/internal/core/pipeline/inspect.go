package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	corereq "github.com/LeXwDeX/DevOps-Agent/platform/internal/core/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/review"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	mpolicy "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// stateError 给出"当前状态不能做这件事"的标准错误：终态用 core.terminal_state，其余用 core.invalid_transition。
func stateError(r *request.ExecutionRequest, op string) error {
	if r.State.IsTerminal() {
		return errcode.Newf(errcode.TerminalState, "call %s is %s, cannot %s", r.CallID, r.State, op)
	}
	return errcode.Newf(errcode.InvalidTransition, "call %s is %s, cannot %s", r.CallID, r.State, op)
}

// needsUnit 说明来源是否必须带已登记单元：已登记来源必须有，独立命令必须没有。
func needsUnit(src consts.RequestSource) bool { return src != consts.SourceAdHoc }

// Inspect 按 61 第 5 节的固定顺序检验申请（步骤 1 至 9），并把结果保存为一次状态转换。
//
// 允许从 draft（转 checking）或 checking（新修订之后）调用，其他状态返回 core.invalid_transition / core.terminal_state。
// 结果以返回的申请状态表达，不是错误：
//   - 范围或单元不符 → rejected（inspection_failed，core.scope_violation / core.unit_mismatch），事件 devops.inspection.failed；
//   - 执行预期不合格 → awaiting_revision，原因记在 Checks.Escalations[0]（"core.expectation_invalid: ..."），
//     事件 devops.inspection.failed（45 第 6.2 节："不能进入策略判定"）；
//   - dcg 禁止 → rejected（dcg_forbid），事件 devops.inspection.passed 后接 devops.policy.decided（Mode forbid）；
//   - 策略禁止 → rejected（policy_forbid）；需要人工 → awaiting_review；自动 → frozen。
//
// 端口返回的其他错误（存储、策略源、登记表查询失败而非"不存在"等）直接返回，状态保持不变。
// 所有事件与状态在最后一次 Save 里一起提交。
func (s *Service) Inspect(ctx context.Context, id ids.CallID) (*request.ExecutionRequest, error) {
	t, err := s.begin(ctx, id)
	if err != nil {
		return nil, err
	}
	r := t.r
	switch r.State {
	case consts.RequestDraft:
		if err := corereq.Transition(r, consts.RequestChecking, s.d.Clock.Now()); err != nil {
			return nil, err
		}
	case consts.RequestChecking:
	default:
		return nil, stateError(r, "be inspected")
	}
	if err := supportedSource(r.Source); err != nil {
		return nil, err
	}
	// 重新检验时清掉上一次的检验结果和版本记录，保证保存的都是本次的。
	r.Checks = request.Checks{}
	r.Fingerprint = nil
	r.Versions.PolicyVersion, r.Versions.PolicyLayer = "", ""
	r.Versions.DCGRulesetVersion, r.Versions.FingerprintMethodVersion = "", ""
	t.emit(events.InspectionStarted, revData(r))

	// 步骤 1：范围校验
	sc, done, err := s.checkScope(ctx, t)
	if err != nil {
		return nil, err
	}
	if done {
		return t.save(ctx)
	}
	// 步骤 2：执行预期
	if done, err = s.checkExpectation(t, sc.unit); err != nil {
		return nil, err
	} else if done {
		return t.save(ctx)
	}
	// 步骤 3 至 6：预演、指纹、自审、dcg
	s.runPreview(ctx, t)
	s.runFingerprint(ctx, t, sc.unit)
	s.recordSelfReview(r, s.d.Clock.Now())
	s.runDCG(ctx, t)
	data := revData(r)
	data["preview"] = string(r.Checks.Preview.Status)
	data["fingerprint"] = string(r.Fingerprint.Status)
	data["dcg"] = string(r.Checks.DCG.Result)
	data["self_review"] = "missing"
	if r.Checks.SelfReview != nil {
		data["self_review"] = string(r.Checks.SelfReview.Verdict)
	}
	t.emit(events.InspectionPassed, data)
	if r.Checks.DCG.Result == consts.DCGForbid {
		if err := s.rejectByDCG(t); err != nil {
			return nil, err
		}
		return t.save(ctx)
	}

	// 步骤 7 至 9：策略、转人工、去向
	if err := s.decide(ctx, t, sc); err != nil {
		return nil, err
	}
	return t.save(ctx)
}

// failInspection 把申请拒绝为 inspection_failed 并产生 devops.inspection.failed。
func (t *txn) failInspection(code errcode.Code, reason string) error {
	if err := corereq.Reject(t.r, consts.RejectedByInspection, code, reason, t.s.d.Clock.Now()); err != nil {
		return err
	}
	data := revData(t.r)
	data["code"] = string(code)
	data["reason"] = reason
	t.emit(events.InspectionFailed, data)
	return nil
}

// scopeResult 是步骤 1 查到的信息：已登记单元的登记信息（独立命令为 nil）和会话模式。
type scopeResult struct {
	unit *ports.UnitInfo
	mode consts.SessionMode
}

// checkScope 是步骤 1。done 表示已产生失败结局（申请已转 rejected、事件已收集），等待保存。
func (s *Service) checkScope(ctx context.Context, t *txn) (sc scopeResult, done bool, err error) {
	r := t.r
	sess, err := s.d.Scope.Session(ctx, r.SessionID)
	switch {
	case hasCode(err, errcode.NotFound):
		return scopeResult{}, true, t.failInspection(errcode.ScopeViolation, "session not found")
	case err != nil:
		return scopeResult{}, false, err
	case sess.Project != r.Scope.Project || sess.Environment != r.Scope.Environment:
		return scopeResult{}, true, t.failInspection(errcode.ScopeViolation,
			fmt.Sprintf("scope %s/%s differs from session scope %s/%s", r.Scope.Project, r.Scope.Environment, sess.Project, sess.Environment))
	}
	outside, err := s.d.Scope.OutsideScope(ctx, r.Scope.Project, r.Scope.Environment, r.Scope.Targets)
	if err != nil {
		return scopeResult{}, false, err
	}
	if len(outside) > 0 {
		return scopeResult{}, true, t.failInspection(errcode.ScopeViolation, "targets outside session scope: "+strings.Join(outside, ", "))
	}

	u := r.Content.Unit
	switch {
	case u == nil && needsUnit(r.Source):
		return scopeResult{}, true, t.failInspection(errcode.UnitMismatch, fmt.Sprintf("source %s requires a registered unit", r.Source))
	case u != nil && !needsUnit(r.Source):
		return scopeResult{}, true, t.failInspection(errcode.UnitMismatch, "ad_hoc source must not reference a registered unit")
	case u == nil:
		return scopeResult{mode: sess.Mode}, false, nil
	}
	info, err := s.d.Registry.Unit(ctx, u.ID, u.Version)
	switch {
	case hasCode(err, errcode.NotFound):
		return scopeResult{}, true, t.failInspection(errcode.UnitMismatch, fmt.Sprintf("unit %s version %d is not registered", u.ID, u.Version))
	case err != nil:
		return scopeResult{}, false, err
	case info.Ref.Digest != u.Digest:
		return scopeResult{}, true, t.failInspection(errcode.UnitMismatch, fmt.Sprintf("unit %s version %d digest differs from the registered digest", u.ID, u.Version))
	}
	return scopeResult{unit: &info, mode: sess.Mode}, false, nil
}

// checkExpectation 是步骤 2：已登记单元的 Category、Reversible、HasPreview 以登记声明为准，
// 然后校验必填项。登记声明为空按 unknown 处理（fail-safe：转人工），不信任提交方的值。
// 独立命令没有登记单元：Category、Reversible 置为 unknown，HasPreview 置为 false，忽略提交方的值（61 第 3.2 节）。
// 覆盖可能改变内容，所以摘要在这里按实际检验的内容重算。
func (s *Service) checkExpectation(t *txn, unit *ports.UnitInfo) (done bool, err error) {
	r := t.r
	if unit != nil {
		r.Expectation.Category = unit.Category
		if !unit.Category.Valid() {
			r.Expectation.Category = consts.CategoryUnknown
		}
		r.Expectation.Reversible = unit.Reversible
		if !unit.Reversible.Valid() {
			r.Expectation.Reversible = consts.ReversibilityUnknown
		}
		r.Expectation.HasPreview = unit.HasPreview
	} else {
		r.Expectation.Category = consts.CategoryUnknown
		r.Expectation.Reversible = consts.ReversibilityUnknown
		r.Expectation.HasPreview = false
	}
	d, err := r.ContentDigest()
	if err != nil {
		return false, err
	}
	r.Digest = d
	verr := r.Expectation.Validate(r.Scope)
	if verr == nil {
		return false, nil
	}
	if err := corereq.Transition(r, consts.RequestAwaitingRevision, s.d.Clock.Now()); err != nil {
		return false, err
	}
	reason := reasonOf(verr)
	r.Checks.Escalations = []string{string(errcode.ExpectationInvalid) + ": " + reason}
	data := revData(r)
	data["code"] = string(errcode.ExpectationInvalid)
	data["reason"] = reason
	t.emit(events.InspectionFailed, data)
	return true, nil
}

// runPreview 是步骤 3。预演端口出错或返回非法结论，按预演失败处理（转人工）。
func (s *Service) runPreview(ctx context.Context, t *txn) {
	r := t.r
	now := s.d.Clock.Now()
	if !r.Expectation.HasPreview {
		r.Checks.Preview = &request.PreviewResult{Status: consts.PreviewNone, At: now}
		return
	}
	res, err := s.d.Preview.Preview(ctx, r.Clone())
	switch {
	case err != nil:
		res = request.PreviewResult{Status: consts.PreviewFailed, Output: err.Error()}
	case res.Status != consts.PreviewPassed && res.Status != consts.PreviewFailed:
		res = request.PreviewResult{Status: consts.PreviewFailed, Output: fmt.Sprintf("preview returned unusable status %q", res.Status), Ref: res.Ref}
	}
	if res.At.IsZero() {
		res.At = now
	}
	r.Checks.Preview = &res
}

// runFingerprint 是步骤 4。采集出错或结果不可用（状态非法、已采集却没有值），记为 unavailable（转人工）。
func (s *Service) runFingerprint(ctx context.Context, t *txn, unit *ports.UnitInfo) {
	r := t.r
	fp, err := s.d.Fingerprint.Collect(ctx, r.Clone())
	if err != nil {
		fp = request.Fingerprint{}
	}
	if err != nil || !fp.Status.Valid() || (fp.Status == consts.FingerprintCollected && fp.Value == "") {
		fp = request.Fingerprint{Status: consts.FingerprintUnavailable, Method: fp.Method, MethodVersion: fp.MethodVersion}
		if fp.Method == "" && unit != nil {
			fp.Method, fp.MethodVersion = unit.FingerprintMethod, unit.FingerprintMethodVersion
		}
	}
	if fp.CollectedAt.IsZero() {
		fp.CollectedAt = s.d.Clock.Now()
	}
	r.Fingerprint = &fp
	r.Versions.FingerprintMethodVersion = fp.MethodVersion
}

// recordSelfReview 是步骤 5：既定任务步骤记 not_applicable，其余照抄提交时附带的自审；缺失保持 nil（按风险处理）。
// 记录的自审一律带 now 作为时间，提交时附带的时间不采用。
func (s *Service) recordSelfReview(r *request.ExecutionRequest, now time.Time) {
	if r.Source == consts.SourceTaskStep {
		r.Checks.SelfReview = &request.SelfReview{Verdict: consts.VerdictNotApplicable, At: now}
		return
	}
	if sr := r.Submission.SelfReview; sr != nil {
		c := *sr
		c.At = now
		r.Checks.SelfReview = &c
	}
}

// runDCG 是步骤 6。出错或结论非法记 unavailable（转人工）。
func (s *Service) runDCG(ctx context.Context, t *txn) {
	r := t.r
	res, err := s.d.DCG.Check(ctx, r.Clone())
	if err != nil || !res.Result.Valid() {
		res = request.DCGCheck{Result: consts.DCGUnavailable, RulesetVersion: res.RulesetVersion}
		if err != nil {
			res.RulesetVersion = ""
		}
	}
	if res.At.IsZero() {
		res.At = s.d.Clock.Now()
	}
	r.Checks.DCG = &res
	r.Versions.DCGRulesetVersion = res.RulesetVersion
}

// rejectByDCG 处理 dcg 禁止：申请结束为 rejected（dcg_forbid），以策略判定事件说明（45 第 6.2 节）。
func (s *Service) rejectByDCG(t *txn) error {
	r := t.r
	reason := "dcg forbid"
	if ids := r.Checks.DCG.RuleIDs; len(ids) > 0 {
		reason += ": " + strings.Join(ids, ", ")
	}
	if err := corereq.Reject(r, consts.RejectedByDCG, "", reason, s.d.Clock.Now()); err != nil {
		return err
	}
	data := revData(r)
	data["mode"] = string(consts.PolicyForbid)
	data["source"] = "dcg"
	data["reasons"] = []string{reason}
	t.emit(events.PolicyDecided, data)
	return nil
}

// decide 是步骤 7 至 9：策略解析、转人工判断、去向。
func (s *Service) decide(ctx context.Context, t *txn, sc scopeResult) error {
	r := t.r
	set, err := s.d.Policy.Load(ctx)
	if err != nil {
		return err
	}
	unit := sc.unit
	q := mpolicy.Query{
		Environment: r.Scope.Environment,
		SessionMode: sc.mode,
		Source:      r.Source,
	}
	if unit != nil {
		q.Subject, q.Action, q.Zone = unit.Subject, unit.Action, unit.Zone
		if !q.Zone.Valid() {
			q.Zone = policy.ZoneOf(r.Expectation.Category)
		}
	} else {
		q.Subject, q.Action, q.Zone = "ad_hoc", "", policy.ZoneOf(r.Expectation.Category)
	}
	dec := policy.ResolveValidated(set, q)
	r.Versions.PolicyVersion, r.Versions.PolicyLayer = dec.PolicyVersion, dec.Layer

	flags := policy.FlagsFrom(r)
	now := s.d.Clock.Now()
	final := policy.Escalate(dec, flags)
	final.At = now
	r.Checks.Policy = &final
	r.Checks.Escalations = flags.Reasons()

	data := revData(r)
	data["mode"] = string(final.Mode)
	data["layer"] = string(final.Layer)
	data["policy_version"] = final.PolicyVersion
	data["allow_auto"] = final.AllowAuto
	data["reasons"] = append([]string(nil), final.Reasons...)
	data["escalations"] = flags.Reasons()

	switch final.Mode {
	case consts.PolicyForbid:
		if err := corereq.Reject(r, consts.RejectedByPolicy, "", strings.Join(final.Reasons, "; "), now); err != nil {
			return err
		}
		t.emit(events.PolicyDecided, data)
	case consts.PolicyManual:
		t.emit(events.PolicyDecided, data)
		if err := corereq.Transition(r, consts.RequestAwaitingReview, now); err != nil {
			return err
		}
		t.emit(events.ReviewRequested, map[string]any{
			"revision": int(r.Revision), "digest": string(r.Digest), "reasons": append([]string(nil), final.Reasons...),
		})
	case consts.PolicyAuto:
		t.emit(events.PolicyDecided, data)
		if err := review.Freeze(r, now); err != nil {
			return err
		}
		t.emit(events.CallFrozen, s.frozenData(r))
	default:
		return errcode.Newf(errcode.InvalidArgument, "policy produced unknown mode %q", final.Mode)
	}
	return nil
}
