import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import axios from 'axios'
import './index.css'
import App from './App.tsx'
import { useStore } from './store'

// Global 401 handler: a session that expires (or is revoked by a password
// change on another device) flips the app back to the login screen. Auth
// endpoints are excluded — their 401s are normal flow (bad password, the
// AuthGate's /me probe when logged out).
axios.interceptors.response.use(undefined, (err) => {
  const url: string = err.config?.url || ''
  if (err.response?.status === 401 && !url.startsWith('/api/auth/')) {
    useStore.getState().setUser(null)
  }
  return Promise.reject(err)
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
