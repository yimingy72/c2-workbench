import type { ReactNode } from 'react'
import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { getToken } from './api'
import AppShell from './layout/AppShell'
import Listeners from './pages/Listeners'
import Login from './pages/Login'
import Payload from './pages/Payload'
import Host from './pages/Host'
import Sessions from './pages/Sessions'
import { ThemeRoot } from './theme/ThemeRoot'

function Guard({ children }: { children: ReactNode }) {
  const loc = useLocation()
  if (!getToken()) return <Navigate to={{ pathname: '/login', search: loc.search }} replace />
  return children
}

function ToListeners() {
  const loc = useLocation()
  return <Navigate to={{ pathname: '/listeners', search: loc.search }} replace />
}

export default function App() {
  return (
    <ThemeRoot>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route
          path="/"
          element={
            <Guard>
              <AppShell />
            </Guard>
          }
        >
          <Route index element={<ToListeners />} />
          <Route path="listeners" element={<Listeners />} />
          <Route path="sessions" element={<Sessions />} />
          <Route path="sessions/:id" element={<Host />} />
          <Route path="payload" element={<Payload />} />
        </Route>
        <Route path="*" element={<ToListeners />} />
      </Routes>
    </ThemeRoot>
  )
}
