package modelgw

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

const reviewHeaderBudget = 32 << 10

// cancelEOFBody 模拟收到取消后正常结束的上游。取消后的 EOF 不等于成功。
type cancelEOFBody struct {
	ctx   context.Context
	first []byte
	last  []byte
}

func (b *cancelEOFBody) Read(p []byte) (int, error) {
	if len(b.first) > 0 {
		n := copy(p, b.first)
		b.first = b.first[n:]
		return n, nil
	}
	<-b.ctx.Done()
	if len(b.last) > 0 {
		n := copy(p, b.last)
		b.last = b.last[n:]
		return n, io.EOF
	}
	return 0, io.EOF
}

func (*cancelEOFBody) Close() error { return nil }

func TestBufferedCleanEOFOnTimeoutRejectsPartial(t *testing.T) {
	p, err := New(validConfig(), Options{ClientAuth: StaticTokenAuth(clientToken), Timeout: 50 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: -1, Body: &cancelEOFBody{ctx: r.Context(), first: []byte("partial-response-marker")}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(validBody)))
	if w.Code != 502 {
		t.Fatalf("timeout followed by clean EOF committed status %d and a partial response", w.Code)
	}
	if errCode(t, w.Body.Bytes()) != string(errcode.ModelGWUpstreamError) || strings.Contains(w.Body.String(), "partial-response-marker") {
		t.Fatal("timed-out buffered response must be rejected without partial contents")
	}
}

func TestStreamCleanEOFOnTimeoutBreaksClient(t *testing.T) {
	for _, finalData := range []string{"", "late-data-must-not-be-emitted"} {
		t.Run(map[bool]string{true: "zero-byte EOF", false: "data and EOF"}[finalData == ""], func(t *testing.T) {
			p, err := New(validConfig(), Options{ClientAuth: StaticTokenAuth(clientToken), Timeout: 100 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: 200,
					Header:     http.Header{"Content-Type": {"text/event-stream"}},
					Body:       &cancelEOFBody{ctx: r.Context(), first: []byte("data: first\n\n" + testKey[:5]), last: []byte(finalData)},
				}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			s := httptest.NewServer(p)
			t.Cleanup(s.Close)
			r, err := http.NewRequest(http.MethodPost, s.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+clientToken)
			resp, err := s.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			if err == nil {
				t.Fatal("stream timeout followed by clean upstream EOF produced a clean client EOF")
			}
			if strings.Contains(string(b), testKey[:5]) {
				t.Fatal("timed-out stream flushed its held key prefix")
			}
			if finalData != "" && strings.Contains(string(b), finalData) {
				t.Fatal("stream emitted bytes returned after timeout alongside EOF")
			}
		})
	}
}

func TestExpiredResponseCannotCommitStreamHeaders(t *testing.T) {
	p, err := New(validConfig(), Options{ClientAuth: StaticTokenAuth(clientToken), Timeout: 30 * time.Millisecond, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Cache-Control": {"no-store"}}, Body: io.NopCloser(strings.NewReader("late-body"))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(`{"model":"m","messages":[],"stream":true}`)))
	if w.Code != 502 || errCode(t, w.Body.Bytes()) != string(errcode.ModelGWUpstreamError) || w.Header().Get("Cache-Control") != "" || strings.Contains(w.Body.String(), "late-body") {
		t.Fatal("expired response must be rejected before stream headers or body are committed")
	}
}

func TestResponseHeaderMaskBudgetDefeatsExpansion(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered", true: "stream"}[streaming], func(t *testing.T) {
			ct := "application/json; padding=" + strings.Repeat("x", 2048)
			body := validBody
			if streaming {
				ct = "text/event-stream; padding=" + strings.Repeat("x", 2048)
				body = `{"model":"m","messages":[],"stream":true}`
			}
			cfg := LoadConfig(env(map[string]string{EnvBaseURL: "https://model.example/v1", EnvAPIKey: "x"}))
			p, err := New(cfg, Options{ClientAuth: StaticTokenAuth(clientToken), Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(body)))
			if w.Code != 502 {
				t.Fatalf("masked header exceeds %d bytes but committed status %d", reviewHeaderBudget, w.Code)
			}
			if errCode(t, w.Body.Bytes()) != string(errcode.ModelGWTooLarge) {
				t.Fatal("oversized response header must return modelgw.too_large")
			}
		})
	}
}

