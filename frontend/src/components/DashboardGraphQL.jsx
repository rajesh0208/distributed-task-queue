/**
 * src/components/DashboardGraphQL.jsx
 *
 * An alternative dashboard view that fetches task and metrics data via
 * GraphQL (Apollo Client) instead of the REST API used by Dashboard.jsx.
 * This file demonstrates how to use both Apollo and plain axios in the same
 * component for operations that have different transport requirements.
 *
 * # Why GraphQL for reads, axios for uploads?
 *
 *   Apollo Client (useQuery / useMutation) handles GraphQL over HTTP POST.
 *   File uploads, however, require multipart/form-data which is not
 *   natively supported by the graphql-go server. We therefore use a
 *   two-step flow:
 *
 *     1. Upload the image via REST   POST /api/v1/upload  (axios, multipart)
 *     2. Submit the task via GraphQL mutation SubmitTask  (Apollo)
 *
 *   After the upload, `response.data.url` is injected into the task payload's
 *   `source_url` field so the worker knows where to fetch the image from.
 *
 * # Polling vs WebSocket subscriptions
 *
 *   useQuery is configured with `pollInterval: 5000` (5-second HTTP polling).
 *   The graphql/client.js file shows the WebSocket subscription setup commented
 *   out — subscriptions would give real-time push updates but require
 *   `graphql-ws` and a matching server-side subscription resolver. Until those
 *   are implemented, polling is the pragmatic fallback.
 *
 * # getDefaultPayload
 *
 *   Returns a type-safe starter object for each task type so the payload
 *   textarea always shows valid JSON. Called:
 *     (a) on mount (initial state for 'image_resize')
 *     (b) when the task type select changes — avoids stale field names from
 *         the previous type leaking into the new payload
 *
 * # Image preview (FileReader API)
 *
 *   handleFileSelect uses the browser's FileReader API to generate a
 *   data: URL (base64-encoded image) for the <img> preview. This happens
 *   entirely in the browser — no network request is made just for the preview.
 *   The actual upload only happens when the user explicitly clicks "Upload".
 *
 * # Upload error handling
 *
 *   handleFileUpload detects 401 Unauthorized responses and clears the
 *   localStorage token, forcing the user back to the Login screen on the
 *   next page load. Generic errors are shown inline below the upload input.
 *
 * # Submit guard
 *
 *   The Submit button is disabled if a file is selected but not yet uploaded
 *   (selectedFile && !uploadedUrl). This prevents submitting a task whose
 *   payload.source_url is still empty.
 */
import React, { useState } from 'react'
import { useQuery, useMutation } from '@apollo/client'
import axios from 'axios'
import { GET_TASKS, GET_METRICS } from '../graphql/queries'
import { SUBMIT_TASK, DELETE_TASK } from '../graphql/mutations'
import TaskList from './TaskList'
import MetricsCard from './MetricsCard'

// Use relative path so the Vite dev-server proxy forwards to http://localhost:8080.
// In production, VITE_API_BASE should be set to the deployed API origin.
const API_BASE = import.meta.env.VITE_API_BASE || '/api/v1'

