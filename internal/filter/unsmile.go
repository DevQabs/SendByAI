package filter

// UnsmileFilter runs smilegate-ai/kor_unsmile (ONNX) for Korean hate-speech detection.
//
// Multi-label sigmoid output — 10 classes.  Score = 1 - P("클린").
// ActionBlock when score ≥ blockThreshold; ActionQuarantine when ≥ quarantineThreshold.
//
// Required files (set via options or env):
//   UNSMILE_ONNX_PATH      path to models/kor_unsmile.onnx
//   UNSMILE_TOKENIZER_PATH path to models/tokenizer/tokenizer.json

import (
	"context"
	"fmt"
	"math"
	"os"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"
)

const (
	maxLen             = 128
	cleanLabelIndex    = 9   // "클린" is last label
	defaultBlock       = 0.7 // P(hate) ≥ 0.7 → block
	defaultQuarantine  = 0.4 // P(hate) ≥ 0.4 → quarantine
)

var unsmileLabels = [10]string{
	"여성/가족", "남성", "성소수자", "인종/국적",
	"연령", "지역", "종교", "기타 혐오", "악플/욕설", "클린",
}

// UnsmileFilter is a Filter that detects Korean hate speech via ONNX inference.
type UnsmileFilter struct {
	session            *ort.DynamicAdvancedSession
	tokenizer          *tokenizers.Tokenizer
	blockThreshold     float64
	quarantineThreshold float64
}

// UnsmileOption configures UnsmileFilter.
type UnsmileOption func(*unsmileConfig)

type unsmileConfig struct {
	onnxPath      string
	tokenizerPath string
	block         float64
	quarantine    float64
}

func WithONNXPath(p string) UnsmileOption      { return func(c *unsmileConfig) { c.onnxPath = p } }
func WithTokenizerPath(p string) UnsmileOption { return func(c *unsmileConfig) { c.tokenizerPath = p } }
func WithBlockThreshold(t float64) UnsmileOption { return func(c *unsmileConfig) { c.block = t } }
func WithQuarantineThreshold(t float64) UnsmileOption {
	return func(c *unsmileConfig) { c.quarantine = t }
}

// NewUnsmile creates and initialises the filter.  Must be called once; Close() when done.
func NewUnsmile(opts ...UnsmileOption) (*UnsmileFilter, error) {
	cfg := &unsmileConfig{
		onnxPath:      filterEnvOr("UNSMILE_ONNX_PATH", "models/kor_unsmile.onnx"),
		tokenizerPath: filterEnvOr("UNSMILE_TOKENIZER_PATH", "models/tokenizer/tokenizer.json"),
		block:         defaultBlock,
		quarantine:    defaultQuarantine,
	}
	for _, o := range opts {
		o(cfg)
	}

	if _, err := os.Stat(cfg.onnxPath); err != nil {
		return nil, fmt.Errorf("unsmile: onnx model not found at %q — run scripts/export_onnx.py first", cfg.onnxPath)
	}
	if _, err := os.Stat(cfg.tokenizerPath); err != nil {
		return nil, fmt.Errorf("unsmile: tokenizer not found at %q — run scripts/export_onnx.py first", cfg.tokenizerPath)
	}

	ort.SetSharedLibraryPath(onnxRuntimeLibPath())
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("unsmile: ort init: %w", err)
	}

	inputNames  := []string{"input_ids", "attention_mask", "token_type_ids"}
	outputNames := []string{"logits"}
	sess, err := ort.NewDynamicAdvancedSession(cfg.onnxPath, inputNames, outputNames, nil)
	if err != nil {
		return nil, fmt.Errorf("unsmile: ort session: %w", err)
	}

	tok, err := tokenizers.FromFile(cfg.tokenizerPath)
	if err != nil {
		_ = sess.Destroy()
		return nil, fmt.Errorf("unsmile: tokenizer: %w", err)
	}

	return &UnsmileFilter{
		session:             sess,
		tokenizer:           tok,
		blockThreshold:      cfg.block,
		quarantineThreshold: cfg.quarantine,
	}, nil
}

// Name implements Filter.
func (f *UnsmileFilter) Name() string { return "kor-unsmile" }

