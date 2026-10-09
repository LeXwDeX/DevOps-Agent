package coretest

import "github.com/LeXwDeX/DevOps-Agent/platform/internal/model/policy"

// Env 把全部替身装配在一起，并共用一个 EventLog，方便检查跨替身的事件顺序。
type Env struct {
	Requests    *MemRequestStore
	Revisions   *MemRevisionLog
	Tasks       *MemTaskStore
	Policy      *FakePolicySource
	Registry    *FakeRegistry
	Scope       *FakeScopeSource
	Secrets     *FakeSecretKeys
	Preview     *FakePreview
	Fingerprint *FakeFingerprint
	DCG         *FakeDCG
	Executor    *FakeExecutor
	Sink        *MemEventSink
	Audit       *MemAuditSink
	Clock       *FixedClock
	IDs         *SeqIDGen
	Log         *EventLog
}

// NewEnv 创建一套全新的替身。时钟从 Epoch 开始，替身用这个时钟给结果打时间。
func NewEnv() *Env {
	e := &Env{
		Requests:    NewMemRequestStore(),
		Revisions:   NewMemRevisionLog(),
		Tasks:       NewMemTaskStore(),
		Policy:      NewFakePolicySource(policy.Set{}),
		Registry:    NewFakeRegistry(),
		Scope:       NewFakeScopeSource(),
		Secrets:     NewFakeSecretKeys(),
		Preview:     NewFakePreview(),
		Fingerprint: NewFakeFingerprint(),
		DCG:         NewFakeDCG(),
		Executor:    NewFakeExecutor(),
		Sink:        NewMemEventSink(),
		Audit:       NewMemAuditSink(),
		Clock:       NewFixedClock(Epoch),
		IDs:         NewSeqIDGen(),
		Log:         &EventLog{},
	}
	e.Requests.SetLog(e.Log)
	e.Tasks.SetLog(e.Log)
	e.Sink.SetLog(e.Log)
	e.Preview.Clock, e.Fingerprint.Clock, e.DCG.Clock, e.Executor.Clock = e.Clock, e.Clock, e.Clock, e.Clock
	return e
}
