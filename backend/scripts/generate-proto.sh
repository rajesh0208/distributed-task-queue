#!/bin/bash
# Generate protobuf and gRPC code from .proto files

set -e

PROTO_DIR="proto"
OUT_DIR="proto"

echo "Generating protobuf code..."

# Check if protoc is installed
if ! command -v protoc &> /dev/null; then
    echo "Error: protoc is not installed"
    echo "Install it from: https://grpc.io/docs/protoc-installation/"
    exit 1
fi

# Generate Go code
protoc --go_out=$OUT_DIR \
       --go_opt=paths=source_relative \
       --go-grpc_out=$OUT_DIR \
       --go-grpc_opt=paths=source_relative \
       $PROTO_DIR/*.proto

echo "✓ Protobuf code generated successfully"

