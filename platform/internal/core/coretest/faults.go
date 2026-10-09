// Package coretest 提供 core 全部端口的内存测试替身，以及可复用的端口契约测试。
// 替身线程安全，存取都返回克隆，支持故障注入、调用计数和脚本化结果。
// 契约测试是导出函数，provider 的测试可以直接复用。
package coretest

import (
	"context"
	"errors"
	"sync"
)

// ErrInjected 是默认的注入错误，测试也可以注入任何自己的错误（例如 context.DeadlineExceeded）。
var ErrInjected = errors.New("coretest: injected fault")

type faultRule struct {
	err       error
	remaining int // 小于 0 表示一直生效
}

// Faults 是故障注入和调用计数的小工具，嵌入各个替身，零值可用。
// 方法名是替身的方法名，例如 "Save"、"Execute"。
type Faults struct {
	mu    sync.Mutex
	rules map[string]*faultRule
	calls map[string]int
}

// FailWith 让 method 每次调用都返回 err，直到 ClearFaults。
func (f *Faults) FailWith(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rules == nil {
		f.rules = map[string]*faultRule{}
	}
	f.rules[method] = &faultRule{err: err, remaining: -1}
}

// FailNext 让 method 接下来 n 次调用返回 err，之后恢复正常。
func (f *Faults) FailNext(method string, n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rules == nil {
		f.rules = map[string]*faultRule{}
	}
	f.rules[method] = &faultRule{err: err, remaining: n}
}

// ClearFaults 清除所有注入的故障，调用计数不变。
func (f *Faults) ClearFaults() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = nil
}

// CallCount 返回 method 被调用的次数（含因注入故障而失败的调用）。
func (f *Faults) CallCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

// begin 记一次调用，先看注入的故障，再看 ctx 是否已取消。
func (f *Faults) begin(ctx context.Context, method string) error {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[method]++
	var injected error
	if r := f.rules[method]; r != nil && r.remaining != 0 {
		injected = r.err
		if r.remaining > 0 {
			r.remaining--
		}
	}
	f.mu.Unlock()
	if injected != nil {
		return injected
	}
	return ctx.Err()
}

// queue 是脚本化结果队列：先按顺序取推入的结果，取完后一直返回默认值。
type queue[T any] struct {
	mu    sync.Mutex
	items []scripted[T]
	def   T
}

type scripted[T any] struct {
	val T
	err error
}

func (q *queue[T]) push(v T, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = append(q.items, scripted[T]{v, err})
}

func (q *queue[T]) setDefault(v T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.def = v
}

func (q *queue[T]) next() (T, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) > 0 {
		it := q.items[0]
		q.items = q.items[1:]
		return it.val, it.err
	}
	return q.def, nil
}
