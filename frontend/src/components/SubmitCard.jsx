// src/components/SubmitCard.jsx

import React, { useState, useCallback, useEffect } from 'react'
import { useDropzone } from 'react-dropzone'
import clsx from 'clsx'
import { uploadFile, createTask, createBatch } from '../api/api'

// ─── Single-image mode ────────────────────────────────────────────────────────

function SingleSubmit({ type, getDefaultPayload, defaultPayload, onTaskCreated }) {
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

  useEffect(() => {
    const newPayload = getDefaultPayload ? getDefaultPayload(type) : (defaultPayload || { source_url: '' })
    setPayloadText(JSON.stringify(newPayload, null, 2))
    setUploadedUrl(null)
    if (preview) URL.revokeObjectURL(preview)
    setSelectedFile(null)
    setPreview(null)
  }, [type])

  useEffect(() => () => { if (preview) URL.revokeObjectURL(preview) }, [preview])

  const onDrop = useCallback((accepted) => {
    if (!accepted || accepted.length === 0) return
    const f = accepted[0]
    if (preview) URL.revokeObjectURL(preview)
    setSelectedFile(f)
    setPreview(URL.createObjectURL(f))
    setUploadedUrl(null)
  }, [preview])

  const { getRootProps, getInputProps, isDragActive } = useDropzone({
    onDrop,
    multiple: false,
    accept: { 'image/*': [] },
  })

  const handleUpload = async () => {
    if (!selectedFile) return
    setUploading(true)
    setError(null)
    try {
      const resp = await uploadFile(selectedFile)
      setUploadedUrl(resp.data.url)
      try {
        const obj = JSON.parse(payloadText)
        obj.source_url = resp.data.url
        setPayloadText(JSON.stringify(obj, null, 2))
      } catch (_) {}
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
    } catch (_) {
      setError('Payload JSON is invalid')
      return
    }
    if (!payloadObj.source_url) {
      setError('Please upload an image or provide "source_url" in the payload.')
      return
    }
    try {
      await createTask({ type, payload: payloadObj, priority, max_retries: maxRetries })
      if (preview) URL.revokeObjectURL(preview)
      setSelectedFile(null)
      setPreview(null)
      setUploadedUrl(null)
      const reset = getDefaultPayload ? getDefaultPayload(type) : (defaultPayload || { source_url: '' })
      setPayloadText(JSON.stringify(reset, null, 2))
      setPriority(0)
      setMaxRetries(3)
      onTaskCreated?.()
    } catch (err) {
      setError(err.response?.data?.error || err.message)
    }
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
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
              <div className="text-xs text-gray-500">PNG, JPG up to 10 MB</div>
              {selectedFile && <div className="mt-1 text-sm text-gray-600">{selectedFile.name}</div>}
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
                if (preview) URL.revokeObjectURL(preview)
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
  )
}

// ─── Batch mode ───────────────────────────────────────────────────────────────

