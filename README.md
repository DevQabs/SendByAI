# SendByAI

> **한국어 라이브 스트리밍 채팅을 위한 실시간 AI 모더레이션 — Go로 만든 오픈소스.**

---

## 비전

라이브 스트리밍 채팅은 생각의 속도로 흘러갑니다. 매 시간 수백만 개의 메시지가 한국 플랫폼을 지나가고, 건강한 커뮤니티와 독성 커뮤니티의 차이는 불과 몇 초에 달려 있습니다. 단어 목록 기반 필터는 취약합니다 — 창의적인 오타 하나면 뚫립니다. 사람 모더레이터는 규모를 감당할 수 없습니다.

SendByAI는 **세 겹의 지능** — 경량 규칙 기반 검사, 도메인 특화 한국어 언어 모델, 심층 추론 LLM — 이 함께 작동하는 실시간 파이프라인이 충분히 빠르면서도 충분히 정확할 수 있다는 믿음 위에 세워졌습니다.

이 프로젝트가 오픈소스인 이유는 모더레이션 도구가 경쟁의 무기가 되어서는 안 되기 때문입니다. 모든 한국어 커뮤니티가 이것을 사용할 자격이 있습니다.

---

## 아키텍처

```
WebSocket 클라이언트
      │
      ▼
  [ Hub ]  ──── 등록 / 해제 ────► room map
      │
      │  (메시지당 goroutine)
      ▼
  Step 1 │ kor_unsmile ONNX 분류기 (동기)
         │   Block(≥0.7)         → sendToClient {type:"block", reason, score}  ← 발신자에게만
         │   Allow(<0.4)         → broadcast {type:"message", msg_id, ...}
         │   Quarantine(0.4~0.7) → broadcast {type:"message", msg_id, ...}
         │                          broadcast {type:"warn", msg_id, score, reason}  ← 즉시 경고
         │                          + Step 3 비동기 실행
      │
      ▼  (Step 3 goroutine — Quarantine 메시지만)
  Step 3 │ Ollama LLM 심층 재판단
         │   맥락·풍자 판단 후 최종 판정:
         │
         ├── allow      → broadcast {type:"clear_warn", msg_id}  ← 경고 해제
         ├── quarantine → 아무 이벤트 없음 (warn 유지)
         └── block      → broadcast {type:"delete", msg_id, user_id, reason, score}
```

각 필터는 네 가지 결정 중 하나를 반환합니다:

| 결정         | 효과                                                  |
| ------------ | ----------------------------------------------------- |
| `Allow`      | 다음 필터로 통과                                      |
| `Replace`    | 정제된 내용으로 교체 후 계속                          |
| `Quarantine` | 낙관적 브로드캐스트 후 Ollama 비동기 재판단 트리거    |
| `Block`      | Step 1: 발신자에게만 `block` 이벤트 전송 / Step 3: 전체에 `delete` 이벤트 전송 |

---

## 사용 모델

### [smilegate-ai/kor_unsmile](https://huggingface.co/smilegate-ai/kor_unsmile)

