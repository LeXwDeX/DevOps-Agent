package request

import (
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// Revise 是受控改写（K-14），按 61 第 7.1 节写入新修订。
//
//   - 只允许在 checking、awaiting_revision、target_changed 调用。
//     frozen 及之后（frozen、executing、succeeded、mismatched、failed、unknown）返回
//     core.frozen_immutable（K-15）；draft、awaiting_review、rejected、stopped 返回 core.invalid_transition。
//   - src 只能是 self_review 或 human_comment，否则返回 core.invalid_revision_source。
//   - 来源为 task_step 的申请不能修订，返回 core.source_not_supported：步骤内容由 Registry
//     按登记的任务定义生成，登记过的步骤不由 Agent 修订（61 第 7.3 节）；否则改写后的内容会以
//     "既定任务步骤"的身份免去模型自审（K-06）。目标变化时运行停止，重跑是一次新的运行。
//   - 成功时：Revision 加一，Content 和 Expectation 换成新值（深拷贝），Digest 重算，
//     Checks、Fingerprint、Frozen、Rejection、Execution 清空，Submission 清空
//     （自审和 Hook 输入都针对旧内容，新修订由调用方重新填写），Reviews 保留为历史
//     （批准绑定 Revision 和 Digest，所以旧批准自动失效，见 review.ApprovedFor），
//     状态回到 checking，UpdatedAt 更新。不改 Version，不产生事件。
//   - 返回修订前的完整副本，调用方写入 RevisionLog。
//
// 出错时 r 不变。
func Revise(r *request.ExecutionRequest, content request.Content, exp request.Expectation,
	src consts.RevisionSource, now time.Time) (*request.ExecutionRequest, error) {
	switch r.State {
	case consts.RequestChecking, consts.RequestAwaitingRevision, consts.RequestTargetChanged:
	case consts.RequestFrozen, consts.RequestExecuting, consts.RequestSucceeded,
		consts.RequestMismatched, consts.RequestFailed, consts.RequestUnknown:
		return nil, errcode.Newf(errcode.FrozenImmutable, "call %s is %s", r.CallID, r.State)
	default:
		return nil, errcode.Newf(errcode.InvalidTransition, "call %s cannot be revised in %s", r.CallID, r.State)
	}
	if !src.Valid() {
		return nil, errcode.Newf(errcode.InvalidRevisionSrc, "revision source %q", src)
	}
	if r.Source == consts.SourceTaskStep {
		return nil, errcode.Newf(errcode.SourceNotSupported, "call %s is a registered task step and cannot be revised", r.CallID)
	}

	next := r.Clone()
	next.Revision = r.Revision.Next()
	next.Content = content.Clone()
	next.Expectation = exp.Clone()
	next.Checks = request.Checks{}
	next.Fingerprint = nil
	next.Frozen = nil
	next.Rejection = nil
	next.Execution = nil
	next.Submission = request.Submission{}
	d, err := next.ContentDigest()
	if err != nil {
		return nil, err
	}
	next.Digest = d
	if err := Transition(next, consts.RequestChecking, now); err != nil {
		return nil, err
	}

	old := r.Clone()
	*r = *next
	return old, nil
}
