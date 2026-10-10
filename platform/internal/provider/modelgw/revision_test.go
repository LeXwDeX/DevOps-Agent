package modelgw

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

func TestSelfGeneratedJSONPreservesProtocolWithShortKeys(t *testing.T) {
	for _, key := range []string{"available", "status", "error", `"`, "modelgw", "missing", "invalid", "message"} {
		t.Run(key, func(t *testing.T) {
			available := LoadConfig(env(map[string]string{EnvBaseURL: "https://model.example/v1", EnvAPIKey: key}))
			missing := LoadConfig(env(map[string]string{EnvAPIKey: key}))
			invalid := LoadConfig(env(map[string]string{EnvBaseURL: "http://model.example/v1", EnvAPIKey: key}))
			for _, tc := range []struct {
				name, method, path, body string
				cfg                      Config
				authorized               bool
				status                   int
				state                    string
				code                     errcode.Code
			}{
				{"available status", "GET", "/v1/status", "", available, true, 200, StateAvailable, ""},
				{"missing status", "GET", "/v1/status", "", missing, true, 200, StateMissing, ""},
				{"invalid status", "GET", "/v1/status", "", invalid, true, 200, StateInvalid, ""},
				{"unauthorized", "POST", "/v1/chat/completions", validBody, available, false, 401, "", errcode.ModelGWUnauthorized},
				{"unavailable", "POST", "/v1/chat/completions", validBody, missing, true, 503, "", errcode.ModelGWUnavailable},
				{"bad request", "POST", "/v1/chat/completions", `{"model":"","messages":[]}`, available, true, 400, "", errcode.ModelGWBadRequest},
			} {
				t.Run(tc.name, func(t *testing.T) {
					p, err := New(tc.cfg, Options{ClientAuth: StaticTokenAuth(clientToken), Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						t.Fatal("proxy-generated response must not call upstream")
						return nil, nil
					})})
					if err != nil {
						t.Fatal(err)
					}
					r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
					if tc.authorized {
						r.Header.Set("Authorization", "Bearer "+clientToken)
					}
					w := httptest.NewRecorder()
					p.ServeHTTP(w, r)
					if w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" {
						t.Fatalf("status %d", w.Code)
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
						t.Fatal("proxy-generated response is not valid JSON")
					}
					var text string
					if tc.state != "" {
						var status Status
						if len(fields) != 2 || fields["status"] == nil || fields["reason"] == nil || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.State != tc.state {
							t.Fatal("fixed status fields or enum changed")
						}
						text = status.Reason
					} else {
						var problem struct {
							Code    string `json:"code"`
							Message string `json:"message"`
						}
						var problemFields map[string]json.RawMessage
						if len(fields) != 1 || fields["error"] == nil || json.Unmarshal(fields["error"], &problemFields) != nil || len(problemFields) != 2 || problemFields["code"] == nil || problemFields["message"] == nil || json.Unmarshal(fields["error"], &problem) != nil || problem.Code != string(tc.code) {
							t.Fatal("fixed error fields or centralized error code changed")
						}
						text = problem.Message
					}
					// 固定字段、状态枚举与错误码可与短 key 同字节；自由文本必须遮盖。
					if strings.Contains(text, key) {
						t.Fatal("free text contains the configured credential")
					}
				})
			}
		})
	}
}

func TestBufferedMaskRespectsInputAndOutputLimits(t *testing.T) {
	longKey := strings.Repeat("Q", 40)
	for _, tc := range []struct {
		name, key, upstream string
		limit               int64
		status              int
	}{
		{"short key expands past limit", "x", strings.Repeat("x", 128), 128, 502},
		{"expanded exactly at limit", "x", "xxxx", int64(4 * len(MaskText)), 200},
		{"expanded one byte past limit", "x", "xxxx", int64(4*len(MaskText) - 1), 502},
		{"maximum limit", "x", "xxxx", math.MaxInt64, 200},
		{"shrinks within limit", longKey, longKey + longKey, 80, 200},
		{"shrinking cannot bypass input limit", longKey, longKey, int64(len(MaskText)), 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := LoadConfig(env(map[string]string{EnvBaseURL: "https://model.example/v1", EnvAPIKey: tc.key}))
			p, err := New(cfg, Options{ClientAuth: StaticTokenAuth(clientToken), MaxResponseBytes: tc.limit, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: io.NopCloser(strings.NewReader(tc.upstream))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(validBody)))
			if w.Code != tc.status {
				t.Fatalf("status %d want %d", w.Code, tc.status)
			}
			if tc.status == http.StatusBadGateway {
				if errCode(t, w.Body.Bytes()) != string(errcode.ModelGWTooLarge) {
					t.Fatal("oversized input or masked output returned the wrong error code")
				}
				return
			}
			want := newMasker(tc.key).str(tc.upstream)
			if w.Body.String() != want || int64(w.Body.Len()) > tc.limit || w.Header().Get("Content-Length") != strconv.Itoa(len(want)) {
				t.Fatal("output did not obey the actual response byte limit")
			}
		})
	}
}

