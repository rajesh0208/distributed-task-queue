// src/api/api.js
// Centralised API client for the distributed task queue frontend.
// All authenticated requests attach a Bearer JWT from localStorage.

import axios from 'axios'

// Vite uses import.meta.env instead of process.env.
// Falls back to '/api/v1' so Vite's dev proxy forwards requests to the backend.
const API_BASE = import.meta.env.VITE_API_BASE || '/api/v1'

// getAuthHeader returns the Authorization header object if a token is stored,
// or an empty object when the user is not logged in.
export const getAuthHeader = () => {
  const token = localStorage.getItem('token')
  return token ? { Authorization: `Bearer ${token}` } : {}
}

// fetchTasks retrieves a paginated list of tasks, optionally filtered by the
// query string q (matches task ID, type, or status).
export const fetchTasks = (page = 1, pageSize = 20, q = '') =>
  axios.get(`${API_BASE}/tasks`, {
    params: { page, page_size: pageSize, q },
    headers: getAuthHeader(),
  })

// fetchMetrics fetches the current system-wide queue and worker metrics.
export const fetchMetrics = () =>
  axios.get(`${API_BASE}/metrics/system`, { headers: getAuthHeader() })

// uploadFile sends a single image file as multipart/form-data to the upload
// endpoint and returns the server-assigned URL. The browser sets Content-Type
// automatically (with the multipart boundary) when FormData is used.
export const uploadFile = (file, onUploadProgress) => {
  const fd = new FormData()
  fd.append('file', file)

  const token = localStorage.getItem('token')
  if (!token) {
    return Promise.reject(new Error('No authentication token found. Please login again.'))
  }

  return axios.post(`${API_BASE}/upload`, fd, {
    headers: getAuthHeader(),
    onUploadProgress,
    maxContentLength: 10 * 1024 * 1024, // 10 MB
    maxBodyLength: 10 * 1024 * 1024,
    timeout: 30000, // 30 s
  })
}

// createTask submits a single image processing task and returns the new task ID.
export const createTask = ({ type, payload, priority, max_retries }) =>
  axios.post(
    `${API_BASE}/tasks`,
    { type, payload, priority, max_retries },
    { headers: getAuthHeader() }
  )

// createBatch submits multiple image processing tasks in a single request.
// payloads is an array of payload objects — one per image — each already
// containing the server-assigned source_url from uploadFile().
export const createBatch = ({ type, payloads, priority, max_retries }) =>
  axios.post(
    `${API_BASE}/tasks/batch`,
    { type, images: payloads, priority, max_retries },
    { headers: getAuthHeader() }
  )

// login authenticates with username + password and returns a JWT token.
export const login = (username, password) =>
  axios.post(`${API_BASE}/auth/login`, { username, password })

// register creates a new user account and returns a JWT token.
export const register = (username, email, password) =>
  axios.post(`${API_BASE}/auth/register`, { username, email, password })
