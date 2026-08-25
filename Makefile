.PHONY: all build test coverage check pre-commit pre-commit-install codespell clean install

LDFLAGS = -X github.com/Eyevinn/hi265/internal.commitVersion=$$(git describe --tags HEAD 2>/dev/null || echo dev-$$(git rev-parse --short HEAD)) \
          -X github.com/Eyevinn/hi265/internal.commitDate=$$(git log -1 --format=%ct)

all: check build test

build: out/hi265dec out/hi265gen out/hi265gray out/hi265retile \
       out/hi265-mp4-extend out/hi265inspect

out/hi265dec: $(shell find pkg cmd/hi265dec internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265dec

out/hi265gen: $(shell find pkg cmd/hi265gen internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265gen

out/hi265gray: $(shell find pkg cmd/hi265gray internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265gray

out/hi265retile: $(shell find pkg cmd/hi265retile internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265retile

out/hi265-mp4-extend: $(shell find pkg cmd/hi265-mp4-extend internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265-mp4-extend

out/hi265inspect: $(shell find pkg cmd/hi265inspect internal -name '*.go')
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/hi265inspect

# The Python venv this Makefile creates lives inside the module, and pre-commit
# ships an empty Go template in its resources — so './...' matches a package
# under venv/ once 'make pre-commit' has run. It is an empty main(), so it does
# not move the coverage percentage, but it appears as a phantom 0.0% package in
# every report and would break the build outright if that template ever stopped
# compiling. CI never sees it (venv/ is gitignored), so filter it here rather
# than in the workflow.
GOPKGS = go list ./... | grep -v '/venv/'

test:
	go test $$($(GOPKGS))

coverage:
	pkgs=$$($(GOPKGS)); \
	go test -coverpkg="$$(echo $$pkgs | tr ' ' ',')" -coverprofile=coverage.out $$pkgs
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

# Paths that are data rather than prose. cmd/hi265gray/data is a gitignored
# local scratch area of hex parameter sets and captures; it is not in the repo,
# but skipping it keeps this target green where it does exist.
CODESPELL_SKIP = venv,vendor,testdata,./cmd/hi265gray/data,coverage.html,*.y4m,*.yuv,*.265,*.hevc,*.mp4

# Words to leave alone, all of them domain vocabulary or deliberate:
#   trun, truns  the MP4 track run box, and mp4ff's Truns field holding them
#   nd           the %Nd frame-number format spec documented in pkg/timecode
#   unparseable  a variant spelling, used in a comment about a missing element
CODESPELL_IGNORE = pich,localy,ue,trun,truns,nd,unparseable

codespell: venv/bin/codespell
	venv/bin/codespell -S '$(CODESPELL_SKIP)' -L '$(CODESPELL_IGNORE)'

clean:
	rm -rf out/ coverage.out coverage.html coverage.txt venv/

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265dec
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265gen
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265gray
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265retile
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265-mp4-extend
	go install -ldflags "$(LDFLAGS)" ./cmd/hi265inspect