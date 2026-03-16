/**
 * src/api/api.js
 *
 * Centralised HTTP API client for the distributed task queue frontend.
 *
 * Why a centralised module instead of inline fetch() calls in each component?
 *   - Single source of truth for the API base URL and auth header logic.
 *     Changing the token storage key (e.g. localStorage → sessionStorage) or the
 *     base URL only requires editing this file, not every component.
 *   - Consistent error handling: axios automatically throws on non-2xx status
 *     codes, so callers can use .catch() uniformly.
 *   - Testability: components can be unit-tested by mocking this module rather
 *     than mocking fetch/XMLHttpRequest directly.
 *
 * Authentication strategy:
 *   The backend uses JWT Bearer tokens (HMAC-SHA256 signed, 24 h expiry).
 *   The token is stored in localStorage after login/register and attached to
 *   every authenticated request via the "Authorization: Bearer <token>" header.
 *   localStorage persists across browser sessions (unlike sessionStorage which
 *   clears on tab close). This is an acceptable trade-off for a developer tool
 *   where UX matters more than cross-tab session isolation.
 *
 * API base URL resolution:
 *   In development (vite dev server), VITE_API_BASE is not set, so requests
 *   go to '/api/v1'. Vite's dev proxy (vite.config.js) forwards '/api' to
 *   'http://localhost:8080', which is the Go Fiber server.
 *   In production (served by nginx), the same '/api/v1' path is proxied by
 *   nginx to 'http://api:8080' (the Docker service name).
 *   Either way, the frontend code is environment-agnostic.
 */

import axios from 'axios'

/**
 * API_BASE — the prefix prepended to every request URL.
 *
 * Vite replaces import.meta.env.VITE_* at build time with the value from
 * the .env file (e.g. VITE_API_BASE=https://api.example.com/api/v1 for prod).
 * Falls back to '/api/v1' so the Vite dev proxy handles it in development.
 */
const API_BASE = import.meta.env.VITE_API_BASE || '/api/v1'

/**
 * getAuthHeader — returns the Authorization header object if a token is stored
 * in localStorage, or an empty object when the user is not logged in.
 *
 * Why an object return instead of a string?
 *   axios.get(url, { headers: getAuthHeader() }) spreads the returned object
 *   directly into the request headers. An empty {} adds nothing; a populated
 *   object adds the Authorization key. This avoids conditional spreading at
 *   every call site.
 *
 * @returns {{ Authorization: string } | {}}
 */
export const getAuthHeader = () => {
  const token = localStorage.getItem('token') // set by login() and register() on success
  return token ? { Authorization: `Bearer ${token}` } : {}
}

/**
 * fetchTasks — retrieves a paginated list of tasks visible to the current user.
 *
 * Regular users see only their own tasks. Admins can pass a ?user_id= param
 * separately (not exposed here) to see other users' tasks.
 *
 * @param {number} page      - 1-indexed page number (default 1)
 * @param {number} pageSize  - results per page (default 20, max 100)
 * @param {string} q         - optional search query (matches task ID, type, status)
 * @returns {Promise<AxiosResponse>} - response.data: { tasks, total, page, page_size }
 */
export const fetchTasks = (page = 1, pageSize = 20, q = '') =>
  axios.get(`${API_BASE}/tasks`, {
    params: { page, page_size: pageSize, q }, // axios serialises this as ?page=1&page_size=20
    headers: getAuthHeader(),
  })

/**
 * fetchMetrics — fetches the current system-wide queue and worker metrics.
 *
 * Returns: { total_tasks, queued, processing, completed, failed, workers[] }
 * Used by MetricsCard to display live system health.
 *
 * @returns {Promise<AxiosResponse>}
 */
export const fetchMetrics = () =>
  axios.get(`${API_BASE}/metrics/system`, { headers: getAuthHeader() })

