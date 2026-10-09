// Package request 定义执行申请及其组成部分（61 第 3 节）。
// 只有数据、校验、深拷贝和摘要；状态机在 core/request。
package request

import (
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/consts"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// TaskRef 指出申请属于哪次任务运行的哪一步（来源为 task_step 时必填）。
type TaskRef struct {
	RunID  string `json:"run_id"`
	StepID string `json:"step_id"`
}

// UnitRef 引用已登记单元：ID、版本、摘要。独立命令没有 UnitRef。
type UnitRef struct {
	ID      string     `json:"id"`
	Version int64      `json:"version"`
	Digest  ids.Digest `json:"digest"`
}

// Content 是待执行内容。SecretKeys 只含 K，没有 V（K-16）。
type Content struct {
	Unit       *UnitRef       `json:"unit,omitempty"`
	Rendered   string         `json:"rendered"` // 展开后的完整内容，不进入事件（45 第 6.1 节）
	Params     map[string]any `json:"params,omitempty"`
	SecretKeys []string       `json:"secret_keys,omitempty"`
}

// Submission 是提交时附带的输入。
type Submission struct {
	SelfReview        *SelfReview `json:"self_review,omitempty"`
	HookRequireManual bool        `json:"hook_require_manual,omitempty"`
	HookReasons       []string    `json:"hook_reasons,omitempty"`
	AnalysisFailed    bool        `json:"analysis_failed,omitempty"` // 内容无法分析（例如 SQL 方言不支持）
	AnalysisReason    string      `json:"analysis_reason,omitempty"`
}

// SelfReview 是自审结论。
type SelfReview struct {
	Verdict consts.SelfReviewVerdict `json:"verdict"`
	Reason  string                   `json:"reason,omitempty"`
}

// Fingerprint 是目标状态指纹（K-04）。
type Fingerprint struct {
	Method        string                   `json:"method"`
	MethodVersion string                   `json:"method_version"`
	CollectedAt   time.Time                `json:"collected_at"`
	Status        consts.FingerprintStatus `json:"status"`
	Value         string                   `json:"value,omitempty"`
}

// PreviewResult 是原生预演结果。Output 只放摘要，Ref 指向完整输出。
type PreviewResult struct {
	Status consts.PreviewStatus `json:"status"`
	Output string               `json:"output,omitempty"`
	Ref    string               `json:"ref,omitempty"`
	At     time.Time            `json:"at"`
}

// DCGCheck 是 dcg 检查结论。
type DCGCheck struct {
	Result         consts.DCGResult `json:"result"`
	RuleIDs        []string         `json:"rule_ids,omitempty"`
	RulesetVersion string           `json:"ruleset_version,omitempty"`
	At             time.Time        `json:"at"`
}

// Checks 汇总检验结果。修订后整体清空。
type Checks struct {
	Preview     *PreviewResult   `json:"preview,omitempty"`
	SelfReview  *SelfReview      `json:"self_review,omitempty"`
	DCG         *DCGCheck        `json:"dcg,omitempty"`
	Policy      *policy.Decision `json:"policy,omitempty"`
	Escalations []string         `json:"escalations,omitempty"` // 转人工的原因
}

// Versions 记录判定时用到的各版本。
type Versions struct {
	SchemaID                 string             `json:"schema_id"`
	PolicyVersion            string             `json:"policy_version,omitempty"`
	PolicyLayer              consts.PolicyLayer `json:"policy_layer,omitempty"`
	DCGRulesetVersion        string             `json:"dcg_ruleset_version,omitempty"`
	FingerprintMethodVersion string             `json:"fingerprint_method_version,omitempty"`
}

// ReviewEntry 是一条人工动作记录，绑定当时的 Revision 和 Digest（K-09、K-13）。
type ReviewEntry struct {
	Action   consts.HumanAction `json:"action"`
	Comment  string             `json:"comment,omitempty"`
	Actor    string             `json:"actor"`
	At       time.Time          `json:"at"`
	Revision ids.Revision       `json:"revision"`
	Digest   ids.Digest         `json:"digest"`
}

// Rejection 记录拒绝来源、错误码和原因。
type Rejection struct {
	Source consts.RejectionSource `json:"source"`
	Code   errcode.Code           `json:"code,omitempty"`
	Reason string                 `json:"reason,omitempty"`
}

// Stop 记录停止原因。
type Stop struct {
	Reason consts.StopReason `json:"reason"`
	Detail string            `json:"detail,omitempty"`
	At     time.Time         `json:"at"`
}

// Frozen 记录冻结摘要和时间；未冻结为 nil（K-15）。
type Frozen struct {
	Digest ids.Digest `json:"digest"`
	At     time.Time  `json:"at"`
}

// ExecutionResult 是执行器回报的结果。
type ExecutionResult struct {
	ExitCode  *int           `json:"exit_code,omitempty"`
	OutputRef string         `json:"output_ref,omitempty"`
	Output    map[string]any `json:"output,omitempty"` // 结构化输出，成功判据作用在它上面
	Err       string         `json:"err,omitempty"`    // 执行器错误文本，没有则为空
	StartedAt time.Time      `json:"started_at"`
	EndedAt   time.Time      `json:"ended_at"`
}

// Execution 是执行记录与对比结论。
type Execution struct {
	StartedAt time.Time           `json:"started_at"`
	EndedAt   time.Time           `json:"ended_at"`
	Result    *ExecutionResult    `json:"result,omitempty"`
	Outcome   consts.RequestState `json:"outcome,omitempty"` // succeeded / mismatched / failed / unknown
	Detail    string              `json:"detail,omitempty"`
}

// ExecutionRequest 是一次调用（一个 CallID）的完整记录。
type ExecutionRequest struct {
	CallID      ids.CallID           `json:"call_id"`
	Revision    ids.Revision         `json:"revision"`
	Digest      ids.Digest           `json:"digest"` // 当前修订的内容摘要，见 ContentDigest
	Source      consts.RequestSource `json:"source"`
	SessionID   string               `json:"session_id"`
	Task        *TaskRef             `json:"task,omitempty"`
	Scope       Scope                `json:"scope"`
	Content     Content              `json:"content"`
	Expectation Expectation          `json:"expectation"`
	Submission  Submission           `json:"submission"`
	Fingerprint *Fingerprint         `json:"fingerprint,omitempty"`
	Checks      Checks               `json:"checks"`
	Versions    Versions             `json:"versions"`
	Reviews     []ReviewEntry        `json:"reviews,omitempty"`
	Rejection   *Rejection           `json:"rejection,omitempty"`
	Stop        *Stop                `json:"stop,omitempty"`
	Frozen      *Frozen              `json:"frozen,omitempty"`
	Execution   *Execution           `json:"execution,omitempty"`
	State       consts.RequestState  `json:"state"`
	Version     ids.Version          `json:"version"`   // 乐观锁版本号，CAS 用
	EventSeq    int64                `json:"event_seq"` // CallID 事件流上最后一个事件的序号
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}
