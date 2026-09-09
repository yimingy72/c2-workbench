import type { ReactNode } from 'react'
import { App as AntApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { Navigate, Route, Routes } from 'react-router-dom'
import { getToken } from './api'
import AppLayout from './pages/AppLayout'
import Listeners from './pages/Listeners'
import Login from './pages/Login'
import PayloadHelp from './pages/PayloadHelp'
import SessionDetail from './pages/SessionDetail'
import Sessions from './pages/Sessions'

function Guard({ children }: { children: ReactNode }) {
  if (!getToken()) return <Navigate to="/login" replace />
  return children
}

export default function App() {
  return (
    <ConfigProvider locale={zhCN}>
      <AntApp>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route
            path="/"
            element={
              <Guard>
                <AppLayout />
              </Guard>
            }
          >
            <Route index element={<Navigate to="/listeners" replace />} />
            <Route path="listeners" element={<Listeners />} />
            <Route path="sessions" element={<Sessions />} />
            <Route path="sessions/:id" element={<SessionDetail />} />
            <Route path="payload" element={<PayloadHelp />} />
          </Route>
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </AntApp>
    </ConfigProvider>
  )
}
