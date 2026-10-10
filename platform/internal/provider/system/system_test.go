package system

import (
	"bytes"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/coretest"
	"github.com/LeXwDeX/DevOps-Agent/platform/internal/core/ports"
)

func TestClockContract(t *testing.T) {
	t.Run("constructor", func(t *testing.T) {
		coretest.ClockContract(t, func() ports.Clock { return NewClock() })
	})
	t.Run("zero value", func(t *testing.T) {
		coretest.ClockContract(t, func() ports.Clock { return &Clock{} })
	})
}

func TestClockClampsRollbackAndResumes(t *testing.T) {
	wall := time.Date(2026, 10, 10, 12, 0, 0, 123456789, time.FixedZone("test", 8*3600))
	c := &Clock{now: func() time.Time { return wall }}
	first := c.Now()
	if want := wall.UTC().Truncate(time.Microsecond); first != want {
		t.Fatalf("first time = %v, want %v", first, want)
	}
	wall = wall.Add(-time.Hour)
	if got := c.Now(); got != first {
		t.Fatalf("rollback time = %v, want %v", got, first)
	}
	wall = first.Add(-time.Nanosecond)
	if got := c.Now(); got != first {
		t.Fatalf("sub-microsecond rollback = %v, want %v", got, first)
	}
	wall = first.Add(time.Microsecond + 999*time.Nanosecond)
	if got, want := c.Now(), first.Add(time.Microsecond); got != want {
		t.Fatalf("after wall clock catches up = %v, want %v", got, want)
	}
}

func TestClockConcurrentRollback(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	reads := 0 // 时间源也在 Clock 的互斥区内读取。
	c := &Clock{now: func() time.Time {
		reads++
		if reads%2 == 0 {
			return base.Add(-time.Hour)
		}
		return base.Add(time.Duration(reads) * time.Microsecond)
	}}
	coretest.ClockContract(t, func() ports.Clock { return c })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prev := c.Now()
			for j := 0; j < 100; j++ {
				now := c.Now()
				if now.Before(prev) {
					t.Errorf("time went backwards: %v after %v", now, prev)
				}
				prev = now
			}
		}()
	}
	wg.Wait()
}

func TestClockIsUTCMicroseconds(t *testing.T) {
	c := NewClock()
	for i := 0; i < 1000; i++ {
		n := c.Now()
		if n.Location() != time.UTC {
			t.Fatalf("location = %v", n.Location())
		}
		if n.Nanosecond()%1000 != 0 {
			t.Fatalf("not truncated to microseconds: %d", n.Nanosecond())
		}
		if !n.Equal(n.Round(0)) || n != n.Round(0) {
			t.Fatal("monotonic reading not stripped")
		}
	}
	a := c.Now()
	time.Sleep(2 * time.Millisecond)
	if !c.Now().After(a) {
		t.Fatal("clock does not advance")
	}
}

func TestIDGenContract(t *testing.T) {
	coretest.IDGenContract(t, func() ports.IDGen { return NewIDGen() })
}

var idRe = regexp.MustCompile(`^call_[0-9A-HJKMNP-TV-Z]{26}$`)

func TestIDFormat(t *testing.T) {
	g := NewIDGen()
	id := g.NewID("call")
	if !idRe.MatchString(id) {
		t.Fatalf("bad id %q", id)
	}
	if g.NewID("") == "" || !strings.HasPrefix(g.NewID("event"), "event_") {
		t.Fatal("prefix wrong")
	}
	// 第一个字符最大为 7（128 位只占 3 位）。
	if c := id[len("call_")]; c > '7' {
		t.Fatalf("first char %q out of range", c)
	}
}

// 编码与 ULID 规范一致：已知输入得到已知输出，且字符序等于数值序。
func TestEncodeKnownValues(t *testing.T) {
	var zero [randBytes]byte
	if got := encode(0, zero); got != strings.Repeat("0", 26) {
		t.Fatalf("zero = %s", got)
	}
	var ones [randBytes]byte
	for i := range ones {
		ones[i] = 0xff
	}
	if got := encode(1<<48-1, ones); got != "7"+strings.Repeat("Z", 25) {
		t.Fatalf("max = %s", got)
	}
	if got := encode(1, zero); got != "0000000001"+strings.Repeat("0", 16) {
		t.Fatalf("ms=1 = %s", got)
	}
}

func TestIDsSortByTime(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	g := NewIDGenWith(func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x11}, 1024)))
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, g.NewID("x"))
		now = now.Add(time.Millisecond)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("ids not ordered by time: %v", ids)
	}
}

func TestIDsMonotonicWithinMillisecond(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	g := NewIDGenWith(func() time.Time { return now }, bytes.NewReader(make([]byte, 4096)))
	prev := g.NewID("x")
	for i := 0; i < 1000; i++ {
		next := g.NewID("x")
		if next <= prev {
			t.Fatalf("not increasing: %s then %s", prev, next)
		}
		prev = next
	}
}

func TestIDGenInitialEpochUsesRandomness(t *testing.T) {
	var entropy [randBytes]byte
	for i := range entropy {
		entropy[i] = 0x55
	}
	g := NewIDGenWith(func() time.Time { return time.UnixMilli(0) }, bytes.NewReader(entropy[:]))
	first := g.NewID("x")
	if want := "x_" + encode(0, entropy); first != want {
		t.Fatalf("first ID at epoch = %s, want %s", first, want)
	}
	if next := g.NewID("x"); next <= first {
		t.Fatalf("second ID at epoch = %s, must be after %s", next, first)
	}
}

