-- audit 库：审计记录，只追加（62 第 3.1 节）。

CREATE TABLE audit_records (
    id        bigint GENERATED ALWAYS AS IDENTITY,
    at        timestamptz NOT NULL,
    actor     text        NOT NULL,
    action    text        NOT NULL,
    call_id   text        NOT NULL,
    revision  integer     NOT NULL,
    run_id    text        NOT NULL,
    schema_id text        NOT NULL,
    doc       jsonb       NOT NULL,
    CONSTRAINT audit_records_pkey PRIMARY KEY (id)
);

CREATE INDEX audit_records_call_idx ON audit_records (call_id) WHERE call_id <> '';
CREATE INDEX audit_records_run_idx ON audit_records (run_id) WHERE run_id <> '';

-- 拒绝修改、删除和清空。
CREATE FUNCTION audit_records_reject() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    RAISE EXCEPTION 'audit_records is append-only: % rejected', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER audit_records_no_update_delete
    BEFORE UPDATE OR DELETE ON audit_records
    FOR EACH ROW EXECUTE FUNCTION audit_records_reject();

CREATE TRIGGER audit_records_no_truncate
    BEFORE TRUNCATE ON audit_records
    FOR EACH STATEMENT EXECUTE FUNCTION audit_records_reject();