func TestBaseURLExplicitPortValidation(t *testing.T) {
	for _, host := range []string{
		"model.example:0", "model.example:65536", "model.example:99999", "model.example:" + strings.Repeat("9", 100), "model.example:", "model.example:abc", "[::1]:0", "[::1]:65536", "[::1]:",
	} {
		cfg := LoadConfig(env(map[string]string{EnvBaseURL: "https://" + host + "/v1", EnvAPIKey: testKey}))
		if cfg.Available() || cfg.Status().State != StateInvalid {
			t.Errorf("invalid explicit port %q is available", host)
		}
		if strings.Contains(cfg.Status().Reason, host) || strings.Contains(cfg.Status().Reason, testKey) {
			t.Error("configuration reason leaked an endpoint or key")
		}
		calls := 0
		p, err := New(cfg, Options{ClientAuth: StaticTokenAuth(clientToken), Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("unused"))}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(validBody)))
		if calls != 0 || w.Code != 503 || errCode(t, w.Body.Bytes()) != string(errcode.ModelGWUnavailable) {
			t.Error("invalid port configuration must reject without contacting upstream")
		}
	}
	for _, host := range []string{"model.example", "model.example:443", "model.example:65535", "[::1]", "[::1]:443", "[::1]:65535"} {
		cfg := LoadConfig(env(map[string]string{EnvBaseURL: "https://" + host + "/v1", EnvAPIKey: testKey}))
		if !cfg.Available() {
			t.Errorf("valid explicit or default port %q was rejected", host)
		}
	}
}

func TestResponseHeaderBudgetBoundaries(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		mode, prefix := "buffered", "application/json; padding="
		body, cc := validBody, ""
		if streaming {
			mode, prefix = "stream", "text/event-stream; padding="
			body, cc = `{"model":"m","messages":[],"stream":true}`, "no-store"
		}
		for _, tc := range []struct {
			name, ct, cc, key string
			wantStatus        int
		}{
			{"raw exactly", prefix + strings.Repeat("q", reviewHeaderBudget-len(prefix)-len(cc)), cc, testKey, 200},
			{"raw over", prefix + strings.Repeat("q", reviewHeaderBudget-len(prefix)-len(cc)+1), cc, testKey, 502},
			{"masked exactly", headerForMaskedLength(prefix, reviewHeaderBudget-len(cc)), cc, "x", 200},
			{"masked over", headerForMaskedLength(prefix, reviewHeaderBudget-len(cc)+1), cc, "x", 502},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				cfg := LoadConfig(env(map[string]string{EnvBaseURL: "https://model.example/v1", EnvAPIKey: tc.key}))
				p, err := New(cfg, Options{ClientAuth: StaticTokenAuth(clientToken), MaxResponseBytes: 1, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {tc.ct}, "Cache-Control": {tc.cc}}, Body: io.NopCloser(strings.NewReader("z"))}, nil
				})})
				if err != nil {
					t.Fatal(err)
				}
				w := httptest.NewRecorder()
				p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(body)))
				if w.Code != tc.wantStatus {
					t.Fatalf("status=%d want=%d", w.Code, tc.wantStatus)
				}
				if tc.wantStatus == 502 {
					if errCode(t, w.Body.Bytes()) != string(errcode.ModelGWTooLarge) || w.Header().Get("Cache-Control") != "" {
						t.Fatal("header failure must precede all forwarded headers")
					}
					return
				}
				gotSize := len(w.Header().Get("Content-Type")) + len(w.Header().Get("Cache-Control"))
				if gotSize != reviewHeaderBudget || w.Body.String() != "z" {
					t.Fatal("header budget at the exact boundary must be independent of a one-byte body limit")
				}
			})
		}
	}
}

