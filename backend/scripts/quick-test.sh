#!/bin/bash

# Quick Test Script - Tests all endpoints and functionality
# Prerequisites: API server running on localhost:8080

set -e

BASE_URL="http://localhost:8080/api/v1"
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${YELLOW}========================================${NC}"
echo -e "${YELLOW}Quick System Test${NC}"
echo -e "${YELLOW}========================================${NC}"
echo ""

# Test 1: Health Check
echo -e "${YELLOW}Test 1: Health Check${NC}"
HEALTH=$(curl -s http://localhost:8080/api/v1/health)
if echo "$HEALTH" | grep -q "healthy"; then
    echo -e "${GREEN}✓ Health check passed${NC}"
else
    echo -e "${RED}✗ Health check failed${NC}"
    echo "$HEALTH"
    exit 1
fi
echo ""

# Test 2: Register User
echo -e "${YELLOW}Test 2: User Registration${NC}"
TEST_USER="testuser_$(date +%s)"
REGISTER=$(curl -s -w "\n%{http_code}" -X POST "${BASE_URL}/auth/register" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${TEST_USER}\",\"email\":\"${TEST_USER}@test.com\",\"password\":\"testpass123\"}")

HTTP_CODE=$(echo "$REGISTER" | tail -n1)
if [ "$HTTP_CODE" -eq 201 ]; then
    echo -e "${GREEN}✓ Registration successful${NC}"
    TOKEN=$(echo "$REGISTER" | sed '$d' | grep -o '"token":"[^"]*' | cut -d'"' -f4 || echo "")
else
    echo -e "${RED}✗ Registration failed (HTTP $HTTP_CODE)${NC}"
    echo "$REGISTER"
fi
echo ""

# Test 3: Login
echo -e "${YELLOW}Test 3: User Login${NC}"
LOGIN=$(curl -s -w "\n%{http_code}" -X POST "${BASE_URL}/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${TEST_USER}\",\"password\":\"testpass123\"}")

HTTP_CODE=$(echo "$LOGIN" | tail -n1)
if [ "$HTTP_CODE" -eq 200 ]; then
    echo -e "${GREEN}✓ Login successful${NC}"
    TOKEN=$(echo "$LOGIN" | sed '$d' | grep -o '"token":"[^"]*' | cut -d'"' -f4 || echo "")
else
    echo -e "${RED}✗ Login failed (HTTP $HTTP_CODE)${NC}"
    echo "$LOGIN"
    exit 1
fi
echo ""

# Test 4: Get Profile
if [ -n "$TOKEN" ]; then
    echo -e "${YELLOW}Test 4: Get User Profile${NC}"
    PROFILE=$(curl -s -w "\n%{http_code}" -X GET "${BASE_URL}/users/me" \
      -H "Authorization: Bearer ${TOKEN}")
    
    HTTP_CODE=$(echo "$PROFILE" | tail -n1)
    if [ "$HTTP_CODE" -eq 200 ]; then
        echo -e "${GREEN}✓ Profile retrieval successful${NC}"
    else
        echo -e "${RED}✗ Profile retrieval failed (HTTP $HTTP_CODE)${NC}"
    fi
    echo ""
fi

# Test 5: Submit Task
if [ -n "$TOKEN" ]; then
    echo -e "${YELLOW}Test 5: Submit Image Task${NC}"
    TASK=$(curl -s -w "\n%{http_code}" -X POST "${BASE_URL}/tasks" \
      -H "Authorization: Bearer ${TOKEN}" \
      -H "Content-Type: application/json" \
      -d '{
        "type": "image_resize",
        "payload": {
          "source_url": "https://picsum.photos/800/600",
          "width": 400,
          "height": 300,
          "maintain_aspect": true
        }
      }')
    
    HTTP_CODE=$(echo "$TASK" | tail -n1)
    if [ "$HTTP_CODE" -eq 201 ]; then
        echo -e "${GREEN}✓ Task submitted successfully${NC}"
        TASK_ID=$(echo "$TASK" | sed '$d' | grep -o '"task_id":"[^"]*' | cut -d'"' -f4 || echo "")
        echo "Task ID: $TASK_ID"
    else
        echo -e "${RED}✗ Task submission failed (HTTP $HTTP_CODE)${NC}"
        echo "$TASK"
    fi
    echo ""
fi

# Test 6: Check Metrics
echo -e "${YELLOW}Test 6: Metrics Endpoint${NC}"
METRICS=$(curl -s http://localhost:8080/metrics)
if echo "$METRICS" | grep -q "taskqueue"; then
    echo -e "${GREEN}✓ Metrics endpoint working${NC}"
else
    echo -e "${YELLOW}⚠ Metrics endpoint may not be fully configured${NC}"
fi
echo ""

echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN}All Tests Complete!${NC}"
echo -e "${GREEN}========================================${NC}"

