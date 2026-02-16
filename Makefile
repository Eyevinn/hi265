.PHONY: all build test coverage check pre-commit-install pre-commit codespell clean install

LDFLAGS = -X github.com/Eyevinn/hi265/internal.commitVersion=$$(git describe --tags HEAD 2>/dev/null || echo dev-$$(git rev-parse --short HEAD)) \
          -X github.com/Eyevinn/hi265/internal.commitDate=$$(git log -1 --format=%ct)

all: check build test

build: out/hi265dec out/hi265gen

out/hi265dec: $(shell find pkg cmd/hi265dec internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265dec

out/hi265gen: $(shell find pkg cmd/hi265gen internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265gen

test:
	go test ./...

coverage:
	go test -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out -o coverage.txt
	@echo "Coverage report: coverage.html"

check:
	golangci-lint run

venv:
	python3 -m venv venv
	venv/bin/pip install --upgrade pip
	venv/bin/pip install pre-commit
	venv/bin/pip install codespell

pre-commit-install: venv
	venv/bin/pre-commit install

pre-commit: venv
	venv/bin/pre-commit run --all-files

codespell: venv
	venv/bin/codespell -S venv,testdata,references -L pich,localy,ue

clean:
	rm -rf out/ coverage.out coverage.html coverage.txt venv/

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265dec
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265gen
