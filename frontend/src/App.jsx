import React, { useState, useEffect } from 'react'
import Dashboard from './components/Dashboard'
import Login from './components/Login'
import './index.css'

function App() {
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  const [checkingAuth, setCheckingAuth] = useState(true)

  useEffect(() => {
    // Handle OAuth callback: backend redirects to /?token=<jwt>
    const params = new URLSearchParams(window.location.search)
    const oauthToken = params.get('token')
    if (oauthToken) {
      localStorage.setItem('token', oauthToken)
      // Remove token from URL without triggering a page reload
      window.history.replaceState({}, document.title, window.location.pathname)
      setIsAuthenticated(true)
      setCheckingAuth(false)
      return
    }

    const token = localStorage.getItem('token')
    if (token) {
      setIsAuthenticated(true)
    }
    setCheckingAuth(false)
  }, [])

  const handleLogin = (token) => {
    localStorage.setItem('token', token)
    setIsAuthenticated(true)
  }

  const handleLogout = () => {
    localStorage.removeItem('token')
    setIsAuthenticated(false)
  }

  if (checkingAuth) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="text-gray-600 text-lg font-medium animate-pulse">
          Checking authentication…
        </div>
      </div>
    )
  }

  if (!isAuthenticated) {
    return <Login onLogin={handleLogin} />
  }

  return (
    <div className="h-screen bg-gray-100 flex flex-col overflow-hidden">
      {/* ──────────────────────────── NAVBAR ──────────────────────────── */}
      <nav className="bg-white border-b border-gray-200 shadow-sm">
        <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
          <div className="flex justify-between h-16 items-center">
            {/* LEFT: BRAND */}
            <div className="flex items-center space-x-3">
              <div className="text-xl font-bold text-gray-900">
                Task Queue Dashboard
              </div>
              <span className="text-sm text-gray-500 hidden sm:block">
                • Manage & Monitor Tasks
              </span>
            </div>

            {/* RIGHT: BUTTONS */}
            <div className="flex items-center space-x-3">
              <button
                onClick={handleLogout}
                className="px-3 py-1.5 rounded-lg bg-red-500 text-white hover:bg-red-600 text-sm font-medium shadow-sm"
              >
                Logout
              </button>
            </div>
          </div>
        </div>
      </nav>

      {/* ─────────────────────────── MAIN CONTENT ─────────────────────────── */}
      <main className="max-w-7xl mx-auto py-6 px-4 sm:px-6 lg:px-8 flex-1 overflow-hidden">
        <Dashboard />
      </main>
    </div>
  )
}

export default App
