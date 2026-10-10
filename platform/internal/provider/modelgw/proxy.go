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
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

// 默认值。
const (
	DefaultTimeout          = 120 * time.Second
	DefaultMaxRequestBytes  = 4 << 20
	DefaultMaxResponseBytes = 16 << 20
)

// Options 是代理的装配参数。
type Options struct {
	// ClientAuth 校验客户端凭证。nil 表示拒绝所有请求（失败即关闭）。
	ClientAuth func(*http.Request) bool
	// Timeout 是上游超时。非流式覆盖整个请求；流式覆盖到响应头，
	// 之后是相邻两块数据之间的空闲时间。默认 120 秒。
	Timeout time.Duration
	// MaxRequestBytes 是请求体上限，默认 4 MiB。
	MaxRequestBytes int64
	// MaxResponseBytes 是非流式响应体上限，默认 16 MiB。
	MaxResponseBytes int64
	// Transport 可选，主要给测试注入信任。nil 时用 TLS 1.2 以上、默认校验的传输。
	Transport http.RoundTripper
	// Logger 可选。只记录路由、状态、耗时和错误码。
	Logger *slog.Logger
}

// Proxy 是模型出口代理，实现 http.Handler。创建后只读，可并发使用。
type Proxy struct {
	cfg        Config
	target     string
	auth       func(*http.Request) bool
	timeout    time.Duration
	maxReq     int64
	maxResp    int64
	client     *http.Client
	mk         masker
	authHeader string
	logger     *slog.Logger
}

// New 创建代理。配置不可用时也能创建：所有模型请求返回 503。
func New(cfg Config, opt Options) (*Proxy, error) {
	if opt.Timeout < 0 || opt.MaxRequestBytes < 0 || opt.MaxResponseBytes < 0 {
		return nil, errcode.New(errcode.InvalidArgument, newMasker(cfg.key).str("modelgw options must not be negative"))
	}
	p := &Proxy{
		cfg:     cfg,
		target:  cfg.target,
		auth:    opt.ClientAuth,
		timeout: opt.Timeout,
		maxReq:  opt.MaxRequestBytes,
		maxResp: opt.MaxResponseBytes,
		mk:      newMasker(cfg.key),
		logger:  opt.Logger,
	}
	if p.timeout == 0 {
		p.timeout = DefaultTimeout
	}
	if p.maxReq == 0 {
		p.maxReq = DefaultMaxRequestBytes
	}
	if p.maxResp == 0 {
		p.maxResp = DefaultMaxResponseBytes
	}
	if p.logger == nil {
		p.logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Available() {
		p.authHeader = "Bearer " + cfg.key
	}
	rt := opt.Transport
	if rt == nil {
		rt = &http.Transport{
			// 不读 HTTP(S)_PROXY：程序不读环境变量表之外的变量（K-31）
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 10 * time.Second,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        32,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	p.client = &http.Client{
		Transport: rt,
		// 不跟随重定向，3xx 由 handler 拒绝
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return p, nil
}

// 下面的方法让 Proxy 在格式化时不带出 key。

func (p Proxy) redacted() string { return p.mk.str("modelgw.Proxy{secrets:redacted}") }

// String 返回遮盖后的描述。
func (p Proxy) String() string { return p.redacted() }

// GoString 返回遮盖后的描述。
func (p Proxy) GoString() string { return p.redacted() }

// Format 对所有动词输出遮盖后的描述。
func (p Proxy) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(p.redacted())) }

// result 是一次请求的结果，用于日志。
type result struct {
	status int
	code   errcode.Code
	abort  bool // 流中途失败：断开连接，让客户端看到错误而不是被截断的正常结束
}

// ServeHTTP 实现 http.Handler。
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	route := "unknown"
	var res result
	switch r.URL.Path {
	case "/v1/chat/completions":
		route = "chat"
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			res = p.fail(w, http.StatusMethodNotAllowed, errcode.ModelGWBadRequest, "method not allowed")
		} else if !p.authorized(r) {
			res = p.unauthorized(w)
		} else {
			res = p.chat(w, r)
		}
	case "/v1/status":
		route = "status"
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			res = p.fail(w, http.StatusMethodNotAllowed, errcode.ModelGWBadRequest, "method not allowed")
		} else if !p.authorized(r) {
			res = p.unauthorized(w)
		} else {
			res = p.status(w)
		}
	default:
		res = p.fail(w, http.StatusNotFound, errcode.ModelGWBadRequest, "not found")
	}
	attrs := []slog.Attr{
		slog.String("route", p.mk.str(route)),
		slog.Int("status", res.status),
		slog.Duration("duration", time.Since(start)),
	}
	if res.code != "" {
		attrs = append(attrs, slog.String("code", p.mk.str(string(res.code))))
	}
	p.logger.LogAttrs(r.Context(), slog.LevelInfo, p.mk.str("modelgw request"), attrs...)
	if res.abort {
		panic(http.ErrAbortHandler)
	}
}

