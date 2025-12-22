#!/bin/bash

# 构建脚本
set -e

echo "Building Single Proxy..."

# 清理旧的构建文件
rm -f singleproxy singleproxy-*

# 构建不同平台的二进制文件
echo "Building for Linux AMD64..."
GOOS=linux GOARCH=amd64 go build -o singleproxy-linux-amd64 ./cmd/singleproxy

echo "Building for Linux ARM64..."
GOOS=linux GOARCH=arm64 go build -o singleproxy-linux-arm64 ./cmd/singleproxy

echo "Building for Windows AMD64..."
GOOS=windows GOARCH=amd64 go build -o singleproxy-windows-amd64.exe ./cmd/singleproxy

echo "Building for macOS AMD64..."
GOOS=darwin GOARCH=amd64 go build -o singleproxy-darwin-amd64 ./cmd/singleproxy

echo "Building for macOS ARM64..."
GOOS=darwin GOARCH=arm64 go build -o singleproxy-darwin-arm64 ./cmd/singleproxy

# 构建本地版本
echo "Building for current platform..."
go build -o singleproxy ./cmd/singleproxy

echo "Build completed successfully!"
echo "Binaries created:"
ls -la singleproxy*