func TestOversizedMaskedOutputIsRejectedBeforeAllocation(t *testing.T) {
	mk := newMasker("x")
	input := bytes.Repeat([]byte("x"), 128)
	if allocations := testing.AllocsPerRun(10, func() {
		if _, ok := mk.boundedBytes(input, 128); ok {
			t.Fatal("expanded output was accepted")
		}
	}); allocations != 0 {
		t.Fatal("rejecting expanded output allocated a replacement buffer")
	}
}

func TestInvalidKeyContainingAllFallbackCandidatesIsMasked(t *testing.T) {
	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}
	for _, prefix := range []string{"*~!? ", string(allBytes)} {
		key := prefix + MaskText
		mk := newMasker(key)
		if !markerCanRecreateKey([]byte(key), []byte(MaskText)) || markerCanRecreateKey([]byte(key), mk.mask) {
			t.Fatal("invalid key did not receive a safe replacement marker")
		}
		input := prefix + key
		if !strings.Contains(strings.ReplaceAll(input, key, MaskText), key) {
			t.Fatal("invalid-key fixture does not reproduce the old leak")
		}
		got := streamMask(input, key, []int{1, 3, 7})
		if got != mk.str(input) || strings.Contains(got, key) {
			t.Fatal("invalid key was reconstructed at a replacement boundary")
		}
		cfg := LoadConfig(env(map[string]string{EnvBaseURL: "https://model.example/v1", EnvAPIKey: key}))
		if cfg.Available() || cfg.Status().State != StateInvalid || strings.Contains(cfg.Status().Reason, key) || strings.Contains(cfg.String(), key) {
			t.Fatal("invalid configuration status or formatting exposed its key")
		}
	}
}

func TestMaskMarkerCannotRecreateKeyAcrossBoundaries(t *testing.T) {
	for _, tc := range []struct{ key, input string }{
		{"b[", "bb["},
		{"]b", "]bb"},
		{"b[MASKED", "bb[MASKED"},
		{"KEY]b", "KEY]bb"},
		{"b" + MaskText + "c", "b" + "b" + MaskText + "c" + "c"},
		{"*~!?b[", "*~!?b*~!?b["},
	} {
		t.Run(tc.key, func(t *testing.T) {
			if !strings.Contains(strings.ReplaceAll(tc.input, tc.key, MaskText), tc.key) {
				t.Fatal("regression fixture does not recreate the key with the old marker")
			}
			mk := newMasker(tc.key)
			if len(mk.mask) != 1 || bytes.IndexByte(mk.key, mk.mask[0]) >= 0 {
				t.Fatal("unsafe marker was not replaced by a byte absent from the key")
			}
			want := mk.str(tc.input)
			if strings.Contains(want, tc.key) {
				t.Fatal("buffered mask recreated the key")
			}
			for chunkSize := 1; chunkSize <= len(tc.input); chunkSize++ {
				var out bytes.Buffer
				mw := mk.writer(&out)
				for start := 0; start < len(tc.input); start += chunkSize {
					if _, err := mw.Write([]byte(tc.input[start:min(start+chunkSize, len(tc.input))])); err != nil {
						t.Fatal(err)
					}
				}
				if err := mw.Close(); err != nil {
					t.Fatal(err)
				}
				if out.String() != want || strings.Contains(out.String(), tc.key) {
					t.Fatal("stream mask recreated the key at a replacement or write boundary")
				}
			}
		})
	}
}

func TestHTTPBufferedAndStreamUseTheSafeMarker(t *testing.T) {
	for _, key := range []string{"b[", "]b"} {
		for _, streaming := range []bool{false, true} {
			t.Run(key+"/stream="+strconv.FormatBool(streaming), func(t *testing.T) {
				input := "b" + key
				if key == "]b" {
					input = key + "b"
				}
				cfg := LoadConfig(env(map[string]string{EnvBaseURL: "https://model.example/v1", EnvAPIKey: key}))
				p, err := New(cfg, Options{ClientAuth: StaticTokenAuth(clientToken), Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					header := make(http.Header)
					if streaming {
						header.Set("Content-Type", "text/event-stream")
					}
					return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(input))}, nil
				})})
				if err != nil {
					t.Fatal(err)
				}
				body := validBody
				if streaming {
					body = `{"model":"m","messages":[],"stream":true}`
				}
				w := httptest.NewRecorder()
				p.ServeHTTP(w, authorizedRequest(context.Background(), strings.NewReader(body)))
				if w.Code != http.StatusOK || w.Body.String() != newMasker(key).str(input) || strings.Contains(w.Body.String(), key) {
					t.Fatal("HTTP response recreated the credential")
				}
			})
		}
	}
}
