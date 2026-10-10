package pgstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/events"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/ids"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/model/task"
	schema "github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/core"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// TaskStore 实现 ports.TaskStore：任务定义和启用指针在 config 库，运行和事件在 harness 库。
// 并发安全（连接池）。
type TaskStore struct {
	config  *pgxpool.Pool
	harness *pgxpool.Pool
}

var _ ports.TaskStore = (*TaskStore)(nil)

// NewTaskStore 返回任务存储。连接池由调用方创建和关闭。
func NewTaskStore(config, harness *pgxpool.Pool) *TaskStore {
	return &TaskStore{config: config, harness: harness}
}

func decodeRun(schemaID string, raw []byte) (*task.Run, error) {
	var doc schema.RunDocV1
	if err := decodeStored("run", schemaID, schema.TaskRunV1, raw, &doc); err != nil {
		return nil, err
	}
	return runFromDoc(doc)
}

func decodeDefinition(schemaID string, raw []byte) (*task.Definition, error) {
	var doc schema.TaskDefinitionDocV1
	if err := decodeStored("task definition", schemaID, schema.TaskDefinitionV1, raw, &doc); err != nil {
		return nil, err
	}
	return definitionFromDoc(doc), nil
}

// ---- 定义与启用版本（config 库）----

// Definition 返回指定版本的任务定义；不存在返回 core.not_found。
func (s *TaskStore) Definition(ctx context.Context, id string, version int64) (*task.Definition, error) {
	var sid string
	var raw []byte
	err := s.config.QueryRow(ctx, sqlDefinitionGet, id, version).Scan(&sid, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errcode.Newf(errcode.NotFound, "task %s version %d", id, version)
	}
	if err != nil {
		return nil, mapErr(ctx, "get definition", err)
	}
	return decodeDefinition(sid, raw)
}

// ActiveVersion 返回当前启用的版本；任务不存在或没有启用版本返回 core.not_found。
func (s *TaskStore) ActiveVersion(ctx context.Context, id string) (int64, error) {
	var v int64
	err := s.config.QueryRow(ctx, sqlActiveGet, id).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errcode.Newf(errcode.NotFound, "task %s has no active version", id)
	}
	if err != nil {
		return 0, mapErr(ctx, "get active version", err)
	}
	return v, nil
}

// PutDefinition 登记一个任务定义版本。已登记的版本不可改：同一 (ID, 版本) 再登记返回 core.already_exists。
// 登记中心（module 块）和测试夹具使用；core 不调用。
func (s *TaskStore) PutDefinition(ctx context.Context, def *task.Definition) error {
	if def == nil || def.ID == "" || def.Version <= 0 {
		return errcode.New(errcode.InvalidArgument, "definition with id and positive version is required")
	}
	raw, err := encodeDoc("task definition", definitionToDoc(def), func(d schema.TaskDefinitionDocV1) (*task.Definition, error) {
		return definitionFromDoc(d), nil
	})
	if err != nil {
		return err
	}
	_, err = s.config.Exec(ctx, sqlDefinitionInsert, def.ID, def.Version, string(def.Digest), schema.TaskDefinitionV1, raw)
	if isConstraint(err, pgUniqueViolation, conDefinitionsPkey) {
		return errcode.Newf(errcode.AlreadyExists, "task %s version %d", def.ID, def.Version)
	}
	return mapErr(ctx, "put definition", err)
}

// SetActive 把启用指针指向已登记的版本；版本未登记返回 core.not_found。
func (s *TaskStore) SetActive(ctx context.Context, id string, version int64) error {
	_, err := s.config.Exec(ctx, sqlActiveUpsert, id, version)
	if isConstraint(err, pgFKViolation, conActiveDefinitionK) {
		return errcode.Newf(errcode.NotFound, "task %s version %d", id, version)
	}
	return mapErr(ctx, "set active version", err)
}

// ---- 运行（harness 库）----

// GetRun 读取运行；不存在返回 core.not_found。
func (s *TaskStore) GetRun(ctx context.Context, runID string) (*task.Run, error) {
	var sid string
	var raw []byte
	err := s.harness.QueryRow(ctx, sqlRunGet, runID).Scan(&sid, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errcode.Newf(errcode.NotFound, "run %s", runID)
	}
	if err != nil {
		return nil, mapErr(ctx, "get run", err)
	}
	return decodeRun(sid, raw)
}

// CreateRun 在一个事务里插入运行和事件；RunID 已存在返回 core.already_exists。
func (s *TaskStore) CreateRun(ctx context.Context, run *task.Run, evts []events.Event) error {
	if run == nil || run.RunID == "" {
		return errcode.New(errcode.InvalidArgument, "run with run id is required")
	}
	raw, err := encodeDoc("run", runToDoc(run), runFromDoc)
	if err != nil {
		return err
	}
	rows, err := prepareEvents(evts)
	if err != nil {
		return err
	}
	return withTx(ctx, s.harness, "create run", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sqlRunInsert,
			run.RunID, run.TaskID, run.TaskVersion, int64(run.Version), string(run.State), run.SessionID, run.EventSeq,
			ut(run.CreatedAt), ut(run.UpdatedAt), schema.TaskRunV1, raw)
		if isConstraint(err, pgUniqueViolation, conTaskRunsPkey) {
			return errcode.Newf(errcode.AlreadyExists, "run %s", run.RunID)
		}
		if err != nil {
			return mapErr(ctx, "insert run", err)
		}
		return insertEvents(ctx, tx, rows)
	})
}

// SaveRun 做 CAS，语义同 HarnessStore.Save。
func (s *TaskStore) SaveRun(ctx context.Context, run *task.Run, expected ids.Version, evts []events.Event) error {
	if run == nil || run.RunID == "" {
		return errcode.New(errcode.InvalidArgument, "run with run id is required")
	}
	if run.Version != expected.Next() {
		return errcode.Newf(errcode.InvalidArgument, "run %s: version must be expected+1 (%d), got %d", run.RunID, expected.Next(), run.Version)
	}
	raw, err := encodeDoc("run", runToDoc(run), runFromDoc)
	if err != nil {
		return err
	}
	rows, err := prepareEvents(evts)
	if err != nil {
		return err
	}
	return withTx(ctx, s.harness, "save run", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sqlRunUpdate,
			run.RunID, int64(expected),
			run.TaskID, run.TaskVersion, int64(run.Version), string(run.State), run.SessionID, run.EventSeq,
			ut(run.CreatedAt), ut(run.UpdatedAt), schema.TaskRunV1, raw)
		if err != nil {
			return mapErr(ctx, "update run", err)
		}
		if tag.RowsAffected() == 0 {
			return versionMismatch(ctx, tx, sqlRunVersion, "run", run.RunID, expected)
		}
		return insertEvents(ctx, tx, rows)
	})
}

// ListNonTerminalRuns 返回全部非终态运行，按 RunID 排序。
func (s *TaskStore) ListNonTerminalRuns(ctx context.Context) ([]*task.Run, error) {
	return queryDocs(ctx, s.harness, "list runs", decodeRun, sqlRunListNonTerminal)
}
