// src/components/SubmitCard.jsx
// Task submission card with two modes:
//   • Single — drag-and-drop one image, edit JSON payload, submit.
//   • Batch  — drop up to BATCH_LIMIT images, upload all, submit as one batch.

import React, { useState, useCallback, useEffect } from 'react'
import { useDropzone } from 'react-dropzone'
import clsx from 'clsx'
import { uploadFile, createTask, createBatch } from '../api/api'

// Maximum images allowed per batch submission.
const BATCH_LIMIT = 5

const TASK_TYPES = [
  { value: 'image_resize',         label: 'Resize' },
  { value: 'image_compress',       label: 'Compress' },
  { value: 'image_filter',         label: 'Filter' },
  { value: 'image_watermark',      label: 'Watermark' },
  { value: 'image_thumbnail',      label: 'Thumbnail' },
  { value: 'image_format_convert', label: 'Format Convert' },
  { value: 'image_crop',           label: 'Crop' },
  { value: 'image_responsive',     label: 'Responsive' },
]

const DEFAULT_PAYLOADS = {
  image_resize:         { source_url: '', width: 800, height: 600, maintain_aspect: true },
  image_compress:       { source_url: '', quality: 80, format: 'jpeg' },
  image_filter:         { source_url: '', filter_type: 'grayscale' },
  image_watermark:      { source_url: '', watermark_url: '', position: 'bottom-right', opacity: 0.5 },
  image_thumbnail:      { source_url: '', size: 200 },
  image_format_convert: { source_url: '', output_format: 'webp' },
  image_crop:           { source_url: '', x: 0, y: 0, width: 400, height: 300 },
  image_responsive:     { source_url: '', widths: [320, 640, 1280] },
}

// ─── Single image mode ────────────────────────────────────────────────────────

