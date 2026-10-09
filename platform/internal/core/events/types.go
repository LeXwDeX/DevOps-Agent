// Package events 定义 devops.* 事件类型和事件信封，以及从申请和运行构造事件的函数。
// 事件名以 docs/40-sdk/45-events-and-hooks.md 第 6 节为准，由文档比对测试守住。
// 事件只含摘要、状态和引用，不含完整命令、SQL 原文或 V（45 第 6.1 节）。
package events

// Type 是事件类型，用类型化字符串常量，名称属于公共契约。
type Type string

// 执行申请、检验与审核（45 第 6.2 节）。
const (
	CallProposed      Type = "devops.call.proposed"
	CallRevised       Type = "devops.call.revised"
	InspectionStarted Type = "devops.inspection.started"
	InspectionPassed  Type = "devops.inspection.passed"
	InspectionFailed  Type = "devops.inspection.failed"
	PolicyDecided     Type = "devops.policy.decided"
	ReviewRequested   Type = "devops.review.requested"
	ReviewApproved    Type = "devops.review.approved"
	ReviewRejected    Type = "devops.review.rejected"
	ReviewCommented   Type = "devops.review.commented"
	CallFrozen        Type = "devops.call.frozen"
	CallTargetChanged Type = "devops.call.target_changed"
	CallStopped       Type = "devops.call.stopped"
)

// 执行（45 第 6.3 节）。
const (
	ExecutionStarted    Type = "devops.execution.started"
	ExecutionCompleted  Type = "devops.execution.completed"
	ExecutionMismatched Type = "devops.execution.mismatched"
	ExecutionFailed     Type = "devops.execution.failed"
	ExecutionUnknown    Type = "devops.execution.unknown"
)

// 既定任务（45 第 6.4 节）。
const (
	TaskStarted       Type = "devops.task.started"
	TaskStepCompleted Type = "devops.task.step_completed"
	TaskStopped       Type = "devops.task.stopped"
	TaskCompleted     Type = "devops.task.completed"
)

// 调度（45 第 6.5 节）。
const (
	ScheduleCycleSubmitted Type = "devops.schedule.cycle.submitted"
	ScheduleCycleMissed    Type = "devops.schedule.cycle.missed"
	SchedulePaused         Type = "devops.schedule.paused"
	ScheduleResumed        Type = "devops.schedule.resumed"
)

// 恢复（45 第 6.6 节）。
const (
	RecoveryStarted   Type = "devops.recovery.started"
	RecoveryCompleted Type = "devops.recovery.completed"
)

// 预授权（45 第 6.7 节）。
const (
	PreauthIssued    Type = "devops.preauth.issued"
	PreauthUsed      Type = "devops.preauth.used"
	PreauthExhausted Type = "devops.preauth.exhausted"
	PreauthExpired   Type = "devops.preauth.expired"
	PreauthRevoked   Type = "devops.preauth.revoked"
)

// 运维表格、部署、事实、告警、Agent（45 第 6.8 节）。
const (
	TableUpdated             Type = "devops.table.updated"
	TableDailyCycleCompleted Type = "devops.table.daily_cycle.completed"
	TableDailyCycleFailed    Type = "devops.table.daily_cycle.failed"
	DeploymentStateChanged   Type = "devops.deployment.state_changed"
	FactUpdated              Type = "devops.fact.updated"
	FactExpired              Type = "devops.fact.expired"
	FactConflict             Type = "devops.fact.conflict"
	AlertOpened              Type = "devops.alert.opened"
	AlertResolved            Type = "devops.alert.resolved"
	AlertDeliveryFailed      Type = "devops.alert.delivery_failed"
	AgentInjectionReported   Type = "devops.agent.injection_reported"
)

var all = []Type{
	CallProposed, CallRevised, InspectionStarted, InspectionPassed, InspectionFailed,
	PolicyDecided, ReviewRequested, ReviewApproved, ReviewRejected, ReviewCommented,
	CallFrozen, CallTargetChanged, CallStopped,
	ExecutionStarted, ExecutionCompleted, ExecutionMismatched, ExecutionFailed, ExecutionUnknown,
	TaskStarted, TaskStepCompleted, TaskStopped, TaskCompleted,
	ScheduleCycleSubmitted, ScheduleCycleMissed, SchedulePaused, ScheduleResumed,
	RecoveryStarted, RecoveryCompleted,
	PreauthIssued, PreauthUsed, PreauthExhausted, PreauthExpired, PreauthRevoked,
	TableUpdated, TableDailyCycleCompleted, TableDailyCycleFailed, DeploymentStateChanged,
	FactUpdated, FactExpired, FactConflict, AlertOpened, AlertResolved, AlertDeliveryFailed,
	AgentInjectionReported,
}

// All 返回全部事件类型（按 45 第 6 节的顺序）。
func All() []Type { return append([]Type(nil), all...) }

// Valid 判断是不是已定义的事件类型。
func (t Type) Valid() bool {
	for _, v := range all {
		if v == t {
			return true
		}
	}
	return false
}
