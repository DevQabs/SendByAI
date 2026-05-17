package filter

// OllamaFilter re-judges messages that were quarantined by an upstream filter.
// It calls a local Ollama instance and asks the LLM to make a final Allow/Quarantine/Block decision.
// Messages not marked quarantined pass through immediately (no LLM call).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	defaultOllamaURL   = "http://localhost:11434"
	defaultOllamaModel = "qwen2.5:7b"
)

// OllamaFilter calls a local Ollama LLM to re-judge quarantined messages.
type OllamaFilter struct {
	url    string
	model  string
	client *http.Client
}

// OllamaOption configures OllamaFilter.
type OllamaOption func(*OllamaFilter)

func WithOllamaURL(url string) OllamaOption   { return func(f *OllamaFilter) { f.url = url } }
func WithOllamaModel(model string) OllamaOption { return func(f *OllamaFilter) { f.model = model } }

// NewOllama creates an OllamaFilter. No heavy init — connection happens per request.
func NewOllama(opts ...OllamaOption) *OllamaFilter {
	f := &OllamaFilter{
		url:    filterEnvOr("OLLAMA_URL", defaultOllamaURL),
		model:  filterEnvOr("OLLAMA_MODEL", defaultOllamaModel),
		client: &http.Client{Timeout: 120 * time.Second},
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Name implements Filter.
func (f *OllamaFilter) Name() string { return "ollama-review" }

// Filter implements Filter. Only calls LLM when msg.Meta["quarantined"] is set.
func (f *OllamaFilter) Filter(ctx context.Context, msg *Message) (*Result, error) {
	if msg.Meta == nil || msg.Meta["quarantined"] == nil {
		return &Result{Action: ActionAllow, Message: msg}, nil
	}

	jr, err := f.judge(ctx, msg)
	if err != nil {
		// LLM error → keep quarantine (fail safe)
		slog.Warn("ollama judge failed, keeping quarantine", "err", err, "user", msg.UserID)
		return &Result{Action: ActionQuarantine, Message: msg, Reason: "ollama unavailable"}, nil
	}

	switch jr.verdict {
	case "block":
		return &Result{Action: ActionBlock, Reason: "ollama: " + jr.reason, Score: 1.0}, nil
	case "allow":
		return &Result{Action: ActionAllow, Message: msg, Reason: "ollama: " + jr.reason, Score: 0.0}, nil
	default:
		return &Result{Action: ActionQuarantine, Message: msg, Reason: "ollama: " + jr.reason}, nil
	}
}

type judgeResult struct {
	verdict string
	reason  string
}

func (f *OllamaFilter) judge(ctx context.Context, msg *Message) (judgeResult, error) {
	prompt := fmt.Sprintf(`당신은 한국어 채팅 모더레이션 전문가입니다. 아래 메시지를 판단하세요.

판단 기준:
- block: 특정 집단을 비하하는 신조어·욕설 사용, 또는 명백한 차별 의도
- quarantine: 특정 집단에 대한 부정적 고정관념·일반화 (비하 신조어 없어도)
- allow: 개인 경험, 중립적 사실, 불만 표현 (특정 집단 타깃 아님)

예시:
메시지: "나이 많은 사람들은 고집이 세"
근거: 연령 집단 전체를 부정적으로 일반화하는 표현이다.
판정: quarantine

메시지: "그 동네 사람들은 좀 그렇더라"
근거: 특정 지역 집단을 막연히 부정적으로 일반화하는 표현이다.
판정: quarantine

메시지: "오늘 버스에서 할아버지가 자리 양보를 안 해줬어"
근거: 개인 경험 서술이며 특정 집단 전체를 타깃하지 않는다.
판정: allow

메시지: "틀딱들은 왜 저래"
근거: 노인 비하 신조어를 사용한 명확한 혐오 표현이다.
판정: block

이제 아래 메시지를 판단하세요:
메시지: "%s"

다음 형식으로 정확히 두 줄만 답하세요:
근거: <한 문장으로 판단 근거>
판정: <allow 또는 quarantine 또는 block>`, msg.Content)

	body, _ := json.Marshal(map[string]any{
		"model":  f.model,
		"stream": false,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"options": map[string]any{
			"temperature": 0,    // deterministic
			"num_ctx":     1024, // few-shot examples need more context
			"num_predict": 80,   // response is only 2 lines
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return judgeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return judgeResult{}, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return judgeResult{}, fmt.Errorf("ollama status %d", resp.StatusCode)
	}

	var ollamaResp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return judgeResult{}, fmt.Errorf("ollama decode: %w", err)
	}

	raw := strings.TrimSpace(ollamaResp.Message.Content)
	jr := parseJudge(raw)

	return jr, nil
}

// parseJudge extracts verdict and reason from LLM response.
// Expected format:
//
//	근거: <reason>
//	판정: <allow|quarantine|block>
//
// Falls back to keyword scan if format is not followed.
func parseJudge(raw string) judgeResult {
	var reason, verdict string

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "근거:"); ok {
			reason = strings.TrimSpace(after)
		}
		if after, ok := strings.CutPrefix(line, "판정:"); ok {
			v := strings.ToLower(strings.TrimSpace(after))
			switch {
			case strings.Contains(v, "block"):
				verdict = "block"
			case strings.Contains(v, "allow"):
				verdict = "allow"
			default:
				verdict = "quarantine"
			}
		}
	}

	if verdict == "" {
		lower := strings.ToLower(raw)
		switch {
		case strings.Contains(lower, "block"):
			verdict = "block"
		case strings.Contains(lower, "allow"):
			verdict = "allow"
		default:
			verdict = "quarantine"
		}
	}
	if reason == "" {
		reason = raw
	}

	return judgeResult{verdict: verdict, reason: reason}
}
