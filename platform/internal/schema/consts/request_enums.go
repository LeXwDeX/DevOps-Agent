package consts

// RequestSource 是申请来源（21 第 3 节）。pre_authorized 值先定义，流水线暂不接受（61 第 11 节）。
type RequestSource string

const (
	SourceTaskStep       RequestSource = "task_step"       // 既定任务步骤
	SourceRegisteredTool RequestSource = "registered_tool" // 已登记工具或命令模板
	SourceTaskLaunch     RequestSource = "task_launch"     // Agent 发起既定任务
	SourceAdHoc          RequestSource = "ad_hoc"          // 独立命令
	SourcePreAuthorized  RequestSource = "pre_authorized"  // 预授权
	SourceHumanTerminal  RequestSource = "human_terminal"  // 人工终端
)

var requestSources = []RequestSource{SourceTaskStep, SourceRegisteredTool, SourceTaskLaunch, SourceAdHoc, SourcePreAuthorized, SourceHumanTerminal}

func RequestSources() []RequestSource { return append([]RequestSource(nil), requestSources...) }

// All 返回该类型全部取值（有序），等同 RequestSources()。
func (RequestSource) All() []RequestSource { return RequestSources() }

func (v RequestSource) Valid() bool { return contains(requestSources, v) }

func ParseRequestSource(s string) (RequestSource, error) {
	return parse("request source", requestSources, s)
}

// Category 是读写类别（21 第 4 节）。unknown 表示无法确定，转人工（K-08）。
type Category string

const (
	CategoryReadOnly         Category = "read_only"         // 只读
	CategoryWrite            Category = "write"             // 写入
	CategoryDelete           Category = "delete"            // 删除
	CategoryPermissionChange Category = "permission_change" // 权限变更
	CategoryStartStop        Category = "start_stop"        // 启停
	CategoryBilling          Category = "billing"           // 计费
	CategoryUnknown          Category = "unknown"           // 无法确定
)

var categories = []Category{CategoryReadOnly, CategoryWrite, CategoryDelete, CategoryPermissionChange, CategoryStartStop, CategoryBilling, CategoryUnknown}

func Categories() []Category { return append([]Category(nil), categories...) }

// All 返回该类型全部取值（有序），等同 Categories()。
func (Category) All() []Category { return Categories() }

func (v Category) Valid() bool { return contains(categories, v) }

func ParseCategory(s string) (Category, error) { return parse("category", categories, s) }

// Reversibility 是可逆性（21 第 4 节）。unknown 按不可逆处理。
type Reversibility string

const (
	Reversible           Reversibility = "reversible"   // 可逆
	Irreversible         Reversibility = "irreversible" // 不可逆
	ReversibilityUnknown Reversibility = "unknown"      // 未知
)

var reversibilities = []Reversibility{Reversible, Irreversible, ReversibilityUnknown}

func Reversibilities() []Reversibility { return append([]Reversibility(nil), reversibilities...) }

// All 返回该类型全部取值（有序），等同 Reversibilities()。
func (Reversibility) All() []Reversibility { return Reversibilities() }

func (v Reversibility) Valid() bool { return contains(reversibilities, v) }

func ParseReversibility(s string) (Reversibility, error) {
	return parse("reversibility", reversibilities, s)
}

// StopReason 是停止原因，申请和任务运行共用（61 第 7.4 节）。
type StopReason string

const (
	StopMismatched          StopReason = "mismatched"           // 与预期不符
	StopUnknown             StopReason = "unknown"              // 结果未知
	StopFailed              StopReason = "failed"               // 失败
	StopRejected            StopReason = "rejected"             // 已拒绝
	StopTargetChanged       StopReason = "target_changed"       // 目标已变化
	StopNoBranchMatched     StopReason = "no_branch_matched"    // 没有分支匹配
	StopCancelledByHuman    StopReason = "cancelled_by_human"   // 人工取消
	StopRecoveryInterrupted StopReason = "recovery_interrupted" // 恢复中断
)

var stopReasons = []StopReason{StopMismatched, StopUnknown, StopFailed, StopRejected, StopTargetChanged, StopNoBranchMatched, StopCancelledByHuman, StopRecoveryInterrupted}

func StopReasons() []StopReason { return append([]StopReason(nil), stopReasons...) }

// All 返回该类型全部取值（有序），等同 StopReasons()。
func (StopReason) All() []StopReason { return StopReasons() }

func (v StopReason) Valid() bool { return contains(stopReasons, v) }

func ParseStopReason(s string) (StopReason, error) { return parse("stop reason", stopReasons, s) }

