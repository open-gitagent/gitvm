#!/bin/bash
set -euo pipefail

# Build the gitvm-agent for Linux
echo "Building gitvm-agent..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o template/base/gitvm-agent ../../agent/cmd/main.go

# Build the Docker image
echo "Building Docker image..."
docker build -t gitvm-base template/base/

# Export rootfs
echo "Exporting rootfs..."
CONTAINER=$(docker create gitvm-base)
mkdir -p "$HOME/.gitvm/templates/base"
docker export "$CONTAINER" | dd of="$HOME/.gitvm/templates/base/rootfs.ext4" bs=1M
docker rm "$CONTAINER"

echo "Template 'base' built at ~/.gitvm/templates/base/rootfs.ext4"
