// Package pgtest 只用于测试：准备 PostgreSQL，并为每次 Start 建三个全新的库（harness、config、audit），
// 已执行迁移（62 第 3.7 节）。
//
// 数据库的来源按顺序：
//  1. 环境变量 DEVOPS_TEST_PG_DSN 有值：用它连接已有的服务器（例如 CI 的服务容器），
//     在上面按随机名字建库，测试结束删除。
//  2. 本机有 initdb 和 pg_ctl（在 PATH 里，或在 /usr/lib/postgresql/*/bin）：
//     在临时目录里初始化一个实例，只监听这个目录里的 Unix socket（不开 TCP），测试结束停止并删除。
//     以 root 运行时，initdb 不肯以 root 执行，改用操作系统用户 postgres 运行。
//  3. 都没有：测试跳过，并写明原因。
//
// 一个测试进程共用一个实例；每个测试从预先迁移好的模板库复制出新库，所以测试之间不共享数据。
// 测试包最好在 TestMain 里调用 Main，让实例在全部测试结束后才停止；
// 没有调用 Main 时，最后一个测试结束就停止实例，下一个测试再启动。
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LeXwDeX/DevOps-Agent/platform/migrations"
)

// EnvDSN 是指定外部 PostgreSQL 的环境变量。它是测试专用变量，不属于运行时环境变量表。
const EnvDSN = "DEVOPS_TEST_PG_DSN"

// Env 是一次 Start 得到的三个库。
type Env struct {
	HarnessDSN, ConfigDSN, AuditDSN string
	Harness, Config, Audit          *pgxpool.Pool
}

// Start 返回三个全新的、已迁移的库。没有可用的 PostgreSQL 时跳过测试；
// 有程序但启动失败时让测试失败（不假装通过）。
func Start(t testing.TB) *Env {
	t.Helper()
	srv := acquire(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	e := &Env{}
	var names []string
	t.Cleanup(func() {
		for _, p := range []*pgxpool.Pool{e.Harness, e.Config, e.Audit} {
			if p != nil {
				p.Close()
			}
		}
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		for _, n := range names {
			if _, err := srv.admin.Exec(dctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{n}.Sanitize()+" WITH (FORCE)"); err != nil {
				t.Logf("pgtest: drop database %s: %v", n, err)
			}
		}
		release()
	})

	suffix := randHex(6)
	for _, kind := range migrations.Databases() {
		name := fmt.Sprintf("devops_t_%s_%s", kind, suffix)
		_, err := srv.admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+
			" TEMPLATE "+pgx.Identifier{srv.templates[kind]}.Sanitize())
		if err != nil {
			t.Fatalf("pgtest: create database %s: %v", name, err)
		}
		names = append(names, name)
		dsn := withDatabase(srv.adminDSN, name)
		pool, err := newPool(ctx, dsn)
		if err != nil {
			t.Fatalf("pgtest: connect %s: %v", name, err)
		}
		switch kind {
		case migrations.Harness:
			e.HarnessDSN, e.Harness = dsn, pool
		case migrations.Config:
			e.ConfigDSN, e.Config = dsn, pool
		case migrations.Audit:
			e.AuditDSN, e.Audit = dsn, pool
		}
	}
	return e
}

// Main 供测试包的 TestMain 使用：运行全部测试，之后停止共用的 PostgreSQL 实例。
//
//	func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }
func Main(m *testing.M) int {
	shared.mu.Lock()
	shared.managed = true
	shared.mu.Unlock()
	code := m.Run()
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.srv != nil {
		shared.srv.close()
		shared.srv = nil
	}
	return code
}

// ApplyMigrations 在 conn 上按文件名顺序执行指定库的全部迁移文件。
func ApplyMigrations(ctx context.Context, conn *pgx.Conn, db string) error {
	files, err := migrations.Files(db)
	if err != nil {
		return err
	}
	for _, f := range files {
		// 没有参数的 Exec 走简单协议，可以一次执行多条语句。
		if _, err := conn.Exec(ctx, f.SQL); err != nil {
			return fmt.Errorf("pgtest: migrate %s/%s: %w", db, f.Name, err)
		}
	}
	return nil
}

func newPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 24
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// withDatabase 返回把库名换成 db 的连接串。
func withDatabase(dsn, db string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		if u, err := url.Parse(dsn); err == nil {
			u.Path = "/" + db
			return u.String()
		}
	}
	return dsn + " dbname=" + db // 键值形式：后出现的覆盖先出现的
}

// ---- 共用的服务器 ----

var shared struct {
	mu      sync.Mutex
	srv     *server
	err     error  // 启动失败的原因，之后每次 Start 都报告它
	skip    string // 非空：没有可用的 PostgreSQL，原因
	refs    int
	managed bool // 调用过 Main：实例活到全部测试结束
}

type server struct {
	adminDSN  string // 连到维护库，用来建库和删库
	admin     *pgxpool.Pool
	templates map[string]string // 库类型 -> 已迁移的模板库名
	stopFn    func()            // 停止并清理临时实例；外部服务器为 nil
}

func acquire(t testing.TB) *server {
	t.Helper()
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.skip != "" {
		t.Skip(shared.skip)
	}
	if shared.err != nil {
		t.Fatalf("pgtest: PostgreSQL unavailable: %v", shared.err)
	}
	if shared.srv == nil {
		srv, skip, err := startServer()
		switch {
		case skip != "":
			shared.skip = skip
			t.Skip(skip)
		case err != nil:
			shared.err = err
			t.Fatalf("pgtest: PostgreSQL unavailable: %v", err)
		}
		shared.srv = srv
	}
	shared.refs++
	return shared.srv
}

func release() {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	shared.refs--
	if !shared.managed && shared.refs == 0 && shared.srv != nil {
		shared.srv.close()
		shared.srv = nil
	}
}

func (s *server) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, n := range s.templates {
		_, _ = s.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{n}.Sanitize()+" WITH (FORCE)")
	}
	s.admin.Close()
	if s.stopFn != nil {
		s.stopFn()
	}
}

// startServer 准备一个可用的服务器。skip 非空表示没有 PostgreSQL（跳过测试），err 表示有但失败了。
func startServer() (srv *server, skip string, err error) {
	var adminDSN string
	var stop func()
	if dsn := os.Getenv(EnvDSN); dsn != "" {
		adminDSN = dsn
	} else {
		var sk string
		adminDSN, stop, sk, err = startTempCluster()
		if sk != "" || err != nil {
			return nil, sk, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fail := func(err error) (*server, string, error) {
		if stop != nil {
			stop()
		}
		return nil, "", err
	}
	admin, err := newPool(ctx, adminDSN)
	if err != nil {
		return fail(fmt.Errorf("connect: %w", err))
	}
	s := &server{adminDSN: adminDSN, admin: admin, templates: map[string]string{}, stopFn: stop}
	suffix := randHex(6)
	for _, kind := range migrations.Databases() {
		name := fmt.Sprintf("devops_tmpl_%s_%s", kind, suffix)
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			s.close()
			return nil, "", fmt.Errorf("create template %s: %w", name, err)
		}
		s.templates[kind] = name
		conn, err := pgx.Connect(ctx, withDatabase(adminDSN, name))
		if err != nil {
			s.close()
			return nil, "", fmt.Errorf("connect template %s: %w", name, err)
		}
		err = ApplyMigrations(ctx, conn, kind)
		_ = conn.Close(ctx)
		if err != nil {
			s.close()
			return nil, "", err
		}
	}
	return s, "", nil
}

// ---- 临时实例 ----

const skipNoServer = "pgtest: no PostgreSQL available: set " + EnvDSN +
	" to a server, or install the server programs (initdb, pg_ctl) on PATH or under /usr/lib/postgresql/*/bin"

// findBinDir 找到同时含 initdb 和 pg_ctl 的目录。
func findBinDir() (string, bool) {
	has := func(dir string) bool {
		for _, n := range []string{"initdb", "pg_ctl"} {
			st, err := os.Stat(filepath.Join(dir, n))
			if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
				return false
			}
		}
		return true
	}
	if p, err := exec.LookPath("initdb"); err == nil {
		if dir := filepath.Dir(p); has(dir) {
			return dir, true
		}
	}
	dirs, _ := filepath.Glob("/usr/lib/postgresql/*/bin")
	sort.Slice(dirs, func(i, j int) bool { return versionKey(dirs[i]) > versionKey(dirs[j]) }) // 新版本优先
	for _, d := range dirs {
		if has(d) {
			return d, true
		}
	}
	return "", false
}

