.PHONY: all build test coverage check pre-commit pre-commit-install codespell clean install

CMDS = hi265dec hi265gen hi265gray hi265retile hi265-mp4-extend hi265inspect
BINARIES = $(addprefix out/,$(CMDS))

all: check build test

build: $(BINARIES)

# Binaries are built as packages, not as main.go files, so that they carry the
# version Go embeds from the git tag and commit (see internal/buildinfo.go).
# They are .PHONY because that version is not a file prerequisite: a binary
# built before a commit or a tag would be kept, still naming the old one. The
# build cache makes the rebuild cheap.
.PHONY: $(BINARIES)
$(BINARIES): out/%:
	go build -o $@ ./cmd/$*

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

# Keep equal to the codespell rev in .pre-commit-config.yaml.
CODESPELL_VERSION = 2.4.3

venv/bin/pre-commit venv/bin/codespell:
	python3 -m venv venv
	venv/bin/pip install pre-commit codespell==$(CODESPELL_VERSION)

# Skips and ignored words are in .codespellrc, which the pre-commit hook reads too.
codespell: venv/bin/codespell
	venv/bin/codespell

clean:
	rm -rf out/ coverage.out coverage.html coverage.txt venv/

install:
	go install $(addprefix ./cmd/,$(CMDS))