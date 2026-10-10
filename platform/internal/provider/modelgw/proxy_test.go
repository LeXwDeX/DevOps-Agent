package modelgw

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

const (
	clientToken = "client-token-xyz"
	validBody   = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
)

// upstream 是带计数的 TLS 假模型服务。
type upstream struct {
	*httptest.Server
	calls atomic.Int64
}

func newUpstream(t *testing.T, h http.HandlerFunc) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		h(w, r)
	}))
	t.Cleanup(u.Server.Close)
	return u
}

func (u *upstream) config(t *testing.T) Config {
	t.Helper()
	cfg := LoadConfig(env(map[string]string{EnvBaseURL: u.URL + "/v1/", EnvAPIKey: testKey}))
	if !cfg.Available() {
		t.Fatalf("config unavailable: %+v", cfg.Status())
	}
	return cfg
}

// frontend 起代理，返回客户端可访问的地址。
func frontend(t *testing.T, cfg Config, opt Options, u *upstream) *httptest.Server {
	t.Helper()
	if opt.ClientAuth == nil {
		opt.ClientAuth = StaticTokenAuth(clientToken)
	}
	if opt.Transport == nil && u != nil {
		opt.Transport = u.Client().Transport
	}
	p, err := New(cfg, opt)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(p)
	t.Cleanup(s.Close)
	return s
}

func post(t *testing.T, s *httptest.Server, path, body string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", s.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+clientToken)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func chat(t *testing.T, s *httptest.Server, body string) (*http.Response, []byte) {
	return post(t, s, "/v1/chat/completions", body, nil)
}

func errCode(t *testing.T, b []byte) string {
	t.Helper()
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatalf("not an error body: %q", b)
	}
	return e.Error.Code
}

func okHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
}

func TestForwardInjectsKeyAndFixedHeaders(t *testing.T) {
	var gotHdr http.Header
	var gotBody []byte
	var gotPath, gotQuery, gotMethod string
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotHdr, gotPath, gotQuery, gotMethod = r.Header.Clone(), r.URL.Path, r.URL.RawQuery, r.Method
		gotBody, _ = io.ReadAll(r.Body)
		okHandler(w, r)
	})
	s := frontend(t, u.config(t), Options{}, u)
	body := `{"model":"m1","messages":[],"temperature":0.3,"x_extra":{"a":[1,2]}}`
	resp, b := post(t, s, "/v1/chat/completions", body, map[string]string{
		"X-Forwarded-Host": "evil.example",
		"Cookie":           "sid=1",
		"X-Custom":         "1",
		"Accept":           "text/html",
		"User-Agent":       "agent/1",
	})
	if resp.StatusCode != 200 || !strings.Contains(string(b), "choices") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	if gotMethod != "POST" || gotPath != "/v1/chat/completions" || gotQuery != "" {
		t.Fatalf("target %s %s ?%s", gotMethod, gotPath, gotQuery)
	}
	if got := gotHdr.Get("Authorization"); got != "Bearer "+testKey {
		t.Fatalf("Authorization=%q", got)
	}
	if gotHdr.Get("Content-Type") != "application/json" || gotHdr.Get("Accept") != "application/json" {
		t.Fatalf("headers: %v", gotHdr)
	}
	allowed := map[string]bool{"Authorization": true, "Content-Type": true, "Accept": true, "Content-Length": true, "Accept-Encoding": true}
	for k := range gotHdr {
		if !allowed[k] {
			t.Errorf("unexpected outbound header %q: %v", k, gotHdr[k])
		}
	}
	if string(gotBody) != body {
		t.Fatalf("body altered: %q", gotBody)
	}
}

