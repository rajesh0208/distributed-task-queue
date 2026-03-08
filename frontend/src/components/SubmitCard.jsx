// src/components/SubmitCard.jsx

import React, { useState, useCallback, useEffect } from 'react'
import { useDropzone } from 'react-dropzone'
import clsx from 'clsx'
import { uploadFile, createTask } from '../api/api'

export default function SubmitCard({ onTaskCreated, defaultPayload, getDefaultPayload }) {
  const [type, setType] = useState('image_resize')
  const [payloadText, setPayloadText] = useState(() => 
    JSON.stringify(defaultPayload || { source_url: '' }, null, 2)
  )
  const [priority, setPriority] = useState(0)
  const [maxRetries, setMaxRetries] = useState(3)

  const [selectedFile, setSelectedFile] = useState(null)
  const [preview, setPreview] = useState(null)
  const [uploadedUrl, setUploadedUrl] = useState(null)
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState(null)

  // Update payload when task type changes
  useEffect(() => {
    if (getDefaultPayload) {
      const newPayload = getDefaultPayload(type)
      setPayloadText(JSON.stringify(newPayload, null, 2))
      setUploadedUrl(null)
      if (preview) {
        URL.revokeObjectURL(preview)
      }
      setSelectedFile(null)
      setPreview(null)
    } else if (defaultPayload) {
      setPayloadText(JSON.stringify(defaultPayload, null, 2))
    }
  }, [type, getDefaultPayload, defaultPayload])

  // Cleanup preview URL on unmount
  useEffect(() => {
    return () => {
      if (preview) {
        URL.revokeObjectURL(preview)
      }
    }
  }, [preview])

  const onDrop = useCallback((accepted) => {
    if (!accepted || accepted.length === 0) return
    const f = accepted[0]
    // Revoke previous preview URL to prevent memory leaks
    if (preview) {
      URL.revokeObjectURL(preview)
    }
    setSelectedFile(f)
    setPreview(URL.createObjectURL(f))
    setUploadedUrl(null)
  }, [preview])

  const { getRootProps, getInputProps, isDragActive } = useDropzone({
    onDrop,
    multiple: false,
    accept: 'image/*',
  })

  const handleUpload = async () => {
    if (!selectedFile) return

    setUploading(true)
    setError(null)
    try {
      const resp = await uploadFile(selectedFile, (evt) => {
        // optional progress: evt.loaded / evt.total
      })
      setUploadedUrl(resp.data.url)
      // update payload's source_url automatically
      try {
        const obj = JSON.parse(payloadText)
        obj.source_url = resp.data.url
        setPayloadText(JSON.stringify(obj, null, 2))
      } catch (e) {
        // ignore
      }
    } catch (err) {
      setError(err.response?.data?.error || err.message)
    } finally {
      setUploading(false)
    }
  }

  const handleSubmit = async (e) => {
    e.preventDefault()
    setError(null)
    let payloadObj
    try {
      payloadObj = JSON.parse(payloadText)
    } catch (e) {
      setError('Payload JSON is invalid')
      return
    }
    if (!payloadObj.source_url) {
      setError('Please upload an image or provide "source_url" in the payload.')
      return
    }
    try {
      await createTask({
        type,
        payload: payloadObj,
        priority,
        max_retries: maxRetries,
      })
      // reset
      if (preview) {
        URL.revokeObjectURL(preview)
      }
      setSelectedFile(null)
      setPreview(null)
      setUploadedUrl(null)
      const resetPayload = getDefaultPayload ? getDefaultPayload(type) : (defaultPayload || { source_url: '' })
      setPayloadText(JSON.stringify(resetPayload, null, 2))
      setPriority(0)
      setMaxRetries(3)
      onTaskCreated?.()
    } catch (err) {
      setError(err.response?.data?.error || err.message)
    }
  }

  return (
    <div className="bg-white rounded-xl shadow p-6">
      <h3 className="text-xl font-semibold mb-4">Submit New Task</h3>

      <form onSubmit={handleSubmit} className="space-y-4">
        <div>
          <label className="block text-sm font-medium text-gray-700">Task Type</label>
          <select
            value={type}
            onChange={(e) => setType(e.target.value)}
            className="mt-1 block w-full rounded-md border-gray-200 p-2"
          >
            <option value="image_resize">Image Resize</option>
            <option value="image_compress">Image Compress</option>
            <option value="image_filter">Image Filter</option>
            <option value="image_watermark">Image Watermark</option>
            <option value="image_thumbnail">Image Thumbnail</option>
            <option value="image_format_convert">Image Format Convert</option>
          </select>
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-2">Upload Image</label>
          <div
            {...getRootProps()}
            className={clsx(
              'p-6 border-2 border-dashed rounded-md cursor-pointer',
              isDragActive ? 'border-blue-400 bg-blue-50' : 'border-gray-200 bg-gray-50'
            )}
          >
            <input {...getInputProps()} />
            <div className="flex items-center gap-4">
              <div className="flex-shrink-0 w-14 h-14 bg-white border rounded overflow-hidden">
                {preview ? (
                  <img src={preview} alt="preview" className="w-full h-full object-cover" />
                ) : (
                  <div className="w-full h-full flex items-center justify-center text-gray-300">IMG</div>
                )}
              </div>
              <div>
                <div className="text-sm font-medium text-gray-700">Drag & drop or click to browse</div>
                <div className="text-xs text-gray-500">PNG, JPG up to server limits</div>
                {selectedFile && <div className="mt-2 text-sm text-gray-600">{selectedFile.name}</div>}
              </div>
            </div>
          </div>

          {selectedFile && !uploadedUrl && (
            <div className="mt-3 flex items-center gap-3">
              <button
                type="button"
                onClick={handleUpload}
                disabled={uploading}
                className="px-4 py-2 bg-blue-600 text-white rounded-md hover:bg-blue-700 disabled:bg-gray-300"
              >
                {uploading ? 'Uploading...' : 'Upload'}
              </button>
              <button
                type="button"
                onClick={() => {
                  if (preview) {
                    URL.revokeObjectURL(preview)
                  }
                  setSelectedFile(null)
                  setPreview(null)
                }}
                className="px-3 py-2 border rounded"
              >
                Remove
              </button>
            </div>
          )}

          {uploadedUrl && <div className="mt-2 text-sm text-green-600">✅ Uploaded: {uploadedUrl}</div>}
        </div>

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-2">Payload (JSON)</label>
          <textarea
            value={payloadText}
            onChange={(e) => setPayloadText(e.target.value)}
            rows={4}
            className="mt-1 block w-full rounded-md border-gray-200 p-3 font-mono text-sm"
          />
        </div>

        <div className="grid grid-cols-2 gap-4">
          <div>
            <label className="block text-sm font-medium text-gray-700">Priority</label>
            <input
              type="number"
              value={priority}
              onChange={(e) => setPriority(parseInt(e.target.value || 0))}
              className="mt-1 block w-full rounded-md border-gray-200 p-2"
            />
          </div>
          <div>
            <label className="block text-sm font-medium text-gray-700">Max Retries</label>
            <input
              type="number"
              value={maxRetries}
              onChange={(e) => setMaxRetries(parseInt(e.target.value || 0))}
              className="mt-1 block w-full rounded-md border-gray-200 p-2"
            />
          </div>
        </div>

        {error && <div className="text-sm text-red-600">{error}</div>}

        <div className="pt-3">
          <button type="submit" className="px-5 py-2 bg-indigo-600 text-white rounded-md hover:bg-indigo-700">
            Submit Task
          </button>
        </div>
      </form>
    </div>
  )
}