function SingleSubmit({ type, onTaskCreated }) {
  const basePayload = DEFAULT_PAYLOADS[type] || { source_url: '' }
  const [payloadText, setPayloadText] = useState(JSON.stringify(basePayload, null, 2))
  const [priority, setPriority] = useState(0)
  const [maxRetries, setMaxRetries] = useState(3)
  const [file, setFile] = useState(null)
  const [preview, setPreview] = useState(null)
  const [uploadedUrl, setUploadedUrl] = useState(null)
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(false)

  // Reset when task type changes.
  useEffect(() => {
    setPayloadText(JSON.stringify(DEFAULT_PAYLOADS[type] || { source_url: '' }, null, 2))
    setUploadedUrl(null)
    if (preview) URL.revokeObjectURL(preview)
    setFile(null)
    setPreview(null)
    setError(null)
  }, [type])

  useEffect(() => () => { if (preview) URL.revokeObjectURL(preview) }, [preview])

  const onDrop = useCallback((accepted) => {
    if (!accepted.length) return
    if (preview) URL.revokeObjectURL(preview)
    setFile(accepted[0])
    setPreview(URL.createObjectURL(accepted[0]))
    setUploadedUrl(null)
    setError(null)
  }, [preview])

  const { getRootProps, getInputProps, isDragActive } = useDropzone({
    onDrop, multiple: false, accept: { 'image/*': [] },
  })

  const handleUpload = async () => {
    if (!file) return
    setUploading(true)
    setError(null)
    try {
      const resp = await uploadFile(file)
      const url = resp.data.url
      setUploadedUrl(url)
      try {
        const obj = JSON.parse(payloadText)
        obj.source_url = url
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
    try { payloadObj = JSON.parse(payloadText) }
    catch (_) { setError('Payload JSON is invalid'); return }
    if (!payloadObj.source_url) {
      setError('Upload an image first or provide source_url in the payload.')
      return
    }
    try {
      await createTask({ type, payload: payloadObj, priority, max_retries: maxRetries })
      if (preview) URL.revokeObjectURL(preview)
      setFile(null); setPreview(null); setUploadedUrl(null)
      setPayloadText(JSON.stringify(DEFAULT_PAYLOADS[type] || { source_url: '' }, null, 2))
      setPriority(0); setMaxRetries(3)
      setSuccess(true)
      setTimeout(() => setSuccess(false), 3000)
      onTaskCreated?.()
    } catch (err) {
      setError(err.response?.data?.error || err.message)
    }
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {/* Drop zone */}
      <div
        {...getRootProps()}
        className={clsx(
          'border-2 border-dashed rounded-xl p-5 cursor-pointer transition-colors',
          isDragActive
            ? 'border-indigo-500 bg-indigo-500/10'
            : 'border-slate-700 bg-slate-800/50 hover:border-slate-600'
        )}
      >
        <input {...getInputProps()} />
        <div className="flex items-center gap-4">
          {preview ? (
            <img src={preview} alt="preview" className="w-14 h-14 rounded-lg object-cover flex-shrink-0 ring-1 ring-slate-600" />
          ) : (
            <div className="w-14 h-14 rounded-lg bg-slate-700 flex items-center justify-center flex-shrink-0">
              <svg className="w-6 h-6 text-slate-500" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5}
                  d="M4 16l4.586-4.586a2 2 0 012.828 0L16 16m-2-2l1.586-1.586a2 2 0 012.828 0L20 14m-6-6h.01M6 20h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z"/>
              </svg>
            </div>
          )}
          <div>
            <p className="text-sm font-medium text-slate-300">
              {isDragActive ? 'Drop image here…' : 'Drag & drop or click to browse'}
            </p>
            <p className="text-xs text-slate-500 mt-0.5">PNG, JPG, WEBP — max 10 MB</p>
            {file && <p className="text-xs text-indigo-400 mt-1">{file.name}</p>}
          </div>
        </div>
      </div>

      {/* Upload / remove controls */}
      {file && !uploadedUrl && (
        <div className="flex gap-2">
          <button type="button" onClick={handleUpload} disabled={uploading}
            className="px-4 py-2 bg-indigo-600 hover:bg-indigo-500 disabled:bg-slate-700 disabled:cursor-not-allowed text-white text-sm rounded-lg transition-colors">
            {uploading ? 'Uploading…' : 'Upload'}
          </button>
          <button type="button" onClick={() => { if (preview) URL.revokeObjectURL(preview); setFile(null); setPreview(null) }}
            className="px-3 py-2 bg-slate-700 hover:bg-slate-600 text-slate-300 text-sm rounded-lg transition-colors">
            Remove
          </button>
        </div>
      )}
      {uploadedUrl && (
        <div className="flex items-center gap-2 text-sm text-emerald-400 bg-emerald-500/10 border border-emerald-500/20 rounded-lg px-3 py-2">
          <svg className="w-4 h-4 flex-shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 13l4 4L19 7"/>
          </svg>
          Uploaded successfully
        </div>
      )}

      {/* Payload editor */}
      <div>
        <label className="block text-xs font-medium text-slate-400 mb-1.5">Payload JSON</label>
        <textarea value={payloadText} onChange={(e) => setPayloadText(e.target.value)} rows={4}
          className="w-full bg-slate-800 border border-slate-700 text-slate-200 text-xs font-mono rounded-xl px-3 py-2.5 focus:outline-none focus:ring-1 focus:ring-indigo-500 resize-none"/>
      </div>

      {/* Priority / retries */}
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-xs font-medium text-slate-400 mb-1.5">Priority</label>
          <input type="number" value={priority} onChange={(e) => setPriority(parseInt(e.target.value || 0))}
            className="w-full bg-slate-800 border border-slate-700 text-slate-200 text-sm rounded-lg px-3 py-2 focus:outline-none focus:ring-1 focus:ring-indigo-500"/>
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-400 mb-1.5">Max Retries</label>
          <input type="number" value={maxRetries} onChange={(e) => setMaxRetries(parseInt(e.target.value || 0))}
            className="w-full bg-slate-800 border border-slate-700 text-slate-200 text-sm rounded-lg px-3 py-2 focus:outline-none focus:ring-1 focus:ring-indigo-500"/>
        </div>
      </div>

      {error && <p className="text-sm text-red-400 bg-red-500/10 border border-red-500/20 rounded-lg px-3 py-2">{error}</p>}
      {success && (
        <p className="text-sm text-emerald-400 bg-emerald-500/10 border border-emerald-500/20 rounded-lg px-3 py-2">
          Task queued successfully!
        </p>
      )}

      <button type="submit"
        className="w-full py-2.5 bg-indigo-600 hover:bg-indigo-500 text-white text-sm font-semibold rounded-xl transition-colors shadow">
        Submit Task
      </button>
    </form>
  )
}

// ─── Batch mode ───────────────────────────────────────────────────────────────

function BatchSubmit({ type, onTaskCreated }) {
  const [files, setFiles] = useState([])   // [{ file, preview, url, uploading, error }]
  const [priority, setPriority] = useState(0)
  const [maxRetries, setMaxRetries] = useState(3)
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState(null)
  const [success, setSuccess] = useState(false)

  useEffect(() => () => files.forEach((f) => f.preview && URL.revokeObjectURL(f.preview)), [])

  const onDrop = useCallback((accepted) => {
    const slots = BATCH_LIMIT - files.length
    if (slots <= 0) return
    const toAdd = accepted.slice(0, slots).map((file) => ({
      file, preview: URL.createObjectURL(file), url: null, uploading: false, error: null,
    }))
    setFiles((prev) => [...prev, ...toAdd])
  }, [files])

  const { getRootProps, getInputProps, isDragActive } = useDropzone({
    onDrop, multiple: true, accept: { 'image/*': [] },
    disabled: files.length >= BATCH_LIMIT,
  })

  const uploadOne = async (index) => {
    setFiles((prev) => prev.map((f, i) => i === index ? { ...f, uploading: true, error: null } : f))
    try {
      const resp = await uploadFile(files[index].file)
      setFiles((prev) => prev.map((f, i) => i === index ? { ...f, uploading: false, url: resp.data.url } : f))
    } catch (err) {
      setFiles((prev) => prev.map((f, i) =>
        i === index ? { ...f, uploading: false, error: err.response?.data?.error || err.message } : f
      ))
    }
  }

  const uploadAll = () => {
    files.forEach((f, i) => { if (!f.url && !f.uploading) uploadOne(i) })
  }

  const removeFile = (index) => {
    const f = files[index]
    if (f.preview) URL.revokeObjectURL(f.preview)
    setFiles((prev) => prev.filter((_, i) => i !== index))
  }

  const handleSubmit = async (e) => {
    e.preventDefault()
    setSubmitError(null)
    if (files.length === 0) { setSubmitError('Add at least one image.'); return }
    const notUploaded = files.filter((f) => !f.url)
    if (notUploaded.length) {
      setSubmitError(`${notUploaded.length} image(s) not yet uploaded. Click "Upload All" first.`)
      return
    }
    const base = DEFAULT_PAYLOADS[type] || {}
    const payloads = files.map((f) => ({ ...base, source_url: f.url }))
    setSubmitting(true)
    try {
      await createBatch({ type, payloads, priority, max_retries: maxRetries })
      files.forEach((f) => f.preview && URL.revokeObjectURL(f.preview))
      setFiles([])
      setSuccess(true)
      setTimeout(() => setSuccess(false), 3000)
      onTaskCreated?.()
    } catch (err) {
      setSubmitError(err.response?.data?.error || err.message)
    } finally {
      setSubmitting(false)
    }
  }

  const allUploaded = files.length > 0 && files.every((f) => f.url)
  const anyUploading = files.some((f) => f.uploading)
  const atLimit = files.length >= BATCH_LIMIT

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {/* Limit notice */}
      <div className="flex items-center gap-2 bg-amber-500/10 border border-amber-500/20 rounded-xl px-3 py-2.5">
        <svg className="w-4 h-4 text-amber-400 flex-shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
            d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/>
        </svg>
        <span className="text-xs text-amber-300">
          Max <strong>{BATCH_LIMIT} images</strong> per batch — each up to 10 MB.
          {files.length > 0 && <> &nbsp;{files.length}/{BATCH_LIMIT} selected.</>}
        </span>
      </div>

      {/* Drop zone */}
      <div
        {...getRootProps()}
        className={clsx(
          'border-2 border-dashed rounded-xl p-5 text-center transition-colors',
          atLimit
            ? 'border-slate-700 bg-slate-800/30 cursor-not-allowed opacity-50'
            : isDragActive
              ? 'border-indigo-500 bg-indigo-500/10 cursor-copy'
              : 'border-slate-700 bg-slate-800/50 hover:border-slate-600 cursor-pointer'
        )}
      >
        <input {...getInputProps()} />
        <svg className="w-8 h-8 text-slate-600 mx-auto mb-2" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5}
            d="M7 16a4 4 0 01-.88-7.903A5 5 0 1115.9 6L16 6a5 5 0 011 9.9M15 13l-3-3m0 0l-3 3m3-3v12"/>
        </svg>
        <p className="text-sm text-slate-400">
          {atLimit ? `Limit reached (${BATCH_LIMIT}/${BATCH_LIMIT})` : isDragActive ? 'Drop images here…' : 'Drag & drop images, or click to browse'}
        </p>
        <p className="text-xs text-slate-600 mt-0.5">Multiple files — PNG, JPG, WEBP</p>
      </div>

      {/* File list */}
      {files.length > 0 && (
        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <span className="text-xs text-slate-400">{files.length} image{files.length !== 1 ? 's' : ''} selected</span>
            {!allUploaded && (
              <button type="button" onClick={uploadAll} disabled={anyUploading}
                className="text-xs px-3 py-1.5 bg-indigo-600 hover:bg-indigo-500 disabled:bg-slate-700 disabled:cursor-not-allowed text-white rounded-lg transition-colors">
                {anyUploading ? 'Uploading…' : 'Upload All'}
              </button>
            )}
          </div>

          <div className="max-h-48 overflow-y-auto space-y-1.5 pr-1">
            {files.map((entry, i) => (
              <div key={i} className="flex items-center gap-3 bg-slate-800 border border-slate-700 rounded-xl px-3 py-2">
                <img src={entry.preview} alt={entry.file.name}
                  className="w-9 h-9 object-cover rounded-lg flex-shrink-0 ring-1 ring-slate-600"/>
                <div className="flex-1 min-w-0">
                  <p className="text-xs text-slate-300 truncate">{entry.file.name}</p>
                  {entry.url && <p className="text-xs text-emerald-400">Uploaded</p>}
                  {entry.uploading && <p className="text-xs text-indigo-400 animate-pulse">Uploading…</p>}
                  {entry.error && <p className="text-xs text-red-400 truncate">{entry.error}</p>}
                </div>
                <div className="flex items-center gap-1.5 flex-shrink-0">
                  {!entry.url && !entry.uploading && (
                    <button type="button" onClick={() => uploadOne(i)}
                      className="text-xs px-2 py-1 bg-slate-700 hover:bg-indigo-600 text-slate-300 hover:text-white rounded-lg transition-colors">
                      Upload
                    </button>
                  )}
                  {entry.url && (
                    <svg className="w-4 h-4 text-emerald-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M5 13l4 4L19 7"/>
                    </svg>
                  )}
                  <button type="button" onClick={() => removeFile(i)}
                    className="text-xs px-2 py-1 bg-slate-700 hover:bg-red-600/50 text-slate-400 hover:text-red-300 rounded-lg transition-colors">
                    ✕
                  </button>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Priority / retries */}
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-xs font-medium text-slate-400 mb-1.5">Priority</label>
          <input type="number" value={priority} onChange={(e) => setPriority(parseInt(e.target.value || 0))}
            className="w-full bg-slate-800 border border-slate-700 text-slate-200 text-sm rounded-lg px-3 py-2 focus:outline-none focus:ring-1 focus:ring-indigo-500"/>
        </div>
        <div>
          <label className="block text-xs font-medium text-slate-400 mb-1.5">Max Retries</label>
          <input type="number" value={maxRetries} onChange={(e) => setMaxRetries(parseInt(e.target.value || 0))}
            className="w-full bg-slate-800 border border-slate-700 text-slate-200 text-sm rounded-lg px-3 py-2 focus:outline-none focus:ring-1 focus:ring-indigo-500"/>
        </div>
      </div>

      {submitError && <p className="text-sm text-red-400 bg-red-500/10 border border-red-500/20 rounded-lg px-3 py-2">{submitError}</p>}
      {success && (
        <p className="text-sm text-emerald-400 bg-emerald-500/10 border border-emerald-500/20 rounded-lg px-3 py-2">
          Batch of {BATCH_LIMIT <= 5 ? files.length || BATCH_LIMIT : BATCH_LIMIT} tasks queued!
        </p>
      )}

      <button type="submit" disabled={submitting || files.length === 0}
        className="w-full py-2.5 bg-indigo-600 hover:bg-indigo-500 disabled:bg-slate-700 disabled:cursor-not-allowed text-white text-sm font-semibold rounded-xl transition-colors shadow">
        {submitting ? 'Submitting…' : `Submit Batch (${files.length} image${files.length !== 1 ? 's' : ''})`}
      </button>
    </form>
  )
}

// ─── Wrapper ──────────────────────────────────────────────────────────────────

export default function SubmitCard({ onTaskCreated }) {
  const [type, setType] = useState('image_resize')
  const [batchMode, setBatchMode] = useState(false)

  return (
    <div className="bg-slate-900 border border-slate-800 rounded-2xl p-5">
      {/* Card header */}
      <div className="flex items-center justify-between mb-5">
        <h3 className="text-sm font-semibold text-white">Submit Task</h3>

        {/* Batch mode toggle */}
        <button
          type="button"
          onClick={() => setBatchMode((v) => !v)}
          className={clsx(
            'flex items-center gap-2 text-xs font-medium px-3 py-1.5 rounded-lg border transition-all',
            batchMode
              ? 'bg-indigo-600/20 border-indigo-500/50 text-indigo-400'
              : 'bg-slate-800 border-slate-700 text-slate-400 hover:text-white'
          )}
        >
          <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
              d="M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10"/>
          </svg>
          {batchMode ? `Batch (max ${BATCH_LIMIT})` : 'Single'}
        </button>
      </div>

      {/* Task type pills */}
      <div className="mb-5">
        <label className="block text-xs font-medium text-slate-400 mb-2">Task Type</label>
        <div className="flex flex-wrap gap-1.5">
          {TASK_TYPES.map((t) => (
            <button
              key={t.value}
              type="button"
              onClick={() => setType(t.value)}
              className={clsx(
                'px-3 py-1 text-xs rounded-lg border transition-all',
                type === t.value
                  ? 'bg-indigo-600 border-indigo-500 text-white font-medium'
                  : 'bg-slate-800 border-slate-700 text-slate-400 hover:text-white hover:border-slate-600'
              )}
            >
              {t.label}
            </button>
          ))}
        </div>
      </div>

      {/* Divider */}
      <div className="border-t border-slate-800 mb-5" />

      {batchMode
        ? <BatchSubmit key={`batch-${type}`} type={type} onTaskCreated={onTaskCreated} />
        : <SingleSubmit key={`single-${type}`} type={type} onTaskCreated={onTaskCreated} />
      }
    </div>
  )
}
