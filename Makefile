.PHONY: all build test coverage check clean install

LDFLAGS = -X github.com/Eyevinn/hi265/internal.commitVersion=$$(git describe --tags HEAD 2>/dev/null || echo dev-$$(git rev-parse --short HEAD)) \
          -X github.com/Eyevinn/hi265/internal.commitDate=$$(git log -1 --format=%ct)

all: check build test

build: out/hi265dec

out/hi265dec: $(shell find pkg cmd/hi265dec internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265dec

test:
	go test ./...

coverage:
	go test -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out -o coverage.txt
	@echo "Coverage report: coverage.html"

check:
	golangci-lint run

clean:
	rm -rf out/ coverage.out coverage.html coverage.txt

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265dec
