// Package review 实现人工审核动作和冻结（K-09、K-13、K-15，61 第 7.2 节）。
// 纯函数，不做 I/O，不产生事件，不改 Version。
package review

import (
	"strings"
	"time"

	corereq "github.com/LeXwDeX/DevOps-Agent/platform/internal/core/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// ApprovedFor 判断是否有一条 ▶ 执行记录，绑定的正是当前的 Revision 和 Digest。
// 修订后 Revision 和 Digest 都变了，旧批准自动失效。
func ApprovedFor(r *request.ExecutionRequest) bool {
	for _, e := range r.Reviews {
		if e.Action == consts.ActionExecute && e.Revision == r.Revision && e.Digest == r.Digest {
			return true
		}
	}
	return false
}

// Freeze 冻结当前修订：计算冻结摘要，记录 Frozen，状态转 frozen（K-15）。
//
//   - 允许从 checking（自动通过）或 awaiting_review（人工 ▶ 执行）冻结；
//     其他状态按状态机返回 core.invalid_transition 或 core.terminal_state。
//   - 从 awaiting_review 冻结必须已有绑定当前 Revision 和 Digest 的 ▶ 执行记录，
//     否则返回 core.review_stale。人工审核不能被绕过。
//   - 当前 Digest 必须等于按内容重算的摘要，否则返回 core.digest_mismatch
//     （内容没有经过 Revise 就被改了）。
//
// 出错时 r 不变。
func Freeze(r *request.ExecutionRequest, now time.Time) error {
	// 先在副本上试转换，状态不对时得到标准错误（状态机里只有 checking 和 awaiting_review 能到 frozen）。
	if err := corereq.Transition(r.Clone(), consts.RequestFrozen, now); err != nil {
		return err
	}
	if r.State == consts.RequestAwaitingReview && !ApprovedFor(r) {
		return errcode.Newf(errcode.ReviewStale, "call %s has no execute approval for revision %d", r.CallID, r.Revision)
	}
	cur, err := r.ContentDigest()
	if err != nil {
		return err
	}
	if cur != r.Digest {
		return errcode.Newf(errcode.DigestMismatch, "call %s content changed without a revision", r.CallID)
	}
	fd, err := r.FreezeDigest()
	if err != nil {
		return err
	}
	if err := corereq.Transition(r, consts.RequestFrozen, now); err != nil {
		return err
	}
	r.Frozen = &request.Frozen{Digest: fd, At: now}
	return nil
}

// Act 记录一次人工动作（K-09）。只在 awaiting_review 可用，否则返回 core.invalid_transition。
//
//   - revision 和 digest 必须等于申请当前的值，否则返回 core.review_stale：人工看的是哪个修订，批准就绑定哪个。
//   - actor 不能为空；action 必须是 execute、reject、comment 之一，否则返回 core.invalid_argument。
//   - execute：记录批准并 Freeze，状态转 frozen。
//   - reject：记录并转 rejected，拒绝来源 human，原因取点评（可为空）。
//   - comment：点评不能为空；记录并转 awaiting_revision，等 Agent 提交新修订。
//
// 记录和状态转换要么都成功，要么 r 不变。
func Act(r *request.ExecutionRequest, action consts.HumanAction, revision ids.Revision, digest ids.Digest,
	actor, comment string, now time.Time) error {
	if r.State != consts.RequestAwaitingReview {
		return errcode.Newf(errcode.InvalidTransition, "call %s is %s, not awaiting_review", r.CallID, r.State)
	}
	if !action.Valid() {
		return errcode.Newf(errcode.InvalidArgument, "unknown human action %q", action)
	}
	if strings.TrimSpace(actor) == "" {
		return errcode.New(errcode.InvalidArgument, "actor is required")
	}
	if revision != r.Revision || digest != r.Digest {
		return errcode.Newf(errcode.ReviewStale, "call %s is at revision %d, action bound to revision %d", r.CallID, r.Revision, revision)
	}
	if action == consts.ActionComment && strings.TrimSpace(comment) == "" {
		return errcode.New(errcode.InvalidArgument, "comment text is required")
	}

	w := r.Clone()
	w.Reviews = append(w.Reviews, request.ReviewEntry{
		Action: action, Comment: comment, Actor: actor, At: now, Revision: revision, Digest: digest,
	})
	var err error
	switch action {
	case consts.ActionExecute:
		err = Freeze(w, now)
	case consts.ActionReject:
		err = corereq.Reject(w, consts.RejectedByHuman, "", comment, now)
	case consts.ActionComment:
		err = corereq.Transition(w, consts.RequestAwaitingRevision, now)
	}
	if err != nil {
		return err
	}
	*r = *w
	return nil
}
