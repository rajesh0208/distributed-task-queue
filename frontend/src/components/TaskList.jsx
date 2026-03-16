/**
 * src/components/TaskList.jsx
 *
 * Task table with colour-coded status badges, an eye-button to preview the
 * processed image in a modal, and a download button for completed tasks.
 *
 * Component responsibilities:
 *   - Renders a styled HTML table of task rows, one row per task in the `tasks` prop.
 *   - STATUS_STYLES maps each task status to a Tailwind badge colour so engineers
 *     can instantly identify failed (red), processing (blue), and completed (green) tasks.
 *   - Preview modal (PreviewModal) renders the output image inline in a fixed overlay,
 *     allowing users to visually verify results without leaving the dashboard.
 *   - Download button: fetches the image as a Blob via the Fetch API and triggers a
 *     browser download. Falls back to window.open if the fetch fails (e.g. CORS issues).
 *
 * Why publicUrl() is necessary:
 *   Workers write result URLs using the Docker service name "api" (e.g.
 *   "http://api:8080/images/foo.jpg"). This URL is only resolvable inside the Docker
 *   network, not in the user's browser. publicUrl() rewrites the hostname to localhost
 *   so the browser's fetch/img src succeeds during local development.
 *   In production behind nginx, the image URL would already use the public domain.
 *
 * Props:
 *   tasks     — array of task objects from GET /api/v1/tasks.
 *   onRefresh — called when the user clicks the Refresh button.
 */

import React, { useState } from 'react'

/**
 * STATUS_STYLES — Tailwind class strings per task status.
 * Using a lookup map instead of conditional className logic keeps the JSX clean
 * and makes it easy to add a new status (e.g. "paused") without touching the JSX.
 */
const STATUS_STYLES = {
  completed: 'bg-emerald-500/15 text-emerald-400 ring-1 ring-emerald-500/30', // task finished successfully
  failed:    'bg-red-500/15 text-red-400 ring-1 ring-red-500/30',             // all retries exhausted
  processing:'bg-blue-500/15 text-blue-400 ring-1 ring-blue-500/30',          // worker is running it right now
  queued:    'bg-amber-500/15 text-amber-400 ring-1 ring-amber-500/30',        // waiting in the Redis Stream
  retrying:  'bg-purple-500/15 text-purple-400 ring-1 ring-purple-500/30',    // in exponential-backoff sleep
  cancelled: 'bg-slate-500/15 text-slate-400 ring-1 ring-slate-500/30',       // cancelled by user or admin
}

/**
 * publicUrl — converts a Docker-internal URL to one the browser can reach.
 *
 * Workers store output URLs using the Docker Compose service name ("api") as the
 * host. This is unreachable outside the Docker network. During local dev the
 * browser must use "localhost:8080" instead.
 *
 * The second transform handles a path pattern where the URL contains the image
 * filename directly under the API root (e.g. "localhost:8080/foo.jpg") and
 * normalises it to the "/images/" static route.
 *
 * @param {string|null} url
 * @returns {string|null}
 */
function publicUrl(url) {
  if (!url) return url
  let u = url.replace('http://api:8080', 'http://localhost:8080') // rewrite Docker internal host
  // If the URL points directly to a bare image filename (not under /images/), add the prefix.
  if (u.match(/^http:\/\/localhost:8080\/[^/]+\.(jpg|jpeg|png|gif|webp)$/i)) {
    u = `http://localhost:8080/images/${u.split('/').pop()}`
  }
  return u
}

/**
 * getOutputUrl — extracts the processed image URL from a task's result field.
 *
 * The `result` field can be:
 *   - null / undefined — task hasn't finished yet.
 *   - a JSON string    — stored as TEXT in PostgreSQL; needs JSON.parse.
 *   - a JS object      — already parsed by axios/JSON.parse.
 * Both snake_case (output_url) and camelCase (outputUrl) field names are handled
 * because the Go API serialises with json:"output_url" tags but some older responses
 * may use camelCase.
 *
 * @param {object} task
 * @returns {string|null}
 */
function getOutputUrl(task) {
  if (task.status !== 'completed') return null // only completed tasks have output URLs
  let result = task.result
  if (typeof result === 'string') {
    try { result = JSON.parse(result) } catch (_) { return null } // silently skip malformed JSON
  }
  return result?.output_url || result?.outputUrl || null
}

