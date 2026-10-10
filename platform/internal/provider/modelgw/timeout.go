package modelgw

import (
	"sync"
	"time"
)

// upstreamTimeout 串行处理截止时间、回调、重置和完成。
// expired 一经置位就不再清除；即使回调尚未运行，也不允许重置越过已到的截止时间。
type upstreamTimeout struct {
	mu       sync.Mutex
	timer    *time.Timer
	cancel   func()
	deadline time.Time
	expired  bool
	stopped  bool
}

func newUpstreamTimeout(duration time.Duration, cancel func()) *upstreamTimeout {
	t := &upstreamTimeout{cancel: cancel, deadline: time.Now().Add(duration)}
	t.mu.Lock()
	t.timer = time.AfterFunc(duration, t.onTimeout)
	t.mu.Unlock()
	return t
}

func (t *upstreamTimeout) expireLocked() bool {
	if !t.stopped && !t.expired && !time.Now().Before(t.deadline) {
		t.expired = true
		return true
	}
	return false
}

func (t *upstreamTimeout) onTimeout() {
	t.mu.Lock()
	if t.stopped || t.expired {
		t.mu.Unlock()
		return
	}
	if remaining := time.Until(t.deadline); remaining > 0 {
		// Reset 前开始的旧回调必须遵守更新后的截止时间。
		t.timer.Reset(remaining)
		t.mu.Unlock()
		return
	}
	t.expired = true
	t.mu.Unlock()
	t.cancel()
}

func (t *upstreamTimeout) timedOut() bool {
	t.mu.Lock()
	newlyExpired := t.expireLocked()
	expired := t.expired
	t.mu.Unlock()
	if newlyExpired {
		t.cancel()
	}
	return expired
}

func (t *upstreamTimeout) reset(duration time.Duration) bool {
	t.mu.Lock()
	newlyExpired := t.expireLocked()
	ok := !t.expired && !t.stopped
	if ok {
		t.deadline = time.Now().Add(duration)
		t.timer.Reset(duration)
	}
	t.mu.Unlock()
	if newlyExpired {
		t.cancel()
	}
	return ok
}

// finish 在提交成功前确认截止时间，再阻止任何迟到回调改变结果。
func (t *upstreamTimeout) finish() bool {
	t.mu.Lock()
	newlyExpired := t.expireLocked()
	ok := !t.expired && !t.stopped
	t.stopped = true
	t.timer.Stop()
	t.mu.Unlock()
	if newlyExpired {
		t.cancel()
	}
	return ok
}

func (t *upstreamTimeout) stop() {
	t.mu.Lock()
	t.stopped = true
	t.timer.Stop()
	t.mu.Unlock()
}
