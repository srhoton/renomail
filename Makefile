.PHONY: build test fmt vet lint cover run tidy

build:
	go build ./...

test:
	go test ./... -race -cover

cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

fmt:
	gofmt -l -w .

vet:
	go vet ./...

# Runs golangci-lint if it is installed; otherwise a no-op with a hint.
lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed; skipping"

run:
	go run ./cmd/renomail

tidy:
	go mod tidy
