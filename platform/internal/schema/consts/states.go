package consts

// RequestState 是执行申请状态（21 第 8 节）。
type RequestState string

const (
	RequestDraft            RequestState = "draft"
	RequestChecking         RequestState = "checking"
	RequestAwaitingReview   RequestState = "awaiting_review"
	RequestRejected         RequestState = "rejected"
	RequestAwaitingRevision RequestState = "awaiting_revision"
	RequestFrozen           RequestState = "frozen"
	RequestTargetChanged    RequestState = "target_changed"
	RequestExecuting        RequestState = "executing"
	RequestSucceeded        RequestState = "succeeded"
	RequestMismatched       RequestState = "mismatched"
	RequestFailed           RequestState = "failed"
	RequestUnknown          RequestState = "unknown"
	RequestStopped          RequestState = "stopped"
)

var requestStates = []RequestState{
	RequestDraft, RequestChecking, RequestAwaitingReview, RequestRejected,
	RequestAwaitingRevision, RequestFrozen, RequestTargetChanged, RequestExecuting,
	RequestSucceeded, RequestMismatched, RequestFailed, RequestUnknown, RequestStopped,
}

func RequestStates() []RequestState { return append([]RequestState(nil), requestStates...) }

// All 返回该类型全部取值（有序），等同 RequestStates()。
func (RequestState) All() []RequestState { return RequestStates() }

func (s RequestState) Valid() bool { return contains(requestStates, s) }

// IsTerminal 判断终态：终态不再转换。
func (s RequestState) IsTerminal() bool {
	switch s {
	case RequestRejected, RequestSucceeded, RequestMismatched, RequestFailed, RequestUnknown, RequestStopped:
		return true
	}
	return false
}

func ParseRequestState(s string) (RequestState, error) {
	return parse("request state", requestStates, s)
}

// RunState 是既定任务运行状态（23 第 2.6 节）。
type RunState string

const (
	RunPending             RunState = "pending"
	RunRunning             RunState = "running"
	RunAwaitingReview      RunState = "awaiting_review"
	RunAwaitingInput       RunState = "awaiting_input"
	RunCompleted           RunState = "completed"
	RunStopped             RunState = "stopped"
	RunCancelled           RunState = "cancelled"
	RunRecoveryInterrupted RunState = "recovery_interrupted"
)

var runStates = []RunState{
	RunPending, RunRunning, RunAwaitingReview, RunAwaitingInput,
	RunCompleted, RunStopped, RunCancelled, RunRecoveryInterrupted,
}

func RunStates() []RunState { return append([]RunState(nil), runStates...) }

// All 返回该类型全部取值（有序），等同 RunStates()。
func (RunState) All() []RunState { return RunStates() }

func (s RunState) Valid() bool { return contains(runStates, s) }

func (s RunState) IsTerminal() bool {
	switch s {
	case RunCompleted, RunStopped, RunCancelled, RunRecoveryInterrupted:
		return true
	}
	return false
}

func ParseRunState(s string) (RunState, error) { return parse("run state", runStates, s) }
