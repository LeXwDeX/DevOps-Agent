package request

import (
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// NewParams 是创建执行申请的输入。
type NewParams struct {
	CallID      ids.CallID
	Source      consts.RequestSource
	SessionID   string
	Task        *request.TaskRef // Source 为 task_step 时必填，其余来源必须为 nil
	Scope       request.Scope
	Content     request.Content
	Expectation request.Expectation
	Submission  request.Submission
	Now         time.Time
}

// New 创建 draft 状态的执行申请：Revision 1，Digest 为内容摘要，Scope 目标排序去重，
// Versions.SchemaID 填执行申请 SchemaID。输入会被深拷贝。
// Version 起始为 1；之后每次保存由调用方设为 expected.Next()。
// 不校验执行预期：预期不合格是检验结果（awaiting_revision），不是创建失败。
// 只有来源 task_step 带 Task，其余来源带了返回 core.invalid_argument。
// 来源 pre_authorized 在这里可创建，流水线暂不接受（core.source_not_supported）。
func New(p NewParams) (*request.ExecutionRequest, error) {
	if !p.CallID.Valid() {
		return nil, errcode.New(errcode.InvalidArgument, "call id is required")
	}
	if _, err := consts.ParseRequestSource(string(p.Source)); err != nil {
		return nil, err
	}
	if p.Source == consts.SourceTaskStep && (p.Task == nil || p.Task.RunID == "" || p.Task.StepID == "") {
		return nil, errcode.New(errcode.InvalidArgument, "task_step source requires task run id and step id")
	}
	if p.Source != consts.SourceTaskStep && p.Task != nil {
		// 否则事件和审计会带上一个不属于任何运行的 RunID。
		return nil, errcode.Newf(errcode.InvalidArgument, "source %s must not carry a task reference", p.Source)
	}
	r := &request.ExecutionRequest{
		CallID:      p.CallID,
		Revision:    ids.FirstRevision,
		Source:      p.Source,
		SessionID:   p.SessionID,
		Scope:       p.Scope.Normalize(),
		Content:     p.Content.Clone(),
		Expectation: p.Expectation.Clone(),
		Versions:    request.Versions{SchemaID: core.ExecutionRequestV1},
		State:       consts.RequestDraft,
		Version:     1,
		CreatedAt:   p.Now,
		UpdatedAt:   p.Now,
	}
	if p.Task != nil {
		t := *p.Task
		r.Task = &t
	}
	r.Submission = p.Submission.Clone()
	d, err := r.ContentDigest()
	if err != nil {
		return nil, err
	}
	r.Digest = d
	return r, nil
}
