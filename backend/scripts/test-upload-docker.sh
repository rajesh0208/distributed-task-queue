#!/bin/bash

# Test Image Upload with Docker
set -e

API_URL="http://localhost:8080"
TEST_USER="testuser"
TEST_PASS="testpass123"

echo "=== Docker Image Upload Test ==="
echo ""

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Step 1: Check API health
echo "1. Checking API server..."
if curl -s "$API_URL/api/v1/health" > /dev/null 2>&1; then
    echo -e "  ${GREEN}✓${NC} API server is running"
else
    echo -e "  ${RED}✗${NC} API server is not running"
    echo "  Start with: docker-compose up -d"
    exit 1
fi

# Step 2: Register/Login
echo ""
echo "2. Authenticating..."
REGISTER_RESPONSE=$(curl -s -X POST "$API_URL/api/v1/auth/register" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${TEST_USER}\",\"email\":\"${TEST_USER}@test.com\",\"password\":\"${TEST_PASS}\"}" 2>&1)

LOGIN_RESPONSE=$(curl -s -X POST "$API_URL/api/v1/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${TEST_USER}\",\"password\":\"${TEST_PASS}\"}")

TOKEN=$(echo "$LOGIN_RESPONSE" | python3 -c "import sys, json; print(json.load(sys.stdin).get('token', ''))" 2>/dev/null || echo "")

if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ]; then
    echo -e "  ${RED}✗${NC} Authentication failed"
    echo "  Response: $LOGIN_RESPONSE"
    exit 1
fi

echo -e "  ${GREEN}✓${NC} Authenticated successfully"
echo "  Token: ${TOKEN:0:30}..."

# Step 3: Upload image
echo ""
echo "3. Uploading test image..."
TEST_IMAGE="test_images/test_upload.png"

if [ ! -f "$TEST_IMAGE" ]; then
    echo -e "  ${RED}✗${NC} Test image not found: $TEST_IMAGE"
    exit 1
fi

UPLOAD_RESPONSE=$(curl -s -X POST "$API_URL/api/v1/upload" \
    -H "Authorization: Bearer ${TOKEN}" \
    -F "file=@${TEST_IMAGE}")

echo "  Upload Response:"
echo "$UPLOAD_RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$UPLOAD_RESPONSE"

UPLOAD_URL=$(echo "$UPLOAD_RESPONSE" | python3 -c "import sys, json; print(json.load(sys.stdin).get('url', ''))" 2>/dev/null || echo "")

if [ -z "$UPLOAD_URL" ] || [ "$UPLOAD_URL" = "null" ]; then
    echo -e "  ${RED}✗${NC} Upload failed"
    exit 1
fi

echo ""
echo -e "  ${GREEN}✓${NC} Upload successful!"
echo "  File URL: $UPLOAD_URL"

# Step 4: Verify file accessibility
echo ""
echo "4. Verifying file accessibility..."
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "$UPLOAD_URL")
if [ "$HTTP_CODE" = "200" ]; then
    echo -e "  ${GREEN}✓${NC} File is accessible (HTTP $HTTP_CODE)"
else
    echo -e "  ${YELLOW}⚠${NC} File returned HTTP $HTTP_CODE"
fi

# Step 5: Submit task
echo ""
echo "5. Submitting task with uploaded image..."
TASK_PAYLOAD=$(python3 -c "
import json
payload = {
    'source_url': '$UPLOAD_URL',
    'width': 400,
    'height': 300
}
print(json.dumps(payload))
" 2>/dev/null)

TASK_RESPONSE=$(curl -s -X POST "$API_URL/api/v1/tasks" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{\"type\":\"image_resize\",\"payload\":$TASK_PAYLOAD,\"priority\":0,\"max_retries\":3}")

echo "  Task Response:"
echo "$TASK_RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$TASK_RESPONSE"

TASK_ID=$(echo "$TASK_RESPONSE" | python3 -c "import sys, json; print(json.load(sys.stdin).get('task_id', json.load(sys.stdin).get('id', '')))" 2>/dev/null || echo "")

if [ -n "$TASK_ID" ] && [ "$TASK_ID" != "null" ]; then
    echo ""
    echo -e "  ${GREEN}✓${NC} Task created successfully!"
    echo "  Task ID: $TASK_ID"
    
    # Step 6: Check task status
    echo ""
    echo "6. Checking task status..."
    sleep 2
    STATUS_RESPONSE=$(curl -s -X GET "$API_URL/api/v1/tasks/$TASK_ID" \
        -H "Authorization: Bearer ${TOKEN}")
    
    echo "  Status Response:"
    echo "$STATUS_RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$STATUS_RESPONSE"
fi

echo ""
echo "=== Test Complete ==="
echo ""
echo "Summary:"
echo "  ✓ Image uploaded: $UPLOAD_URL"
if [ -n "$TASK_ID" ]; then
    echo "  ✓ Task created: $TASK_ID"
fi
echo ""
echo "View uploaded image: $UPLOAD_URL"

