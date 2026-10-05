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
	go install $(addprefix ./cmd/,$(CMDS))