function BatchSubmit({ type, getDefaultPayload, defaultPayload, onTaskCreated }) {
  const [files, setFiles] = useState([]) // [{ file, preview, url, uploading, error }]
  const [priority, setPriority] = useState(0)
  const [maxRetries, setMaxRetries] = useState(3)
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState(null)
  const [submitted, setSubmitted] = useState(false)

  // Cleanup object URLs on unmount
  useEffect(() => {
    return () => files.forEach((f) => f.preview && URL.revokeObjectURL(f.preview))
  }, [])

  const onDrop = useCallback((accepted) => {
    const newEntries = accepted.map((file) => ({
      file,
      preview: URL.createObjectURL(file),
      url: null,
      uploading: false,
      error: null,
    }))
    setFiles((prev) => [...prev, ...newEntries])
  }, [])

  const { getRootProps, getInputProps, isDragActive } = useDropzone({
    onDrop,
    multiple: true,
    accept: { 'image/*': [] },
  })

  const uploadOne = async (index) => {
    setFiles((prev) =>
      prev.map((f, i) => (i === index ? { ...f, uploading: true, error: null } : f))
    )
    try {
      const resp = await uploadFile(files[index].file)
      setFiles((prev) =>
        prev.map((f, i) => (i === index ? { ...f, uploading: false, url: resp.data.url } : f))
      )
    } catch (err) {
      setFiles((prev) =>
        prev.map((f, i) =>
          i === index
            ? { ...f, uploading: false, error: err.response?.data?.error || err.message }
            : f
        )
      )
    }
  }

  const uploadAll = async () => {
    const pending = files.map((_, i) => i).filter((i) => !files[i].url)
    await Promise.all(pending.map(uploadOne))
  }

  const removeFile = (index) => {
    const f = files[index]
    if (f.preview) URL.revokeObjectURL(f.preview)
    setFiles((prev) => prev.filter((_, i) => i !== index))
  }

  const handleSubmit = async (e) => {
    e.preventDefault()
    setSubmitError(null)

    const notUploaded = files.filter((f) => !f.url)
    if (notUploaded.length > 0) {
      setSubmitError(`${notUploaded.length} image(s) not yet uploaded. Click "Upload All" first.`)
      return
    }
    if (files.length === 0) {
      setSubmitError('Add at least one image.')
      return
    }

    const basePayload = getDefaultPayload ? getDefaultPayload(type) : (defaultPayload || {})
    const payloads = files.map((f) => ({ ...basePayload, source_url: f.url }))

    setSubmitting(true)
    try {
      await createBatch({ type, payloads, priority, max_retries: maxRetries })
      files.forEach((f) => f.preview && URL.revokeObjectURL(f.preview))
      setFiles([])
      setPriority(0)
      setMaxRetries(3)
      setSubmitted(true)
      setTimeout(() => setSubmitted(false), 3000)
      onTaskCreated?.()
    } catch (err) {
      setSubmitError(err.response?.data?.error || err.message)
    } finally {
      setSubmitting(false)
    }
  }

  const allUploaded = files.length > 0 && files.every((f) => f.url)
  const anyUploading = files.some((f) => f.uploading)

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {/* Drop zone */}
      <div>
        <label className="block text-sm font-medium text-gray-700 mb-2">
          Upload Images <span className="text-gray-400 font-normal">(select multiple)</span>
        </label>
        <div
          {...getRootProps()}
          className={clsx(
            'p-6 border-2 border-dashed rounded-md cursor-pointer text-center',
            isDragActive ? 'border-blue-400 bg-blue-50' : 'border-gray-200 bg-gray-50'
          )}
        >
          <input {...getInputProps()} />
          <div className="text-sm font-medium text-gray-700">Drag & drop images here, or click to browse</div>
          <div className="text-xs text-gray-500 mt-1">PNG, JPG up to 10 MB each — multiple files allowed</div>
        </div>
      </div>

      {/* File list */}
      {files.length > 0 && (
        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-gray-700">{files.length} image(s)</span>
            {!allUploaded && (
              <button
                type="button"
                onClick={uploadAll}
                disabled={anyUploading}
                className="px-3 py-1 text-sm bg-blue-600 text-white rounded hover:bg-blue-700 disabled:bg-gray-300"
              >
                {anyUploading ? 'Uploading...' : 'Upload All'}
              </button>
            )}
          </div>

          <div className="max-h-56 overflow-y-auto space-y-2 pr-1">
            {files.map((entry, i) => (
              <div key={i} className="flex items-center gap-3 bg-gray-50 rounded p-2 border">
                <img
                  src={entry.preview}
                  alt={entry.file.name}
                  className="w-10 h-10 object-cover rounded flex-shrink-0"
                />
                <div className="flex-1 min-w-0">
                  <div className="text-sm text-gray-700 truncate">{entry.file.name}</div>
                  {entry.url && <div className="text-xs text-green-600">✅ Uploaded</div>}
                  {entry.uploading && <div className="text-xs text-blue-500">Uploading…</div>}
                  {entry.error && <div className="text-xs text-red-500">{entry.error}</div>}
                </div>
                <div className="flex gap-2 flex-shrink-0">
                  {!entry.url && !entry.uploading && (
                    <button
                      type="button"
                      onClick={() => uploadOne(i)}
                      className="text-xs px-2 py-1 bg-blue-100 text-blue-700 rounded hover:bg-blue-200"
                    >
                      Upload
                    </button>
                  )}
                  <button
                    type="button"
                    onClick={() => removeFile(i)}
                    className="text-xs px-2 py-1 bg-red-100 text-red-600 rounded hover:bg-red-200"
                  >
                    ✕
                  </button>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

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

      {submitError && <div className="text-sm text-red-600">{submitError}</div>}
      {submitted && (
        <div className="text-sm text-green-600">✅ Batch submitted — {files.length === 0 ? 'tasks queued!' : ''}</div>
      )}

      <div className="pt-3">
        <button
          type="submit"
          disabled={submitting || files.length === 0}
          className="px-5 py-2 bg-indigo-600 text-white rounded-md hover:bg-indigo-700 disabled:bg-gray-300"
        >
          {submitting ? 'Submitting...' : `Submit Batch (${files.length} images)`}
        </button>
      </div>
    </form>
  )
}

// ─── Main SubmitCard — wraps both modes ───────────────────────────────────────

const TASK_TYPES = [
  { value: 'image_resize', label: 'Image Resize' },
  { value: 'image_compress', label: 'Image Compress' },
  { value: 'image_filter', label: 'Image Filter' },
  { value: 'image_watermark', label: 'Image Watermark' },
  { value: 'image_thumbnail', label: 'Image Thumbnail' },
  { value: 'image_format_convert', label: 'Image Format Convert' },
  { value: 'image_crop', label: 'Image Crop' },
  { value: 'image_responsive', label: 'Image Responsive' },
]

const DEFAULT_PAYLOADS = {
  image_resize: { source_url: '', width: 800, height: 600, maintain_aspect: true },
  image_compress: { source_url: '', quality: 80, format: 'jpeg' },
  image_filter: { source_url: '', filter_type: 'grayscale' },
  image_watermark: { source_url: '', watermark_url: '', position: 'bottom-right', opacity: 0.5 },
  image_thumbnail: { source_url: '', size: 200 },
  image_format_convert: { source_url: '', output_format: 'webp' },
  image_crop: { source_url: '', x: 0, y: 0, width: 400, height: 300 },
  image_responsive: { source_url: '', widths: [320, 640, 1280] },
}

export default function SubmitCard({ onTaskCreated }) {
  const [type, setType] = useState('image_resize')
  const [batchMode, setBatchMode] = useState(false)

  const getDefaultPayload = (t) => DEFAULT_PAYLOADS[t] || { source_url: '' }

  return (
    <div className="bg-white rounded-xl shadow p-6">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-xl font-semibold">Submit New Task</h3>
        {/* Batch mode toggle */}
        <label className="flex items-center gap-2 cursor-pointer select-none">
          <span className="text-sm text-gray-600">Batch mode</span>
          <div
            onClick={() => setBatchMode((v) => !v)}
            className={clsx(
              'relative w-10 h-5 rounded-full transition-colors',
              batchMode ? 'bg-indigo-600' : 'bg-gray-300'
            )}
          >
            <div
              className={clsx(
                'absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform',
                batchMode ? 'translate-x-5' : 'translate-x-0'
              )}
            />
          </div>
        </label>
      </div>

      <div className="space-y-4">
        {/* Task type selector — shared between modes */}
        <div>
          <label className="block text-sm font-medium text-gray-700">Task Type</label>
          <select
            value={type}
            onChange={(e) => setType(e.target.value)}
            className="mt-1 block w-full rounded-md border-gray-200 p-2"
          >
            {TASK_TYPES.map((t) => (
              <option key={t.value} value={t.value}>{t.label}</option>
            ))}
          </select>
        </div>

        {batchMode ? (
          <BatchSubmit
            key={`batch-${type}`}
            type={type}
            getDefaultPayload={getDefaultPayload}
            onTaskCreated={onTaskCreated}
          />
        ) : (
          <SingleSubmit
            key={`single-${type}`}
            type={type}
            getDefaultPayload={getDefaultPayload}
            defaultPayload={getDefaultPayload(type)}
            onTaskCreated={onTaskCreated}
          />
        )}
      </div>
    </div>
  )
}
