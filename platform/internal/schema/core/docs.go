package core

import "time"

// 持久化文档结构 v1（62 第 3.3 节）。
//
// 规则：
//   - 字段名固定为 snake_case，一个 model 字段对应一个文档字段，不省略任何字段（不用 omitempty），
//     这样 nil 与空集合在往返后保持不变。
//   - 只用基本类型：枚举、ID 和摘要都是字符串；schema 是叶子层，不导入 model。
//   - map[string]any 是类 JSON 数据（参数、输出、事件数据）。
//   - 读取时按严格模式解码：未知字段是错误；枚举由读取方按 consts.ParseX 校验。

// ScopeDoc 是作用范围。
type ScopeDoc struct {
	Project     string   `json:"project"`
	Environment string   `json:"environment"`
	Targets     []string `json:"targets"`
}

// UnitRefDoc 是已登记单元引用。
type UnitRefDoc struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Digest  string `json:"digest"`
}

// TaskRefDoc 指出申请属于哪次任务运行的哪一步。
type TaskRefDoc struct {
	RunID  string `json:"run_id"`
	StepID string `json:"step_id"`
}

// ContentDoc 是待执行内容。
type ContentDoc struct {
	Unit       *UnitRefDoc    `json:"unit"`
	Rendered   string         `json:"rendered"`
	Params     map[string]any `json:"params"`
	SecretKeys []string       `json:"secret_keys"`
}

// ValueRangeDoc 是预期变更的结构化范围。
type ValueRangeDoc struct {
	Field string   `json:"field"`
	Min   *float64 `json:"min"`
	Max   *float64 `json:"max"`
}

// ExpectedChangeDoc 是预期变更。
type ExpectedChangeDoc struct {
	Text   string          `json:"text"`
	Ranges []ValueRangeDoc `json:"ranges"`
}

// CriterionDoc 是一项成功判据。
type CriterionDoc struct {
	Expr string `json:"expr"`
	Text string `json:"text"`
}

// ExpectationDoc 是执行预期。
type ExpectationDoc struct {
	Intent          string            `json:"intent"`
	Targets         []string          `json:"targets"`
	Category        string            `json:"category"`
	ExpectedChange  ExpectedChangeDoc `json:"expected_change"`
	ExpectedOutput  string            `json:"expected_output"`
	SuccessCriteria []CriterionDoc    `json:"success_criteria"`
	Reversible      string            `json:"reversible"`
	Rollback        string            `json:"rollback"`
	HasPreview      bool              `json:"has_preview"`
	RiskNote        string            `json:"risk_note"`
}

// SelfReviewDoc 是自审结论。
type SelfReviewDoc struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// SubmissionDoc 是提交时附带的输入。
type SubmissionDoc struct {
	SelfReview        *SelfReviewDoc `json:"self_review"`
	HookRequireManual bool           `json:"hook_require_manual"`
	HookReasons       []string       `json:"hook_reasons"`
	AnalysisFailed    bool           `json:"analysis_failed"`
	AnalysisReason    string         `json:"analysis_reason"`
}

// FingerprintDoc 是目标状态指纹。
type FingerprintDoc struct {
	Method        string    `json:"method"`
	MethodVersion string    `json:"method_version"`
	CollectedAt   time.Time `json:"collected_at"`
	Status        string    `json:"status"`
	Value         string    `json:"value"`
}

// PreviewDoc 是原生预演结果。
type PreviewDoc struct {
	Status string    `json:"status"`
	Output string    `json:"output"`
	Ref    string    `json:"ref"`
	At     time.Time `json:"at"`
}

// DCGCheckDoc 是 dcg 检查结论。
type DCGCheckDoc struct {
	Result         string    `json:"result"`
	RuleIDs        []string  `json:"rule_ids"`
	RulesetVersion string    `json:"ruleset_version"`
	At             time.Time `json:"at"`
}

// PolicyDecisionDoc 是策略判定。
type PolicyDecisionDoc struct {
	Mode          string   `json:"mode"`
	Layer         string   `json:"layer"`
	AllowAuto     bool     `json:"allow_auto"`
	PolicyVersion string   `json:"policy_version"`
	Reasons       []string `json:"reasons"`
}

// ChecksDoc 汇总检验结果。
type ChecksDoc struct {
	Preview     *PreviewDoc        `json:"preview"`
	SelfReview  *SelfReviewDoc     `json:"self_review"`
	DCG         *DCGCheckDoc       `json:"dcg"`
	Policy      *PolicyDecisionDoc `json:"policy"`
	Escalations []string           `json:"escalations"`
}

// VersionsDoc 记录判定时用到的各版本。
type VersionsDoc struct {
	SchemaID                 string `json:"schema_id"`
	PolicyVersion            string `json:"policy_version"`
	PolicyLayer              string `json:"policy_layer"`
	DCGRulesetVersion        string `json:"dcg_ruleset_version"`
	FingerprintMethodVersion string `json:"fingerprint_method_version"`
}

// ReviewEntryDoc 是一条人工动作记录。
type ReviewEntryDoc struct {
	Action   string    `json:"action"`
	Comment  string    `json:"comment"`
	Actor    string    `json:"actor"`
	At       time.Time `json:"at"`
	Revision int       `json:"revision"`
	Digest   string    `json:"digest"`
}

// RejectionDoc 记录拒绝来源、错误码和原因。
type RejectionDoc struct {
	Source string `json:"source"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// StopDoc 记录停止原因。
type StopDoc struct {
	Reason string    `json:"reason"`
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
}

// FrozenDoc 记录冻结摘要和时间。
type FrozenDoc struct {
	Digest string    `json:"digest"`
	At     time.Time `json:"at"`
}

// ExecutionResultDoc 是执行器回报的结果。
type ExecutionResultDoc struct {
	ExitCode  *int           `json:"exit_code"`
	OutputRef string         `json:"output_ref"`
	Output    map[string]any `json:"output"`
	Err       string         `json:"err"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   time.Time      `json:"ended_at"`
}

