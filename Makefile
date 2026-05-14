# libtokenizers.a must be present in the project root.
# Download: make fetch-libs
# Then: make build  or  make run

GOFLAGS   := CGO_LDFLAGS="-L$(CURDIR) -ltokenizers -ldl -lstdc++"
ORT_LIB   ?= /opt/homebrew/lib/libonnxruntime.dylib

.PHONY: fetch-libs build run export-model

fetch-libs:
	@ARCH=$$(uname -m); \
	  case $$ARCH in \
	    arm64|aarch64) TARGET=darwin-aarch64 ;; \
	    x86_64)        TARGET=darwin-x86_64  ;; \
	    *) echo "Unsupported arch: $$ARCH"; exit 1 ;; \
	  esac; \
	  echo "Downloading libtokenizers for $$TARGET …"; \
	  curl -sL "https://github.com/daulet/tokenizers/releases/download/v1.27.0/libtokenizers.$$TARGET.tar.gz" | tar -xz -C . libtokenizers.a; \
	  echo "Done: libtokenizers.a"

export-model:
	pip install -q transformers torch onnx onnxruntime
	python scripts/export_onnx.py

build: libtokenizers.a
	$(GOFLAGS) ORT_LIB=$(ORT_LIB) go build -o bin/sendbyai ./cmd/server

run: build
	ORT_LIB=$(ORT_LIB) ./bin/sendbyai

test: libtokenizers.a
	$(GOFLAGS) go test ./...

libtokenizers.a:
	@echo "libtokenizers.a not found — run: make fetch-libs"; exit 1
