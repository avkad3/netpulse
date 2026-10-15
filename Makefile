VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: run build test vet docker
run:
	go run ./cmd/netpulse -config config.example.json

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/netpulse ./cmd/netpulse

test:
	go test -race ./...

vet:
	go vet ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t netpulse:$(VERSION) .
