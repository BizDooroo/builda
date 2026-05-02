.PHONY: fmt lint test build check

fmt:
	gofmt -w main.go cli.go service.go main_test.go

lint:
	git diff --check
	gitleaks detect --source . --no-banner --redact --verbose

test:
	go test ./...

build:
	go build -o builda .

check: fmt lint test build
