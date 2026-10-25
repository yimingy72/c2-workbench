import { App as AntApp, ConfigProvider, theme as antdTheme } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { isThemeId, palettes, type Palette, type ThemeId } from './palettes'

const STORAGE_KEY = 'wb-theme'

type ThemeCtx = {
  id: ThemeId
  palette: Palette
  setTheme: (id: ThemeId) => void
}

const Ctx = createContext<ThemeCtx | null>(null)

function readUrlTheme(): ThemeId | null {
  const t = new URLSearchParams(window.location.search).get('theme')
  return isThemeId(t) ? t : null
}

function readStoredTheme(): ThemeId {
  try {
    const t = localStorage.getItem(STORAGE_KEY)
    if (isThemeId(t)) return t
  } catch {
    /* storage unavailable */
  }
  return 'silver'
}

function applyCssVars(p: Palette) {
  const root = document.documentElement
  const vars: Record<string, string> = {
    '--wb-frame': p.frame,
    '--wb-canvas': p.canvas,
    '--wb-surface': p.surface,
    '--wb-overlay': p.overlay,
    '--wb-border': p.border,
    '--wb-text': p.text,
    '--wb-text-secondary': p.textSecondary,
    '--wb-placeholder': p.placeholder,
    '--wb-selected': p.selected,
    '--wb-accent': p.accent,
    '--wb-success': p.success,
    '--wb-warning': p.warning,
    '--wb-error': p.error,
    '--wb-evidence': p.evidence,
    '--wb-evidence-bg': p.evidenceBg,
    '--wb-primary-button-text': p.primaryButtonText,
  }
  for (const [k, v] of Object.entries(vars)) root.style.setProperty(k, v)
  root.dataset.theme = p.id
  root.style.colorScheme = p.dark ? 'dark' : 'light'
}

function writeUrlTheme(id: ThemeId) {
  const url = new URL(window.location.href)
  url.searchParams.set('theme', id)
  history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`)
}

export function ThemeRoot({ children }: { children: ReactNode }) {
  const [id, setId] = useState<ThemeId>(() => readUrlTheme() ?? readStoredTheme())
  const palette = palettes[id]

  const setTheme = useCallback((next: ThemeId) => {
    setId(next)
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      /* storage unavailable — still switch in-memory */
    }
    writeUrlTheme(next)
  }, [])

  useEffect(() => {
    applyCssVars(palette)
  }, [palette])

  const antd = useMemo(
    () => ({
      algorithm: palette.dark ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
      token: {
        colorPrimary: palette.accent,
        colorInfo: palette.accent,
        colorSuccess: palette.success,
        colorWarning: palette.warning,
        colorError: palette.error,
        colorBgBase: palette.canvas,
        colorBgLayout: palette.frame,
        colorBgContainer: palette.surface,
        colorBgElevated: palette.overlay,
        colorBorder: palette.border,
        colorBorderSecondary: palette.border,
        colorText: palette.text,
        colorTextSecondary: palette.textSecondary,
        colorTextTertiary: palette.textSecondary,
        colorTextPlaceholder: palette.placeholder,
        colorPrimaryBg: palette.selected,
        colorTextLightSolid: palette.primaryButtonText,
        borderRadius: 6,
        controlHeight: 32,
        fontSize: 13,
        fontFamily: `'Inter Variable', 'PingFang SC', 'Microsoft YaHei', sans-serif`,
        fontFamilyCode: `'IBM Plex Mono', ui-monospace, Menlo, monospace`,
      },
    }),
    [palette],
  )

  const value = useMemo(() => ({ id, palette, setTheme }), [id, palette, setTheme])

  return (
    <Ctx.Provider value={value}>
      <ConfigProvider locale={zhCN} theme={antd}>
        <AntApp>{children}</AntApp>
      </ConfigProvider>
    </Ctx.Provider>
  )
}

export function useTheme() {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('useTheme outside ThemeRoot')
  return ctx
}
