package policy

import (
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
)

// Flags 是 61 第 6.3 节的转人工条件（K-08、K-10）。
type Flags struct {
	SelfReviewRiskOrMissing    bool // 自审有风险或缺失
	PreviewFailed              bool // 原生预演失败
	DCGHitOrUnavailable        bool // dcg 命中、不可用或没有结论
	FingerprintUnavailable     bool // 无法采集指纹
	CategoryUnknown            bool // Category 为 unknown
	IrreversibleWithoutPreview bool // 不可逆或未知，且没有原生预演
	HookRequiresManual         bool // Before Hook 要求人工
	AnalysisFailed             bool // 内容无法分析
	AdHocCommand               bool // 来源是独立命令
}

type flagReason struct {
	on     bool
	reason string
}

func (f Flags) list() []flagReason {
	return []flagReason{
		{f.SelfReviewRiskOrMissing, "自审有风险或缺失"},
		{f.PreviewFailed, "原生预演失败"},
		{f.DCGHitOrUnavailable, "dcg 命中或不可用"},
		{f.FingerprintUnavailable, "无法采集目标指纹"},
		{f.CategoryUnknown, "读写类别未知"},
		{f.IrreversibleWithoutPreview, "不可逆或可逆性未知，且没有原生预演"},
		{f.HookRequiresManual, "Before Hook 要求人工"},
		{f.AnalysisFailed, "内容无法分析"},
		{f.AdHocCommand, "独立命令只能人工执行"},
	}
}

// Any 判断是否有任一条件成立。
func (f Flags) Any() bool { return len(f.Reasons()) > 0 }

// Reasons 返回成立条件的中文说明，顺序固定。
func (f Flags) Reasons() []string {
	var out []string
	for _, x := range f.list() {
		if x.on {
			out = append(out, x.reason)
		}
	}
	return out
}

// FlagsFrom 从申请的检验结果、预期、提交输入和来源推出转人工条件。缺失或无法识别的值一律按"需要人工"处理。
//
//   - 自审：Checks.SelfReview 缺失、为 risk、值未知，都算风险或缺失；
//     not_applicable 只对来源 task_step 有效，其余来源按风险处理。
//   - dcg：Checks.DCG 缺失，或结果不是 pass，都算命中或不可用。
//   - 指纹：Fingerprint 缺失，或状态不是 collected。
//   - Category：unknown 或空值、未知值。
//   - 可逆性：不是 reversible（含 unknown、空值、未知值）且 Expectation.HasPreview 为 false。
func FlagsFrom(r *request.ExecutionRequest) Flags {
	var f Flags
	switch sr := r.Checks.SelfReview; {
	case sr == nil:
		f.SelfReviewRiskOrMissing = true
	case sr.Verdict == consts.VerdictNoRisk:
	case sr.Verdict == consts.VerdictNotApplicable && r.Source == consts.SourceTaskStep:
	default:
		f.SelfReviewRiskOrMissing = true
	}
	f.PreviewFailed = r.Checks.Preview != nil && r.Checks.Preview.Status == consts.PreviewFailed
	f.DCGHitOrUnavailable = r.Checks.DCG == nil || r.Checks.DCG.Result != consts.DCGPass
	f.FingerprintUnavailable = r.Fingerprint == nil || r.Fingerprint.Status != consts.FingerprintCollected
	f.CategoryUnknown = !r.Expectation.Category.Valid() || r.Expectation.Category == consts.CategoryUnknown
	f.IrreversibleWithoutPreview = r.Expectation.Reversible != consts.Reversible && !r.Expectation.HasPreview
	f.HookRequiresManual = r.Submission.HookRequireManual
	f.AnalysisFailed = r.Submission.AnalysisFailed
	f.AdHocCommand = r.Source == consts.SourceAdHoc
	return f
}

// Escalate 按 K-08 汇总转人工条件。forbid 保持 forbid；其余情况下任一条件成立就变成 manual，
// 每个成立的条件在 Reasons 里追加一条。返回新值，不改入参。
func Escalate(d policy.Decision, f Flags) policy.Decision {
	d = d.Clone()
	if d.Mode == consts.PolicyForbid {
		return d
	}
	reasons := f.Reasons()
	if len(reasons) == 0 {
		return d
	}
	d.Mode = consts.PolicyManual
	d.Reasons = append(d.Reasons, reasons...)
	return d
}
