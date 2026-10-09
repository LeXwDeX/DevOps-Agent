package modelgw

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LeXwDeX/DevOps-Agent/platform/internal/schema/errcode"
)

const testKey = "sk-TESTKEY-0123456789abcdef"

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoadConfigBaseURL(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		wantOK bool
		target string
	}{
		{"https plain", "https://llm.example.com", true, "https://llm.example.com/chat/completions"},
		{"https path", "https://llm.example.com/v1", true, "https://llm.example.com/v1/chat/completions"},
		{"trailing slash stripped", "https://llm.example.com/v1/", true, "https://llm.example.com/v1/chat/completions"},
		{"many trailing slashes", "https://llm.example.com/v1///", true, "https://llm.example.com/v1/chat/completions"},
		{"port", "https://llm.example.com:8443/v1", true, "https://llm.example.com:8443/v1/chat/completions"},
		{"ipv6", "https://[::1]:8443/v1", true, "https://[::1]:8443/v1/chat/completions"},
		{"upper scheme", "HTTPS://llm.example.com/v1", true, "https://llm.example.com/v1/chat/completions"},
		{"path ending chat only", "https://llm.example.com/chat", true, "https://llm.example.com/chat/chat/completions"},
		{"http", "http://llm.example.com/v1", false, ""},
		{"no scheme", "llm.example.com/v1", false, ""},
		{"ftp", "ftp://llm.example.com/v1", false, ""},
		{"no host", "https:///v1", false, ""},
		{"port only host", "https://:443/v1", false, ""},
		{"userinfo", "https://user:pw@llm.example.com/v1", false, ""},
		{"userinfo empty", "https://@llm.example.com/v1", false, ""},
		{"user only", "https://user@llm.example.com/v1", false, ""},
		{"query", "https://llm.example.com/v1?a=b", false, ""},
		{"bare query", "https://llm.example.com/v1?", false, ""},
		{"fragment", "https://llm.example.com/v1#x", false, ""},
		{"bare fragment", "https://llm.example.com/v1#", false, ""},
		{"chat completions", "https://llm.example.com/v1/chat/completions", false, ""},
		{"chat completions slash", "https://llm.example.com/v1/chat/completions/", false, ""},
		{"chat completions case", "https://llm.example.com/v1/Chat/Completions", false, ""},
		{"chat completions escaped", "https://llm.example.com/v1/chat%2Fcompletions", false, ""},
		{"space inside", "https://llm.example.com/v 1", false, ""},
		{"leading space", " https://llm.example.com/v1", false, ""},
		{"newline", "https://llm.example.com/v1\n", false, ""},
		{"bad port", "https://llm.example.com:abc/v1", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := LoadConfig(env(map[string]string{EnvBaseURL: tc.raw, EnvAPIKey: testKey}))
			if cfg.Available() != tc.wantOK {
				t.Fatalf("Available=%v want %v (status %+v)", cfg.Available(), tc.wantOK, cfg.Status())
			}
			if tc.wantOK {
				if cfg.target != tc.target {
					t.Fatalf("target=%q want %q", cfg.target, tc.target)
				}
				return
			}
			st := cfg.Status()
			if st.State != StateInvalid {
				t.Fatalf("state=%q want invalid", st.State)
			}
			if !strings.Contains(st.Reason, EnvBaseURL) || strings.Contains(st.Reason, EnvAPIKey) {
				t.Fatalf("reason should name only the URL variable: %q", st.Reason)
			}
			if cfg.target != "" {
				t.Fatal("invalid config must not keep a target")
			}
		})
	}
}

func TestLoadConfigKey(t *testing.T) {
	good := "https://llm.example.com/v1"
	cases := []struct {
		name   string
		val    string
		set    bool
		want   string
		wantOK bool
	}{
		{"ok", testKey, true, StateAvailable, true},
		{"ok punctuation", "a.b-c_d/e+f=", true, StateAvailable, true},
		{"unset", "", false, StateMissing, false},
		{"empty", "", true, StateMissing, false},
		{"space inside", "ab cd", true, StateInvalid, false},
		{"only space", "   ", true, StateInvalid, false},
		{"trailing newline", testKey + "\n", true, StateInvalid, false},
		{"tab", "ab\tcd", true, StateInvalid, false},
		{"nul", "ab\x00cd", true, StateInvalid, false},
		{"del", "ab\x7fcd", true, StateInvalid, false},
		{"unicode space", "ab cd", true, StateInvalid, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]string{EnvBaseURL: good}
			if tc.set {
				m[EnvAPIKey] = tc.val
			}
			cfg := LoadConfig(env(m))
			st := cfg.Status()
			if st.State != tc.want || cfg.Available() != tc.wantOK {
				t.Fatalf("status=%+v available=%v", st, cfg.Available())
			}
			if tc.wantOK {
				if st.Reason != "" {
					t.Fatalf("available must have empty reason: %q", st.Reason)
				}
				return
			}
			if !strings.Contains(st.Reason, EnvAPIKey) || strings.Contains(st.Reason, EnvBaseURL) {
				t.Fatalf("reason should name only the key variable: %q", st.Reason)
			}
			if tc.val != "" && strings.Contains(st.Reason, tc.val) {
				t.Fatalf("reason leaks value: %q", st.Reason)
			}
		})
	}
}

