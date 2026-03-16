/**
 * src/components/TaskRow.jsx
 *
 * A single <tr> inside TaskTable's <tbody>. Clicking the row calls onOpen(task)
 * to open the SlideOver detail panel for that task.
 *
 * Badge component:
 *   Renders a pill-shaped status label with colour-coded background.
 *   Uses `clsx` (a lightweight className utility) to combine base Tailwind classes
 *   with the per-status colour class from the `map` lookup. Falls back to the
 *   "default" gray style for any unknown status values (e.g. future additions).
 *
 * Props:
 *   task   — task object from the API (snake_case or camelCase fields).
 *   onOpen — callback called with the task object when the row is clicked.
 */

import React from 'react'
import clsx from 'clsx' // utility for conditional className concatenation

/**
 * Badge — renders a coloured pill for the task status.
 * @param {{ status: string }} props
 */
const Badge = ({ status }) => {
  const map = {
    completed:  'bg-green-100 text-green-800',   // terminal success
    processing: 'bg-blue-100 text-blue-800',     // in-progress
    failed:     'bg-red-100 text-red-800',       // terminal failure
    queued:     'bg-yellow-100 text-yellow-800', // waiting in queue
    default:    'bg-gray-100 text-gray-800',     // unknown/future status
  }
  return (
    <span className={clsx('px-3 py-1 rounded-full text-sm font-medium', map[status] || map.default)}>
      {status}
    </span>
  )
}

export default function TaskRow({ task, onOpen }) {
  const idShort = task.id?.slice(0, 8) ?? '—' // show only the first 8 chars of the UUID to save space
  // The API can return either snake_case (json:"created_at") or camelCase depending
  // on the serialisation path. Handle both to be resilient to API changes.
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

