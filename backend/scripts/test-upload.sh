#!/bin/bash

# Test Image Upload Script
# This script tests the file upload functionality

set -e

API_URL="${API_URL:-http://localhost:8080}"
TEST_USER="${TEST_USER:-testuser}"
TEST_PASS="${TEST_PASS:-testpass123}"

echo "=== Image Upload Test ==="
echo ""

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Step 1: Check if API is running
echo "1. Checking API server..."
if curl -s "$API_URL/api/v1/health" > /dev/null 2>&1; then
    echo -e "  ${GREEN}✓${NC} API server is running"
else
    echo -e "  ${RED}✗${NC} API server is not running"
    echo "  Please start the API server first:"
    echo "    cd backend && go run ./cmd/api/main.go"
    exit 1
fi

# Step 2: Register user (if needed)
echo ""
echo "2. Registering/Logging in user..."
REGISTER_RESPONSE=$(curl -s -X POST "$API_URL/api/v1/auth/register" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${TEST_USER}\",\"email\":\"${TEST_USER}@test.com\",\"password\":\"${TEST_PASS}\"}" 2>&1)

LOGIN_RESPONSE=$(curl -s -X POST "$API_URL/api/v1/auth/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"${TEST_USER}\",\"password\":\"${TEST_PASS}\"}")

TOKEN=$(echo "$LOGIN_RESPONSE" | grep -o '"token":"[^"]*' | cut -d'"' -f4)

if [ -z "$TOKEN" ]; then
    # Try alternative JSON parsing
    TOKEN=$(echo "$LOGIN_RESPONSE" | python3 -c "import sys, json; print(json.load(sys.stdin).get('token', ''))" 2>/dev/null || echo "")
fi

if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ]; then
    echo -e "  ${RED}✗${NC} Failed to get authentication token"
    echo "  Response: $LOGIN_RESPONSE"
    exit 1
fi

echo -e "  ${GREEN}✓${NC} Authentication successful"
echo "  Token: ${TOKEN:0:20}..."

# Step 3: Create test image if it doesn't exist
echo ""
echo "3. Preparing test image..."
TEST_IMAGE="test_images/test_image.png"
mkdir -p test_images

if [ ! -f "$TEST_IMAGE" ]; then
    # Try to download a test image
    echo "  Downloading test image..."
    curl -s -o "$TEST_IMAGE" "https://picsum.photos/400/300" || {
        # Create a simple test file
        echo "  Creating simple test file..."
        echo "PNG_TEST" > "$TEST_IMAGE"
    }
fi

if [ ! -f "$TEST_IMAGE" ]; then
    echo -e "  ${RED}✗${NC} Could not create test image"
    exit 1
fi

FILE_SIZE=$(stat -f%z "$TEST_IMAGE" 2>/dev/null || stat -c%s "$TEST_IMAGE" 2>/dev/null || echo "0")
echo -e "  ${GREEN}✓${NC} Test image ready: $TEST_IMAGE (${FILE_SIZE} bytes)"

# Step 4: Upload image
echo ""
echo "4. Uploading image..."
UPLOAD_RESPONSE=$(curl -s -X POST "$API_URL/api/v1/upload" \
    -H "Authorization: Bearer ${TOKEN}" \
    -F "file=@${TEST_IMAGE}")

echo "  Upload Response:"
echo "$UPLOAD_RESPONSE" | python3 -m json.tool 2>/dev/null || echo "$UPLOAD_RESPONSE"

# Extract URL from response
UPLOAD_URL=$(echo "$UPLOAD_RESPONSE" | grep -o '"url":"[^"]*' | cut -d'"' -f4)
if [ -z "$UPLOAD_URL" ]; then
    UPLOAD_URL=$(echo "$UPLOAD_RESPONSE" | python3 -c "import sys, json; print(json.load(sys.stdin).get('url', ''))" 2>/dev/null || echo "")
fi

if [ -n "$UPLOAD_URL" ] && [ "$UPLOAD_URL" != "null" ]; then
    echo ""
    echo -e "  ${GREEN}✓${NC} Upload successful!"
    echo "  File URL: $UPLOAD_URL"
    
    # Step 5: Verify file is accessible
    echo ""
    echo "5. Verifying uploaded file..."
    if curl -s -I "$UPLOAD_URL" | head -1 | grep -q "200\|OK"; then
        echo -e "  ${GREEN}✓${NC} File is accessible at: $UPLOAD_URL"
    else
        echo -e "  ${YELLOW}⚠${NC} File may not be accessible yet"
    fi
    
    # Step 6: Submit task with uploaded image
    echo ""
    echo "6. Submitting task with uploaded image..."
    TASK_PAYLOAD=$(python3 -c "
import json
payload = {
    'source_url': '$UPLOAD_URL',
    'width': 400,
    'height': 300
}
print(json.dumps(payload))
" 2>/dev/null || echo '{"source_url":"'$UPLOAD_URL'","width":400,"height":300}')
    
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
        echo ""
        echo "  View task status:"
        echo "    curl -H \"Authorization: Bearer ${TOKEN}\" $API_URL/api/v1/tasks/$TASK_ID"
    fi
else
    echo -e "  ${RED}✗${NC} Upload failed"
    echo "  Response: $UPLOAD_RESPONSE"
    exit 1
fi

echo ""
echo "=== Test Complete ==="
echo ""
echo "Summary:"
echo "  ✓ Image uploaded successfully"
echo "  ✓ File URL: $UPLOAD_URL"
if [ -n "$TASK_ID" ]; then
    echo "  ✓ Task created: $TASK_ID"
fi
echo ""
echo "You can view the uploaded image at: $UPLOAD_URL"

