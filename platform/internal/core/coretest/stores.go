package coretest

import (
	"context"
	"sort"
	"sync"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/request"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// ---- RequestStore ----

// MemRequestStore 是 ports.RequestStore 的内存实现：存取克隆，CAS，状态和事件同一把锁内提交。
// 方法名（故障注入和计数用）：Get、Create、Save、ListNonTerminal。
type MemRequestStore struct {
	Faults
	mu   sync.Mutex
	reqs map[ids.CallID]*request.ExecutionRequest
	rec  recorder
}

var (
	_ ports.RequestStore = (*MemRequestStore)(nil)
	_ EventRecorder      = (*MemRequestStore)(nil)
)

func NewMemRequestStore() *MemRequestStore {
	return &MemRequestStore{reqs: map[ids.CallID]*request.ExecutionRequest{}}
}

// SetLog 让该替身收到的事件同时记入共享的 EventLog。
func (s *MemRequestStore) SetLog(l *EventLog) { s.mu.Lock(); s.rec.log = l; s.mu.Unlock() }

// Seed 直接放入一个申请，不走 CAS、不记事件，用来准备测试数据。
func (s *MemRequestStore) Seed(r *request.ExecutionRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs[r.CallID] = r.Clone()
}

// Events 返回 Create 和 Save 收到的全部事件（按先后顺序，克隆）。
func (s *MemRequestStore) Events() []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneEvents(s.rec.evts)
}

