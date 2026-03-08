// src/components/TaskRow.jsx

import React from 'react'
import clsx from 'clsx'

const Badge = ({ status }) => {
  const map = {
    completed: 'bg-green-100 text-green-800',
    processing: 'bg-blue-100 text-blue-800',
    failed: 'bg-red-100 text-red-800',
    queued: 'bg-yellow-100 text-yellow-800',
    default: 'bg-gray-100 text-gray-800',
  }
  return (
    <span className={clsx('px-3 py-1 rounded-full text-sm font-medium', map[status] || map.default)}>
      {status}
    </span>
  )
}

export default function TaskRow({ task, onOpen }) {
  const idShort = task.id?.slice(0, 8) ?? '—'
  // Handle both camelCase and snake_case date fields
  const createdAt = task.created_at || task.createdAt
  
  return (
    <tr className="hover:bg-gray-50 cursor-pointer" onClick={() => onOpen?.(task)}>
      <td className="py-3 px-4 text-sm text-gray-700">{idShort}…</td>
      <td className="py-3 px-4 text-sm text-gray-600">{task.type}</td>
      <td className="py-3 px-4">
        <Badge status={task.status} />
      </td>
      <td className="py-3 px-4 text-sm text-gray-700">{task.priority ?? '-'}</td>
      <td className="py-3 px-4 text-sm text-gray-600">
        {createdAt ? new Date(createdAt).toLocaleString() : '-'}
      </td>
    </tr>
  )
}

