.PHONY: build build-controlplane build-node build-agent build-cli test clean

build: build-controlplane build-node build-agent build-cli

# Control plane — runs centrally, manages nodes & sandboxes
build-controlplane:
	go build -o bin/gitvmd ./cmd/gitvmd

# Node agent — runs on each cloud VM, manages local Firecracker sandboxes
build-node:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/gitvm-node ./cmd/gitvm-node

# Guest agent — runs inside each Firecracker VM
build-agent:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/gitvm-agent ./cmd/gitvm

# CLI for local usage
build-cli:
	go build -o bin/gitvm ./cmd/gitvm

test:
	go test -race -v ./...

clean:
	rm -rf bin/
