.PHONY: fmt lint test build check

fmt:
	gofmt -w *.go

lint:
	git diff --check
	gitleaks detect --source . --no-banner --redact --verbose

test:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build
	go test ./...

build:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build
	go build -o builda .

check: fmt lint test build
