import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import axios from 'axios'
import type { AxiosError, InternalAxiosRequestConfig } from 'axios'
import './index.css'
import App from './App.tsx'
import { useStore } from './store'

// Read a cookie value by name (used for the non-httpOnly CSRF token).
function readCookie(name: string): string | null {
  const match = document.cookie.match('(^|;)\\s*' + name + '\\s*=\\s*([^;]+)')
  return match ? decodeURIComponent(match.pop() as string) : null
}

// CSRF double-submit: echo the hc1_csrf cookie in the X-CSRF-Token header on
// every mutating request. A cross-site page can trigger the browser to send our
// cookie but cannot read it, so it cannot populate this header.
axios.interceptors.request.use((config: InternalAxiosRequestConfig) => {
  const method = (config.method || 'get').toLowerCase()
  if (method !== 'get' && method !== 'head' && method !== 'options') {
    const token = readCookie('hc1_csrf')
    if (token) config.headers.set('X-CSRF-Token', token)
  }
  return config
})

// Coalesce concurrent refreshes: many requests can 401 at once when the access
// session lapses, but only one refresh call should fire.
let refreshInFlight: Promise<boolean> | null = null
function refreshAccess(): Promise<boolean> {
  if (!refreshInFlight) {
    refreshInFlight = axios
      .post('/api/auth/refresh')
      .then(() => true)
      .catch(() => false)
      .finally(() => { refreshInFlight = null })
  }
  return refreshInFlight
}

// Global 401 handler. A short-lived access session lapses routinely; before
// bouncing to the login screen we try once to exchange the refresh token for a
// new pair and replay the original request. Only if that fails do we log out.
// Auth endpoints are excluded — their 401s are normal flow (unknown passkey,
// the AuthGate's /me probe when logged out, an exhausted refresh token).
axios.interceptors.response.use(undefined, async (err: AxiosError) => {
  const config = err.config as (InternalAxiosRequestConfig & { _retried?: boolean }) | undefined
  const url: string = config?.url || ''
  const isAuthUrl = url.startsWith('/api/auth/')

  if (err.response?.status === 401 && config && !isAuthUrl && !config._retried) {
    config._retried = true
    if (await refreshAccess()) {
      return axios(config) // replay with the fresh access cookie
    }
    useStore.getState().setUser(null)
  } else if (err.response?.status === 401 && !isAuthUrl) {
    useStore.getState().setUser(null)
  }
  return Promise.reject(err)
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
