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
┌─────────────────────────────────────────────┐
│              Filter Chain                    │
│                                             │
│  Step 1 │ kor_unsmile ONNX 분류기 (현재)    │
│  Step 2 │ Ollama / Llama 3 심층 추론         │  ◄── 예정
└─────────────────────────────────────────────┘
      │
      ▼
  broadcast → room의 모든 클라이언트
```

각 필터는 네 가지 결정 중 하나를 반환합니다:

| 결정         | 효과                          |
| ------------ | ----------------------------- |
| `Allow`      | 다음 필터로 통과              |
| `Replace`    | 정제된 내용으로 교체 후 계속  |
| `Quarantine` | 수동 검토 대기; 발신자 미통보 |
| `Block`      | 메시지 즉시 삭제              |

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
│       └── unsmile.go           # kor_unsmile ONNX 필터
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
# macOS
brew install onnxruntime

# libtokenizers.a (이미 포함) — 재다운로드 필요 시:
make fetch-libs
```

### 빌드 및 실행

```bash
make build   # → bin/sendbyai
make run     # 빌드 후 :8080에서 실행
```

### 환경 변수

| 변수                     | 기본값                                   | 설명                         |
| ------------------------ | ---------------------------------------- | ---------------------------- |
| `ADDR`                   | `:8080`                                  | 서버 수신 주소               |
| `UNSMILE_ONNX_PATH`      | `models/kor_unsmile.onnx`                | ONNX 모델 경로               |
| `UNSMILE_TOKENIZER_PATH` | `models/tokenizer/tokenizer.json`        | 토크나이저 경로              |
| `ORT_LIB`                | `/opt/homebrew/lib/libonnxruntime.dylib` | ONNX Runtime 라이브러리 경로 |

### 접속 테스트

```bash
# WebSocket 클라이언트로 접속
ws://localhost:8080/ws?user_id=alice&room_id=room1

# 헬스 체크
curl http://localhost:8080/health
```

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

`cmd/server/main.go`의 체인에 추가:

```go
chain := filter.NewChain(
    unsmile,
    &MyFilter{...},  // ← 여기
)
```

Hub 코드 수정 불필요.

---

## 로드맵

- [x] **Step 1** — WebSocket 서버 + 확장 가능한 필터 파이프라인
- [x] **Step 2** — [`smilegate-ai/kor_unsmile`](https://huggingface.co/smilegate-ai/kor_unsmile) ONNX 한국어 혐오 발언 분류기
- [ ] **Step 3** — Ollama / Llama 3 심층 추론 레이어 (풍자·맥락 판단)
- [ ] 관리자 대시보드 — 격리 큐, 실시간 통계
- [ ] Docker / Kubernetes 배포 가이드

---

## 기여

이슈 및 PR 환영합니다. 새 필터를 추가할 경우 p99 지연 시간 벤치마크를 함께 제출해 주세요 — 파이프라인 목표는 **Step 2 < 200ms**, **Step 3 < 2s** (엔드투엔드) 입니다.

---

## 라이선스

MIT — [LICENSE](LICENSE) 참조.