func TestAcceptSelection(t *testing.T) {
	cases := map[string]string{
		"":                                 "application/json",
		"application/json":                 "application/json",
		"text/event-stream":                "text/event-stream",
		"text/event-stream; charset=utf-8": "text/event-stream",
		"text/html":                        "application/json",
		"*/*":                              "application/json",
		"text/event-stream, text/html":     "application/json",
	}
	for in, want := range cases {
		if got := pickAccept(in); got != want {
			t.Errorf("pickAccept(%q)=%q want %q", in, got, want)
		}
	}
}

func TestClientCannotChangeTarget(t *testing.T) {
	var paths, hosts []string
	var mu sync.Mutex
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		hosts = append(hosts, r.Host)
		mu.Unlock()
		okHandler(w, r)
	})
	s := frontend(t, u.config(t), Options{}, u)
	// 带查询的路径不是已知路由
	resp, _ := post(t, s, "/v1/chat/completions/../../etc", validBody, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	resp, _ = post(t, s, "/v1/chat/completions?url=https://evil.example/x", validBody, map[string]string{"Host": "evil.example"})
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	resp, _ = post(t, s, "/v1/chat/completions", `{"model":"m","messages":[],"url":"https://evil.example","base_url":"https://evil.example"}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 {
		t.Fatalf("upstream calls=%v", paths)
	}
	for i, p := range paths {
		if p != "/v1/chat/completions" {
			t.Errorf("path %q", p)
		}
		if hosts[i] != strings.TrimPrefix(u.URL, "https://") {
			t.Errorf("host %q", hosts[i])
		}
	}
}

func TestRoutes(t *testing.T) {
	u := newUpstream(t, okHandler)
	s := frontend(t, u.config(t), Options{}, u)
	do := func(method, path string) int {
		req, _ := http.NewRequest(method, s.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+clientToken)
		resp, err := s.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, c := range []struct {
		m, p string
		want int
	}{
		{"GET", "/v1/chat/completions", 405},
		{"PUT", "/v1/chat/completions", 405},
		{"POST", "/v1/status", 405},
		{"GET", "/v1/status", 200},
		{"GET", "/", 404},
		{"POST", "/v1/models", 404},
		{"POST", "/v1/chat/completions/", 404},
		{"POST", "/chat/completions", 404},
	} {
		if got := do(c.m, c.p); got != c.want {
			t.Errorf("%s %s: %d want %d", c.m, c.p, got, c.want)
		}
	}
	if u.calls.Load() != 0 {
		t.Fatal("non-chat routes must not reach upstream")
	}
}

func TestUnauthorized(t *testing.T) {
	u := newUpstream(t, okHandler)
	for name, auth := range map[string]func(*http.Request) bool{
		"nil auth":   nil,
		"empty tok":  StaticTokenAuth(""),
		"wrong auth": StaticTokenAuth("other"),
	} {
		s := frontend(t, u.config(t), Options{ClientAuth: auth}, u)
		if auth == nil {
			// frontend 会填默认认证，这里直接构造 nil 认证的代理
			p, _ := New(u.config(t), Options{Transport: u.Client().Transport})
			s = httptest.NewServer(p)
			t.Cleanup(s.Close)
		}
		resp, b := chat(t, s, validBody)
		if resp.StatusCode != 401 || errCode(t, b) != string(errcode.ModelGWUnauthorized) {
			t.Errorf("%s: %d %s", name, resp.StatusCode, b)
		}
		sreq, _ := http.NewRequest("GET", s.URL+"/v1/status", nil)
		sreq.Header.Set("Authorization", "Bearer "+clientToken)
		sresp, err := s.Client().Do(sreq)
		if err != nil {
			t.Fatal(err)
		}
		sresp.Body.Close()
		if sresp.StatusCode != 401 {
			t.Errorf("%s: status route %d", name, sresp.StatusCode)
		}
	}
	// 没有凭证
	s := frontend(t, u.config(t), Options{}, u)
	req, _ := http.NewRequest("POST", s.URL+"/v1/chat/completions", strings.NewReader(validBody))
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("no creds: %d", resp.StatusCode)
	}
	if u.calls.Load() != 0 {
		t.Fatalf("upstream called %d times", u.calls.Load())
	}
}

func TestAuthBeforeBodyRead(t *testing.T) {
	u := newUpstream(t, okHandler)
	s := frontend(t, u.config(t), Options{MaxRequestBytes: 16}, u)
	// 未授权且超大：应得到 401，而不是 413
	req, _ := http.NewRequest("POST", s.URL+"/v1/chat/completions", strings.NewReader(strings.Repeat("x", 1000)))
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestRequestTooLarge(t *testing.T) {
	u := newUpstream(t, okHandler)
	s := frontend(t, u.config(t), Options{MaxRequestBytes: 64}, u)
	big := `{"model":"m","messages":[{"content":"` + strings.Repeat("a", 200) + `"}]}`
	resp, b := chat(t, s, big)
	if resp.StatusCode != 413 || errCode(t, b) != string(errcode.ModelGWTooLarge) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	// 分块上传（没有 Content-Length）同样受限
	req, _ := http.NewRequest("POST", s.URL+"/v1/chat/completions", io.NopCloser(strings.NewReader(big)))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+clientToken)
	resp2, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 413 || errCode(t, b2) != string(errcode.ModelGWTooLarge) {
		t.Fatalf("chunked: %d %s", resp2.StatusCode, b2)
	}
	if u.calls.Load() != 0 {
		t.Fatal("upstream must not be called")
	}
}

func TestBadRequests(t *testing.T) {
	u := newUpstream(t, okHandler)
	s := frontend(t, u.config(t), Options{}, u)
	for name, body := range map[string]string{
		"empty":           ``,
		"not json":        `hello`,
		"trailing":        validBody + `x`,
		"array":           `[]`,
		"null":            `null`,
		"string":          `"x"`,
		"no model":        `{"messages":[]}`,
		"empty model":     `{"model":"","messages":[]}`,
		"model number":    `{"model":1,"messages":[]}`,
		"model null":      `{"model":null,"messages":[]}`,
		"no messages":     `{"model":"m"}`,
		"messages object": `{"model":"m","messages":{}}`,
		"messages string": `{"model":"m","messages":"hi"}`,
		"messages null":   `{"model":"m","messages":null}`,
	} {
		resp, b := chat(t, s, body)
		if resp.StatusCode != 400 || errCode(t, b) != string(errcode.ModelGWBadRequest) {
			t.Errorf("%s: %d %s", name, resp.StatusCode, b)
		}
	}
	if u.calls.Load() != 0 {
		t.Fatal("upstream must not be called")
	}
	// 空的 messages 数组是合格的
	resp, _ := chat(t, s, `{"model":"m","messages":[]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("empty array should pass: %d", resp.StatusCode)
	}
}

func TestUnavailable(t *testing.T) {
	u := newUpstream(t, okHandler)
	for name, m := range map[string]map[string]string{
		"missing":     {},
		"bad url":     {EnvBaseURL: u.URL + "/v1/chat/completions", EnvAPIKey: testKey},
		"http":        {EnvBaseURL: strings.Replace(u.URL, "https", "http", 1), EnvAPIKey: testKey},
		"missing key": {EnvBaseURL: u.URL + "/v1"},
		"bad key":     {EnvBaseURL: u.URL + "/v1", EnvAPIKey: "a b"},
	} {
		s := frontend(t, LoadConfig(env(m)), Options{}, u)
		resp, b := chat(t, s, validBody)
		if resp.StatusCode != 503 || errCode(t, b) != string(errcode.ModelGWUnavailable) {
			t.Errorf("%s: %d %s", name, resp.StatusCode, b)
		}
		if strings.Contains(string(b), u.URL) || strings.Contains(string(b), testKey) {
			t.Errorf("%s: leaks: %s", name, b)
		}
		// 状态接口仍可用
		req, _ := http.NewRequest("GET", s.URL+"/v1/status", nil)
		req.Header.Set("Authorization", "Bearer "+clientToken)
		sr, err := s.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		sb, _ := io.ReadAll(sr.Body)
		sr.Body.Close()
		var st Status
		if sr.StatusCode != 200 || json.Unmarshal(sb, &st) != nil || st.State == StateAvailable {
			t.Errorf("%s: status %d %s", name, sr.StatusCode, sb)
		}
	}
	if u.calls.Load() != 0 {
		t.Fatalf("upstream got %d requests, want 0", u.calls.Load())
	}
}

func TestStatusEndpoint(t *testing.T) {
	u := newUpstream(t, okHandler)
	s := frontend(t, u.config(t), Options{}, u)
	req, _ := http.NewRequest("GET", s.URL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+clientToken)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != `{"status":"available","reason":""}` {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if strings.Contains(string(b), testKey) || strings.Contains(string(b), "127.0.0.1") {
		t.Fatalf("leak: %s", b)
	}
	// 未授权不能读状态
	req2, _ := http.NewRequest("GET", s.URL+"/v1/status", nil)
	resp2, _ := s.Client().Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != 401 {
		t.Fatalf("status without creds: %d", resp2.StatusCode)
	}
}

func TestRedirectRefused(t *testing.T) {
	var targetHits atomic.Int64
	target := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		okHandler(w, r)
	})
	for _, code := range []int{301, 302, 303, 307, 308} {
		u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL+"/steal")
			w.WriteHeader(code)
			_, _ = io.WriteString(w, "redirect body marker")
		})
		s := frontend(t, u.config(t), Options{}, u)
		resp, b := chat(t, s, validBody)
		if resp.StatusCode != 502 || errCode(t, b) != string(errcode.ModelGWRedirectRefused) {
			t.Fatalf("%d: %d %s", code, resp.StatusCode, b)
		}
		if strings.Contains(string(b), "marker") || resp.Header.Get("Location") != "" {
			t.Fatalf("redirect leaked to client: %v %s", resp.Header, b)
		}
	}
	if targetHits.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}
}