/**
 * downloadImage — fetches the processed image as a Blob and triggers a browser download.
 *
 * Why Blob + createObjectURL instead of window.open(url)?
 *   window.open opens the image in a new tab instead of downloading it. Programmatic
 *   download requires creating a temporary <a> element with the `download` attribute
 *   set to the desired filename. Attaching it to document.body ensures click() works
 *   in all browsers. revokeObjectURL frees the Blob URL after download.
 *
 *   Falls back to window.open if the fetch fails (e.g. the image has already been
 *   cleaned up from disk or the URL is not reachable from the browser).
 *
 * @param {object} task
 */
async function downloadImage(task) {
  const raw = getOutputUrl(task)
  if (!raw) return
  const url = publicUrl(raw) // translate Docker-internal URL to browser-reachable URL
  try {
    const res = await fetch(url)
    if (!res.ok) throw new Error(`${res.status}`) // non-2xx → trigger fallback
    const blob = await res.blob()
    // Derive file extension from MIME type (e.g. "image/webp" → "webp").
    // Falls back to the URL extension, then "jpg" if neither is available.
    const ext = blob.type.split('/')[1] || url.split('.').pop()?.split('?')[0] || 'jpg'
    const a = document.createElement('a')
    a.href = window.URL.createObjectURL(blob) // create a temporary in-memory URL for the blob
    a.download = `task-${task.id.substring(0, 8)}.${ext}` // suggested filename for the browser's save dialog
    document.body.appendChild(a) // must be in DOM for Firefox to trigger the download
    a.click()
    document.body.removeChild(a) // clean up the temporary element
    window.URL.revokeObjectURL(a.href) // free the memory used by the blob URL
  } catch (_) {
    window.open(url, '_blank') // fallback: open in new tab so user can save manually
  }
}

/**
 * fmtDate — formats an ISO timestamp into a short localised date+time string.
 * Returns '—' for null/undefined values (e.g. completed_at before a task finishes).
 *
 * @param {string|null} v - ISO 8601 timestamp string
 * @returns {string}
 */
function fmtDate(v) {
  if (!v) return '—'
  return new Date(v).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' })
}

// ─── Preview modal ────────────────────────────────────────────────────────────

function PreviewModal({ task, onClose }) {
  const raw = getOutputUrl(task)
  const url = publicUrl(raw)

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="bg-slate-900 border border-slate-700 rounded-2xl shadow-2xl max-w-2xl w-full mx-4 overflow-hidden"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Modal header */}
        <div className="flex items-center justify-between px-5 py-3.5 border-b border-slate-800">
          <div>
            <p className="text-sm font-semibold text-white">Processed Image</p>
            <p className="text-xs text-slate-500 mt-0.5">
              {task.type.replace('image_', '')} · {task.id.substring(0, 8)}…
            </p>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => downloadImage(task)}
              className="flex items-center gap-1.5 text-xs font-medium px-3 py-1.5 bg-indigo-600 hover:bg-indigo-500 text-white rounded-lg transition-colors"
            >
              <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
                  d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"/>
              </svg>
              Download
            </button>
            <button
              onClick={onClose}
              className="p-1.5 text-slate-400 hover:text-white bg-slate-800 hover:bg-slate-700 rounded-lg transition-colors"
            >
              <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12"/>
              </svg>
            </button>
          </div>
        </div>

        {/* Image */}
        <div className="p-5 flex items-center justify-center min-h-48 bg-slate-950/50">
          {url ? (
            <img
              src={url}
              alt="Processed output"
              className="max-h-[60vh] max-w-full rounded-xl object-contain"
              onError={(e) => {
                e.target.style.display = 'none'
                e.target.nextSibling.style.display = 'block'
              }}
            />
          ) : null}
          <p className="hidden text-sm text-slate-500">Could not load image. Try downloading it directly.</p>
        </div>

        {/* URL */}
        {url && (
          <div className="px-5 pb-4">
            <p className="text-xs text-slate-600 font-mono truncate">{url}</p>
          </div>
        )}
      </div>
    </div>
  )
}

