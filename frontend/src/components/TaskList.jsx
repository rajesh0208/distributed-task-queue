import React from 'react'

function TaskList({ tasks, onRefresh }) {
  const getStatusColor = (status) => {
    switch (status) {
      case 'completed':
        return 'bg-green-100 text-green-800'
      case 'failed':
        return 'bg-red-100 text-red-800'
      case 'processing':
        return 'bg-blue-100 text-blue-800'
      case 'queued':
        return 'bg-yellow-100 text-yellow-800'
      default:
        return 'bg-gray-100 text-gray-800'
    }
  }

  return (
    <div className="bg-white shadow rounded-lg">
      <div className="px-6 py-4 border-b border-gray-200 flex justify-between items-center">
        <h2 className="text-xl font-bold">Tasks</h2>
        <button
          onClick={onRefresh}
          className="bg-gray-200 text-gray-700 px-4 py-2 rounded-md hover:bg-gray-300"
        >
          Refresh
        </button>
      </div>
      <div className="overflow-x-auto">
        <table className="min-w-full divide-y divide-gray-200">
          <thead className="bg-gray-50">
            <tr>
              <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">
                ID
              </th>
              <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">
                Type
              </th>
              <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">
                Status
              </th>
              <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">
                Priority
              </th>
              <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">
                Created
              </th>
              <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">
                Completed
              </th>
              <th className="px-6 py-3 text-left text-xs font-medium text-gray-500 uppercase tracking-wider">
                Action
              </th>
            </tr>
          </thead>
          <tbody className="bg-white divide-y divide-gray-200">
            {tasks.length === 0 ? (
              <tr>
                <td colSpan="7" className="px-6 py-4 text-center text-gray-500">
                  No tasks found
                </td>
              </tr>
            ) : (
              tasks.map((task) => {
                // Parse result to get output_url for download
                let outputUrl = null
                if (task.status === 'completed' && task.result) {
                  try {
                    const result = typeof task.result === 'string' 
                      ? JSON.parse(task.result) 
                      : task.result
                    outputUrl = result.output_url || result.outputUrl
                  } catch (e) {
                    console.error('Failed to parse task result:', e)
                  }
                }

                return (
                  <tr key={task.id}>
                    <td className="px-6 py-4 whitespace-nowrap text-sm font-mono text-gray-900">
                      {task.id.substring(0, 8)}...
                    </td>
                    <td className="px-6 py-4 whitespace-nowrap text-sm text-gray-500">
                      {task.type}
                    </td>
                    <td className="px-6 py-4 whitespace-nowrap">
                      <span
                        className={`px-2 inline-flex text-xs leading-5 font-semibold rounded-full ${getStatusColor(
                          task.status
                        )}`}
                      >
                        {task.status}
                      </span>
                    </td>
                    <td className="px-6 py-4 whitespace-nowrap text-sm text-gray-500">
                      {task.priority}
                    </td>
                    <td className="px-6 py-4 whitespace-nowrap text-sm text-gray-500">
                      {task.createdAt || task.created_at
                        ? new Date(task.createdAt || task.created_at).toLocaleString()
                        : '-'}
                    </td>
                    <td className="px-6 py-4 whitespace-nowrap text-sm text-gray-500">
                      {task.completedAt || task.completed_at
                        ? new Date(task.completedAt || task.completed_at).toLocaleString()
                        : '-'}
                    </td>
                    <td className="px-6 py-4 whitespace-nowrap text-sm">
                      {task.status === 'completed' && outputUrl ? (
                        <button
                          onClick={async () => {
                            try {
                              // Convert Docker internal URL to public URL
                              let publicUrl = outputUrl
                              // Replace api:8080 with localhost:8080
                              if (publicUrl.includes('api:8080')) {
                                publicUrl = publicUrl.replace('http://api:8080', 'http://localhost:8080')
                              }
                              
                              // Fix path: if URL is http://localhost:8080/filename.ext, add /images/ prefix
                              // The API serves processed images from /images/ endpoint
                              if (publicUrl.match(/^http:\/\/localhost:8080\/[^\/]+\.(jpg|jpeg|png|gif|webp)$/i)) {
                                const filename = publicUrl.split('/').pop()
                                publicUrl = `http://localhost:8080/images/${filename}`
                              }
                              
                              console.log('Downloading from:', publicUrl)
                              
                              // Fetch the image as blob
                              const response = await fetch(publicUrl)
                              if (!response.ok) {
                                throw new Error(`Failed to fetch image: ${response.status} ${response.statusText}`)
                              }
                              const blob = await response.blob()
                              
                              // Create download link
                              const url = window.URL.createObjectURL(blob)
                              const a = document.createElement('a')
                              a.href = url
                              // Extract file extension from URL or blob type
                              const urlExt = publicUrl.split('.').pop()?.split('?')[0] || 'jpg'
                              const blobExt = blob.type.split('/')[1] || urlExt
                              a.download = `task-${task.id.substring(0, 8)}-${Date.now()}.${blobExt}`
                              document.body.appendChild(a)
                              a.click()
                              document.body.removeChild(a)
                              window.URL.revokeObjectURL(url)
                            } catch (err) {
                              console.error('Download failed:', err)
                              // Fallback: convert URL and open in new tab
                              let publicUrl = outputUrl
                              if (publicUrl.includes('api:8080')) {
                                publicUrl = publicUrl.replace('http://api:8080', 'http://localhost:8080')
                              }
                              // Fix path for fallback too
                              if (publicUrl.match(/^http:\/\/localhost:8080\/[^\/]+\.(jpg|jpeg|png|gif|webp)$/i)) {
                                const filename = publicUrl.split('/').pop()
                                publicUrl = `http://localhost:8080/images/${filename}`
                              }
                              window.open(publicUrl, '_blank')
                            }
                          }}
                          className="inline-flex items-center px-3 py-1.5 bg-blue-500 text-white text-xs font-medium rounded-md hover:bg-blue-600 transition-colors cursor-pointer"
                        >
                          <svg
                            className="w-4 h-4 mr-1"
                            fill="none"
                            stroke="currentColor"
                            viewBox="0 0 24 24"
                          >
                            <path
                              strokeLinecap="round"
                              strokeLinejoin="round"
                              strokeWidth={2}
                              d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"
                            />
                          </svg>
                          Download
                        </button>
                      ) : (
                        <span className="text-gray-400">-</span>
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
  )
}

export default TaskList