func TestIDsMonotonicWhenClockGoesBackwards(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	g := NewIDGenWith(func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0x01}, 4096)))
	a := g.NewID("x")
	now = now.Add(-time.Hour)
	b := g.NewID("x")
	if b <= a {
		t.Fatalf("clock rollback broke ordering: %s then %s", a, b)
	}
}

// 随机部分在同一毫秒内溢出时进位到下一毫秒，顺序保持。
func TestIDsOverflowCarriesToNextMillisecond(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	rnd := append(bytes.Repeat([]byte{0xff}, randBytes), bytes.Repeat([]byte{0x00}, randBytes)...)
	g := NewIDGenWith(func() time.Time { return now }, bytes.NewReader(rnd))
	a := g.NewID("x") // 随机部分全 1
	b := g.NewID("x") // 溢出
	if b <= a {
		t.Fatalf("overflow broke ordering: %s then %s", a, b)
	}
	if c := g.NewID("x"); c <= b {
		t.Fatalf("logical millisecond broke ordering: %s then %s", b, c)
	}
}

func TestIDGenPanicsWithoutRandomness(t *testing.T) {
	g := NewIDGenWith(time.Now, bytes.NewReader(nil))
	assertPanic(t, "system: random source failed", func() { g.NewID("x") })
}

type brokenRandomSource struct{}

func (brokenRandomSource) Read([]byte) (int, error) {
	return 0, errors.New("mock-sensitive-source-detail")
}

func TestIDGenRandomFailureIsRedactedAndRetryable(t *testing.T) {
	for _, phase := range []string{"initial epoch", "new millisecond", "entropy overflow"} {
		t.Run(phase, func(t *testing.T) {
			now := time.UnixMilli(0)
			seed := make([]byte, randBytes)
			if phase == "entropy overflow" {
				for i := range seed {
					seed[i] = 0xff
				}
			}
			g := NewIDGenWith(func() time.Time { return now }, bytes.NewReader(seed))
			previous := ""
			if phase != "initial epoch" {
				previous = g.NewID("x")
				if phase == "new millisecond" {
					now = now.Add(time.Millisecond)
				}
			}
			g.rnd = brokenRandomSource{}
			assertPanic(t, "system: random source failed", func() { g.NewID("x") })
			// 模拟上层 recover 后恢复随机源；调用必须能结束且生成完整、唯一的新 ID。
			var recoveredEntropy [randBytes]byte
			for i := range recoveredEntropy {
				recoveredEntropy[i] = 0x22
			}
			g.rnd = bytes.NewReader(recoveredEntropy[:])
			next := idWithDeadline(t, g)
			if previous != "" && next <= previous {
				t.Fatalf("retry ID %s is not after %s", next, previous)
			}
			ms := uint64(now.UnixMilli())
			if phase == "entropy overflow" {
				ms++
			}
			if want := "x_" + encode(ms, recoveredEntropy); next != want {
				t.Fatalf("retry ID = %s, want %s", next, want)
			}
		})
	}
}

func TestIDGenPartialEntropyCannotBecomeAnID(t *testing.T) {
	rnd := bytes.NewReader([]byte{0x55, 0x66})
	g := NewIDGenWith(func() time.Time { return time.UnixMilli(0) }, rnd)
	assertPanic(t, "system: random source failed", func() { g.NewID("x") })
	var complete [randBytes]byte
	for i := range complete {
		complete[i] = 0x77
	}
	rnd.Reset(complete[:])
	if got, want := idWithDeadline(t, g), "x_"+encode(0, complete); got != want {
		t.Fatalf("retry after partial entropy = %s, want %s", got, want)
	}
}

func TestIDGenRejectsUnrepresentableTimes(t *testing.T) {
	for _, wall := range []time.Time{
		time.UnixMilli(-1),
		time.UnixMilli(maxMilliseconds + 1),
		// UnixMilli 会溢出成很小的正数；实际日期仍然超过 48 位可表示范围。
		time.Unix(18_446_744_073_709_552, 0),
	} {
		g := NewIDGenWith(func() time.Time { return wall }, bytes.NewReader(make([]byte, randBytes)))
		assertPanic(t, "system: ID time outside 48-bit range", func() { g.NewID("x") })
	}
}

func TestIDGenTimestampExhaustionDoesNotWrap(t *testing.T) {
	g := NewIDGenWith(func() time.Time { return time.UnixMilli(maxMilliseconds) }, bytes.NewReader(bytes.Repeat([]byte{0xff}, randBytes)))
	if got, want := g.NewID("x"), "x_7"+strings.Repeat("Z", 25); got != want {
		t.Fatalf("max ID = %s, want %s", got, want)
	}
	for i := 0; i < 2; i++ {
		assertPanic(t, "system: ID timestamp exhausted", func() { g.NewID("x") })
	}
}

func assertPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		if got := recover(); got != want {
			t.Errorf("panic = %v, want fixed failure %q", got, want)
		}
	}()
	f()
}

func idWithDeadline(t *testing.T, g *IDGen) string {
	t.Helper()
	ids := make(chan string, 1)
	go func() { ids <- g.NewID("x") }()
	select {
	case id := <-ids:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("ID generation remained locked after a recovered failure")
		return ""
	}
}

func TestIDsUniqueUnderConcurrency(t *testing.T) {
	g := NewIDGen()
	const workers, each = 32, 500
	var mu sync.Mutex
	seen := make(map[string]bool, workers*each)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]string, 0, each)
			for i := 0; i < each; i++ {
				local = append(local, g.NewID("call"))
			}
			mu.Lock()
			defer mu.Unlock()
			for _, id := range local {
				if seen[id] {
					t.Errorf("duplicate id %s", id)
				}
				seen[id] = true
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*each {
		t.Fatalf("got %d unique ids", len(seen))
	}
}