// x 的安全默认标记是 MaskText；q 是不匹配 key 的填充。
func headerForMaskedLength(prefix string, size int) string {
	remaining := size - len(strings.ReplaceAll(prefix, "x", MaskText))
	return prefix + strings.Repeat("x", remaining/len(MaskText)) + strings.Repeat("q", remaining%len(MaskText))
}

func TestStreamResponseHeaderAggregateBudget(t *testing.T) {
	for _, masked := range []bool{false, true} {
		key := testKey
		ct, cc := "text/event-stream; padding="+strings.Repeat("q", 17000), strings.Repeat("q", 17000)
		if masked {
			key = "x"
			ct, cc = "text/event-stream; padding="+strings.Repeat("x", 650), strings.Repeat("x", 650)
		}
		cfg := LoadConfig(env(map[string]string{EnvBaseURL: "https://model.example/v1", EnvAPIKey: key}))
		p, err := New(cfg, Options{ClientAuth: StaticTokenAuth(clientToken), Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}, "Cache-Control": {cc}}, Body: io.NopCloser(strings.NewReader("unused"))}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(`{"model":"m","messages":[],"stream":true}`)))
		if w.Code != 502 || errCode(t, w.Body.Bytes()) != string(errcode.ModelGWTooLarge) || w.Header().Get("Cache-Control") != "" {
			t.Fatal("individually bounded fields must also fit the aggregate raw and masked budgets")
		}
	}
}

func TestHeaderMaskPreflightDoesNotAllocateExpandedContent(t *testing.T) {
	mk := newMasker("x")
	value := strings.Repeat("x", 2048)
	allocs := testing.AllocsPerRun(100, func() {
		if _, ok := mk.boundedStringSize(value, reviewHeaderBudget); ok {
			panic("unbounded header expansion accepted")
		}
	})
	if allocs != 0 {
		t.Fatalf("rejected header expansion allocated output: %g allocations", allocs)
	}
}

func trustedDefaultProxy(t *testing.T, u *upstream) *Proxy {
	t.Helper()
	p, err := New(u.config(t), Options{ClientAuth: StaticTokenAuth(clientToken)})
	if err != nil {
		t.Fatal(err)
	}
	tr := p.client.Transport.(*http.Transport)
	pool := x509.NewCertPool()
	pool.AddCert(u.Certificate())
	tr.TLSClientConfig.RootCAs = pool
	t.Cleanup(tr.CloseIdleConnections)
	return p
}

func TestDefaultTransportHeaderCap(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		ct, body := "application/json", validBody
		if streaming {
			ct, body = "text/event-stream", `{"model":"m","messages":[],"stream":true}`
		}
		u := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("X-Unforwarded-Padding", strings.Repeat("q", reviewHeaderBudget+1))
			_, _ = io.WriteString(w, "z")
		})
		p := trustedDefaultProxy(t, u)
		tr := p.client.Transport.(*http.Transport)
		if tr.MaxResponseHeaderBytes != reviewHeaderBudget {
			t.Fatal("default transport must cap all raw upstream response headers at 32 KiB")
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(body)))
		// 原生 Transport 的错误不按文本解析成 too_large。
		if w.Code != 502 || errCode(t, w.Body.Bytes()) != string(errcode.ModelGWUpstreamError) {
			t.Fatal("native transport header rejection must remain an upstream error")
		}
	}
}

