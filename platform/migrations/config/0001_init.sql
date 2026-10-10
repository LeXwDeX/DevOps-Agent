-- config 库：任务定义与启用版本（62 第 3.1 节）。

CREATE TABLE task_definitions (
    task_id    text        NOT NULL,
    version    bigint      NOT NULL,
    digest     text        NOT NULL,
    schema_id  text        NOT NULL,
    doc        jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT task_definitions_pkey PRIMARY KEY (task_id, version),
    CONSTRAINT task_definitions_version_positive CHECK (version > 0)
);

-- 启用版本指针：必须指向已登记的版本。
CREATE TABLE task_active (
    task_id    text        NOT NULL,
    version    bigint      NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT task_active_pkey PRIMARY KEY (task_id),
    CONSTRAINT task_active_definition_fkey FOREIGN KEY (task_id, version)
        REFERENCES task_definitions (task_id, version)
);
