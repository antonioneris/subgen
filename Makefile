.PHONY: build install test release-check

build:
	mkdir -p bin
	go build -o bin/subgen ./cmd/subgen

install:
	./install.sh

test:
	go test ./...
	go test -race ./...
	go vet ./...

release-check: test
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /tmp/subgen-darwin-amd64 ./cmd/subgen
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /tmp/subgen-darwin-arm64 ./cmd/subgen
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/subgen-linux-amd64 ./cmd/subgen
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/subgen-linux-arm64 ./cmd/subgen
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /tmp/subgen-windows-amd64.exe ./cmd/subgen
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -o /tmp/subgen-windows-arm64.exe ./cmd/subgen