func TestDefaultTransportOnlySendsFixedHeaders(t *testing.T) {
	got := make(chan http.Header, 1)
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
		okHandler(w, r)
	})
	p := trustedDefaultProxy(t, u)
	if !p.client.Transport.(*http.Transport).DisableCompression {
		t.Fatal("default transport must disable automatic compression negotiation")
	}
	r := authorizedRequest(context.Background(), strings.NewReader(validBody))
	r.Header.Set("Accept-Encoding", "gzip")
	r.Header.Set("User-Agent", "client-agent")
	r.Header.Set("Cookie", "client-cookie")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	h := <-got
	for name := range h {
		if name != "Authorization" && name != "Content-Type" && name != "Accept" && name != "Content-Length" {
			t.Errorf("unexpected default transport header %q", name)
		}
	}
	if h.Get("Accept-Encoding") != "" || h.Get("Authorization") != "Bearer "+testKey {
		t.Fatal("default transport leaked client compression metadata or omitted injected auth")
	}
}

func TestUpstreamTimeoutExpiryCannotBeResetOrFinished(t *testing.T) {
	for _, finish := range []bool{false, true} {
		var cancelled atomic.Int32
		timeout := newUpstreamTimeout(time.Hour, func() { cancelled.Add(1) })
		timeout.mu.Lock()
		timeout.deadline = time.Now().Add(-time.Second)
		timeout.mu.Unlock()
		ok := false
		if finish {
			ok = timeout.finish()
		} else {
			ok = timeout.reset(time.Hour)
		}
		if ok || !timeout.timedOut() || cancelled.Load() != 1 {
			t.Error("deadline expiry before callback must reject reset and finish, and cancel exactly once")
		}
		timeout.onTimeout()
		timeout.stop()
		if cancelled.Load() != 1 {
			t.Error("late callback cancelled twice")
		}
	}
}

func TestUpstreamTimeoutExpiredCallbackBeforeCancelRemainsSticky(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	timeout := newUpstreamTimeout(time.Hour, func() {
		close(entered)
		<-release
		cancel()
	})
	defer timeout.stop()
	defer func() { close(release); <-done }()
	timeout.mu.Lock()
	timeout.deadline = time.Now().Add(-time.Second)
	timeout.mu.Unlock()
	go func() { timeout.onTimeout(); close(done) }()
	<-entered
	if ctx.Err() != nil {
		t.Fatal("test cancel must still be blocked")
	}
	if timeout.reset(time.Hour) || timeout.finish() || !timeout.timedOut() {
		t.Fatal("an expired callback cannot be undone while cancellation is still pending")
	}
}

func TestUpstreamTimeoutStaleCallbackAfterResetAndFinish(t *testing.T) {
	var cancelled atomic.Int32
	timeout := newUpstreamTimeout(time.Hour, func() { cancelled.Add(1) })
	defer timeout.stop()
	if !timeout.reset(2 * time.Hour) {
		t.Fatal("live deadline reset was rejected")
	}
	timeout.onTimeout() // Reset 前排队的回调。
	if timeout.timedOut() || cancelled.Load() != 0 || !timeout.finish() {
		t.Fatal("stale callback must respect the new deadline")
	}
	timeout.onTimeout() // Stop 不能等待回调；完成后到达的回调也必须无效。
	if timeout.reset(time.Hour) || timeout.timedOut() || cancelled.Load() != 0 {
		t.Fatal("late callback must not cancel completed request")
	}
}

func TestClientCancelLogsSeparateOutcome(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		var logs bytes.Buffer
		p, err := New(validConfig(), Options{ClientAuth: StaticTokenAuth(clientToken), Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			cancel()
			return nil, r.Context().Err()
		})})
		if err != nil {
			t.Fatal(err)
		}
		body := validBody
		if streaming {
			body = `{"model":"m","messages":[],"stream":true}`
		}
		p.ServeHTTP(httptest.NewRecorder(), authorizedRequest(ctx, strings.NewReader(body)))
		var record map[string]any
		if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record["status"] != float64(499) || record["outcome"] != "client_cancelled" || record["code"] != nil {
			t.Fatal("client cancellation must not be reported as upstream failure")
		}
	}
}
