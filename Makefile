VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: run build test lint docker

run:
	go run ./cmd/mailcore

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/mailcore ./cmd/mailcore

test:
	go test -race -count=1 ./...

lint:
	gofmt -l . | (! grep .) && go vet ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t mailcore:$(VERSION) .
