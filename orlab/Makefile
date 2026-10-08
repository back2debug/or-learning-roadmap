GO ?= go

.PHONY: all build test vet staticcheck vuln sec race check insecure-grep

all: check build

build:
	$(GO) build ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

staticcheck:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

sec:
	$(GO) run github.com/securego/gosec/v2/cmd/gosec@latest ./...

# InsecureSkipVerify must never appear in this codebase.
insecure-grep:
	@! grep -rn "InsecureSkipVerify" --include='*.go' . || (echo "InsecureSkipVerify found" && exit 1)

check: vet race insecure-grep