function DashboardGraphQL() {
  /**
   * getDefaultPayload — returns a starter payload object for the given task type.
   *
   * Why a function rather than a static map? The payload objects contain mutable
   * default values (e.g. width: 400) that callers mutate when they set source_url.
   * If we used a static map, every call would mutate the same shared object.
   * Returning a fresh literal each time avoids that aliasing bug.
   *
   * @param {string} taskType — one of the image_* task type constants
   * @returns {object} — a plain JS object with all required payload fields
   */
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

  // newTask holds the form state for the submission panel.
  // payload is kept as a JSON string so the <textarea> stays editable as plain text.
  const [newTask, setNewTask] = useState({
    type: 'image_resize',
    payload: JSON.stringify(getDefaultPayload('image_resize')), // seed with default fields
    priority: 0,
    maxRetries: 3,
  })
  const [selectedFile, setSelectedFile] = useState(null)   // File object chosen in the input
  const [uploading, setUploading] = useState(false)         // true while the axios upload is in-flight
  const [preview, setPreview] = useState(null)              // data: URL for the in-browser image preview
  const [uploadedUrl, setUploadedUrl] = useState(null)      // server URL returned after successful upload
  const [error, setError] = useState(null)                  // user-facing error message string

  // GET_TASKS is polled every 5 s — no WebSocket subscriptions wired up yet.
  // refetchTasks is called after a successful submitTask mutation to force an
  // immediate refresh rather than waiting for the next poll tick.
  const { data: tasksData, loading: tasksLoading, refetch: refetchTasks } = useQuery(
    GET_TASKS,
    {
      variables: { page: 1, pageSize: 20 }, // first page of 20 tasks
      pollInterval: 5000,                   // re-fetch every 5 000 ms
    }
  )

  // Metrics query — same polling interval as tasks so the counters stay in sync.
  const { data: metricsData, loading: metricsLoading } = useQuery(GET_METRICS, {
    pollInterval: 5000,
  })

  // submitTask mutation — onCompleted fires after the server ACKs the new task.
  // We reset the form here (not in handleSubmitTask) so the reset is tied to
  // the server response, not the local submit click.
  const [submitTask] = useMutation(SUBMIT_TASK, {
    onCompleted: () => {
      refetchTasks()       // immediate refresh so the new task appears in the list
      setNewTask({
        type: newTask.type,
        payload: JSON.stringify(getDefaultPayload(newTask.type)), // reset to defaults
        priority: 0,
        maxRetries: 3,
      })
      setSelectedFile(null)
      setPreview(null)
      setUploadedUrl(null)
    },
  })

  /**
   * handleFileSelect — invoked when the user picks a file in the <input type="file">.
   *
   * Uses the browser FileReader API to generate a data: URL for the preview
   * <img> tag. readAsDataURL encodes the file as base64 in a single async read;
   * reader.onloadend fires with the result once the encoding is done.
   *
   * No network request is made here — that only happens in handleFileUpload.
   */
  const handleFileSelect = (e) => {
    const file = e.target.files[0]
    if (file) {
      setSelectedFile(file)
      // FileReader reads the file from disk into memory and encodes it as
      // a data: URI so we can show a live preview before uploading.
      const reader = new FileReader()
      reader.onloadend = () => {
        setPreview(reader.result) // reader.result is the full data:image/... string
      }
      reader.readAsDataURL(file)
    }
  }

  /**
   * handleFileUpload — uploads the selected file to the REST endpoint
   * POST /api/v1/upload using multipart/form-data (axios).
   *
   * After a successful upload the server returns { url: "/images/<uuid>.ext" }.
   * We inject that URL into payload.source_url so the worker knows where to
   * fetch the input image from.
   *
   * Auth: reads the JWT from localStorage and sets the Authorization header.
   * A 401 response (or "token"/"unauthorized" in the error message) is treated
   * as a session expiry — the stored token is cleared and the user is asked
   * to log in again.
   */
  const handleFileUpload = async () => {
    if (!selectedFile) return

    setUploading(true)
    try {
      const token = localStorage.getItem('token')
      const formData = new FormData()
      formData.append('file', selectedFile)  // key must match what the Go handler expects

      const response = await axios.post(`${API_BASE}/upload`, formData, {
        headers: {
          Authorization: `Bearer ${token}`,
          'Content-Type': 'multipart/form-data',  // tells axios not to JSON-encode the body
        },
      })

      // Inject the server-assigned URL into the payload so the worker can
      // find the image. We parse → mutate → re-stringify to preserve any
      // other fields the user may have edited in the textarea.
      const payload = JSON.parse(newTask.payload)
      payload.source_url = response.data.url
      setNewTask({
        ...newTask,
        payload: JSON.stringify(payload),
      })

      setUploadedUrl(response.data.url)   // show the green "uploaded" confirmation
      setError(null)
      setSelectedFile(null)
      setPreview(null)
      setUploading(false)
    } catch (err) {
      console.error('Failed to upload file:', err)
      const errorMsg = err.response?.data?.error || err.message || 'Upload failed'
      // Treat any auth-related error as a session expiry — clear the token so
      // the user is redirected to Login on their next interaction.
      if (errorMsg.includes('token') || errorMsg.includes('unauthorized') || err.response?.status === 401) {
        setError('Authentication required. Please refresh the page and login again.')
        localStorage.removeItem('token')
      } else {
        setError(errorMsg)
      }
      setUploading(false)
    }
  }

  /**
   * handleSubmitTask — validates the payload then dispatches the GraphQL mutation.
   *
   * Validation: payload must be valid JSON AND must have a non-empty source_url.
   * The two different error messages guide the user toward the correct action:
   *   - file selected but not uploaded → remind them to click Upload first
   *   - no file and no URL             → remind them to provide a URL manually
   *
   * On success, Apollo's onCompleted callback (wired in useMutation above) resets
   * the form and calls refetchTasks — so we only need to clear local ephemeral
   * state here.
   */
  const handleSubmitTask = async (e) => {
    e.preventDefault()
    try {
      const payload = JSON.parse(newTask.payload)   // throws SyntaxError if user edited to invalid JSON
      if (!payload.source_url) {
        // Distinguish between "file staged but not uploaded" and "nothing provided at all"
        // so the error message is actionable rather than generic.
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
          payload: newTask.payload,   // serialized JSON string — the GraphQL scalar type is String
          priority: newTask.priority,
          maxRetries: newTask.maxRetries,
        },
      })
      // Clear upload-related state; form reset is handled in onCompleted above.
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