func (s *MemRequestStore) Get(ctx context.Context, id ids.CallID) (*request.ExecutionRequest, error) {
	if err := s.begin(ctx, "Get"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reqs[id]
	if !ok {
		return nil, errcode.Newf(errcode.NotFound, "call %s", id)
	}
	return r.Clone(), nil
}

func (s *MemRequestStore) Create(ctx context.Context, r *request.ExecutionRequest, evts []events.Event) error {
	if err := s.begin(ctx, "Create"); err != nil {
		return err
	}
	if r == nil || !r.CallID.Valid() {
		return errcode.New(errcode.InvalidArgument, "request with call id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.reqs[r.CallID]; ok {
		return errcode.Newf(errcode.AlreadyExists, "call %s", r.CallID)
	}
	s.reqs[r.CallID] = r.Clone()
	s.rec.add(evts)
	return nil
}

func (s *MemRequestStore) Save(ctx context.Context, r *request.ExecutionRequest, expected ids.Version, evts []events.Event) error {
	if err := s.begin(ctx, "Save"); err != nil {
		return err
	}
	if r == nil {
		return errcode.New(errcode.InvalidArgument, "request is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.reqs[r.CallID]
	if !ok {
		return errcode.Newf(errcode.NotFound, "call %s", r.CallID)
	}
	if cur.Version != expected {
		return errcode.Newf(errcode.CASConflict, "call %s: stored version %d, expected %d", r.CallID, cur.Version, expected)
	}
	if r.Version != expected.Next() {
		return errcode.Newf(errcode.InvalidArgument, "call %s: version must be expected+1 (%d), got %d", r.CallID, expected.Next(), r.Version)
	}
	s.reqs[r.CallID] = r.Clone()
	s.rec.add(evts)
	return nil
}

func (s *MemRequestStore) ListNonTerminal(ctx context.Context) ([]*request.ExecutionRequest, error) {
	if err := s.begin(ctx, "ListNonTerminal"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*request.ExecutionRequest
	for _, r := range s.reqs {
		if !r.State.IsTerminal() {
			out = append(out, r.Clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CallID < out[j].CallID })
	return out, nil
}

// ---- RevisionLog ----

// MemRevisionLog 是 ports.RevisionLog 的内存实现，只追加。方法名：Append、List。
type MemRevisionLog struct {
	Faults
	mu   sync.Mutex
	logs map[ids.CallID][]*request.ExecutionRequest
}

var _ ports.RevisionLog = (*MemRevisionLog)(nil)

func NewMemRevisionLog() *MemRevisionLog {
	return &MemRevisionLog{logs: map[ids.CallID][]*request.ExecutionRequest{}}
}

func (l *MemRevisionLog) Append(ctx context.Context, old *request.ExecutionRequest) error {
	if err := l.begin(ctx, "Append"); err != nil {
		return err
	}
	if old == nil || !old.CallID.Valid() {
		return errcode.New(errcode.InvalidArgument, "request with call id is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logs[old.CallID] = append(l.logs[old.CallID], old.Clone())
	return nil
}

// List 按追加顺序返回；调用没有记录时返回空列表而不是错误。
func (l *MemRevisionLog) List(ctx context.Context, id ids.CallID) ([]*request.ExecutionRequest, error) {
	if err := l.begin(ctx, "List"); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*request.ExecutionRequest, 0, len(l.logs[id]))
	for _, r := range l.logs[id] {
		out = append(out, r.Clone())
	}
	return out, nil
}

// ---- TaskStore ----

// DefinitionSeeder 由能直接放入任务定义的 TaskStore 实现，契约测试用它准备数据。
type DefinitionSeeder interface {
	SeedDefinition(def *task.Definition, active bool)
}

// MemTaskStore 是 ports.TaskStore 的内存实现。
// 方法名：Definition、ActiveVersion、GetRun、CreateRun、SaveRun、ListNonTerminalRuns。
type MemTaskStore struct {
	Faults
	mu     sync.Mutex
	defs   map[string]map[int64]*task.Definition
	active map[string]int64
	runs   map[string]*task.Run
	rec    recorder
}

var (
	_ ports.TaskStore  = (*MemTaskStore)(nil)
	_ DefinitionSeeder = (*MemTaskStore)(nil)
	_ EventRecorder    = (*MemTaskStore)(nil)
)

func NewMemTaskStore() *MemTaskStore {
	return &MemTaskStore{
		defs:   map[string]map[int64]*task.Definition{},
		active: map[string]int64{},
		runs:   map[string]*task.Run{},
	}
}

func (s *MemTaskStore) SetLog(l *EventLog) { s.mu.Lock(); s.rec.log = l; s.mu.Unlock() }

// SeedDefinition 放入一个任务定义；active 为 true 时把它设为启用版本。
func (s *MemTaskStore) SeedDefinition(def *task.Definition, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.defs[def.ID] == nil {
		s.defs[def.ID] = map[int64]*task.Definition{}
	}
	s.defs[def.ID][def.Version] = def.Clone()
	if active {
		s.active[def.ID] = def.Version
	}
}

// SeedRun 直接放入一个运行，不走 CAS、不记事件。
func (s *MemTaskStore) SeedRun(run *task.Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.RunID] = run.Clone()
}

// SetActive 切换启用版本（模拟任务升级）。
func (s *MemTaskStore) SetActive(id string, version int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[id] = version
}

// Events 返回 CreateRun 和 SaveRun 收到的全部事件。
func (s *MemTaskStore) Events() []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneEvents(s.rec.evts)
}

func (s *MemTaskStore) Definition(ctx context.Context, id string, version int64) (*task.Definition, error) {
	if err := s.begin(ctx, "Definition"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.defs[id][version]
	if !ok {
		return nil, errcode.Newf(errcode.NotFound, "task %s version %d", id, version)
	}
	return d.Clone(), nil
}

func (s *MemTaskStore) ActiveVersion(ctx context.Context, id string) (int64, error) {
	if err := s.begin(ctx, "ActiveVersion"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.active[id]
	if !ok {
		return 0, errcode.Newf(errcode.NotFound, "task %s has no active version", id)
	}
	return v, nil
}

func (s *MemTaskStore) GetRun(ctx context.Context, runID string) (*task.Run, error) {
	if err := s.begin(ctx, "GetRun"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[runID]
	if !ok {
		return nil, errcode.Newf(errcode.NotFound, "run %s", runID)
	}
	return r.Clone(), nil
}

func (s *MemTaskStore) CreateRun(ctx context.Context, run *task.Run, evts []events.Event) error {
	if err := s.begin(ctx, "CreateRun"); err != nil {
		return err
	}
	if run == nil || run.RunID == "" {
		return errcode.New(errcode.InvalidArgument, "run with run id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[run.RunID]; ok {
		return errcode.Newf(errcode.AlreadyExists, "run %s", run.RunID)
	}
	s.runs[run.RunID] = run.Clone()
	s.rec.add(evts)
	return nil
}

func (s *MemTaskStore) SaveRun(ctx context.Context, run *task.Run, expected ids.Version, evts []events.Event) error {
	if err := s.begin(ctx, "SaveRun"); err != nil {
		return err
	}
	if run == nil {
		return errcode.New(errcode.InvalidArgument, "run is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.runs[run.RunID]
	if !ok {
		return errcode.Newf(errcode.NotFound, "run %s", run.RunID)
	}
	if cur.Version != expected {
		return errcode.Newf(errcode.CASConflict, "run %s: stored version %d, expected %d", run.RunID, cur.Version, expected)
	}
	if run.Version != expected.Next() {
		return errcode.Newf(errcode.InvalidArgument, "run %s: version must be expected+1 (%d), got %d", run.RunID, expected.Next(), run.Version)
	}
	s.runs[run.RunID] = run.Clone()
	s.rec.add(evts)
	return nil
}

func (s *MemTaskStore) ListNonTerminalRuns(ctx context.Context) ([]*task.Run, error) {
	if err := s.begin(ctx, "ListNonTerminalRuns"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*task.Run
	for _, r := range s.runs {
		if !r.State.IsTerminal() {
			out = append(out, r.Clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunID < out[j].RunID })
	return out, nil
}
