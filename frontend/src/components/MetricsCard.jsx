// src/components/MetricsCard.jsx
// Three stat cards showing live queue and worker metrics.

import React from 'react'

export default function MetricsCard({ metrics }) {
  if (!metrics) return null

  const queued = metrics.queued_tasks ?? metrics.queuedTasks ?? 0
  const workers = metrics.active_workers ?? metrics.activeWorkers ?? 0
  const avgMs = metrics.avg_processing_time ?? metrics.avgProcessingTime ?? 0

  const stats = [
    {
      label: 'Queue Depth',
      value: queued,
      icon: (
        <svg className="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
            d="M4 6h16M4 10h16M4 14h16M4 18h16"/>
        </svg>
      ),
      color: 'text-amber-400',
      bg: 'bg-amber-400/10',
    },
    {
      label: 'Workers Active',
      value: workers,
      icon: (
        <svg className="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
            d="M9 3H5a2 2 0 00-2 2v4m6-6h10a2 2 0 012 2v4M9 3v18m0 0h10a2 2 0 002-2V9M9 21H5a2 2 0 01-2-2V9m0 0h18"/>
        </svg>
      ),
      color: 'text-emerald-400',
      bg: 'bg-emerald-400/10',
    },
    {
      label: 'Avg Latency',
      value: avgMs ? `${Math.round(avgMs)} ms` : '— ms',
      icon: (
        <svg className="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
            d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/>
        </svg>
      ),
      color: 'text-indigo-400',
      bg: 'bg-indigo-400/10',
    },
  ]

  return (
    <div className="grid grid-cols-3 gap-3">
      {stats.map((s) => (
        <div key={s.label} className="bg-slate-900 border border-slate-800 rounded-xl px-4 py-3 flex items-center gap-3">
          <div className={`${s.bg} ${s.color} p-2 rounded-lg flex-shrink-0`}>{s.icon}</div>
          <div>
            <div className="text-xs text-slate-500">{s.label}</div>
            <div className="text-lg font-semibold text-white leading-tight">{s.value}</div>
          </div>
        </div>
      ))}
    </div>
  )
}