func TestUpstreamTimeout(t *testing.T) {
	release := make(chan struct{})
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	s := frontend(t, u.config(t), Options{Timeout: 150 * time.Millisecond}, u)
	start := time.Now()
	resp, b := chat(t, s, validBody)
	if resp.StatusCode != 502 || errCode(t, b) != string(errcode.ModelGWUpstreamError) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not enforced")
	}
	if strings.Contains(string(b), u.URL) {
		t.Fatalf("error leaks endpoint: %s", b)
	}
}

func TestUpstreamConnectError(t *testing.T) {
	u := newUpstream(t, okHandler)
	cfg := u.config(t)
	tr := u.Client().Transport
	u.Close() // 连接被拒
	s := frontend(t, cfg, Options{Transport: tr}, u)
	resp, b := chat(t, s, validBody)
	if resp.StatusCode != 502 || errCode(t, b) != string(errcode.ModelGWUpstreamError) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if strings.Contains(string(b), "127.0.0.1") {
		t.Fatalf("error leaks endpoint: %s", b)
	}
}

func TestUntrustedUpstreamTLS(t *testing.T) {
	// 不注入 Transport：默认校验，不信任测试证书
	u := newUpstream(t, okHandler)
	p, err := New(u.config(t), Options{ClientAuth: StaticTokenAuth(clientToken)})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(p)
	t.Cleanup(s.Close)
	resp, b := chat(t, s, validBody)
	if resp.StatusCode != 502 || errCode(t, b) != string(errcode.ModelGWUpstreamError) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if u.calls.Load() != 0 {
		t.Fatal("request must not reach upstream over an unverified connection")
	}
}

