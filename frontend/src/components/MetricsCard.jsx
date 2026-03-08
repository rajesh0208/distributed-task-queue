// src/components/MetricsCard.jsx

import React from 'react'

export default function MetricsCard({ metrics }) {
  if (!metrics) return null

  // Map API response fields to component props
  // API returns: queued_tasks, active_workers, avg_processing_time
  const queueDepth = metrics.queued_tasks ?? metrics.queuedTasks ?? '-'
  const workersActive = metrics.active_workers ?? metrics.activeWorkers ?? '-'
  const avgLatency = metrics.avg_processing_time ?? metrics.avgProcessingTime
  const avgLatencyMs = avgLatency ? `${Math.round(avgLatency)} ms` : '-'

  return (
    <div className="grid grid-cols-3 gap-4">
      <div className="bg-white p-4 rounded-lg shadow">
        <div className="text-sm text-gray-500">Queue Depth</div>
        <div className="text-2xl font-semibold">{queueDepth}</div>
      </div>
      <div className="bg-white p-4 rounded-lg shadow">
        <div className="text-sm text-gray-500">Workers Active</div>
        <div className="text-2xl font-semibold">{workersActive}</div>
      </div>
      <div className="bg-white p-4 rounded-lg shadow">
        <div className="text-sm text-gray-500">Avg Latency</div>
        <div className="text-2xl font-semibold">{avgLatencyMs}</div>
      </div>
    </div>
  )
}

