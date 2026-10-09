package modelgw

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// 可选的真实模型联调：只在 DEVOPS_LLM_LIVE_TEST=1 且两个模型接入变量都有值时运行。
// 测试专用变量，不属于运行时环境变量表。不输出 key 和完整响应体。
func liveProxy(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	if os.Getenv("DEVOPS_LLM_LIVE_TEST") != "1" {
		t.Skip("live test disabled: set DEVOPS_LLM_LIVE_TEST=1 to run")
	}
	cfg := LoadConfig(os.LookupEnv)
	if !cfg.Available() {
		t.Skipf("live test needs %s and %s: %s", EnvBaseURL, EnvAPIKey, cfg.Status().Reason)
	}
	model := os.Getenv("DEVOPS_LLM_TEST_MODEL")
	if model == "" {
		model = "deepseek"
	}
	const token = "live-test-token"
	p, err := New(cfg, Options{ClientAuth: StaticTokenAuth(token), Timeout: 180 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(p)
	t.Cleanup(s.Close)
	return s, token, model
}

func liveDo(t *testing.T, s *httptest.Server, token string, body map[string]any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", s.URL+"/v1/chat/completions", strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func TestLiveNonStream(t *testing.T) {
	s, token, model := liveProxy(t)
	resp := liveDo(t, s, token, map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "Say hello in one short line."}},
		"max_tokens": 512,
	})
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	t.Logf("status=%d body_len=%d", resp.StatusCode, len(b))
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		Choices []json.RawMessage `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	// 不断言 content 非空：部分模型把内容放在 reasoning_content，或因 length 返回空
	if len(out.Choices) == 0 {
		t.Fatal("choices is empty")
	}
}

func TestLiveStream(t *testing.T) {
	s, token, model := liveProxy(t)
	resp := liveDo(t, s, token, map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "Say hello in one short line."}},
		"max_tokens": 512,
		"stream":     true,
	})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body_len=%d", resp.StatusCode, len(b))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	dataLines, done, total := 0, false, 0
	for sc.Scan() {
		line := sc.Text()
		total += len(line)
		if strings.HasPrefix(line, "data:") {
			dataLines++
			if strings.TrimSpace(strings.TrimPrefix(line, "data:")) == "[DONE]" {
				done = true
			}
		}
	}
	t.Log(fmt.Sprintf("status=200 data_lines=%d total_len=%d done=%v", dataLines, total, done))
	if err := sc.Err(); err != nil {
		t.Fatalf("stream read: %v", err)
	}
	if dataLines == 0 {
		t.Fatal("no data lines")
	}
	if !done {
		t.Fatal("missing [DONE]")
	}
}
