import React, { useState, useEffect } from 'react'
import { fetchTasks as apiFetchTasks, fetchMetrics as apiFetchMetrics, uploadFile, createTask } from '../api/api'
import TaskList from './TaskList'
import MetricsCard from './MetricsCard'

function Dashboard() {
  const [tasks, setTasks] = useState([])
  const [metrics, setMetrics] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  
  // Helper function to get default payload for each task type
  const getDefaultPayload = (taskType) => {
    switch (taskType) {
      case 'image_resize':
        return { source_url: '', width: 400, height: 300, maintain_aspect: false }
      case 'image_compress':
        return { source_url: '', quality: 80, format: 'jpeg' }
      case 'image_watermark':
        return { source_url: '', watermark_url: '', position: 'bottom-right', opacity: 0.5 }
      case 'image_filter':
        return { source_url: '', filter_type: 'grayscale' }
      case 'image_thumbnail':
        return { source_url: '', size: 200 }
      case 'image_format_convert':
        return { source_url: '', output_format: 'png' }
      default:
        return { source_url: '' }
    }
  }

  const [newTask, setNewTask] = useState({
    type: 'image_resize',
    payload: JSON.stringify(getDefaultPayload('image_resize')),
    priority: 0,
    maxRetries: 3,
  })
  const [selectedFile, setSelectedFile] = useState(null)
  const [uploading, setUploading] = useState(false)
  const [preview, setPreview] = useState(null)
  const [uploadedUrl, setUploadedUrl] = useState(null)

  useEffect(() => {
    fetchTasks()
    fetchMetrics()
    const interval = setInterval(() => {
      fetchTasks()
      fetchMetrics()
    }, 5000)
    return () => clearInterval(interval)
  }, [])

  const fetchTasks = async () => {
    try {
      const token = localStorage.getItem('token')
      if (!token) {
        setError('No authentication token found. Please login again.')
        setLoading(false)
        return
      }
      const response = await apiFetchTasks(1, 20, '')
      setTasks(response.data.tasks || [])
      setError(null)
      setLoading(false)
    } catch (err) {
      const errorMsg = err.response?.data?.error || err.message
      if (err.response?.status === 401 || errorMsg.includes('token') || errorMsg.includes('unauthorized')) {
        setError('Authentication expired. Please refresh and login again.')
        localStorage.removeItem('token')
        window.location.reload()
      } else {
        setError(errorMsg)
      }
      setLoading(false)
    }
  }

  const fetchMetrics = async () => {
    try {
      const response = await apiFetchMetrics()
      setMetrics(response.data)
    } catch (err) {
      // Handle 403 gracefully - metrics endpoint requires admin role
      // This is non-critical, so we just log and continue
      if (err.response?.status === 403) {
        console.warn('Metrics endpoint requires admin role. Showing default values.')
        // Set default metrics so UI doesn't break
        setMetrics({
          queued_tasks: 0,
          active_workers: 0,
          avg_processing_time: 0
        })
      } else {
        console.error('Failed to fetch metrics:', err)
      }
    }
  }

  const handleFileSelect = (e) => {
    const file = e.target.files[0]
    if (file) {
      setSelectedFile(file)
      // Create preview
      const reader = new FileReader()
      reader.onloadend = () => {
        setPreview(reader.result)
      }
      reader.readAsDataURL(file)
    }
  }

  const handleFileUpload = async () => {
    if (!selectedFile) {
      setError('Please select a file first')
      return
    }

    // Check authentication
    const token = localStorage.getItem('token')
    if (!token) {
      setError('No authentication token found. Please login again.')
      return
    }

    setUploading(true)
    setError(null)
    try {
      console.log('=== UPLOAD DEBUG ===')
      console.log('File:', selectedFile.name, 'Size:', selectedFile.size, 'Type:', selectedFile.type)
      console.log('Task type:', newTask.type)
      console.log('Token present:', !!token)
      console.log('Token length:', token?.length)
      
      const response = await uploadFile(selectedFile)
      
      if (!response || !response.data || !response.data.url) {
        throw new Error('Invalid response from server')
      }
      
      const uploadedUrlValue = response.data.url
      console.log('Upload successful, URL:', uploadedUrlValue)
      
      // Update payload with uploaded file URL (works for all task types)
      try {
        let payload = JSON.parse(newTask.payload)
        // Ensure payload is an object (not null, not array)
        if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) {
          console.warn('Payload is not an object, creating new one')
          payload = getDefaultPayload(newTask.type)
        }
        // Update source_url while preserving all other fields (like filter_type for image_filter, watermark_url, etc.)
        payload.source_url = uploadedUrlValue
        setNewTask({
          ...newTask,
          payload: JSON.stringify(payload, null, 2),
        })
        console.log('✅ Updated payload for', newTask.type, ':', JSON.stringify(payload, null, 2))
      } catch (parseErr) {
        console.error('Failed to parse payload:', parseErr, 'Payload:', newTask.payload)
        // If payload is invalid, create a new one with the uploaded URL
        const newPayload = getDefaultPayload(newTask.type)
        newPayload.source_url = uploadedUrlValue
        setNewTask({
          ...newTask,
          payload: JSON.stringify(newPayload, null, 2),
        })
        console.log('Created new payload for', newTask.type, ':', newPayload)
      }

      setUploadedUrl(uploadedUrlValue)
      setError(null)
      // Keep file and preview until user explicitly removes them or submits
      setUploading(false)
    } catch (err) {
      console.error('Upload error:', err)
      const errorMsg = err.response?.data?.error || err.message || 'Upload failed'
      console.error('Upload error details:', {
        message: err.message,
        response: err.response?.data,
        status: err.response?.status
      })
      if (errorMsg.includes('token') || errorMsg.includes('unauthorized') || err.response?.status === 401) {
        setError('Authentication required. Please refresh the page and login again.')
        localStorage.removeItem('token')
      } else {
        setError(`Upload failed: ${errorMsg}`)
      }
      setUploading(false)
    }
  }

  const handleSubmitTask = async (e) => {
    e.preventDefault()
    setError(null)
    try {
      let payload
      try {
        payload = JSON.parse(newTask.payload)
      } catch (parseErr) {
        setError('Invalid JSON in payload. Please check the payload format.')
        console.error('Payload parse error:', parseErr, 'Payload:', newTask.payload)
        return
      }
      
      // Ensure payload is an object
      if (typeof payload !== 'object' || payload === null || Array.isArray(payload)) {
        setError('Payload must be a valid JSON object')
        return
      }
      
      // Check if source_url is provided
      if (!payload.source_url || (typeof payload.source_url === 'string' && payload.source_url.trim() === '')) {
        if (selectedFile && !uploadedUrl) {
          setError('Please click "Upload" button first to upload the selected image')
        } else {
          setError('Please upload an image or provide a source URL in the payload')
        }
        return
      }

      console.log('Submitting task:', newTask.type, 'with payload:', payload)
      
      await createTask({
        type: newTask.type,
        payload: payload,
        priority: newTask.priority,
        max_retries: newTask.maxRetries,
      })
      setNewTask({
        type: newTask.type,
        payload: JSON.stringify(getDefaultPayload(newTask.type)),
        priority: 0,
        maxRetries: 3,
      })
      setSelectedFile(null)
      setPreview(null)
      setUploadedUrl(null)
      setError(null)
      fetchTasks()
    } catch (err) {
      setError(err.response?.data?.error || err.message)
    }
  }

  if (loading) {
    return <div className="text-center py-8">Loading...</div>
  }

  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 h-full overflow-hidden">
      {/* Left Side: Task Submission */}
      <div className="bg-white shadow rounded-lg p-6 overflow-y-auto">
        <h2 className="text-2xl font-bold mb-4">Submit New Task</h2>
        <form onSubmit={handleSubmitTask} className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700">
              Task Type
            </label>
            <select
              value={newTask.type}
              onChange={(e) => {
                const newType = e.target.value
                const newPayload = getDefaultPayload(newType)
                
                // Preserve source_url if it was already uploaded or exists in current payload
                try {
                  const currentPayload = JSON.parse(newTask.payload)
                  if (currentPayload.source_url && currentPayload.source_url.trim() !== '') {
                    newPayload.source_url = currentPayload.source_url
                  } else if (uploadedUrl) {
                    newPayload.source_url = uploadedUrl
                  }
                } catch (e) {
                  // If current payload is invalid, use uploadedUrl if available
                  if (uploadedUrl) {
                    newPayload.source_url = uploadedUrl
                  }
                }
                
                setNewTask({
                  ...newTask,
                  type: newType,
                  payload: JSON.stringify(newPayload, null, 2),
                })
              }}
              className="mt-1 block w-full rounded-md border-gray-300 shadow-sm"
            >
              <option value="image_resize">Image Resize</option>
              <option value="image_compress">Image Compress</option>
              <option value="image_watermark">Image Watermark</option>
              <option value="image_filter">Image Filter</option>
              <option value="image_thumbnail">Image Thumbnail</option>
              <option value="image_format_convert">Image Format Convert</option>
            </select>
          </div>

          {/* File Upload Section */}
          <div>
            <label className="block text-sm font-medium text-gray-700 mb-2">
              Upload Image
            </label>
            <div className="flex items-center space-x-4 flex-wrap">
              <input
                type="file"
                accept="image/*"
                onChange={handleFileSelect}
                className="block text-sm text-gray-500 file:mr-4 file:py-2 file:px-4 file:rounded-md file:border-0 file:text-sm file:font-semibold file:bg-blue-50 file:text-blue-700 hover:file:bg-blue-100"
                id="file-upload-input"
              />
              {selectedFile && (
                <>
                  {!uploadedUrl && (
                    <button
                      type="button"
                      onClick={handleFileUpload}
                      disabled={uploading}
                      className="px-4 py-2 bg-green-500 text-white rounded-md hover:bg-green-600 disabled:bg-gray-400 disabled:cursor-not-allowed"
                    >
                      {uploading ? 'Uploading...' : 'Upload'}
                    </button>
                  )}
                  <button
                    type="button"
                    onClick={() => {
                      setSelectedFile(null)
                      setPreview(null)
                      setUploadedUrl(null)
                      // Clear source_url from payload
                      try {
                        const payload = JSON.parse(newTask.payload)
                        payload.source_url = ''
                        setNewTask({
                          ...newTask,
                          payload: JSON.stringify(payload, null, 2),
                        })
                      } catch (e) {
                        // If payload is invalid, reset to default
                        setNewTask({
                          ...newTask,
                          payload: JSON.stringify(getDefaultPayload(newTask.type), null, 2),
                        })
                      }
                    }}
                    className="px-3 py-2 bg-gray-200 text-gray-700 rounded-md hover:bg-gray-300 text-sm"
                  >
                    Remove
                  </button>
                </>
              )}
            </div>
            {preview && (
              <div className="mt-4">
                <img
                  src={preview}
                  alt="Preview"
                  className="max-w-xs max-h-48 rounded-md border border-gray-300"
                />
                <p className="text-sm text-gray-500 mt-2">
                  {selectedFile?.name} ({(selectedFile?.size / 1024).toFixed(2)} KB)
                </p>
                {!uploadedUrl && !uploading && (
                  <p className="text-sm text-yellow-600 mt-2">
                    ⚠️ Click "Upload" button to upload this image before submitting
                  </p>
                )}
                {uploading && (
                  <p className="text-sm text-blue-600 mt-2">
                    ⏳ Uploading... Please wait
                  </p>
                )}
              </div>
            )}
            {uploadedUrl && (
              <div className="mt-2 p-3 bg-green-50 border border-green-200 rounded-md">
                <p className="text-sm text-green-700 font-medium">
                  ✅ Image uploaded successfully
                </p>
                <p className="text-xs text-green-600 mt-1 break-all">{uploadedUrl}</p>
                <p className="text-xs text-gray-500 mt-1">
                  The source_url has been automatically added to your payload
                </p>
              </div>
            )}
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700">
              Payload (JSON) - source_url will be auto-filled after upload
            </label>
            <textarea
              value={newTask.payload}
              onChange={(e) => setNewTask({ ...newTask, payload: e.target.value })}
              rows={4}
              className="mt-1 block w-full rounded-md border-gray-300 shadow-sm"
              placeholder='{"source_url": "", "width": 400, "height": 300}'
            />
          </div>
          <div className="flex space-x-4">
            <div>
              <label className="block text-sm font-medium text-gray-700">
                Priority
              </label>
              <input
                type="number"
                value={newTask.priority}
                onChange={(e) =>
                  setNewTask({ ...newTask, priority: parseInt(e.target.value) })
                }
                className="mt-1 block w-full rounded-md border-gray-300 shadow-sm"
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-gray-700">
                Max Retries
              </label>
              <input
                type="number"
                value={newTask.maxRetries}
                onChange={(e) =>
                  setNewTask({ ...newTask, maxRetries: parseInt(e.target.value) })
                }
                className="mt-1 block w-full rounded-md border-gray-300 shadow-sm"
              />
            </div>
          </div>
          <button
            type="submit"
            disabled={selectedFile && !uploadedUrl}
            className="bg-blue-500 text-white px-4 py-2 rounded-md hover:bg-blue-600 disabled:bg-gray-400 disabled:cursor-not-allowed"
          >
            Submit Task
          </button>
          {selectedFile && !uploadedUrl && (
            <p className="text-sm text-yellow-600 mt-2">
              Please upload the selected image first
            </p>
          )}
        </form>
      </div>

      {/* Right Side: Task Details */}
      <div className="space-y-6 overflow-y-auto h-full">
          {error && (
          <div className="bg-red-100 border border-red-400 text-red-700 px-4 py-3 rounded relative">
            <strong className="font-bold">Error: </strong>
            <span className="block sm:inline">{error}</span>
            <button
              onClick={() => setError(null)}
              className="absolute top-0 right-0 px-3 py-1 text-red-700 hover:text-red-900"
            >
              ×
            </button>
          </div>
        )}
        {metrics && <MetricsCard metrics={metrics} />}
        <TaskList tasks={tasks} onRefresh={fetchTasks} />
      </div>
    </div>
  )
}

export default Dashboard