func TestNonStandardResponsePassthrough(t *testing.T) {
	bodies := []string{
		`{"id":"x","choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":"","reasoning_content":"thinking...\n"}}],"usage":{"completion_tokens":512},"unknown_field":{"a":[null,true]}}`,
		`{"choices":[]}`,
		`not even json`,
		``,
	}
	for _, want := range bodies {
		u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Upstream-Secret", "dropme")
			_, _ = io.WriteString(w, want)
		})
		s := frontend(t, u.config(t), Options{}, u)
		resp, b := chat(t, s, validBody)
		if resp.StatusCode != 200 || string(b) != want {
			t.Fatalf("not byte-identical: %d %q want %q", resp.StatusCode, b, want)
		}
		if resp.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("content-type %q", resp.Header.Get("Content-Type"))
		}
		if resp.Header.Get("X-Upstream-Secret") != "" {
			t.Fatal("upstream headers must be dropped")
		}
	}
}

func TestUpstreamStatusPassthrough(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down"}}`)
	})
	s := frontend(t, u.config(t), Options{}, u)
	resp, b := chat(t, s, validBody)
	if resp.StatusCode != 429 || string(b) != `{"error":{"message":"slow down"}}` {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestUpstreamErrorBodyMasksKey(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; x="+testKey)
		w.Header().Set("X-Echo", testKey)
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided: `+r.Header.Get("Authorization")+` / `+testKey+`"}}`)
	})
	s := frontend(t, u.config(t), Options{}, u)
	resp, b := chat(t, s, validBody)
	if resp.StatusCode != 401 {
		t.Fatalf("%d", resp.StatusCode)
	}
	if strings.Contains(string(b), testKey) {
		t.Fatalf("body leaks key: %s", b)
	}
	if !strings.Contains(string(b), MaskText) {
		t.Fatalf("expected mask in body: %s", b)
	}
	for k, v := range resp.Header {
		if strings.Contains(strings.Join(v, ","), testKey) {
			t.Fatalf("header %s leaks key", k)
		}
	}
	if resp.Header.Get("X-Echo") != "" {
		t.Fatal("echoed header should be dropped")
	}
}

