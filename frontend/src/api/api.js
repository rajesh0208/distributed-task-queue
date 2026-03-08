// src/api/api.js

import axios from 'axios'

// Vite uses import.meta.env instead of process.env
// Use relative path to go through Vite proxy, or absolute URL if VITE_API_BASE is set
const API_BASE = import.meta.env.VITE_API_BASE || '/api/v1'

export const getAuthHeader = () => {
  const token = localStorage.getItem('token')
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export const fetchTasks = (page = 1, pageSize = 20, q = '') =>
  axios.get(`${API_BASE}/tasks`, {
    params: { page, page_size: pageSize, q },
    headers: getAuthHeader()
  })

export const fetchMetrics = () =>
  axios.get(`${API_BASE}/metrics/system`, { headers: getAuthHeader() })

export const uploadFile = (file, onUploadProgress) => {
  const fd = new FormData()
  fd.append('file', file)
  
  // Get auth header
  const authHeaders = getAuthHeader()
  
  // Check if token exists
  const token = localStorage.getItem('token')
  if (!token) {
    return Promise.reject(new Error('No authentication token found. Please login again.'))
  }
  
  console.log('Uploading file:', file.name, 'Size:', file.size, 'Type:', file.type)
  console.log('Using API_BASE:', API_BASE)
  console.log('Token present:', !!token)
  
  // Don't set Content-Type header - let browser set it with boundary
  // Setting it manually can break multipart/form-data
  return axios.post(`${API_BASE}/upload`, fd, {
    headers: authHeaders,
    onUploadProgress,
    maxContentLength: 10 * 1024 * 1024, // 10MB
    maxBodyLength: 10 * 1024 * 1024, // 10MB
    timeout: 30000, // 30 second timeout
  }).then(response => {
    console.log('Upload response:', response)
    return response
  }).catch(error => {
    console.error('Upload error details:', {
      message: error.message,
      response: error.response?.data,
      status: error.response?.status,
      statusText: error.response?.statusText,
      headers: error.response?.headers,
    })
    throw error
  })
}

export const createTask = ({ type, payload, priority, max_retries }) =>
  axios.post(`${API_BASE}/tasks`, { type, payload, priority, max_retries }, {
    headers: getAuthHeader()
  })

export const login = (username, password) =>
  axios.post(`${API_BASE}/auth/login`, { username, password })

export const register = (username, email, password) =>
  axios.post(`${API_BASE}/auth/register`, { username, email, password })