func (p *Proxy) authorized(r *http.Request) bool {
	return p.auth != nil && p.auth(r)
}

func (p *Proxy) unauthorized(w http.ResponseWriter) result {
	w.Header().Set("WWW-Authenticate", "Bearer")
	return p.fail(w, http.StatusUnauthorized, errcode.ModelGWUnauthorized, "invalid client credentials")
}

// fail 写出统一的 JSON 错误。文本先 mask 再序列化，序列化后再 mask 一次。
func (p *Proxy) fail(w http.ResponseWriter, status int, code errcode.Code, msg string) result {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	body.Error.Code = string(code)
	body.Error.Message = p.mk.str(msg)
	b, _ := json.Marshal(body)
	b = p.mk.bytes(b)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
	return result{status: status, code: code}
}

func (p *Proxy) status(w http.ResponseWriter) result {
	b, _ := json.Marshal(p.cfg.Status())
	b = p.mk.bytes(b)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
	return result{status: http.StatusOK}
}

// chatRequest 只取校验需要的字段，其余原样转发。
type chatRequest struct {
	Model    json.RawMessage `json:"model"`
	Messages json.RawMessage `json:"messages"`
	Stream   json.RawMessage `json:"stream"`
}

// parseChat 校验请求体，返回是否流式。
func parseChat(body []byte) (stream bool, ok bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return false, false
	}
	var model string
	if json.Unmarshal(raw["model"], &model) != nil || model == "" {
		return false, false
	}
	msgs := bytes.TrimSpace(raw["messages"])
	if len(msgs) == 0 || msgs[0] != '[' {
		return false, false
	}
	return string(bytes.TrimSpace(raw["stream"])) == "true", true
}

func (p *Proxy) chat(w http.ResponseWriter, r *http.Request) result {
	if !p.cfg.Available() {
		return p.fail(w, http.StatusServiceUnavailable, errcode.ModelGWUnavailable, "model is unavailable: "+p.cfg.Status().Reason)
	}
	if r.ContentLength > p.maxReq {
		return p.fail(w, http.StatusRequestEntityTooLarge, errcode.ModelGWTooLarge, "request body too large")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.maxReq))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return p.fail(w, http.StatusRequestEntityTooLarge, errcode.ModelGWTooLarge, "request body too large")
		}
		return p.fail(w, http.StatusBadRequest, errcode.ModelGWBadRequest, "cannot read request body")
	}
	wantStream, ok := parseChat(body)
	if !ok {
		return p.fail(w, http.StatusBadRequest, errcode.ModelGWBadRequest,
			"body must be a JSON object with a non-empty string model and a messages array")
	}

	// 超时：非流式是总时限，流式在收到响应头后改为空闲时限
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var timedOut atomic.Bool
	timer := time.AfterFunc(p.timeout, func() { timedOut.Store(true); cancel() })
	defer timer.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.target, bytes.NewReader(body))
	if err != nil {
		return p.fail(w, http.StatusBadGateway, errcode.ModelGWUpstreamError, "cannot build upstream request")
	}
	// 出站头只有这三个；User-Agent 置空以免传输层补默认值
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", pickAccept(r.Header.Get("Accept")))
	req.Header.Set("Authorization", p.authHeader)
	req.Header["User-Agent"] = []string{""}

	resp, err := p.client.Do(req)
	if err != nil {
		if r.Context().Err() != nil { // 客户端已断开
			return result{status: 499, code: errcode.ModelGWUpstreamError}
		}
		msg := "upstream request failed"
		if timedOut.Load() {
			msg = "upstream request timed out"
		}
		return p.fail(w, http.StatusBadGateway, errcode.ModelGWUpstreamError, msg)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return p.fail(w, http.StatusBadGateway, errcode.ModelGWRedirectRefused, "upstream returned a redirect")
	}
	// 传输层已解压的响应没有这个头。仍有它说明内容是压缩的，无法按 key 遮盖
	for _, ce := range resp.Header.Values("Content-Encoding") {
		if ce != "" && !strings.EqualFold(ce, "identity") {
			return p.fail(w, http.StatusBadGateway, errcode.ModelGWUpstreamError, "upstream response encoding is not supported")
		}
	}

	if wantStream && mediaType(resp.Header.Get("Content-Type")) == "text/event-stream" {
		return p.stream(w, resp, timer)
	}
	return p.buffered(w, resp, &timedOut)
}