func TestResponseTooLarge(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"with length": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("A", 500))
		},
		"chunked": func(w http.ResponseWriter, r *http.Request) {
			for i := 0; i < 5; i++ {
				_, _ = io.WriteString(w, strings.Repeat("B", 100))
				w.(http.Flusher).Flush()
			}
		},
	} {
		u := newUpstream(t, h)
		s := frontend(t, u.config(t), Options{MaxResponseBytes: 128}, u)
		resp, b := chat(t, s, validBody)
		if resp.StatusCode != 502 || errCode(t, b) != string(errcode.ModelGWTooLarge) {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, b)
		}
		if strings.Contains(string(b), "AAAA") || strings.Contains(string(b), "BBBB") {
			t.Fatalf("%s: partial body leaked: %s", name, b)
		}
	}
	// 恰好等于上限可以通过
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, strings.Repeat("C", 128)) })
	s := frontend(t, u.config(t), Options{MaxResponseBytes: 128}, u)
	if resp, b := chat(t, s, validBody); resp.StatusCode != 200 || len(b) != 128 {
		t.Fatalf("at-limit: %d %d", resp.StatusCode, len(b))
	}
}

// sseServer 按给定的块写出 SSE，块之间 flush 并等待客户端确认读到。
func sseUpstream(t *testing.T, chunks []string, gate func(i int)) *upstream {
	return newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Drop", "me")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		fl.Flush()
		for i, c := range chunks {
			_, _ = io.WriteString(w, c)
			fl.Flush()
			if gate != nil {
				gate(i)
			}
		}
	})
}

