#!/bin/bash

# Distributed Task Queue - Setup Script
# This script helps set up the development environment

set -e

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}Distributed Task Queue - Setup${NC}"
echo -e "${YELLOW}========================================${NC}"
echo ""

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo -e "${RED}✗ Go is not installed. Please install Go 1.21 or later.${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Go is installed: $(go version)${NC}"

# Check if Docker is installed
if ! command -v docker &> /dev/null; then
    echo -e "${YELLOW}⚠ Docker is not installed. Docker Compose features will not work.${NC}"
else
    echo -e "${GREEN}✓ Docker is installed: $(docker --version)${NC}"
fi

# Step 1: Install dependencies
echo ""
echo -e "${YELLOW}Step 1: Installing dependencies...${NC}"
go mod download
go mod tidy
echo -e "${GREEN}✓ Dependencies installed${NC}"

# Step 2: Verify compilation
echo ""
echo -e "${YELLOW}Step 2: Verifying compilation...${NC}"
if go build ./cmd/api && go build ./cmd/worker; then
    echo -e "${GREEN}✓ Compilation successful${NC}"
    rm -f api worker
else
    echo -e "${RED}✗ Compilation failed${NC}"
    exit 1
fi

# Step 3: Create .env file if it doesn't exist
echo ""
echo -e "${YELLOW}Step 3: Setting up environment configuration...${NC}"
if [ ! -f .env ]; then
    if [ -f .env.example ]; then
        cp .env.example .env
        echo -e "${GREEN}✓ Created .env file from .env.example${NC}"
        echo -e "${YELLOW}⚠ Please edit .env and update JWT_SECRET and other sensitive values${NC}"
    else
        echo -e "${YELLOW}⚠ .env.example not found, creating basic .env${NC}"
        cat > .env << EOF
JWT_SECRET=$(openssl rand -hex 32)
REDIS_ADDR=localhost:6379
POSTGRES_DSN=postgres://taskqueue_user:password@localhost:5432/taskqueue?sslmode=disable
API_PORT=8080
STORAGE_DIR=./storage/images
BASE_URL=http://localhost:8080/images
EOF
        echo -e "${GREEN}✓ Created basic .env file${NC}"
    fi
else
    echo -e "${GREEN}✓ .env file already exists${NC}"
fi

# Step 4: Create storage directory
echo ""
echo -e "${YELLOW}Step 4: Creating storage directories...${NC}"
mkdir -p storage/images
chmod -R 755 storage/
echo -e "${GREEN}✓ Storage directories created${NC}"

# Step 5: Check Docker services
echo ""
echo -e "${YELLOW}Step 5: Checking Docker services...${NC}"
if command -v docker &> /dev/null && docker ps &> /dev/null; then
    echo -e "${GREEN}✓ Docker is running${NC}"
    
    # Check if services are already running
    if docker ps | grep -q "taskqueue-redis\|taskqueue-postgres"; then
        echo -e "${YELLOW}⚠ Some services are already running${NC}"
    else
        echo -e "${YELLOW}ℹ Services not running. Start them with: docker-compose up -d${NC}"
    fi
else
    echo -e "${YELLOW}⚠ Docker is not running or not accessible${NC}"
fi

# Summary
echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}Setup Complete!${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo "Next steps:"
echo "  1. Edit .env file and update JWT_SECRET"
echo "  2. Start services: docker-compose up -d"
echo "  3. Run API: make run-api"
echo "  4. Run Worker: make run-worker"
echo ""
echo "For detailed instructions, see README.md"

