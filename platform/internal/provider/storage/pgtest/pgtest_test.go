package pgtest

import (
	"context"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(Main(m)) }

func count(t *testing.T, e *Env, q string) int {
	t.Helper()
	var n int
	if err := e.Harness.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStartGivesMigratedIsolatedDatabases(t *testing.T) {
	ctx := context.Background()
	a, b := Start(t), Start(t)
	if a.HarnessDSN == b.HarnessDSN {
		t.Fatal("databases are shared")
	}
	for name, tc := range map[string]struct {
		e     *Env
		table string
	}{
		"requests":         {a, "requests"},
		"outbox":           {a, "outbox"},
		"request_revision": {a, "request_revisions"},
		"task_runs":        {a, "task_runs"},
	} {
		if n := count(t, tc.e, "SELECT count(*) FROM "+tc.table); n != 0 {
			t.Errorf("%s: %d rows in a fresh database", name, n)
		}
	}
	if _, err := a.Harness.Exec(ctx, `INSERT INTO outbox (event_id, stream_id, sequence, event_type, occurred_at, schema_id, doc)
		VALUES ('e', 's', 1, 't', now(), 'x', '{}')`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, b, "SELECT count(*) FROM outbox"); n != 0 {
		t.Fatalf("second database sees %d rows from the first", n)
	}
	if _, err := a.Config.Exec(ctx, "SELECT 1 FROM task_definitions, task_active"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Audit.Exec(ctx, "SELECT 1 FROM audit_records"); err != nil {
		t.Fatal(err)
	}
}

func TestWithDatabase(t *testing.T) {
	for in, want := range map[string]string{
		"postgres://u:p@h:5432/old?sslmode=disable": "postgres://u:p@h:5432/new?sslmode=disable",
		"host=/tmp/x user=postgres dbname=postgres": "host=/tmp/x user=postgres dbname=postgres dbname=new",
	} {
		if got := withDatabase(in, "new"); got != want {
			t.Errorf("withDatabase(%q) = %q, want %q", in, got, want)
		}
	}
}