func TestStreamPassthroughMultipleChunks(t *testing.T) {
	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"a\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	u := sseUpstream(t, chunks, func(int) { time.Sleep(30 * time.Millisecond) })
	s := frontend(t, u.config(t), Options{}, u)
	req, _ := http.NewRequest("POST", s.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+clientToken)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers: %d %v", resp.StatusCode, resp.Header)
	}
	if resp.Header.Get("X-Drop") != "" {
		t.Fatal("extra upstream header forwarded")
	}
	// 逐块读取：每块在上游下一块之前就应该到达（说明有 flush）
	var got strings.Builder
	buf := make([]byte, 4096)
	reads := 0
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			reads++
			got.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	if got.String() != strings.Join(chunks, "") {
		t.Fatalf("stream differs: %q", got.String())
	}
	if reads < 3 {
		t.Fatalf("expected chunks to arrive separately, got %d reads", reads)
	}
}

func TestStreamArrivesBeforeUpstreamFinishes(t *testing.T) {
	proceed := make(chan struct{})
	u := sseUpstream(t, []string{"data: one\n\n", "data: two\n\n"}, func(i int) {
		if i == 0 {
			<-proceed // 等客户端先拿到第一块
		}
	})
	s := frontend(t, u.config(t), Options{}, u)
	req, _ := http.NewRequest("POST", s.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+clientToken)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	done := make(chan string, 1)
	go func() { n, _ := resp.Body.Read(buf); done <- string(buf[:n]) }()
	select {
	case first := <-done:
		if first != "data: one\n\n" {
			t.Fatalf("first=%q", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first chunk was not flushed while upstream still open")
	}
	close(proceed)
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != "data: two\n\n" {
		t.Fatalf("rest=%q", rest)
	}
}

func TestStreamMasksKeySplitAcrossChunks(t *testing.T) {
	half := len(testKey) / 2
	chunks := []string{
		"data: leak ", testKey[:half], testKey[half:], " end\n\n",
		"data: " + testKey[:3], testKey[3:] + "\n\n",
		"data: [DONE]\n\n",
	}
	u := sseUpstream(t, chunks, func(int) { time.Sleep(10 * time.Millisecond) })
	s := frontend(t, u.config(t), Options{}, u)
	_, b := chat(t, s, `{"model":"m","messages":[],"stream":true}`)
	want := "data: leak " + MaskText + " end\n\ndata: " + MaskText + "\n\ndata: [DONE]\n\n"
	if string(b) != want {
		t.Fatalf("got %q want %q", b, want)
	}
	if strings.Contains(string(b), testKey[:6]) {
		t.Fatal("key fragment leaked")
	}
}

func TestStreamPartialPrefixFlushedAtEnd(t *testing.T) {
	u := sseUpstream(t, []string{"data: tail " + testKey[:5]}, nil)
	s := frontend(t, u.config(t), Options{}, u)
	_, b := chat(t, s, `{"model":"m","messages":[],"stream":true}`)
	if string(b) != "data: tail "+testKey[:5] {
		t.Fatalf("got %q", b)
	}
}

func TestStreamRequestButUpstreamJSONError(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":"bad"}`)
	})
	s := frontend(t, u.config(t), Options{}, u)
	resp, b := chat(t, s, `{"model":"m","messages":[],"stream":true}`)
	if resp.StatusCode != 400 || string(b) != `{"error":"bad"}` {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestStreamIsBufferedWhenNotRequested(t *testing.T) {
	// 请求没有 stream:true 时，即使上游返回 SSE 也按非流式处理（有上限）
	u := sseUpstream(t, []string{"data: " + testKey + "\n\n"}, nil)
	s := frontend(t, u.config(t), Options{}, u)
	resp, b := chat(t, s, validBody)
	if resp.StatusCode != 200 || string(b) != "data: "+MaskText+"\n\n" {
		t.Fatalf("%d %q", resp.StatusCode, b)
	}
}

func TestStreamUpstreamAbortBreaksClient(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: part\n\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // 不发正常的结束标记
	})
	s := frontend(t, u.config(t), Options{}, u)
	req, _ := http.NewRequest("POST", s.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+clientToken)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("truncated stream must surface as an error, got clean EOF with %q", b)
	}
}

