// src/components/Dashboard.jsx
// Two-column layout: left = task submission (single + batch via SubmitCard),
// right = live metrics + scrollable task list.

import React, { useState, useEffect } from 'react'
import { fetchTasks as apiFetchTasks, fetchMetrics as apiFetchMetrics } from '../api/api'
import TaskList from './TaskList'
import MetricsCard from './MetricsCard'
import SubmitCard from './SubmitCard'

function Dashboard() {
  const [tasks, setTasks] = useState([])
  const [metrics, setMetrics] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)

  // Fetch tasks from the API and update local state.
  const fetchTasks = async () => {
    try {
      const token = localStorage.getItem('token')
      if (!token) {
        setError('No authentication token found. Please login again.')
        setLoading(false)
        return
      }
      const response = await apiFetchTasks(1, 50, '')
      setTasks(response.data.tasks || [])
      setError(null)
      setLoading(false)
    } catch (err) {
      const msg = err.response?.data?.error || err.message
      if (err.response?.status === 401 || msg.includes('token') || msg.includes('unauthorized')) {
        localStorage.removeItem('token')
        window.location.reload()
      } else {
        setError(msg)
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
        // Metrics require admin role — show safe defaults instead of crashing.
        setMetrics({ queued_tasks: 0, active_workers: 0, avg_processing_time: 0 })
      }
    }
  }

  // Poll tasks and metrics every 5 seconds so the table stays live.
  useEffect(() => {
    fetchTasks()
    fetchMetrics()
    const interval = setInterval(() => {
      fetchTasks()
      fetchMetrics()
    }, 5000)
    return () => clearInterval(interval)
  }, [])

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
