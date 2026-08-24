.PHONY: all build test coverage check pre-commit pre-commit-install codespell clean install

LDFLAGS = -X github.com/Eyevinn/hi265/internal.commitVersion=$$(git describe --tags HEAD 2>/dev/null || echo dev-$$(git rev-parse --short HEAD)) \
          -X github.com/Eyevinn/hi265/internal.commitDate=$$(git log -1 --format=%ct)

all: check build test

build: out/hi265dec out/hi265gen out/hi265gray out/hi265retile

out/hi265dec: $(shell find pkg cmd/hi265dec internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265dec

out/hi265gen: $(shell find pkg cmd/hi265gen internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265gen

out/hi265gray: $(shell find pkg cmd/hi265gray internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265gray

out/hi265retile: $(shell find pkg cmd/hi265retile internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265retile

test:
	go test ./...

coverage:
	go test -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out -o coverage.txt
	@echo "Coverage report: coverage.html"

check:
	golangci-lint run

pre-commit-install: venv/bin/pre-commit
	venv/bin/pre-commit install

pre-commit: venv/bin/pre-commit
	venv/bin/pre-commit run --all-files

venv/bin/pre-commit venv/bin/codespell:
	python3 -m venv venv
	venv/bin/pip install pre-commit codespell

codespell: venv/bin/codespell
	venv/bin/codespell -S venv,vendor,testdata,coverage.html,'*.y4m','*.265','*.hevc','*.mp4' -L pich,localy,ue

clean:
	rm -rf out/ coverage.out coverage.html coverage.txt venv/

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265dec
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265gen
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265gray
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265retile