func TestStreamIdleTimeout(t *testing.T) {
	release := make(chan struct{})
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	s := frontend(t, u.config(t), Options{Timeout: 200 * time.Millisecond}, u)
	req, _ := http.NewRequest("POST", s.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+clientToken)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	done := make(chan struct{})
	go func() { _, _ = io.ReadAll(resp.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle stream was not cut off")
	}
}

func TestLongStreamNotCutByTotalTimeout(t *testing.T) {
	// 总时长超过 Timeout，但块间隔小于 Timeout，流应完整结束
	var chunks []string
	for i := 0; i < 8; i++ {
		chunks = append(chunks, "data: x\n\n")
	}
	u := sseUpstream(t, chunks, func(int) { time.Sleep(60 * time.Millisecond) })
	s := frontend(t, u.config(t), Options{Timeout: 200 * time.Millisecond}, u)
	_, b := chat(t, s, `{"model":"m","messages":[],"stream":true}`)
	if string(b) != strings.Join(chunks, "") {
		t.Fatalf("got %q", b)
	}
}

func TestContentEncodingRefused(t *testing.T) {
	// 传输层不解压时，压缩内容无法 mask，必须拒绝
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, "xxxx")
	})
	s := frontend(t, u.config(t), Options{}, u)
	resp, b := chat(t, s, validBody)
	if resp.StatusCode != 502 || errCode(t, b) != string(errcode.ModelGWUpstreamError) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestLoggingNeverContainsSecrets(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, "bad key "+testKey)
	})
	s := frontend(t, u.config(t), Options{Logger: logger}, u)
	chat(t, s, `{"model":"m","messages":[{"content":"SECRET-PROMPT-MARKER"}]}`)
	chat(t, s, `garbage SECRET-PROMPT-MARKER`)
	post(t, s, "/v1/chat/completions?k="+testKey, validBody, map[string]string{"Authorization": "Bearer wrong"})
	out := logs.String()
	if out == "" {
		t.Fatal("expected log lines")
	}
	for _, bad := range []string{testKey, "SECRET-PROMPT-MARKER", "127.0.0.1:", u.URL, "Authorization"} {
		if strings.Contains(out, bad) {
			t.Fatalf("log contains %q:\n%s", bad, out)
		}
	}
	for _, want := range []string{"route=chat", "status=", "duration=", "code=modelgw.unauthorized"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestNewRejectsNegativeOptions(t *testing.T) {
	for _, o := range []Options{{Timeout: -1}, {MaxRequestBytes: -1}, {MaxResponseBytes: -1}} {
		if _, err := New(Config{}, o); err == nil {
			t.Errorf("expected error for %+v", o)
		} else if c, _ := errcode.CodeOf(err); c != errcode.InvalidArgument {
			t.Errorf("code=%v", c)
		}
	}
}

func TestConcurrentRequests(t *testing.T) {
	u := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			w.WriteHeader(500)
			return
		}
		if bytes.Contains(b, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < 3; i++ {
				_, _ = io.WriteString(w, "data: "+testKey[:7])
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, testKey[7:]+"\n\n")
				w.(http.Flusher).Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"echo":"`+testKey+`"}`)
	})
	s := frontend(t, u.config(t), Options{}, u)
	var wg sync.WaitGroup
	var failures atomic.Int64
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := validBody
			want := `{"echo":"` + MaskText + `"}`
			if i%2 == 0 {
				body = `{"model":"m","messages":[],"stream":true}`
				want = strings.Repeat("data: "+MaskText+"\n\n", 3)
			}
			req, _ := http.NewRequest("POST", s.URL+"/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+clientToken)
			resp, err := s.Client().Do(req)
			if err != nil {
				failures.Add(1)
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 || string(b) != want {
				failures.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d concurrent requests failed", failures.Load())
	}
}
