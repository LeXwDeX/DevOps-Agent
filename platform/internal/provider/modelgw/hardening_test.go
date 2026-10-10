package modelgw

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type readFunc func([]byte) (int, error)

func (f readFunc) Read(b []byte) (int, error) { return f(b) }

func validConfig() Config {
	return LoadConfig(env(map[string]string{EnvBaseURL: "https://model.example/v1", EnvAPIKey: testKey}))
}

func authorizedRequest(ctx context.Context, body io.Reader) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+clientToken)
	return r
}

func TestDefaultTransportAndLimits(t *testing.T) {
	p, err := New(validConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.timeout != DefaultTimeout || p.maxReq != DefaultMaxRequestBytes || p.maxResp != DefaultMaxResponseBytes {
		t.Fatal("zero options did not select the documented defaults")
	}
	tr, ok := p.client.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil || tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("default transport must verify TLS 1.2 or newer and ignore environment proxies")
	}
	if err := p.client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("default client must refuse redirects")
	}
}

func TestProxyValueFormattingRedacts(t *testing.T) {
	p, err := New(validConfig(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	type wrapper struct{ Proxy Proxy }
	for _, out := range []string{
		fmt.Sprintf("%v %+v %#v %s %q %d", *p, *p, *p, *p, *p, *p),
		fmt.Sprintf("%v %+v %#v", wrapper{*p}, wrapper{*p}, wrapper{*p}),
	} {
		if strings.Contains(out, testKey) || strings.Contains(out, "model.example") {
			t.Fatal("formatting a copied proxy leaked configuration")
		}
	}
}

func TestShortKeysAndMaskMarkerCollisions(t *testing.T) {
	for _, key := range []string{"chat", "modelgw", "available", "MASKED", EnvAPIKey, "[", "]", "route"} {
		t.Run(key, func(t *testing.T) {
			cfg := LoadConfig(env(map[string]string{EnvBaseURL: "https://model.example/v1", EnvAPIKey: key}))
			if !cfg.Available() {
				t.Fatal("short non-empty keys remain accepted")
			}
			mk := newMasker(key)
			if bytes.Contains(mk.mask, []byte(key)) {
				t.Fatal("replacement marker contains the credential")
			}
			var stream bytes.Buffer
			mw := mk.writer(&stream)
			for _, b := range []byte(key) {
				if _, err := mw.Write([]byte{b}); err != nil {
					t.Fatal(err)
				}
			}
			if err := mw.Close(); err != nil {
				t.Fatal(err)
			}
			if stream.String() != string(mk.mask) {
				t.Fatal("credential split into single bytes did not receive the safe marker")
			}
			var logs bytes.Buffer
			rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(key))}, nil
			})
			p, err := New(cfg, Options{ClientAuth: StaticTokenAuth(clientToken), Transport: rt, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(validBody)))
			if w.Code != http.StatusOK || w.Body.String() != string(mk.mask) {
				t.Fatal("buffered credential did not receive the safe marker")
			}
			for _, out := range []string{fmt.Sprintf("%#v", cfg), fmt.Sprintf("%#v", p), fmt.Sprintf("%#v", *p)} {
				if strings.Contains(out, key) {
					t.Fatal("formatting text contains the known credential")
				}
			}
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			// 固定属性名、数字和 Go 日志自动生成的时间不是凭证内容。
			// 校验代理输出的日志文本值，而非公共元数据的字节碰撞。
			for _, field := range []string{"msg", "route", "code"} {
				if text, ok := record[field].(string); ok && strings.Contains(text, key) {
					t.Fatalf("log field %s contains the known credential", field)
				}
			}
		})
	}
	for _, key := range []string{"https", "must", "invalid", EnvBaseURL} {
		cfg := LoadConfig(env(map[string]string{EnvBaseURL: "http://model.example/v1", EnvAPIKey: key}))
		if strings.Contains(cfg.Status().Reason, key) {
			t.Fatal("status reason contains the known credential")
		}
	}
}

func TestRequestAtLimitAndReadError(t *testing.T) {
	var upstreamCalls int
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		upstreamCalls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	p, err := New(validConfig(), Options{ClientAuth: StaticTokenAuth(clientToken), MaxRequestBytes: int64(len(validBody)), Transport: rt})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(validBody)))
	if w.Code != http.StatusOK || upstreamCalls != 1 {
		t.Fatalf("request at exact byte limit rejected: status %d", w.Code)
	}
	w = httptest.NewRecorder()
	r := authorizedRequest(context.Background(), nil)
	r.Body = io.NopCloser(readFunc(func([]byte) (int, error) { return 0, errors.New(testKey) }))
	p.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || errCode(t, w.Body.Bytes()) != string(errcode.ModelGWBadRequest) || upstreamCalls != 1 {
		t.Fatalf("request read failure must not reach upstream: status %d", w.Code)
	}
	if strings.Contains(w.Body.String(), testKey) {
		t.Fatal("request read error leaked the key")
	}
}

