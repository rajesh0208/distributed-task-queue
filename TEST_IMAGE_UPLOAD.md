# Testing Image Upload Feature

## Quick Start Guide

### Step 1: Start Services

**Option A: Using Docker Compose (Recommended)**
```bash
cd distributed-task-queue
docker-compose up -d
```

**Option B: Manual Start**
```bash
# Terminal 1: Start API Server
cd distributed-task-queue
go run ./cmd/api/main.go

# Terminal 2: Start Worker (optional, for processing tasks)
go run ./cmd/worker/main.go
```

### Step 2: Test Image Upload

**Using the Test Script:**
```bash
cd distributed-task-queue
./scripts/test-upload.sh
```

**Manual Testing with curl:**
```bash
# 1. Register/Login to get token
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"testuser","password":"testpass123"}' | \
  python3 -c "import sys, json; print(json.load(sys.stdin)['token'])")

# 2. Upload an image
curl -X POST http://localhost:8080/api/v1/upload \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@/path/to/your/image.png"

# Response will be:
# {
#   "url": "http://localhost:8080/uploads/uuid-filename.png",
#   "filename": "uuid-filename.png",
#   "size": 12345
# }
```

### Step 3: Submit Task with Uploaded Image

```bash
# Use the URL from upload response
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "image_resize",
    "payload": {
      "source_url": "http://localhost:8080/uploads/uuid-filename.png",
      "width": 400,
      "height": 300
    },
    "priority": 0,
    "max_retries": 3
  }'
```

## Using the Frontend Dashboard

1. **Start the Dashboard:**
   ```bash
   cd distributed-task-queue/dashboard
   npm run dev
   ```

2. **Access Dashboard:**
   - Open http://localhost:5173 in your browser
   - Login with your credentials

3. **Upload Image:**
   - Click "Choose File" button
   - Select an image file (jpg, png, gif, webp)
   - Click "Upload" button
   - The `source_url` in payload will be auto-filled
   - Click "Submit Task"

## Expected Results

### Successful Upload Response:
```json
{
  "url": "http://localhost:8080/uploads/550e8400-e29b-41d4-a716-446655440000.png",
  "filename": "550e8400-e29b-41d4-a716-446655440000.png",
  "size": 45678
}
```

### Successful Task Submission:
```json
{
  "task_id": "123e4567-e89b-12d3-a456-426614174000",
  "status": "queued",
  "message": "Task queued successfully",
  "created_at": "2024-11-20T01:30:00Z"
}
```

## File Validation

The upload endpoint validates:
- **File Type**: Only `.jpg`, `.jpeg`, `.png`, `.gif`, `.webp` allowed
- **File Size**: Maximum 10MB
- **Authentication**: Requires valid JWT token

## File Storage

- Uploaded files are stored in: `storage/uploads/`
- Files are accessible at: `http://localhost:8080/uploads/{filename}`
- Filenames are UUID-based to prevent conflicts

## Troubleshooting

**Error: "API server is not running"**
- Start the API server: `go run ./cmd/api/main.go`

**Error: "No file provided"**
- Ensure you're using `-F "file=@path/to/image.png"` format

**Error: "Invalid file type"**
- Only image files are allowed (jpg, jpeg, png, gif, webp)

**Error: "File size exceeds 10MB limit"**
- Compress or resize your image before uploading

**Error: "Unauthorized"**
- Make sure you're logged in and using a valid token
- Token should be in format: `Authorization: Bearer <token>`

