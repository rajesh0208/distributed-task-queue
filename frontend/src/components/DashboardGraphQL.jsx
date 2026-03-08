import React, { useState } from 'react'
import { useQuery, useMutation } from '@apollo/client'
import axios from 'axios'
import { GET_TASKS, GET_METRICS } from '../graphql/queries'
import { SUBMIT_TASK, DELETE_TASK } from '../graphql/mutations'
import TaskList from './TaskList'
import MetricsCard from './MetricsCard'

// Use relative path to go through Vite proxy
const API_BASE = import.meta.env.VITE_API_BASE || '/api/v1'

function DashboardGraphQL() {
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
  const [error, setError] = useState(null)

  const { data: tasksData, loading: tasksLoading, refetch: refetchTasks } = useQuery(
    GET_TASKS,
    {
      variables: { page: 1, pageSize: 20 },
      pollInterval: 5000,
    }
  )

  const { data: metricsData, loading: metricsLoading } = useQuery(GET_METRICS, {
    pollInterval: 5000,
  })

  const [submitTask] = useMutation(SUBMIT_TASK, {
    onCompleted: () => {
      refetchTasks()
      setNewTask({
        type: newTask.type,
        payload: JSON.stringify(getDefaultPayload(newTask.type)),
        priority: 0,
        maxRetries: 3,
      })
      setSelectedFile(null)
      setPreview(null)
      setUploadedUrl(null)
    },
  })

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
    if (!selectedFile) return

    setUploading(true)
    try {
      const token = localStorage.getItem('token')
      const formData = new FormData()
      formData.append('file', selectedFile)

      const response = await axios.post(`${API_BASE}/upload`, formData, {
        headers: {
          Authorization: `Bearer ${token}`,
          'Content-Type': 'multipart/form-data',
        },
      })

      // Update payload with uploaded file URL
      const payload = JSON.parse(newTask.payload)
      payload.source_url = response.data.url
      setNewTask({
        ...newTask,
        payload: JSON.stringify(payload),
      })

      setUploadedUrl(response.data.url)
      setError(null)
      setSelectedFile(null)
      setPreview(null)
      setUploading(false)
    } catch (err) {
      console.error('Failed to upload file:', err)
      const errorMsg = err.response?.data?.error || err.message || 'Upload failed'
      if (errorMsg.includes('token') || errorMsg.includes('unauthorized') || err.response?.status === 401) {
        setError('Authentication required. Please refresh the page and login again.')
        localStorage.removeItem('token')
      } else {
        setError(errorMsg)
      }
      setUploading(false)
    }
  }

  const handleSubmitTask = async (e) => {
    e.preventDefault()
    try {
      const payload = JSON.parse(newTask.payload)
      if (!payload.source_url) {
        if (selectedFile && !uploadedUrl) {
          setError('Please click "Upload" button first to upload the selected image')
        } else {
          setError('Please upload an image or provide a source URL in the payload')
        }
        return
      }

      await submitTask({
        variables: {
          type: newTask.type,
          payload: newTask.payload,
          priority: newTask.priority,
          maxRetries: newTask.maxRetries,
        },
      })
      setSelectedFile(null)
      setPreview(null)
      setUploadedUrl(null)
      setError(null)
    } catch (err) {
      console.error('Failed to submit task:', err)
      setError(err.message || 'Failed to submit task')
    }
  }

  if (tasksLoading || metricsLoading) {
    return <div className="text-center py-8">Loading...</div>
  }

  return (
    <div className="space-y-6">
      <div className="bg-white shadow rounded-lg p-6">
        <h2 className="text-2xl font-bold mb-4">Submit New Task (GraphQL)</h2>
        <form onSubmit={handleSubmitTask} className="space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700">
              Task Type
            </label>
            <select
              value={newTask.type}
              onChange={(e) => {
                const newType = e.target.value
                // Reset payload when task type changes
                setNewTask({
                  ...newTask,
                  type: newType,
                  payload: JSON.stringify(getDefaultPayload(newType)),
                })
                // Clear uploaded URL when task type changes
                setUploadedUrl(null)
                setSelectedFile(null)
                setPreview(null)
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
            <div className="flex items-center space-x-4">
              <input
                type="file"
                accept="image/*"
                onChange={handleFileSelect}
                className="block text-sm text-gray-500 file:mr-4 file:py-2 file:px-4 file:rounded-md file:border-0 file:text-sm file:font-semibold file:bg-blue-50 file:text-blue-700 hover:file:bg-blue-100"
              />
              {selectedFile && (
                <button
                  type="button"
                  onClick={handleFileUpload}
                  disabled={uploading}
                  className="px-4 py-2 bg-green-500 text-white rounded-md hover:bg-green-600 disabled:bg-gray-400"
                >
                  {uploading ? 'Uploading...' : 'Upload'}
                </button>
              )}
            </div>
            {preview && (
              <div className="mt-4">
                <img
                  src={preview}
                  alt="Preview"
                  className="max-w-xs max-h-48 rounded-md border border-gray-300"
                />
                <p className="text-sm text-gray-500 mt-2">{selectedFile?.name}</p>
                {!uploadedUrl && (
                  <p className="text-sm text-yellow-600 mt-2">
                    ⚠️ Click "Upload" button to upload this image before submitting
                  </p>
                )}
              </div>
            )}
            {uploadedUrl && (
              <div className="mt-2">
                <p className="text-sm text-green-600">
                  ✅ Image uploaded successfully: {uploadedUrl}
                </p>
              </div>
            )}
            {error && (
              <div className="mt-2">
                <p className="text-sm text-red-600">{error}</p>
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

      {metricsData?.metrics && <MetricsCard metrics={metricsData.metrics} />}
      {tasksData?.tasks && (
        <TaskList
          tasks={tasksData.tasks.tasks || []}
          onRefresh={refetchTasks}
        />
      )}
    </div>
  )
}

export default DashboardGraphQL

