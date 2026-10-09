-- harness 库：执行申请、修订日志、任务运行、outbox（62 第 3.1 节）。
-- 每张主表的关键字段建列，完整内容放 doc（JSONB），schema_id 记录 doc 的结构版本。

CREATE TABLE requests (
    call_id    text        NOT NULL,
    version    bigint      NOT NULL,
    revision   integer     NOT NULL,
    state      text        NOT NULL,
    session_id text        NOT NULL,
    event_seq  bigint      NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    schema_id  text        NOT NULL,
    doc        jsonb       NOT NULL,
    CONSTRAINT requests_pkey PRIMARY KEY (call_id),
    CONSTRAINT requests_version_positive CHECK (version > 0)
);

-- 只索引非终态：恢复只读这部分。终态集合与 consts.RequestState.IsTerminal 一致，由测试守住。
CREATE INDEX requests_nonterminal_state_idx ON requests (state)
    WHERE state NOT IN ('rejected', 'succeeded', 'mismatched', 'failed', 'unknown', 'stopped');

CREATE TABLE request_revisions (
    id         bigint GENERATED ALWAYS AS IDENTITY,
    call_id    text        NOT NULL,
    revision   integer     NOT NULL,
    digest     text        NOT NULL,
    schema_id  text        NOT NULL,
    doc        jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT request_revisions_pkey PRIMARY KEY (id),
    CONSTRAINT request_revisions_unique UNIQUE (call_id, revision, digest)
);

CREATE TABLE task_runs (
    run_id       text        NOT NULL,
    task_id      text        NOT NULL,
    task_version bigint      NOT NULL,
    version      bigint      NOT NULL,
    state        text        NOT NULL,
    session_id   text        NOT NULL,
    event_seq    bigint      NOT NULL,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    schema_id    text        NOT NULL,
    doc          jsonb       NOT NULL,
    CONSTRAINT task_runs_pkey PRIMARY KEY (run_id),
    CONSTRAINT task_runs_version_positive CHECK (version > 0)
);

-- 终态集合与 consts.RunState.IsTerminal 一致，由测试守住。
CREATE INDEX task_runs_nonterminal_state_idx ON task_runs (state)
    WHERE state NOT IN ('completed', 'stopped', 'cancelled', 'recovery_interrupted');

CREATE TABLE outbox (
    id           bigint GENERATED ALWAYS AS IDENTITY,
    event_id     text        NOT NULL,
    stream_id    text        NOT NULL,
    sequence     bigint      NOT NULL,
    event_type   text        NOT NULL,
    occurred_at  timestamptz NOT NULL,
    schema_id    text        NOT NULL,
    doc          jsonb       NOT NULL,
    delivered_at timestamptz,
    CONSTRAINT outbox_pkey PRIMARY KEY (id),
    CONSTRAINT outbox_event_id_key UNIQUE (event_id),
    CONSTRAINT outbox_stream_sequence_key UNIQUE (stream_id, sequence)
);

-- 只索引未投递的事件，投递服务按 id 顺序读取。
CREATE INDEX outbox_pending_idx ON outbox (id) WHERE delivered_at IS NULL;