// RejectionSource 是拒绝来源（21 第 8 节）。
type RejectionSource string

const (
	RejectedByHuman      RejectionSource = "human"             // 人工拒绝
	RejectedByPolicy     RejectionSource = "policy_forbid"     // 策略禁止
	RejectedByDCG        RejectionSource = "dcg_forbid"        // dcg 禁止
	RejectedByInspection RejectionSource = "inspection_failed" // 检验不合格
)

var rejectionSources = []RejectionSource{RejectedByHuman, RejectedByPolicy, RejectedByDCG, RejectedByInspection}

func RejectionSources() []RejectionSource { return append([]RejectionSource(nil), rejectionSources...) }

// All 返回该类型全部取值（有序），等同 RejectionSources()。
func (RejectionSource) All() []RejectionSource { return RejectionSources() }

func (v RejectionSource) Valid() bool { return contains(rejectionSources, v) }

func ParseRejectionSource(s string) (RejectionSource, error) {
	return parse("rejection source", rejectionSources, s)
}

// DCGResult 是dcg 检查结果（22 第 2.2 节）。
type DCGResult string

const (
	DCGPass        DCGResult = "pass"        // 放行
	DCGHit         DCGResult = "hit"         // 命中
	DCGForbid      DCGResult = "forbid"      // 禁止
	DCGUnavailable DCGResult = "unavailable" // 不可用
)

var dCGResults = []DCGResult{DCGPass, DCGHit, DCGForbid, DCGUnavailable}

func DCGResults() []DCGResult { return append([]DCGResult(nil), dCGResults...) }

// All 返回该类型全部取值（有序），等同 DCGResults()。
func (DCGResult) All() []DCGResult { return DCGResults() }

func (v DCGResult) Valid() bool { return contains(dCGResults, v) }

func ParseDCGResult(s string) (DCGResult, error) { return parse("dcg result", dCGResults, s) }

// PreviewStatus 是原生预演状态（61 第 3.1 节）。
type PreviewStatus string

const (
	PreviewNone   PreviewStatus = "none"   // 未声明预演
	PreviewPassed PreviewStatus = "passed" // 预演成功
	PreviewFailed PreviewStatus = "failed" // 预演失败
)

var previewStatuses = []PreviewStatus{PreviewNone, PreviewPassed, PreviewFailed}

func PreviewStatuses() []PreviewStatus { return append([]PreviewStatus(nil), previewStatuses...) }

// All 返回该类型全部取值（有序），等同 PreviewStatuses()。
func (PreviewStatus) All() []PreviewStatus { return PreviewStatuses() }

func (v PreviewStatus) Valid() bool { return contains(previewStatuses, v) }

func ParsePreviewStatus(s string) (PreviewStatus, error) {
	return parse("preview status", previewStatuses, s)
}

// FingerprintStatus 是指纹状态（61 第 3.1 节）。
type FingerprintStatus string

const (
	FingerprintCollected   FingerprintStatus = "collected"   // 已采集
	FingerprintUnavailable FingerprintStatus = "unavailable" // 采集不到
)

var fingerprintStatuses = []FingerprintStatus{FingerprintCollected, FingerprintUnavailable}

func FingerprintStatuses() []FingerprintStatus {
	return append([]FingerprintStatus(nil), fingerprintStatuses...)
}

// All 返回该类型全部取值（有序），等同 FingerprintStatuses()。
func (FingerprintStatus) All() []FingerprintStatus { return FingerprintStatuses() }

func (v FingerprintStatus) Valid() bool { return contains(fingerprintStatuses, v) }

func ParseFingerprintStatus(s string) (FingerprintStatus, error) {
	return parse("fingerprint status", fingerprintStatuses, s)
}

// RevisionSource 是修订来源，只有两种（K-14）。
type RevisionSource string

const (
	RevisionSelfReview   RevisionSource = "self_review"   // Agent 自审
	RevisionHumanComment RevisionSource = "human_comment" // 人工点评
)

var revisionSources = []RevisionSource{RevisionSelfReview, RevisionHumanComment}

func RevisionSources() []RevisionSource { return append([]RevisionSource(nil), revisionSources...) }

// All 返回该类型全部取值（有序），等同 RevisionSources()。
func (RevisionSource) All() []RevisionSource { return RevisionSources() }

func (v RevisionSource) Valid() bool { return contains(revisionSources, v) }

func ParseRevisionSource(s string) (RevisionSource, error) {
	return parse("revision source", revisionSources, s)
}
