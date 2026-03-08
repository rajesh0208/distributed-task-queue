// src/components/TaskTable.jsx

import React, { useMemo, useState } from 'react'
import TaskRow from './TaskRow'

export default function TaskTable({ tasks = [], onRefresh = () => {}, onOpen }) {
  const [search, setSearch] = useState('')
  const [statusFilter, setStatusFilter] = useState('any')

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return tasks.filter((t) => {
      if (statusFilter !== 'any' && t.status !== statusFilter) return false
      if (!q) return true
      return (
        (t.id && t.id.toLowerCase().includes(q)) ||
        (t.type && t.type.toLowerCase().includes(q)) ||
        (t.status && t.status.toLowerCase().includes(q))
      )
    })
  }, [tasks, search, statusFilter])

  return (
    <div className="bg-white rounded-xl shadow p-6">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-xl font-semibold">Tasks</h3>

        <div className="flex items-center gap-3">
          <input
            type="text"
            placeholder="Search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="border rounded-md p-2 text-sm"
          />
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
            className="border rounded-md p-2 text-sm"
          >
            <option value="any">Any Status</option>
            <option value="queued">queued</option>
            <option value="processing">processing</option>
            <option value="completed">completed</option>
            <option value="failed">failed</option>
          </select>
          <button onClick={onRefresh} className="px-3 py-2 bg-gray-100 rounded-md">
            Refresh
          </button>
        </div>
      </div>

      <div className="overflow-x-auto">
        <table className="w-full table-auto text-left">
          <thead className="text-sm text-gray-500">
            <tr>
              <th className="py-2 px-4">ID</th>
              <th className="py-2 px-4">TYPE</th>
              <th className="py-2 px-4">STATUS</th>
              <th className="py-2 px-4">PRIORITY</th>
              <th className="py-2 px-4">CREATED</th>
            </tr>
          </thead>
          <tbody>
            {filtered.map((task) => (
              <TaskRow key={task.id} task={task} onOpen={onOpen} />
            ))}
            {filtered.length === 0 && (
              <tr>
                <td colSpan="5" className="p-6 text-center text-gray-400">
                  No tasks
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}