// Filter implements Filter.
func (f *UnsmileFilter) Filter(_ context.Context, msg *Message) (*Result, error) {
	score, labels, err := f.infer(msg.Content)
	if err != nil {
		return nil, fmt.Errorf("unsmile inference: %w", err)
	}

	reason := fmt.Sprintf("hate=%.3f labels=%v", score, labels)

	switch {
	case score >= f.blockThreshold:
		return &Result{Action: ActionBlock, Reason: reason, Score: score}, nil
	case score >= f.quarantineThreshold:
		return &Result{Action: ActionQuarantine, Message: msg, Reason: reason, Score: score}, nil
	default:
		return &Result{Action: ActionAllow, Message: msg, Score: score}, nil
	}
}

// Close releases ONNX session and tokenizer resources.
func (f *UnsmileFilter) Close() error {
	f.tokenizer.Close()
	return f.session.Destroy()
}

// infer returns hate score (0–1) and triggered label names.
func (f *UnsmileFilter) infer(text string) (float64, []string, error) {
	enc, err := f.tokenize(text)
	if err != nil {
		return 0, nil, err
	}

	// Build input tensors  [1, maxLen]
	shape := ort.NewShape(1, int64(maxLen))

	inputIDs, err := ort.NewTensor(shape, enc.inputIDs)
	if err != nil {
		return 0, nil, err
	}
	defer inputIDs.Destroy()

	attnMask, err := ort.NewTensor(shape, enc.attentionMask)
	if err != nil {
		return 0, nil, err
	}
	defer attnMask.Destroy()

	tokenTypeIDs, err := ort.NewTensor(shape, enc.tokenTypeIDs)
	if err != nil {
		return 0, nil, err
	}
	defer tokenTypeIDs.Destroy()

	// Output tensor [1, 10]
	logitsTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 10))
	if err != nil {
		return 0, nil, err
	}
	defer logitsTensor.Destroy()

	err = f.session.Run(
		[]ort.ArbitraryTensor{inputIDs, attnMask, tokenTypeIDs},
		[]ort.ArbitraryTensor{logitsTensor},
	)
	if err != nil {
		return 0, nil, err
	}

	logits := logitsTensor.GetData()
	probs  := make([]float64, 10)
	for i, l := range logits {
		probs[i] = sigmoid(float64(l))
	}

	// Hate score = 1 - P(clean)
	hateScore := 1.0 - probs[cleanLabelIndex]

	var triggered []string
	for i, p := range probs[:cleanLabelIndex] { // exclude clean label
		if p >= 0.5 {
			triggered = append(triggered, unsmileLabels[i])
		}
	}

	return hateScore, triggered, nil
}

type tokenEncoding struct {
	inputIDs      []int64
	attentionMask []int64
	tokenTypeIDs  []int64
}

func (f *UnsmileFilter) tokenize(text string) (*tokenEncoding, error) {
	enc := f.tokenizer.EncodeWithOptions(text,
		true, // add special tokens ([CLS], [SEP])
		tokenizers.WithReturnAllAttributes(),
	)

	ids   := enc.IDs
	masks := enc.AttentionMask
	types := enc.TypeIDs

	out := &tokenEncoding{
		inputIDs:      make([]int64, maxLen),
		attentionMask: make([]int64, maxLen),
		tokenTypeIDs:  make([]int64, maxLen),
	}

	n := len(ids)
	if n > maxLen {
		n = maxLen
	}
	for i := 0; i < n; i++ {
		out.inputIDs[i]      = int64(ids[i])
		out.attentionMask[i] = int64(masks[i])
		out.tokenTypeIDs[i]  = int64(types[i])
	}
	return out, nil
}

func sigmoid(x float64) float64 { return 1.0 / (1.0 + math.Exp(-x)) }

// onnxRuntimeLibPath returns platform-default shared library path.
// Override with ORT_LIB env var for non-standard installs.
func onnxRuntimeLibPath() string {
	if p := os.Getenv("ORT_LIB"); p != "" {
		return p
	}
	// macOS homebrew default; Linux: /usr/lib/libonnxruntime.so
	return "/opt/homebrew/lib/libonnxruntime.dylib"
}

func filterEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
