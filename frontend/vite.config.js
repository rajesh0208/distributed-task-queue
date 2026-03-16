/**
 * vite.config.js
 *
 * Vite build and dev-server configuration for the React frontend.
 *
 * # @vitejs/plugin-react
 *   Enables JSX transformation, Fast Refresh (HMR without losing component
 *   state), and automatic React import (no `import React from 'react'`
 *   required in every file — though this project adds it explicitly for clarity).
 *
 * # Dev server port
 *   Runs on port 3001 (not the default 5173) to avoid conflicting with other
 *   projects. The Go API runs on 8080; docker-compose maps the frontend
 *   container to host port 3001 as well.
 *
 * # Proxy rules
 *   The proxy forwards matching URL prefixes from the Vite dev server to the
 *   Go API at http://localhost:8080. This solves the same-origin policy
 *   (CORS) problem in development: the browser thinks everything is on
 *   localhost:3001, so no preflight OPTIONS requests are generated.
 *
 *   In production the reverse proxy (nginx in the frontend Docker image)
 *   handles the same routing, so no VITE_API_BASE override is needed.
 *
 *   /api      → REST API calls (auth, tasks, metrics, upload)
 *   /graphql  → GraphQL POST and WebSocket upgrade (ws: true enables ws proxying)
 *   /images   → Processed image files served by the Go static file handler
 *   /uploads  → Uploaded (input) image files served by the Go static file handler
 *
 * # changeOrigin: true
 *   Rewrites the Host header in the proxied request to match the target.
 *   Required when the backend server checks the Host header (e.g. virtual
 *   hosting). Without it the backend might reject requests with
 *   "Host: localhost:3001".
 *
 * # secure: false
 *   Disables SSL certificate verification for the proxy target. Safe here
 *   because the target is always localhost (HTTP, no TLS in development).
 */
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],  // JSX transform + Fast Refresh
  server: {
    port: 3001,        // fixed port so docker-compose and npm run dev agree
    proxy: {
      // REST API — covers /api/v1/tasks, /api/v1/auth, /api/v1/upload, etc.
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        secure: false,
        ws: true   // allows WebSocket upgrades under /api/ws/* paths
      },
      // GraphQL endpoint — POST for queries/mutations; ws:true for subscriptions
      '/graphql': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        secure: false,
        ws: true
      },
      // Processed output images served as static files by the Go API
      '/images': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        secure: false
      },
      // Raw uploaded input images served as static files by the Go API
      '/uploads': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        secure: false
      }
    }
  }
})

