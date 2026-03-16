/**
 * src/components/Dashboard.jsx
 *
 * Two-column layout: left = task submission (single + batch via SubmitCard),
 * right = live metrics + scrollable task list.
 *
 * Data fetching strategy:
 *   This component uses polling (setInterval, every 5 s) rather than a WebSocket
 *   subscription for simplicity. The backend does expose a WebSocket endpoint at
 *   /api/v1/ws that pushes task updates in real-time, but integrating it here would
 *   require reconnect logic, message parsing, and partial state merging. Polling is
 *   predictable, easy to debug, and sufficient for a demo tool where 5-second staleness
 *   is acceptable.
 *
 * Token expiry handling:
 *   If the server returns HTTP 401 (token expired or blacklisted), the handler removes
 *   the token from localStorage and reloads the page, which sends the user back to the
 *   Login screen (App.jsx checks for a stored token on mount).
 *
 * State:
 *   tasks   — current page of task objects (up to 50 most recent).
 *   metrics — { queued_tasks, active_workers, avg_processing_time } or null until loaded.
 *   loading — true only on the very first load (hides the skeleton spinner).
 *   error   — non-null string shows a dismissable red banner.
 */

import React, { useState, useEffect } from 'react'
import { fetchTasks as apiFetchTasks, fetchMetrics as apiFetchMetrics } from '../api/api'
import TaskList from './TaskList'
import MetricsCard from './MetricsCard'
import SubmitCard from './SubmitCard'

function Dashboard() {
  const [tasks, setTasks] = useState([])      // list of task objects from GET /tasks
  const [metrics, setMetrics] = useState(null) // system metrics object or null
  const [loading, setLoading] = useState(true) // initial load skeleton
  const [error, setError] = useState(null)     // error banner message

  // Fetch tasks from the API and update local state.
  const fetchTasks = async () => {
    try {
      const token = localStorage.getItem('token')
      if (!token) {
        // No token means the user was never logged in or it was cleared externally.
        setError('No authentication token found. Please login again.')
        setLoading(false)
        return
      }
      const response = await apiFetchTasks(1, 50, '') // page 1, up to 50 tasks, no filter
      setTasks(response.data.tasks || []) // fallback to empty array if API returns null
      setError(null)
      setLoading(false)
    } catch (err) {
      const msg = err.response?.data?.error || err.message
      if (err.response?.status === 401 || msg.includes('token') || msg.includes('unauthorized')) {
        // Token is expired or blacklisted — clear it and reload to show the login screen.
        localStorage.removeItem('token')
        window.location.reload()
      } else {
        setError(msg) // show other errors (e.g. network failure) in the banner
      }
      setLoading(false)
    }
  }

  // Fetch system-wide queue and worker metrics.
  const fetchMetrics = async () => {
    try {
      const response = await apiFetchMetrics()
      setMetrics(response.data)
    } catch (err) {
      if (err.response?.status === 403) {
        // Metrics endpoint requires admin role — non-admin users get 403.
        // Show safe zero defaults instead of crashing or hiding the MetricsCard entirely.
        setMetrics({ queued_tasks: 0, active_workers: 0, avg_processing_time: 0 })
      }
      // Other errors (network, 500) are silently ignored — metrics are non-critical.
    }
  }

  // Fetch immediately on mount, then poll every 5 seconds.
  // The cleanup function (return () => clearInterval) stops the interval when this
  // component unmounts (e.g. user navigates away), preventing memory leaks.
  useEffect(() => {
    fetchTasks()
    fetchMetrics()
    const interval = setInterval(() => {
      fetchTasks()
      fetchMetrics()
    }, 5000) // 5-second polling interval
    return () => clearInterval(interval) // cleanup: stop polling when component unmounts
  }, []) // empty deps: run once on mount (equivalent to componentDidMount)

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-gray-500 text-lg animate-pulse">Loading…</div>
      </div>
    )
  }

  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-6 h-full overflow-hidden">

      {/* ── LEFT: task submission ── */}
      <div className="overflow-y-auto">
        {/* SubmitCard handles both single-image and batch modes via its own toggle */}
        <SubmitCard onTaskCreated={fetchTasks} />
      </div>

      {/* ── RIGHT: metrics + task list ── */}
      <div className="space-y-4 overflow-y-auto h-full">
        {error && (
          <div className="bg-red-100 border border-red-400 text-red-700 px-4 py-3 rounded relative">
            <strong className="font-bold">Error: </strong>
            <span>{error}</span>
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