// buffered 读完整个响应体再 mask 写出，超限时不泄露任何部分内容。
func (p *Proxy) buffered(w http.ResponseWriter, resp *http.Response, timedOut *atomic.Bool) result {
	if resp.ContentLength > p.maxResp {
		return p.fail(w, http.StatusBadGateway, errcode.ModelGWTooLarge, "upstream response too large")
	}
	limit := p.maxResp
	if limit < math.MaxInt64 {
		limit++ // 多读一字节识别超限，不让 MaxInt64 配置溢出。
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		msg := "upstream response read failed"
		if timedOut.Load() {
			msg = "upstream request timed out"
		}
		return p.fail(w, http.StatusBadGateway, errcode.ModelGWUpstreamError, msg)
	}
	if int64(len(b)) > p.maxResp {
		return p.fail(w, http.StatusBadGateway, errcode.ModelGWTooLarge, "upstream response too large")
	}
	b = p.mk.bytes(b)
	h := w.Header()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		h.Set("Content-Type", p.mk.str(ct))
	}
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(b)
	return result{status: resp.StatusCode}
}

// flushWriter 每次写完立即 flush。
type flushWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (f flushWriter) Write(b []byte) (int, error) {
	n, err := f.w.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		// 不支持 flush 的 writer 不算错
		if ferr := f.rc.Flush(); ferr != nil && !errors.Is(ferr, http.ErrNotSupported) {
			err = ferr
		}
	}
	return n, err
}

// stream 边收边转，经 maskWriter 遮盖，每块写出后 flush。
func (p *Proxy) stream(w http.ResponseWriter, resp *http.Response, timer *time.Timer) result {
	h := w.Header()
	h.Set("Content-Type", p.mk.str(resp.Header.Get("Content-Type")))
	if cc := resp.Header.Get("Cache-Control"); cc != "" {
		h.Set("Cache-Control", p.mk.str(cc))
	}
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return result{status: resp.StatusCode, code: errcode.ModelGWUpstreamError}
	}
	mw := p.mk.writer(flushWriter{w: w, rc: rc})

	timer.Reset(p.timeout)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			timer.Reset(p.timeout)
			if _, werr := mw.Write(buf[:n]); werr != nil { // 客户端写失败
				return result{status: resp.StatusCode, code: errcode.ModelGWUpstreamError}
			}
		}
		if err == io.EOF {
			if err := mw.Close(); err != nil {
				return result{status: resp.StatusCode, code: errcode.ModelGWUpstreamError}
			}
			return result{status: resp.StatusCode}
		}
		if err != nil {
			// 上游中途出错：丢弃留存的尾部，断开连接
			return result{status: resp.StatusCode, code: errcode.ModelGWUpstreamError, abort: true}
		}
	}
}

// pickAccept 只放行两种媒体类型，其余用 application/json。
func pickAccept(v string) string {
	switch mediaType(v) {
	case "text/event-stream":
		return "text/event-stream"
	default:
		return "application/json"
	}
}

func mediaType(v string) string {
	mt, _, err := mime.ParseMediaType(v)
	if err != nil {
		return ""
	}
	return mt
}
