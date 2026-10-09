// Package request 实现执行申请的状态机、创建和受控修订（61 第 4、7.1 节）。
// 这里的函数都是纯函数：只改传入的对象，不做 I/O，也不产生事件，不改 Version。
// 调用方（流水线）负责在转换后构造事件、设置 Version 并经存储端口 CAS 保存。
package request

import (
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// transitions 是 61 第 4 节状态图的全部边。终态没有出边。
var transitions = map[consts.RequestState][]consts.RequestState{
	consts.RequestDraft: {consts.RequestChecking, consts.RequestStopped},
	consts.RequestChecking: {
		consts.RequestRejected, consts.RequestAwaitingReview, consts.RequestFrozen,
		consts.RequestChecking, // 自审产生新修订
		consts.RequestAwaitingRevision, consts.RequestStopped,
	},
	consts.RequestAwaitingReview: {
		consts.RequestFrozen, consts.RequestRejected, consts.RequestAwaitingRevision, consts.RequestStopped,
	},
	consts.RequestAwaitingRevision: {consts.RequestChecking, consts.RequestStopped},
	consts.RequestFrozen:           {consts.RequestTargetChanged, consts.RequestExecuting, consts.RequestStopped},
	consts.RequestTargetChanged:    {consts.RequestChecking, consts.RequestStopped},
	consts.RequestExecuting: {
		consts.RequestSucceeded, consts.RequestMismatched, consts.RequestFailed, consts.RequestUnknown,
	},
}

// CanTransition 判断 from → to 是否在状态机表内。未知状态一律为否。
func CanTransition(from, to consts.RequestState) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// NextStates 返回 from 的全部合法去向（拷贝）。
func NextStates(from consts.RequestState) []consts.RequestState {
	return append([]consts.RequestState(nil), transitions[from]...)
}

// Transition 把 r 转到 to，更新 State 和 UpdatedAt。
// 当前是终态返回 core.terminal_state；不在表内返回 core.invalid_transition。
// 失败时 r 不变。成功不改 Version，由调用方的保存负责。
func Transition(r *request.ExecutionRequest, to consts.RequestState, now time.Time) error {
	if r.State.IsTerminal() {
		return errcode.Newf(errcode.TerminalState, "call %s is %s", r.CallID, r.State)
	}
	if !CanTransition(r.State, to) {
		return errcode.Newf(errcode.InvalidTransition, "call %s: %s -> %s", r.CallID, r.State, to)
	}
	r.State = to
	r.UpdatedAt = now
	return nil
}

// Reject 转到 rejected 并记录拒绝来源、错误码和原因。
func Reject(r *request.ExecutionRequest, src consts.RejectionSource, code errcode.Code, reason string, now time.Time) error {
	if !src.Valid() {
		return errcode.Newf(errcode.InvalidArgument, "unknown rejection source %q", src)
	}
	if err := Transition(r, consts.RequestRejected, now); err != nil {
		return err
	}
	r.Rejection = &request.Rejection{Source: src, Code: code, Reason: reason}
	return nil
}

// Stop 转到 stopped 并记录停止原因。
func Stop(r *request.ExecutionRequest, reason consts.StopReason, detail string, now time.Time) error {
	if !reason.Valid() {
		return errcode.Newf(errcode.InvalidArgument, "unknown stop reason %q", reason)
	}
	if err := Transition(r, consts.RequestStopped, now); err != nil {
		return err
	}
	r.Stop = &request.Stop{Reason: reason, Detail: detail, At: now}
	return nil
}
