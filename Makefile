.PHONY: build install test fmt lint clean

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/ataca-io/wagon/internal/buildinfo.version=$(VERSION)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o wagon .

# Installs into GOBIN (`go env GOBIN`, else $(GOPATH)/bin).
install:
	CGO_ENABLED=0 go install -trimpath -ldflags="$(LDFLAGS)" .
	@echo "==> installed $$(go env GOPATH)/bin/wagon $(VERSION)"

test:
	go test ./... -race

fmt:
	go fmt ./...
	goimports -w .

lint:
	golangci-lint run ./...

clean:
	rm -f wagon wagon-*