- **출처:** [Korean Unsmile Dataset](https://github.com/smilegate-ai/korean_unsmile_dataset) (Smilegate AI)
- **아키텍처:** `BertForSequenceClassification`
- **분류:** 10개 레이블 (멀티 라벨)

| 레이블    | 설명                   |
| --------- | ---------------------- |
| 여성/가족 | 여성 및 가족 대상 혐오 |
| 남성      | 남성 대상 혐오         |
| 성소수자  | 성소수자 대상 혐오     |
| 인종/국적 | 인종·국적 대상 혐오    |
| 연령      | 나이 관련 혐오         |
| 지역      | 지역 감정              |
| 종교      | 종교 대상 혐오         |
| 기타 혐오 | 기타 혐오 표현         |
| 악플/욕설 | 욕설·악성 댓글         |
| 클린      | 정상 발언              |

**점수 계산:** `hate_score = 1 - P(클린)`. 점수 ≥ 0.7 → Block, ≥ 0.4 → Quarantine.

모델은 ONNX로 변환되어 `models/kor_unsmile.onnx`에 저장됩니다.

### Ollama LLM (Step 3)

kor_unsmile이 Quarantine으로 표시한 경계선 메시지만 재판단합니다. Step 1 이후 **비동기**로 실행되므로 메시지는 판단 전에 이미 클라이언트에 표시됩니다.

- **기본 모델:** `qwen2.5:7b` (한국어 강세, Intel/AMD CPU에서 실용적 속도)
- **판단 형식:** 근거 한 문장 + 최종 판정 (allow / quarantine / block)
- **실패 시 동작:** Ollama 응답 없으면 `warn` 이벤트 전송 (fail-safe)

---

## 프로젝트 구조

```
SendByAI/
├── cmd/
│   └── server/
│       └── main.go              # 진입점; 필터 체인 구성
├── internal/
│   ├── hub/
│   │   ├── hub.go               # 이벤트 루프, 브로드캐스트, room 관리
│   │   ├── client.go            # WebSocket read/write pump
│   │   └── message.go           # 수신/송신 메시지 타입
│   └── filter/
│       ├── filter.go            # Filter 인터페이스 + Action 열거형
│       ├── chain.go             # 순차 파이프라인 (short-circuit 지원)
│       ├── unsmile.go           # kor_unsmile ONNX 필터
│       └── ollama.go            # Ollama LLM 재판단 필터
├── models/
│   ├── kor_unsmile.onnx         # 변환된 ONNX 모델
│   └── tokenizer/               # HuggingFace 토크나이저 파일
├── libtokenizers.a              # daulet/tokenizers Rust 바이너리
├── Makefile
├── go.mod
└── README.md
```

---

## 빠른 시작

### 사전 요구사항

```bash
# Go 1.22+
go version

# ONNX Runtime (macOS)
brew install onnxruntime

# Ollama — https://ollama.com 에서 설치 후:
ollama pull qwen2.5:7b

# libtokenizers.a (이미 포함) — 재다운로드 필요 시:
make fetch-libs

# WebSocket 테스트 클라이언트 (선택)
brew install websocat
```

### ORT_LIB 경로 확인

brew 설치 위치는 칩에 따라 다릅니다:

```bash
# Apple Silicon (M1/M2/M3)
ls /opt/homebrew/lib/libonnxruntime.dylib

# Intel Mac
ls /usr/local/lib/libonnxruntime.dylib
```

없으면 직접 찾기:

```bash
find /opt /usr/local -name "libonnxruntime.dylib" 2>/dev/null
```

### 빌드 및 실행

```bash
# Apple Silicon
make run

# Intel Mac (또는 경로가 다를 경우)
make run ORT_LIB=/usr/local/lib/libonnxruntime.dylib
```

서버 시작 시 아래 로그가 출력됩니다:

```
INFO SendByAI listening addr=:8080
```

### 환경 변수

| 변수                     | 기본값                                   | 설명                         |
| ------------------------ | ---------------------------------------- | ---------------------------- |
| `ADDR`                   | `:8080`                                  | 서버 수신 주소               |
| `UNSMILE_ONNX_PATH`      | `models/kor_unsmile.onnx`                | ONNX 모델 경로               |
| `UNSMILE_TOKENIZER_PATH` | `models/tokenizer/tokenizer.json`        | 토크나이저 경로              |
| `ORT_LIB`                | `/opt/homebrew/lib/libonnxruntime.dylib` | ONNX Runtime 라이브러리 경로 |
| `OLLAMA_URL`             | `http://localhost:11434`                 | Ollama 서버 주소             |
| `OLLAMA_MODEL`           | `qwen2.5:7b`                             | Ollama 사용 모델             |

### 동작 테스트

**헬스 체크:**

```bash
curl http://localhost:8080/health
# 200 OK 반환 시 정상
```

**WebSocket 연결** (`user_id`, `room_id` 필수):

```bash
websocat "ws://localhost:8080/ws?user_id=alice&room_id=room1"
```

연결 후 JSON 형식으로 메시지 전송:

```json
{ "content": "안녕하세요" }
```

**클라이언트가 수신하는 이벤트 형식:**

```jsonc
// 일반 메시지 — 전체 브로드캐스트
{ "type": "message", "msg_id": "a1b2c3d4", "user_id": "alice", "content": "...", "at": "..." }

// Step 1 → block: 발신자에게만 전송
{ "type": "block", "reason": "욕설 탐지", "score": 0.92 }

// Step 1 → quarantine: message와 동시에 즉시 전송 — 전체 브로드캐스트
{ "type": "warn", "msg_id": "a1b2c3d4", "user_id": "alice", "reason": "...", "score": 0.54 }

// Step 3 → allow: warn 해제 — 전체 브로드캐스트
{ "type": "clear_warn", "msg_id": "a1b2c3d4", "user_id": "alice" }

// Step 3 → block: 메시지 제거 — 전체 브로드캐스트
{ "type": "delete", "msg_id": "a1b2c3d4", "user_id": "alice", "reason": "...", "score": 1.0 }
```

**서버 로그 키워드:**

| 로그 키워드                                      | 의미                                   |
| ------------------------------------------------ | -------------------------------------- |
| (로그 없음)                                      | Allow — 정상 메시지                    |
| `message quarantined (fast), broadcasting optimistically` | Step 1 Quarantine, 낙관적 전송 후 Step 3 대기 |
| `deep filter: allow`                             | Ollama → 정상 확정, 추가 이벤트 없음   |
| `deep filter: quarantine — warning clients`      | Ollama → 보류 확정, `warn` 이벤트 전송 |
| `deep filter: block — retracting message`        | Ollama → 혐오 확정, `delete` 이벤트 전송 |
| `message blocked (fast)`                         | Step 1에서 즉시 차단 (전송 안 됨)      |

**판정 기준 예시:**

| 입력 예시                      | unsmile score | Step 1 이벤트              | Ollama 재판단 | Step 3 이벤트       |
| ------------------------------ | ------------- | -------------------------- | ------------- | ------------------- |
| `안녕하세요`                   | 0.10          | `message`                  | —             | —                   |
| `나이 많은 사람들은 고집이 세` | 0.54          | `message` + `warn`         | quarantine    | — (warn 유지)       |
| `그 동네 사람들은 좀 그래`     | 0.41          | `message` + `warn`         | quarantine    | — (warn 유지)       |
| `오늘 버스에서 할아버지가 …`   | 0.43          | `message` + `warn`         | allow         | `clear_warn`        |
| `급식충`                       | 0.92          | `block` (발신자만)         | —             | —                   |
| `틀딱`                         | 0.85          | `block` (발신자만)         | —             | —                   |

---

## 커스텀 필터 구현

```go
type MyFilter struct { /* 모델 클라이언트, 임계값 등 */ }

func (f *MyFilter) Name() string { return "my-filter" }

func (f *MyFilter) Filter(ctx context.Context, msg *filter.Message) (*filter.Result, error) {
    score := f.model.Score(ctx, msg.Content)
    if score > 0.85 {
        return &filter.Result{Action: filter.ActionBlock, Score: score, Reason: "toxic"}, nil
    }
    return &filter.Result{Action: filter.ActionAllow, Message: msg, Score: score}, nil
}
```

**Step 1 (동기 fast filter)로 추가** — `filter.NewChain`으로 묶어서 전달:

```go
fastChain := filter.NewChain(unsmile, &MyFilter{})
h := hub.New(fastChain, ollama)
```

**Step 3 (비동기 deep filter)로 교체** — `hub.New`의 두 번째 인자:

```go
h := hub.New(unsmile, &MyDeepFilter{})
```

`hub.New(fast, deep filter.Filter)` — 두 인자 모두 `filter.Filter` 인터페이스를 구현하면 됨.

---

## 로드맵

- [x] **Step 1** — WebSocket 서버 + 확장 가능한 필터 파이프라인
- [x] **Step 2** — [`smilegate-ai/kor_unsmile`](https://huggingface.co/smilegate-ai/kor_unsmile) ONNX 한국어 혐오 발언 분류기
- [x] **Step 3** — Ollama LLM 심층 재판단 레이어 (풍자·맥락 판단, `qwen2.5:7b` 기본)
- [x] **낙관적 브로드캐스트** — Quarantine 메시지 즉시 전송 후 Ollama 비동기 재판단; 결과에 따라 `warn` / `delete` 이벤트 발행

---

## 기여

이슈 및 PR 환영합니다. 새 필터를 추가할 경우 p99 지연 시간 벤치마크를 함께 제출해 주세요 — 파이프라인 목표는 **Step 2 < 200ms**, **Step 3 < 2s** (엔드투엔드) 입니다.

---

## 라이선스

MIT — [LICENSE](LICENSE) 참조.