func TestStatusCombinations(t *testing.T) {
	cases := []struct {
		name  string
		m     map[string]string
		state string
		names []string
	}{
		{"both missing", map[string]string{}, StateMissing, []string{EnvBaseURL, EnvAPIKey}},
		{"url missing key ok", map[string]string{EnvAPIKey: testKey}, StateMissing, []string{EnvBaseURL}},
		{"url invalid key missing", map[string]string{EnvBaseURL: "http://x"}, StateInvalid, []string{EnvBaseURL, EnvAPIKey}},
		{"url ok key invalid", map[string]string{EnvBaseURL: "https://x.example.com", EnvAPIKey: "a b"}, StateInvalid, []string{EnvAPIKey}},
	}
	for _, tc := range cases {
		st := LoadConfig(env(tc.m)).Status()
		if st.State != tc.state {
			t.Errorf("%s: state=%q want %q", tc.name, st.State, tc.state)
		}
		for _, n := range tc.names {
			if !strings.Contains(st.Reason, n) {
				t.Errorf("%s: reason %q lacks %s", tc.name, st.Reason, n)
			}
		}
	}
	// 零值和 nil lookup 都是缺失
	if st := (Config{}).Status(); st.State != StateMissing {
		t.Errorf("zero config: %+v", st)
	}
	if st := LoadConfig(nil).Status(); st.State != StateMissing {
		t.Errorf("nil lookup: %+v", st)
	}
}

func TestLoadConfigReadsOnlyTwoVariables(t *testing.T) {
	var asked []string
	LoadConfig(func(k string) (string, bool) { asked = append(asked, k); return "", false })
	for _, k := range asked {
		if k != EnvBaseURL && k != EnvAPIKey {
			t.Fatalf("read unexpected variable %q", k)
		}
	}
}

func TestStatusReasonNoValues(t *testing.T) {
	cfg := LoadConfig(env(map[string]string{
		EnvBaseURL: "http://secret-host.example/v1?token=SUPERSECRET",
		EnvAPIKey:  "bad key SUPERKEY",
	}))
	st := cfg.Status()
	for _, bad := range []string{"secret-host", "SUPERSECRET", "SUPERKEY", "token"} {
		if strings.Contains(st.Reason, bad) {
			t.Fatalf("reason leaks %q: %q", bad, st.Reason)
		}
	}
}

func TestConfigRedaction(t *testing.T) {
	cfg := LoadConfig(env(map[string]string{EnvBaseURL: "https://host-xyz.example/v1", EnvAPIKey: testKey}))
	type wrap struct{ C Config }
	outs := []string{
		fmt.Sprintf("%v %+v %#v %s %q %d", cfg, cfg, cfg, cfg, cfg, cfg),
		fmt.Sprintf("%v %+v %#v", &cfg, &cfg, &cfg),
		fmt.Sprintf("%v %+v %#v", wrap{cfg}, wrap{cfg}, wrap{cfg}),
		fmt.Sprint(cfg), cfg.String(), cfg.GoString(),
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	outs = append(outs, string(b))
	b, err = json.Marshal(wrap{cfg})
	if err != nil {
		t.Fatal(err)
	}
	outs = append(outs, string(b))
	b, _ = cfg.MarshalText()
	outs = append(outs, string(b))
	for _, o := range outs {
		if strings.Contains(o, testKey) || strings.Contains(o, "host-xyz") {
			t.Fatalf("leak in %q", o)
		}
	}

	p, err := New(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := fmt.Sprintf("%v %+v %#v %s", p, p, p, p)
	if strings.Contains(s, testKey) || strings.Contains(s, "host-xyz") {
		t.Fatalf("proxy leaks: %q", s)
	}
}

func TestForbidInCore(t *testing.T) {
	if err := ForbidInCore(nil); err != nil {
		t.Fatal(err)
	}
	if err := ForbidInCore([]string{"PATH=/bin", "DEVOPS_LLM_API_KEY_X=1", "XDEVOPS_LLM_BASE_URL=1"}); err != nil {
		t.Fatalf("similar names must pass: %v", err)
	}
	for _, e := range [][]string{
		{"DEVOPS_LLM_BASE_URL=https://secret.example/v1"},
		{"A=1", "DEVOPS_LLM_API_KEY=" + testKey},
		{"DEVOPS_LLM_API_KEY="},
		{"DEVOPS_LLM_API_KEY"},
	} {
		err := ForbidInCore(e)
		if code, ok := errcode.CodeOf(err); !ok || code != errcode.ConfigForbiddenEnv {
			t.Fatalf("env %v: err=%v", e, err)
		}
		if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "secret.example") {
			t.Fatalf("error leaks value: %v", err)
		}
	}
	err := ForbidInCore([]string{"DEVOPS_LLM_API_KEY=x", "DEVOPS_LLM_BASE_URL=y"})
	if !strings.Contains(err.Error(), EnvBaseURL) || !strings.Contains(err.Error(), EnvAPIKey) {
		t.Fatalf("should name both: %v", err)
	}
}

func TestStaticTokenAuth(t *testing.T) {
	mk := func(h string) *http.Request {
		r := httptest.NewRequest("POST", "/", nil)
		if h != "" {
			r.Header.Set("Authorization", h)
		}
		return r
	}
	a := StaticTokenAuth("tok-123")
	for h, want := range map[string]bool{
		"Bearer tok-123":  true,
		"bearer tok-123":  true,
		"Bearer tok-1234": false,
		"Bearer tok-12":   false,
		"Bearer ":         false,
		"Bearer":          false,
		"Basic tok-123":   false,
		"tok-123":         false,
		"":                false,
	} {
		if got := a(mk(h)); got != want {
			t.Errorf("header %q: got %v want %v", h, got, want)
		}
	}
	empty := StaticTokenAuth("")
	for _, h := range []string{"", "Bearer ", "Bearer x", "Bearer"} {
		if empty(mk(h)) {
			t.Errorf("empty token must never pass (header %q)", h)
		}
	}
}
