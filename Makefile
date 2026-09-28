# Makefile for building and testing `boring`
# Usage:
#   make / make build    - Build the binary
#   make build-grid      - Cross-compile for all OS/ARCH
#   make test            - Run tests
#   make cover           - Run tests with coverage
#   make cover-html      - Generate interactive HTML report
#   make test-race       - Run tests with the race detector, incl. the daemon
#   make lint            - Run the same static checks as CI
#   make tools           - Install the tools used by 'make lint'
#   make sandbox-up      - Start SSH containers for manual testing
#   make sandbox-down    - Stop them again

TAG := $(shell git describe --tags --exact-match 2>/dev/null)
VERSION := $(TAG:v%=%)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null)
MOD := $(shell go list -m)
LDFLAGS := -s -w -X $(MOD)/internal/buildinfo.Version=$(VERSION) \
	-X $(MOD)/internal/buildinfo.Commit=$(COMMIT)

DIST_DIR := ./dist
COVER_DIR := $(CURDIR)/cover
COVER_LINES := cover_lines.out
TEST_BINARY := boring.test
RACE_DIR := $(CURDIR)/race

.PHONY: test cover test-race lint tools sandbox-up sandbox-down

default: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/boring ./cmd/boring

build-grid:
	mkdir -p $(DIST_DIR)
	$(foreach os,darwin linux, \
		$(foreach arch,arm64 amd64, \
			echo "Building for $(os)/$(arch)"; \
			GOOS=$(os) GOARCH=$(arch) $(MAKE) build; \
			tar -czf $(DIST_DIR)/boring-$(TAG)-$(os)-$(arch).tar.gz LICENSE -C $(DIST_DIR) boring; \
			rm -f $(DIST_DIR)/boring; \
		) \
	)

build-test:
	go build -o $(TEST_BINARY) ./cmd/boring

test: build-test
	go test ./... 2>&1 | grep -v '\[no test files\]'

# The e2e tests run the daemon as a separate process, so its race reports
# would be lost on its stderr. GORACE log_path writes them to files instead,
# and the target fails if any were written.
test-race:
	go build -race -o $(TEST_BINARY) ./cmd/boring
	rm -rf $(RACE_DIR) && mkdir -p $(RACE_DIR)
	GORACE="log_path=$(RACE_DIR)/race" go test -race -count=1 ./... 2>&1 | grep -v '\[no test files\]'
	@if ls $(RACE_DIR)/race.* >/dev/null 2>&1; then \
		echo "Data races reported, see $(RACE_DIR):"; \
		grep -h -A6 "WARNING: DATA RACE" $(RACE_DIR)/race.* | head -40; \
		exit 1; \
	fi

tools:
	go install github.com/gordonklaus/ineffassign@latest
	go install github.com/client9/misspell/cmd/misspell@latest
	go install github.com/fzipp/gocyclo/cmd/gocyclo@latest

lint:
	test -z "$$(gofmt -l .)" || (gofmt -l . && echo "Code is not gofmt'ed" && exit 1)
	go vet ./...
	gocyclo -over 15 .
	ineffassign ./...
	misspell -error ./...

sandbox-up:
	./test/sandbox/sandbox.sh up

sandbox-down:
	./test/sandbox/sandbox.sh down

build-cover:
	go build -cover -coverpkg=./... -o $(TEST_BINARY) ./cmd/boring

cover: build-cover
	@# e2e tests
	rm -rf $(COVER_DIR) && mkdir -p $(COVER_DIR)
	GOCOVERDIR=$(COVER_DIR) go test  ./test/e2e
	go tool covdata textfmt -i=$(COVER_DIR) -o $(COVER_LINES)
	@# unit tests
	tmpfile=$(mktemp)
	go test -coverprofile=tmpfile ./cmd/... ./internal/... > /dev/null
	@# combine
	tail -n +2 tmpfile >> $(COVER_LINES)
	rm -f tmpfile
	@# output total coverage
	go tool cover -func=$(COVER_LINES) | grep total

cover-html: cover
	go tool cover -html=$(COVER_LINES)

clean:
	rm -rf $(DIST_DIR) $(TEST_BINARY) $(COVER_DIR) $(COVER_LINES) $(RACE_DIR)