// ExecutionDoc 是执行记录与对比结论。
type ExecutionDoc struct {
	StartedAt time.Time           `json:"started_at"`
	EndedAt   time.Time           `json:"ended_at"`
	Result    *ExecutionResultDoc `json:"result"`
	Outcome   string              `json:"outcome"`
	Detail    string              `json:"detail"`
}

// RequestDocV1 是执行申请的持久化文档，SchemaID 为 ExecutionRequestV1。
type RequestDocV1 struct {
	CallID      string           `json:"call_id"`
	Revision    int              `json:"revision"`
	Digest      string           `json:"digest"`
	Source      string           `json:"source"`
	SessionID   string           `json:"session_id"`
	Task        *TaskRefDoc      `json:"task"`
	Scope       ScopeDoc         `json:"scope"`
	Content     ContentDoc       `json:"content"`
	Expectation ExpectationDoc   `json:"expectation"`
	Submission  SubmissionDoc    `json:"submission"`
	Fingerprint *FingerprintDoc  `json:"fingerprint"`
	Checks      ChecksDoc        `json:"checks"`
	Versions    VersionsDoc      `json:"versions"`
	Reviews     []ReviewEntryDoc `json:"reviews"`
	Rejection   *RejectionDoc    `json:"rejection"`
	Stop        *StopDoc         `json:"stop"`
	Frozen      *FrozenDoc       `json:"frozen"`
	Execution   *ExecutionDoc    `json:"execution"`
	State       string           `json:"state"`
	Version     int64            `json:"version"`
	EventSeq    int64            `json:"event_seq"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// StepRecordDoc 是运行中一步的记录。
type StepRecordDoc struct {
	StepID string         `json:"step_id"`
	CallID string         `json:"call_id"`
	State  string         `json:"state"`
	Output map[string]any `json:"output"`
	Next   string         `json:"next"`
}

// RunStopDoc 记录运行停止的步骤和原因。
type RunStopDoc struct {
	Reason string    `json:"reason"`
	StepID string    `json:"step_id"`
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
}

// RunDocV1 是任务运行的持久化文档，SchemaID 为 TaskRunV1。
type RunDocV1 struct {
	RunID         string          `json:"run_id"`
	TaskID        string          `json:"task_id"`
	TaskVersion   int64           `json:"task_version"`
	TaskDigest    string          `json:"task_digest"`
	SessionID     string          `json:"session_id"`
	Scope         ScopeDoc        `json:"scope"`
	Params        map[string]any  `json:"params"`
	State         string          `json:"state"`
	Current       string          `json:"current"`
	Steps         []StepRecordDoc `json:"steps"`
	MissingInputs []string        `json:"missing_inputs"`
	Stop          *RunStopDoc     `json:"stop"`
	Version       int64           `json:"version"`
	EventSeq      int64           `json:"event_seq"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// FieldTypeDoc 是参数或输出字段的类型。
type FieldTypeDoc struct {
	Kind string `json:"kind"`
	Elem string `json:"elem"`
}

// FieldSpecDoc 描述一个参数或输出字段。
type FieldSpecDoc struct {
	Name     string       `json:"name"`
	Type     FieldTypeDoc `json:"type"`
	Required bool         `json:"required"`
}

// BranchDoc 是一条分支。
type BranchDoc struct {
	When string `json:"when"`
	To   string `json:"to"`
}

// TaskStepDoc 是任务定义的一步。
type TaskStepDoc struct {
	ID       string         `json:"id"`
	Unit     UnitRefDoc     `json:"unit"`
	Outputs  []FieldSpecDoc `json:"outputs"`
	Next     string         `json:"next"`
	Branches []BranchDoc    `json:"branches"`
}

// TaskDefinitionDocV1 是任务定义的持久化文档，SchemaID 为 TaskDefinitionV1。
type TaskDefinitionDocV1 struct {
	ID         string         `json:"id"`
	Version    int64          `json:"version"`
	Digest     string         `json:"digest"`
	Params     []FieldSpecDoc `json:"params"`
	SecretKeys []string       `json:"secret_keys"`
	Entry      string         `json:"entry"`
	Steps      []TaskStepDoc  `json:"steps"`
}

// EventDocV1 是事件信封的持久化文档，SchemaID 为 EventV1。
// SchemaVersion 是信封自带的版本字段，和存储列 schema_id 分开保存。
type EventDocV1 struct {
	SchemaVersion string         `json:"schema_version"`
	EventID       string         `json:"event_id"`
	Type          string         `json:"type"`
	StreamID      string         `json:"stream_id"`
	Sequence      int64          `json:"sequence"`
	OccurredAt    time.Time      `json:"occurred_at"`
	SourceKind    string         `json:"source_kind"`
	SessionID     string         `json:"session_id"`
	RunID         string         `json:"run_id"`
	CallID        string         `json:"call_id"`
	Revision      int            `json:"revision"`
	Project       string         `json:"project"`
	Environment   string         `json:"environment"`
	Targets       []string       `json:"targets"`
	Status        string         `json:"status"`
	Data          map[string]any `json:"data"`
}

// AuditRecordDocV1 是审计记录的持久化文档，SchemaID 为 AuditRecordV1。
type AuditRecordDocV1 struct {
	At       time.Time      `json:"at"`
	Actor    string         `json:"actor"`
	Action   string         `json:"action"`
	CallID   string         `json:"call_id"`
	Revision int            `json:"revision"`
	RunID    string         `json:"run_id"`
	Detail   map[string]any `json:"detail"`
}
