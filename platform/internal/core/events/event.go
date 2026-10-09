package events

import (
	"slices"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// SourceHostConfirmed 是宿主核验过的来源类别（45 第 3 节）。
const SourceHostConfirmed = "HostConfirmed"

// 事件流 ID 前缀：执行申请以 CallID 为流，任务运行以 RunID 为流（61 第 8.2 节）。
const (
	CallStreamPrefix = "call:"
	RunStreamPrefix  = "run:"
)

// Event 是事件信封（45 第 2 节）。Producer 由装配它的二进制填写，不在 core 里。
type Event struct {
	SchemaVersion string         `json:"schema_version"`
	EventID       string         `json:"event_id"`
	Type          Type           `json:"type"`
	StreamID      string         `json:"stream_id"`
	Sequence      int64          `json:"sequence"`
	OccurredAt    time.Time      `json:"occurred_at"`
	SourceKind    string         `json:"source_kind"`
	SessionID     string         `json:"session_id,omitempty"`
	RunID         string         `json:"run_id,omitempty"`
	CallID        ids.CallID     `json:"call_id,omitempty"`
	Revision      ids.Revision   `json:"revision,omitempty"`
	Project       string         `json:"project,omitempty"`
	Environment   string         `json:"environment,omitempty"`
	Targets       []string       `json:"targets,omitempty"`
	Status        string         `json:"status,omitempty"` // 事件后的申请或运行状态
	Data          map[string]any `json:"data,omitempty"`   // 只放摘要、引用和错误码，不放内容原文
}

// Clone 深拷贝。
func (e Event) Clone() Event {
	e.Targets = slices.Clone(e.Targets)
	e.Data = request.CloneMap(e.Data)
	return e
}

// Validate 检查信封的必填项。
func (e Event) Validate() error {
	switch {
	case !e.Type.Valid():
		return errcode.Newf(errcode.InvalidArgument, "unknown event type %q", e.Type)
	case e.EventID == "":
		return errcode.New(errcode.InvalidArgument, "event id is required")
	case e.StreamID == "":
		return errcode.New(errcode.InvalidArgument, "stream id is required")
	case e.Sequence < 1:
		return errcode.New(errcode.InvalidArgument, "sequence must be >= 1")
	}
	return nil
}

// ForRequest 构造一条执行申请事件，并把 r.EventSeq 加一作为事件序号。
// 调用时 r 应已是转换后的状态：Status 取 r.State。
// data 只能放摘要、引用和错误码；Rendered 等内容原文不得放入（45 第 6.1 节）。
func ForRequest(r *request.ExecutionRequest, t Type, data map[string]any, now time.Time, eventID string) Event {
	r.EventSeq++
	e := Event{
		SchemaVersion: core.EventV1,
		EventID:       eventID,
		Type:          t,
		StreamID:      CallStreamPrefix + string(r.CallID),
		Sequence:      r.EventSeq,
		OccurredAt:    now,
		SourceKind:    SourceHostConfirmed,
		SessionID:     r.SessionID,
		CallID:        r.CallID,
		Revision:      r.Revision,
		Project:       r.Scope.Project,
		Environment:   r.Scope.Environment,
		Targets:       slices.Clone(r.Scope.Targets),
		Status:        string(r.State),
		Data:          request.CloneMap(data),
	}
	if r.Task != nil {
		e.RunID = r.Task.RunID
	}
	return e
}

// ForRun 构造一条任务运行事件，并把 run.EventSeq 加一作为事件序号。Status 取 run.State。
func ForRun(run *task.Run, t Type, data map[string]any, now time.Time, eventID string) Event {
	run.EventSeq++
	return Event{
		SchemaVersion: core.EventV1,
		EventID:       eventID,
		Type:          t,
		StreamID:      RunStreamPrefix + run.RunID,
		Sequence:      run.EventSeq,
		OccurredAt:    now,
		SourceKind:    SourceHostConfirmed,
		SessionID:     run.SessionID,
		RunID:         run.RunID,
		Project:       run.Scope.Project,
		Environment:   run.Scope.Environment,
		Targets:       slices.Clone(run.Scope.Targets),
		Status:        string(run.State),
		Data:          request.CloneMap(data),
	}
}
