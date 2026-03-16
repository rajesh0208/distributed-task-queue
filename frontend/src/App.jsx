/**
 * src/App.jsx
 *
 * Root component — owns the global authentication state and renders either
 * the Login screen or the authenticated Dashboard layout.
 *
 * # Authentication state machine
 *
 *   checkingAuth=true   (initial)   → render spinner, do NOT flash Login
 *   checkingAuth=false, isAuth=false → render <Login>
 *   checkingAuth=false, isAuth=true  → render navbar + <Dashboard>
 *
 *   The `checkingAuth` flag exists to prevent a one-frame flash of the Login
 *   form on page load when the user is actually already logged in (token in
 *   localStorage). Without it, the component would briefly show Login before
 *   the useEffect fires and sets isAuthenticated=true.
 *
 * # OAuth callback token pickup
 *
 *   After a successful Google/GitHub OAuth login, the Go backend redirects
 *   the browser to:
 *
 *     http://localhost:3001/?token=<signed-jwt>
 *
 *   The useEffect reads the `token` query param, stores it in localStorage
 *   (same location as the password-based login flow), then calls
 *   window.history.replaceState to remove the token from the URL bar. This
 *   prevents the JWT from appearing in browser history or server logs if the
 *   user navigates back.
 *
 * # handleLogin / handleLogout
 *
 *   handleLogin(token)  — called by <Login> after a successful POST /auth/login.
 *                         Stores token in localStorage and flips isAuthenticated.
 *   handleLogout()      — removes token from localStorage and returns to Login.
 *                         Does NOT call a server-side logout endpoint because
 *                         JWTs are stateless; the token simply expires.
 *
 * # Layout structure
 *
 *   <div h-screen flex flex-col>   ← full-viewport container, prevents scroll
 *     <nav flex-shrink-0>          ← fixed-height header (56 px / h-14)
 *     <main flex-1 overflow-hidden> ← takes all remaining vertical space;
 *                                     Dashboard's inner scroll handles overflow
 */

import React, { useState, useEffect } from 'react'
import Dashboard from './components/Dashboard'
import Login from './components/Login'
import './index.css'

function App() {
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  const [checkingAuth, setCheckingAuth] = useState(true)

  useEffect(() => {
    // Handle OAuth callback: backend redirects to /?token=<jwt> after social login.
    const params = new URLSearchParams(window.location.search)
    const oauthToken = params.get('token')
    if (oauthToken) {
      localStorage.setItem('token', oauthToken)
      window.history.replaceState({}, document.title, window.location.pathname)
      setIsAuthenticated(true)
      setCheckingAuth(false)
      return
    }
    const token = localStorage.getItem('token')
    if (token) setIsAuthenticated(true)
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
      <div className="min-h-screen flex items-center justify-center bg-slate-950">
        <div className="flex items-center gap-3 text-slate-400">
          <svg className="animate-spin w-5 h-5" fill="none" viewBox="0 0 24 24">
            <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4"/>
            <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8H4z"/>
          </svg>
          Checking authentication…
        </div>
      </div>
    )
  }

  if (!isAuthenticated) {
    return <Login onLogin={handleLogin} />
  }

  return (
    <div className="h-screen bg-slate-950 flex flex-col overflow-hidden">
      {/* Navbar */}
      <nav className="flex-shrink-0 bg-slate-900 border-b border-slate-800">
        <div className="max-w-screen-xl mx-auto px-4 sm:px-6 lg:px-8 h-14 flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="w-7 h-7 rounded-lg bg-indigo-600 flex items-center justify-center">
              <svg className="w-4 h-4 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
                  d="M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10" />
              </svg>
            </div>
            <span className="text-white font-semibold text-sm">Task Queue</span>
            <span className="hidden sm:inline text-slate-500 text-xs">/ Dashboard</span>
          </div>
          <button
            onClick={handleLogout}
            className="flex items-center gap-1.5 text-xs font-medium text-slate-400 hover:text-white transition-colors px-3 py-1.5 rounded-lg hover:bg-slate-800"
          >
            <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
                d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1"/>
            </svg>
            Sign out
          </button>
        </div>
      </nav>

      {/* Main content */}
      <main className="flex-1 overflow-hidden max-w-screen-xl w-full mx-auto px-4 sm:px-6 lg:px-8 py-5">
        <Dashboard />
      </main>
    </div>
  )
}

export default App
