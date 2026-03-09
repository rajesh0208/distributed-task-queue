#!/bin/bash

# Manual Authentication Flow Test Script
# This script tests the authentication endpoints manually
# Prerequisites: API server running on localhost:8080

BASE_URL="http://localhost:8080/api/v1"
TEST_USERNAME="testuser_$(date +%s)"
TEST_EMAIL="${TEST_USERNAME}@example.com"
TEST_PASSWORD="testpassword123"

echo "========================================="
echo "Authentication Flow Test"
echo "========================================="
echo ""

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test 1: User Registration
echo -e "${YELLOW}Test 1: User Registration${NC}"
REGISTER_RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "${BASE_URL}/auth/register" \
  -H "Content-Type: application/json" \
  -d "{
    \"username\": \"${TEST_USERNAME}\",
    \"email\": \"${TEST_EMAIL}\",
    \"password\": \"${TEST_PASSWORD}\"
  }")

HTTP_CODE=$(echo "$REGISTER_RESPONSE" | tail -n1)
BODY=$(echo "$REGISTER_RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -eq 201 ]; then
  echo -e "${GREEN}✓ Registration successful${NC}"
  echo "Response: $BODY"
  TOKEN=$(echo "$BODY" | grep -o '"token":"[^"]*' | cut -d'"' -f4)
  API_KEY=$(echo "$BODY" | grep -o '"api_key":"[^"]*' | cut -d'"' -f4)
  USER_ID=$(echo "$BODY" | grep -o '"id":"[^"]*' | cut -d'"' -f4)
else
  echo -e "${RED}✗ Registration failed (HTTP $HTTP_CODE)${NC}"
  echo "Response: $BODY"
  exit 1
fi

echo ""

# Test 2: User Login
echo -e "${YELLOW}Test 2: User Login${NC}"
LOGIN_RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "${BASE_URL}/auth/login" \
  -H "Content-Type: application/json" \
  -d "{
    \"username\": \"${TEST_USERNAME}\",
    \"password\": \"${TEST_PASSWORD}\"
  }")

HTTP_CODE=$(echo "$LOGIN_RESPONSE" | tail -n1)
BODY=$(echo "$LOGIN_RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -eq 200 ]; then
  echo -e "${GREEN}✓ Login successful${NC}"
  echo "Response: $BODY"
  TOKEN=$(echo "$BODY" | grep -o '"token":"[^"]*' | cut -d'"' -f4)
else
  echo -e "${RED}✗ Login failed (HTTP $HTTP_CODE)${NC}"
  echo "Response: $BODY"
  exit 1
fi

echo ""

# Test 3: Get Current User (Protected Endpoint)
if [ -n "$TOKEN" ]; then
  echo -e "${YELLOW}Test 3: Get Current User Profile${NC}"
  PROFILE_RESPONSE=$(curl -s -w "\n%{http_code}" -X GET "${BASE_URL}/users/me" \
    -H "Authorization: Bearer ${TOKEN}")

  HTTP_CODE=$(echo "$PROFILE_RESPONSE" | tail -n1)
  BODY=$(echo "$PROFILE_RESPONSE" | sed '$d')

  if [ "$HTTP_CODE" -eq 200 ]; then
    echo -e "${GREEN}✓ Profile retrieval successful${NC}"
    echo "Response: $BODY"
  else
    echo -e "${RED}✗ Profile retrieval failed (HTTP $HTTP_CODE)${NC}"
    echo "Response: $BODY"
  fi
  echo ""
fi

# Test 4: Update Current User
if [ -n "$TOKEN" ]; then
  echo -e "${YELLOW}Test 4: Update Current User Profile${NC}"
  UPDATE_RESPONSE=$(curl -s -w "\n%{http_code}" -X PUT "${BASE_URL}/users/me" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{
      \"email\": \"updated_${TEST_EMAIL}\"
    }")

  HTTP_CODE=$(echo "$UPDATE_RESPONSE" | tail -n1)
  BODY=$(echo "$UPDATE_RESPONSE" | sed '$d')

  if [ "$HTTP_CODE" -eq 200 ]; then
    echo -e "${GREEN}✓ Profile update successful${NC}"
    echo "Response: $BODY"
  else
    echo -e "${RED}✗ Profile update failed (HTTP $HTTP_CODE)${NC}"
    echo "Response: $BODY"
  fi
  echo ""
fi

# Test 5: Invalid Token
echo -e "${YELLOW}Test 5: Invalid Token Test${NC}"
INVALID_RESPONSE=$(curl -s -w "\n%{http_code}" -X GET "${BASE_URL}/users/me" \
  -H "Authorization: Bearer invalid_token_12345")

HTTP_CODE=$(echo "$INVALID_RESPONSE" | tail -n1)
BODY=$(echo "$INVALID_RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -eq 401 ]; then
  echo -e "${GREEN}✓ Invalid token correctly rejected${NC}"
else
  echo -e "${RED}✗ Invalid token not rejected (HTTP $HTTP_CODE)${NC}"
  echo "Response: $BODY"
fi
echo ""

# Test 6: Missing Token
echo -e "${YELLOW}Test 6: Missing Token Test${NC}"
MISSING_RESPONSE=$(curl -s -w "\n%{http_code}" -X GET "${BASE_URL}/users/me")

HTTP_CODE=$(echo "$MISSING_RESPONSE" | tail -n1)
BODY=$(echo "$MISSING_RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -eq 401 ]; then
  echo -e "${GREEN}✓ Missing token correctly rejected${NC}"
else
  echo -e "${RED}✗ Missing token not rejected (HTTP $HTTP_CODE)${NC}"
  echo "Response: $BODY"
fi
echo ""

# Test 7: Duplicate Registration
echo -e "${YELLOW}Test 7: Duplicate Registration Test${NC}"
DUPLICATE_RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "${BASE_URL}/auth/register" \
  -H "Content-Type: application/json" \
  -d "{
    \"username\": \"${TEST_USERNAME}\",
    \"email\": \"${TEST_EMAIL}\",
    \"password\": \"${TEST_PASSWORD}\"
  }")

HTTP_CODE=$(echo "$DUPLICATE_RESPONSE" | tail -n1)
BODY=$(echo "$DUPLICATE_RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -eq 409 ]; then
  echo -e "${GREEN}✓ Duplicate registration correctly rejected${NC}"
else
  echo -e "${RED}✗ Duplicate registration not rejected (HTTP $HTTP_CODE)${NC}"
  echo "Response: $BODY"
fi
echo ""

# Test 8: Wrong Password Login
echo -e "${YELLOW}Test 8: Wrong Password Login Test${NC}"
WRONG_PASS_RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "${BASE_URL}/auth/login" \
  -H "Content-Type: application/json" \
  -d "{
    \"username\": \"${TEST_USERNAME}\",
    \"password\": \"wrongpassword\"
  }")

HTTP_CODE=$(echo "$WRONG_PASS_RESPONSE" | tail -n1)
BODY=$(echo "$WRONG_PASS_RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -eq 401 ]; then
  echo -e "${GREEN}✓ Wrong password correctly rejected${NC}"
else
  echo -e "${RED}✗ Wrong password not rejected (HTTP $HTTP_CODE)${NC}"
  echo "Response: $BODY"
fi
echo ""

echo "========================================="
echo -e "${GREEN}Authentication Flow Test Complete${NC}"
echo "========================================="
echo ""
echo "Test User Created:"
echo "  Username: ${TEST_USERNAME}"
echo "  Email: ${TEST_EMAIL}"
echo "  User ID: ${USER_ID}"
echo ""
echo "Note: Clean up test user manually if needed"

