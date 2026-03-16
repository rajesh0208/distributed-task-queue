/**
 * src/components/SlideOver.jsx
 *
 * A slide-in panel that displays the full details of a selected task.
 * Rendered when a user clicks a row in TaskTable.
 *
 * Layout:
 *   - Fixed full-screen overlay with a semi-transparent black backdrop.
 *   - A white panel slides in from the right edge (max-w-lg).
 *   - Header: "Task Details" + close button.
 *   - Body: scrollable list of labelled fields (ID, type, status, payload, error, etc.).
 *
 * Slide animation:
 *   translate-x-0 (open=true) / translate-x-full (open=false) combined with
 *   Tailwind's `transition-transform duration-300` CSS transition.
 *   React renders this component whenever `open` changes, and the CSS handles
 *   the visual slide-in/out. When `open` is false AND `task` is null, we
 *   return null immediately to avoid rendering an invisible overlay that still
 *   intercepts pointer events.
 *
 * Payload display:
 *   task.payload can be a raw JSON string (stored as TEXT in PostgreSQL) or
 *   a JS object (if the axios response already parsed nested JSON). Both are
 *   handled: strings are displayed as-is; objects are pretty-printed with
 *   JSON.stringify.
 *
 * Props:
 *   open    — boolean controlling visibility.
 *   task    — the task object to display; null means "no task selected".
 *   onClose — called when the user clicks the backdrop or the X button.
 */

import React from 'react'
import clsx from 'clsx'

export default function SlideOver({ open, task, onClose }) {
  // Guard: if not open or no task selected, render nothing so the overlay doesn't
  // block pointer events on the dashboard behind it.
  if (!open || !task) return null

  return (
    <div className="fixed inset-0 z-50 overflow-hidden">
      {/* Backdrop */}
      <div
        className="fixed inset-0 bg-black bg-opacity-50 transition-opacity"
        onClick={onClose}
      />

      {/* Slide panel */}
      <div
        className={clsx(
          'fixed right-0 top-0 h-full w-full max-w-lg bg-white shadow-xl transform transition-transform duration-300 ease-in-out',
          open ? 'translate-x-0' : 'translate-x-full'
        )}
      >
        <div className="flex flex-col h-full">
          {/* Header */}
          <div className="flex items-center justify-between px-6 py-4 border-b border-gray-200">
            <h2 className="text-xl font-semibold">Task Details</h2>
            <button
              onClick={onClose}
              className="text-gray-400 hover:text-gray-600 transition-colors"
            >
              <svg
                className="w-6 h-6"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M6 18L18 6M6 6l12 12"
                />
              </svg>
            </button>
          </div>

          {/* Content */}
          <div className="flex-1 overflow-y-auto px-6 py-4">
            <div className="space-y-4">
              <div>
                <label className="text-sm font-medium text-gray-500">Task ID</label>
                <p className="mt-1 text-sm text-gray-900 font-mono">{task.id}</p>
              </div>

              <div>
                <label className="text-sm font-medium text-gray-500">Type</label>
                <p className="mt-1 text-sm text-gray-900">{task.type}</p>
              </div>

              <div>
                <label className="text-sm font-medium text-gray-500">Status</label>
                <p className="mt-1 text-sm text-gray-900">{task.status}</p>
              </div>

              <div>
                <label className="text-sm font-medium text-gray-500">Priority</label>
                <p className="mt-1 text-sm text-gray-900">{task.priority ?? '-'}</p>
              </div>

              {task.created_at || task.createdAt ? (
                <div>
                  <label className="text-sm font-medium text-gray-500">Created At</label>
                  <p className="mt-1 text-sm text-gray-900">
                    {new Date(task.created_at || task.createdAt).toLocaleString()}
                  </p>
                </div>
              ) : null}

              {task.completed_at || task.completedAt ? (
                <div>
                  <label className="text-sm font-medium text-gray-500">Completed At</label>
                  <p className="mt-1 text-sm text-gray-900">
                    {new Date(task.completed_at || task.completedAt).toLocaleString()}
                  </p>
                </div>
              ) : null}

              {task.payload && (
                <div>
                  <label className="text-sm font-medium text-gray-500">Payload</label>
                  <pre className="mt-1 p-3 bg-gray-50 rounded text-xs text-gray-900 overflow-x-auto">
                    {typeof task.payload === 'string'
                      ? task.payload
                      : JSON.stringify(task.payload, null, 2)}
                  </pre>
                </div>
              )}

              {task.error && (
                <div>
                  <label className="text-sm font-medium text-red-500">Error</label>
                  <p className="mt-1 text-sm text-red-600">{task.error}</p>
                </div>
              )}

              {task.worker_id || task.workerId ? (
                <div>
                  <label className="text-sm font-medium text-gray-500">Worker ID</label>
                  <p className="mt-1 text-sm text-gray-900 font-mono">
                    {task.worker_id || task.workerId}
                  </p>
                </div>
              ) : null}

              {task.processing_time || task.processingTime ? (
                <div>
                  <label className="text-sm font-medium text-gray-500">Processing Time</label>
                  <p className="mt-1 text-sm text-gray-900">
                    {task.processing_time || task.processingTime} ms
                  </p>
                </div>
              ) : null}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