func TestUpstreamFailuresHideTransportAndBodyErrors(t *testing.T) {
	for _, transportFailure := range []bool{true, false} {
		t.Run(fmt.Sprintf("transport=%v", transportFailure), func(t *testing.T) {
			var logs bytes.Buffer
			rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
				if transportFailure {
					return nil, errors.New("https://model.example/v1/" + testKey)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(readFunc(func(b []byte) (int, error) {
					return copy(b, "untrusted body "+testKey), errors.New(testKey)
				}))}, nil
			})
			p, err := New(validConfig(), Options{ClientAuth: StaticTokenAuth(clientToken), Transport: rt, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(validBody)))
			if w.Code != http.StatusBadGateway || errCode(t, w.Body.Bytes()) != string(errcode.ModelGWUpstreamError) {
				t.Fatalf("status %d", w.Code)
			}
			for _, out := range []string{w.Body.String(), logs.String()} {
				if strings.Contains(out, testKey) || strings.Contains(out, "model.example") || strings.Contains(out, "untrusted body") {
					t.Fatal("failed upstream request leaked private data")
				}
			}
		})
	}
}

func TestMaximumResponseLimitDoesNotOverflow(t *testing.T) {
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("small response")), ContentLength: -1}, nil
	})
	p, err := New(validConfig(), Options{ClientAuth: StaticTokenAuth(clientToken), Transport: rt, MaxResponseBytes: math.MaxInt64})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(validBody)))
	if w.Code != http.StatusOK || w.Body.String() != "small response" {
		t.Fatal("maximum response limit overflowed")
	}
}

func TestEveryContentEncodingHeaderIsChecked(t *testing.T) {
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Encoding": {"identity", "br"}},
			Body: io.NopCloser(readFunc(func([]byte) (int, error) {
				t.Error("unsupported encoding body must not be read")
				return 0, io.EOF
			})),
		}, nil
	})
	p, err := New(validConfig(), Options{ClientAuth: StaticTokenAuth(clientToken), Transport: rt})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(validBody)))
	if w.Code != http.StatusBadGateway || errCode(t, w.Body.Bytes()) != string(errcode.ModelGWUpstreamError) {
		t.Fatal("second content encoding bypassed the mask prerequisite")
	}
}

func TestBufferedResponseBodyTimeout(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "partial response "+testKey)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	s := frontend(t, u.config(t), Options{Timeout: 200 * time.Millisecond}, u)
	resp, b := chat(t, s, validBody)
	if resp.StatusCode != http.StatusBadGateway || errCode(t, b) != string(errcode.ModelGWUpstreamError) {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if strings.Contains(string(b), "partial response") || strings.Contains(string(b), testKey) {
		t.Fatal("timeout exposed the partially buffered upstream body")
	}
}

func TestClientCancellationReachesUpstream(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			started, stopped := make(chan struct{}), make(chan struct{})
			u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: first\n\n")
					w.(http.Flusher).Flush()
				}
				close(started)
				<-r.Context().Done()
				close(stopped)
			})
			s := frontend(t, u.config(t), Options{}, u)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := validBody
			if streaming {
				body = `{"model":"m","messages":[],"stream":true}`
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+"/v1/chat/completions", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+clientToken)
			clientDone := make(chan struct{})
			go func() {
				defer close(clientDone)
				resp, err := s.Client().Do(r)
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream request did not start")
			}
			cancel()
			for _, ch := range []<-chan struct{}{clientDone, stopped} {
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatal("client cancellation did not terminate client and upstream requests")
				}
			}
		})
	}
}

type errorResponseWriter struct {
	header     http.Header
	writeError error
	flushError error
	shortWrite bool
}

func (w *errorResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (*errorResponseWriter) WriteHeader(int) {}

func (w *errorResponseWriter) Write(b []byte) (int, error) {
	if w.writeError != nil {
		return 0, w.writeError
	}
	if w.shortWrite {
		return len(b) - 1, nil
	}
	return len(b), nil
}

func (w *errorResponseWriter) FlushError() error { return w.flushError }

func TestStreamingReportsWriterFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		w    *errorResponseWriter
	}{
		{"initial flush", "unused", &errorResponseWriter{flushError: io.ErrClosedPipe}},
		{"chunk write", "safe chunk", &errorResponseWriter{writeError: io.ErrClosedPipe}},
		{"chunk short write", "safe chunk", &errorResponseWriter{shortWrite: true}},
		{"final tail write", testKey[:5], &errorResponseWriter{writeError: io.ErrClosedPipe}},
		{"final tail short write", testKey[:5], &errorResponseWriter{shortWrite: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(validConfig(), Options{})
			if err != nil {
				t.Fatal(err)
			}
			timeout := newUpstreamTimeout(time.Hour, func() {})
			defer timeout.stop()
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			res := p.stream(tc.w, resp, timeout, context.Background())
			if res.code != errcode.ModelGWUpstreamError {
				t.Fatal("stream writer failure was recorded as success")
			}
		})
	}
}

func TestMaskWriterPropagatesWriteErrors(t *testing.T) {
	for _, closing := range []bool{false, true} {
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprintf("close=%v/short=%v", closing, short), func(t *testing.T) {
				w := &errorResponseWriter{shortWrite: short}
				want := io.ErrShortWrite
				if !short {
					w.writeError = io.ErrClosedPipe
					want = io.ErrClosedPipe
				}
				mw := newMaskWriter(w, []byte("SECRET"), []byte(MaskText))
				var err error
				if closing {
					if _, err := mw.Write([]byte("SECR")); err != nil {
						t.Fatal(err)
					}
					err = mw.Close()
				} else {
					_, err = mw.Write([]byte("safe SECRET chunk"))
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v want %v", err, want)
				}
			})
		}
	}
}
