package pgstore

import "github.com/LeXwDeX/DevOps-Agent/platform/migrations"

// 全部固定 SQL。非终态集合必须与 consts 的 IsTerminal 一致（部分索引按同一列表建立），由测试守住。

// ---- harness：执行申请 ----

const sqlRequestGet = `SELECT schema_id, doc FROM requests WHERE call_id = $1`

const sqlRequestInsert = `INSERT INTO requests
  (call_id, version, revision, state, session_id, event_seq, created_at, updated_at, schema_id, doc)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

const sqlRequestUpdate = `UPDATE requests SET
  version = $3, revision = $4, state = $5, session_id = $6, event_seq = $7,
  created_at = $8, updated_at = $9, schema_id = $10, doc = $11
WHERE call_id = $1 AND version = $2`

const sqlRequestVersion = `SELECT version FROM requests WHERE call_id = $1`

const sqlRequestListNonTerminal = `SELECT schema_id, doc FROM requests
WHERE state NOT IN ('rejected', 'succeeded', 'mismatched', 'failed', 'unknown', 'stopped')
ORDER BY call_id COLLATE "C"`

// ---- harness：修订日志 ----

const sqlRevisionInsert = `INSERT INTO request_revisions (call_id, revision, digest, schema_id, doc)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT ON CONSTRAINT request_revisions_unique DO NOTHING`

const sqlRevisionList = `SELECT schema_id, doc FROM request_revisions WHERE call_id = $1 ORDER BY id`

// ---- harness：任务运行 ----

const sqlRunGet = `SELECT schema_id, doc FROM task_runs WHERE run_id = $1`

const sqlRunInsert = `INSERT INTO task_runs
  (run_id, task_id, task_version, version, state, session_id, event_seq, created_at, updated_at, schema_id, doc)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

const sqlRunUpdate = `UPDATE task_runs SET
  task_id = $3, task_version = $4, version = $5, state = $6, session_id = $7, event_seq = $8,
  created_at = $9, updated_at = $10, schema_id = $11, doc = $12
WHERE run_id = $1 AND version = $2`

const sqlRunVersion = `SELECT version FROM task_runs WHERE run_id = $1`

const sqlRunListNonTerminal = `SELECT schema_id, doc FROM task_runs
WHERE state NOT IN ('completed', 'stopped', 'cancelled', 'recovery_interrupted')
ORDER BY run_id COLLATE "C"`

// ---- harness：outbox ----

const sqlOutboxInsert = `INSERT INTO outbox (event_id, stream_id, sequence, event_type, occurred_at, schema_id, doc)
VALUES ($1, $2, $3, $4, $5, $6, $7)`

const sqlOutboxPending = `SELECT schema_id, doc FROM outbox WHERE delivered_at IS NULL ORDER BY id LIMIT $1`

const sqlOutboxMarkDelivered = `UPDATE outbox SET delivered_at = now()
WHERE event_id = ANY($1::text[]) AND delivered_at IS NULL`

// ---- config：任务定义与启用版本 ----

const sqlDefinitionGet = `SELECT schema_id, doc FROM task_definitions WHERE task_id = $1 AND version = $2`

const sqlDefinitionInsert = `INSERT INTO task_definitions (task_id, version, digest, schema_id, doc)
VALUES ($1, $2, $3, $4, $5)`

const sqlActiveGet = `SELECT version FROM task_active WHERE task_id = $1`

const sqlActiveUpsert = `INSERT INTO task_active (task_id, version) VALUES ($1, $2)
ON CONFLICT (task_id) DO UPDATE SET version = EXCLUDED.version, updated_at = now()`

// ---- audit ----

const sqlAuditInsert = `INSERT INTO audit_records (at, actor, action, call_id, revision, run_id, schema_id, doc)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

// namedQuery 是清单里的一条 SQL：所在的库和参数个数，供 PREPARE 检查使用。
type namedQuery struct {
	Name   string
	DB     string // migrations.Harness / Config / Audit
	SQL    string
	Params int
}

// allQueries 是全部 SQL 的清单。新增 SQL 常量必须同时加进来（测试检查）。
var allQueries = []namedQuery{
	{"RequestGet", migrations.Harness, sqlRequestGet, 1},
	{"RequestInsert", migrations.Harness, sqlRequestInsert, 10},
	{"RequestUpdate", migrations.Harness, sqlRequestUpdate, 11},
	{"RequestVersion", migrations.Harness, sqlRequestVersion, 1},
	{"RequestListNonTerminal", migrations.Harness, sqlRequestListNonTerminal, 0},
	{"RevisionInsert", migrations.Harness, sqlRevisionInsert, 5},
	{"RevisionList", migrations.Harness, sqlRevisionList, 1},
	{"RunGet", migrations.Harness, sqlRunGet, 1},
	{"RunInsert", migrations.Harness, sqlRunInsert, 11},
	{"RunUpdate", migrations.Harness, sqlRunUpdate, 12},
	{"RunVersion", migrations.Harness, sqlRunVersion, 1},
	{"RunListNonTerminal", migrations.Harness, sqlRunListNonTerminal, 0},
	{"OutboxInsert", migrations.Harness, sqlOutboxInsert, 7},
	{"OutboxPending", migrations.Harness, sqlOutboxPending, 1},
	{"OutboxMarkDelivered", migrations.Harness, sqlOutboxMarkDelivered, 1},
	{"DefinitionGet", migrations.Config, sqlDefinitionGet, 2},
	{"DefinitionInsert", migrations.Config, sqlDefinitionInsert, 5},
	{"ActiveGet", migrations.Config, sqlActiveGet, 1},
	{"ActiveUpsert", migrations.Config, sqlActiveUpsert, 2},
	{"AuditInsert", migrations.Audit, sqlAuditInsert, 8},
}