/**
 * uploadFile — sends a single image file to the server and returns the URL
 * that can then be used as `source_url` in a task payload.
 *
 * Why upload first, then create a task?
 *   If the task payload included the raw file bytes, the API handler would need
 *   to write to disk synchronously inside the request, blocking the Fasthttp
 *   goroutine. The two-step pattern keeps the API handler fast (just a DB write)
 *   and allows the client to track upload progress separately from task creation.
 *
 * Why FormData instead of JSON?
 *   FormData is the browser's multipart/form-data encoding — the only standard
 *   way to upload binary file data without Base64-encoding it (which would inflate
 *   size by ~33%).
 *
 * @param {File} file                - the File object from <input type="file">
 * @param {Function} onUploadProgress - axios progress callback (for progress bars)
 * @returns {Promise<AxiosResponse>}  - response.data: { url, filename, size }
 */
export const uploadFile = (file, onUploadProgress) => {
  const fd = new FormData()
  fd.append('file', file) // 'file' must match the FormFile("file") field name on the server

  const token = localStorage.getItem('token')
  if (!token) {
    // Reject early rather than letting the server return 401 — gives a clearer error message.
    return Promise.reject(new Error('No authentication token found. Please login again.'))
  }

  return axios.post(`${API_BASE}/upload`, fd, {
    headers: getAuthHeader(), // axios merges these with its own Content-Type (multipart/form-data; boundary=...)
    onUploadProgress,         // called repeatedly by the browser with { loaded, total } for progress bars
    maxContentLength: 10 * 1024 * 1024, // 10 MB — mirrors the server's file size limit
    maxBodyLength: 10 * 1024 * 1024,    // same limit applied to the raw request body
    timeout: 30000, // 30 s — uploads on slow connections can take longer than the default axios timeout
  })
}

/**
 * createTask — submits a single image processing task.
 *
 * The payload object is task-type specific (e.g. { source_url, width, height }
 * for image_resize). The server validates it against the task type before queuing.
 *
 * @param {{ type: string, payload: object, priority: number, max_retries: number }} params
 * @returns {Promise<AxiosResponse>} - response.data: { task_id, status, message, created_at }
 */
export const createTask = ({ type, payload, priority, max_retries }) =>
  axios.post(
    `${API_BASE}/tasks`,
    { type, payload, priority, max_retries }, // serialised to JSON by axios
    { headers: getAuthHeader() }
  )

/**
 * createBatch — submits multiple image processing tasks in a single API call.
 *
 * Why batch instead of looping createTask?
 *   A single createBatch call creates one Batch metadata row and N Task rows in
 *   Postgres within a single 30-second timeout. Looping N createTask calls would
 *   issue N separate HTTP requests, each with its own round-trip and timeout.
 *   The batch endpoint is significantly more efficient for > 5 images.
 *
 * @param {{ type: string, payloads: object[], priority: number, max_retries: number }} params
 *   payloads — array of payload objects; each must include source_url from uploadFile()
 * @returns {Promise<AxiosResponse>} - response.data: { batch_id, total, status, created_at }
 */
export const createBatch = ({ type, payloads, priority, max_retries }) =>
  axios.post(
    `${API_BASE}/tasks/batch`,
    { type, images: payloads, priority, max_retries }, // 'images' matches BatchSubmitRequest.Images
    { headers: getAuthHeader() }
  )

/**
 * login — authenticates with username + password.
 * On success the caller should store response.data.token in localStorage.
 *
 * @param {string} username
 * @param {string} password
 * @returns {Promise<AxiosResponse>} - response.data: { token, user: { id, username, email, roles } }
 */
export const login = (username, password) =>
  axios.post(`${API_BASE}/auth/login`, { username, password })
  // No auth header — this is the endpoint that produces the token

/**
 * register — creates a new user account and returns a JWT immediately so the
 * user is logged in right after signup without a separate login step.
 *
 * @param {string} username
 * @param {string} email
 * @param {string} password  - minimum 8 characters (validated server-side)
 * @returns {Promise<AxiosResponse>} - response.data: { message, token, user, api_key }
 */
export const register = (username, email, password) =>
  axios.post(`${API_BASE}/auth/register`, { username, email, password })
