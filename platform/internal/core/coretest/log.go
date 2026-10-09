package coretest

import (
	"sync"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
)

// EventLog 按先后顺序记录多个替身收到的全部事件，用来检查跨替身的事件顺序。
type EventLog struct {
	mu   sync.Mutex
	evts []events.Event
}

// Append 追加事件（存克隆）。
func (l *EventLog) Append(evts ...events.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range evts {
		l.evts = append(l.evts, e.Clone())
	}
}

// All 返回全部事件的克隆。
func (l *EventLog) All() []events.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneEvents(l.evts)
}

func cloneEvents(in []events.Event) []events.Event {
	out := make([]events.Event, len(in))
	for i, e := range in {
		out[i] = e.Clone()
	}
	return out
}

// EventRecorder 由会记录事件的替身实现，契约测试用它检查事件是否随状态一起保存。
type EventRecorder interface {
	Events() []events.Event
}

// recorder 是替身内部的事件记录。持有者负责加锁。
type recorder struct {
	evts []events.Event
	log  *EventLog
}

func (r *recorder) add(evts []events.Event) {
	for _, e := range evts {
		r.evts = append(r.evts, e.Clone())
	}
	if r.log != nil {
		r.log.Append(evts...)
	}
}
