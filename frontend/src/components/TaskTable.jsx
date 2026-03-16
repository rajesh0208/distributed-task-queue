/**
 * src/components/TaskTable.jsx
 *
 * Alternative task table with client-side search and status filter controls.
 * This component is the older-style version (white card, lighter theme) used
 * in the DashboardGraphQL view. The main dashboard uses TaskList instead.
 *
 * Why useMemo for filtering?
 *   The `tasks` array can contain up to 100 entries (page size). Without memoisation,
 *   the filter runs on every render including unrelated state updates (e.g. the
 *   search input typing). useMemo caches the result and only re-runs when `tasks`,
 *   `search`, or `statusFilter` actually change.
 *
 * Props:
 *   tasks     — full unfiltered array of task objects.
 *   onRefresh — called when the Refresh button is clicked.
 *   onOpen    — called with the selected task object when a row is clicked;
 *               used to open the SlideOver detail panel.
 */

import React, { useMemo, useState } from 'react'
import TaskRow from './TaskRow'

export default function TaskTable({ tasks = [], onRefresh = () => {}, onOpen }) {
  const [search, setSearch] = useState('')          // free-text search box value
  const [statusFilter, setStatusFilter] = useState('any') // dropdown: "any" | "queued" | "processing" | "completed" | "failed"

  /**
   * filtered — the subset of tasks that match both the status filter and search query.
   * Memoised so it is only recomputed when the inputs change.
   */
  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase() // normalise search query once
    return tasks.filter((t) => {
      // Status filter: skip tasks that don't match the selected status.
      if (statusFilter !== 'any' && t.status !== statusFilter) return false
      // If search box is empty, all tasks pass the text filter.
      if (!q) return true
      // Match against task ID prefix, type string, or status string.
      return (
        (t.id && t.id.toLowerCase().includes(q)) ||
        (t.type && t.type.toLowerCase().includes(q)) ||
        (t.status && t.status.toLowerCase().includes(q))
      )
    })
  }, [tasks, search, statusFilter]) // recompute only when these change

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