func versionKey(dir string) int {
	n, _ := strconv.Atoi(filepath.Base(filepath.Dir(dir)))
	return n
}

// startTempCluster 初始化并启动临时实例，返回连接串和停止函数。
func startTempCluster() (dsn string, stop func(), skip string, err error) {
	bin, ok := findBinDir()
	if !ok {
		return "", nil, skipNoServer, nil
	}

	// initdb 不肯以 root 运行：root 时改用操作系统用户 postgres。
	var cred *syscall.Credential
	var uid, gid int = -1, -1
	if os.Geteuid() == 0 {
		u, uerr := user.Lookup("postgres")
		if uerr != nil {
			return "", nil, "pgtest: running as root and OS user \"postgres\" does not exist, cannot run initdb; set " + EnvDSN, nil
		}
		uid, _ = strconv.Atoi(u.Uid)
		gid, _ = strconv.Atoi(u.Gid)
		cred = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}

	// Unix socket 路径有长度上限，所以目录放在 /tmp 下（TMPDIR 太长时也用 /tmp）。
	base := os.TempDir()
	if len(base) > 40 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "devops-pg-")
	if err != nil {
		return "", nil, "", fmt.Errorf("make temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if cred != nil {
		if err := os.Chown(dir, uid, gid); err != nil {
			cleanup()
			return "", nil, "", fmt.Errorf("chown %s: %w", dir, err)
		}
	}
	data := filepath.Join(dir, "data")
	logFile := filepath.Join(dir, "pg.log")

	run := func(timeout time.Duration, name string, args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(bin, name), args...)
		cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + dir, "LANG=C", "LC_ALL=C", "TZ=UTC"}
		cmd.Dir = dir
		if cred != nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w\n%s\n%s", name, err, out, tail(logFile))
		}
		return nil
	}

	if err := run(time.Minute, "initdb", "-D", data, "-U", "postgres", "-A", "trust", "-E", "UTF8", "--no-locale", "--no-sync"); err != nil {
		cleanup()
		return "", nil, "", err
	}
	opts := strings.Join([]string{
		"-c listen_addresses=''", // 不开 TCP，只用 Unix socket
		"-c unix_socket_directories=" + dir,
		"-c fsync=off", "-c synchronous_commit=off", "-c full_page_writes=off",
		"-c max_connections=200", "-c shared_buffers=32MB", "-c timezone=UTC",
	}, " ")
	if err := run(2*time.Minute, "pg_ctl", "-D", data, "-l", logFile, "-w", "-t", "90", "-o", opts, "start"); err != nil {
		killPostmaster(data)
		cleanup()
		return "", nil, "", err
	}
	stop = func() {
		if err := run(time.Minute, "pg_ctl", "-D", data, "-m", "immediate", "-w", "-t", "30", "stop"); err != nil {
			killPostmaster(data)
		}
		cleanup()
	}
	return "host=" + dir + " user=postgres dbname=postgres", stop, "", nil
}

// killPostmaster 在正常停止失败时按 postmaster.pid 杀掉进程，避免残留。
func killPostmaster(data string) {
	b, err := os.ReadFile(filepath.Join(data, "postmaster.pid"))
	if err != nil {
		return
	}
	first, _, _ := strings.Cut(string(b), "\n")
	if pid, err := strconv.Atoi(strings.TrimSpace(first)); err == nil && pid > 1 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		time.Sleep(200 * time.Millisecond)
	}
}

func tail(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		return err.Error()
	}
	if len(b) > 2000 {
		b = b[len(b)-2000:]
	}
	return "server log tail:\n" + string(b)
}