// ─── Main component ───────────────────────────────────────────────────────────

export default function TaskList({ tasks, onRefresh }) {
  const [previewTask, setPreviewTask] = useState(null)

  return (
    <>
      <div className="bg-slate-900 border border-slate-800 rounded-2xl overflow-hidden">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-3.5 border-b border-slate-800">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold text-white">Tasks</h2>
            {tasks.length > 0 && (
              <span className="text-xs bg-slate-800 text-slate-400 rounded-full px-2 py-0.5">{tasks.length}</span>
            )}
          </div>
          <button
            onClick={onRefresh}
            className="flex items-center gap-1.5 text-xs text-slate-400 hover:text-white bg-slate-800 hover:bg-slate-700 px-3 py-1.5 rounded-lg transition-colors"
          >
            <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
                d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"/>
            </svg>
            Refresh
          </button>
        </div>

        {/* Table */}
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-slate-800">
                {['ID', 'Type', 'Status', 'Priority', 'Created', 'Completed', 'Actions'].map((h) => (
                  <th key={h} className="px-4 py-3 text-left text-xs font-medium text-slate-500 uppercase tracking-wider whitespace-nowrap">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-800/60">
              {tasks.length === 0 ? (
                <tr>
                  <td colSpan={7} className="px-4 py-12 text-center">
                    <div className="flex flex-col items-center gap-2 text-slate-600">
                      <svg className="w-8 h-8" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5}
                          d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2"/>
                      </svg>
                      <span className="text-sm">No tasks yet. Submit one on the left.</span>
                    </div>
                  </td>
                </tr>
              ) : (
                tasks.map((task) => {
                  const outputUrl = getOutputUrl(task)
                  const canPreview = !!outputUrl
                  return (
                    <tr key={task.id} className="hover:bg-slate-800/40 transition-colors group">
                      <td className="px-4 py-3 font-mono text-xs text-slate-400 whitespace-nowrap">
                        {task.id.substring(0, 8)}…
                      </td>
                      <td className="px-4 py-3 text-slate-300 whitespace-nowrap text-xs">
                        {task.type.replace('image_', '')}
                      </td>
                      <td className="px-4 py-3 whitespace-nowrap">
                        <span className={`inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium ${STATUS_STYLES[task.status] || STATUS_STYLES.cancelled}`}>
                          {task.status}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-slate-400 text-xs">{task.priority}</td>
                      <td className="px-4 py-3 text-slate-500 text-xs whitespace-nowrap">
                        {fmtDate(task.created_at || task.createdAt)}
                      </td>
                      <td className="px-4 py-3 text-slate-500 text-xs whitespace-nowrap">
                        {fmtDate(task.completed_at || task.completedAt)}
                      </td>
                      <td className="px-4 py-3 whitespace-nowrap">
                        {canPreview ? (
                          <div className="flex items-center gap-2">
                            {/* Preview (eye) button */}
                            <button
                              onClick={() => setPreviewTask(task)}
                              title="Preview image"
                              className="p-1.5 text-slate-400 hover:text-white bg-slate-800 hover:bg-slate-700 rounded-lg transition-colors"
                            >
                              <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
                                  d="M15 12a3 3 0 11-6 0 3 3 0 016 0z"/>
                                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
                                  d="M2.458 12C3.732 7.943 7.523 5 12 5c4.478 0 8.268 2.943 9.542 7-1.274 4.057-5.064 7-9.542 7-4.477 0-8.268-2.943-9.542-7z"/>
                              </svg>
                            </button>
                            {/* Download button */}
                            <button
                              onClick={() => downloadImage(task)}
                              title="Download image"
                              className="p-1.5 text-slate-400 hover:text-indigo-400 bg-slate-800 hover:bg-slate-700 rounded-lg transition-colors"
                            >
                              <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
                                  d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"/>
                              </svg>
                            </button>
                          </div>
                        ) : (
                          <span className="text-slate-700 text-xs">—</span>
                        )}
                      </td>
                    </tr>
                  )
                })
              )}
            </tbody>
          </table>
        </div>
      </div>

      {/* Preview modal */}
      {previewTask && (
        <PreviewModal task={previewTask} onClose={() => setPreviewTask(null)} />
      )}
    </>
  )
}
