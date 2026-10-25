export const THEME_IDS = ['silver', 'glacier', 'celadon', 'slate', 'graphite'] as const

export type ThemeId = (typeof THEME_IDS)[number]

export type Palette = {
  id: ThemeId
  label: string
  dark: boolean
  frame: string
  canvas: string
  surface: string
  overlay: string
  border: string
  text: string
  textSecondary: string
  placeholder: string
  selected: string
  accent: string
  success: string
  warning: string
  error: string
  evidence: string
  evidenceBg: string
  primaryButtonText: string
}

export const palettes: Record<ThemeId, Palette> = {
  silver: {
    id: 'silver',
    label: '雾银',
    dark: false,
    frame: '#e8eef5',
    canvas: '#f4f7f9',
    surface: '#ffffff',
    overlay: '#ffffff',
    border: '#e5e6eb',
    text: '#1d2129',
    textSecondary: '#4e5969',
    placeholder: '#86909c',
    selected: '#e8f3ff',
    accent: '#0065cc',
    success: '#00b42a',
    warning: '#ff7d00',
    error: '#f53f3f',
    evidence: '#d7e0d0',
    evidenceBg: '#0d1117',
    primaryButtonText: '#ffffff',
  },
  glacier: {
    id: 'glacier',
    label: '冰蓝',
    dark: false,
    frame: '#dce8f4',
    canvas: '#eef4fa',
    surface: '#ffffff',
    overlay: '#ffffff',
    border: '#d0dce8',
    text: '#152433',
    textSecondary: '#4e6478',
    placeholder: '#7e93a6',
    selected: '#d7e8f8',
    accent: '#0b5cab',
    success: '#00b42a',
    warning: '#ff7d00',
    error: '#f53f3f',
    evidence: '#d7e0d0',
    evidenceBg: '#0d1117',
    primaryButtonText: '#ffffff',
  },
  celadon: {
    id: 'celadon',
    label: '青瓷',
    dark: false,
    frame: '#dceae4',
    canvas: '#eef6f2',
    surface: '#ffffff',
    overlay: '#ffffff',
    border: '#cfe0d8',
    text: '#14241f',
    textSecondary: '#4a655c',
    placeholder: '#7a948a',
    selected: '#d5ebe3',
    accent: '#0f7a66',
    success: '#00b42a',
    warning: '#ff7d00',
    error: '#f53f3f',
    evidence: '#d7e0d0',
    evidenceBg: '#0d1117',
    primaryButtonText: '#ffffff',
  },
  slate: {
    id: 'slate',
    label: '亮石墨',
    dark: true,
    frame: '#242b3a',
    canvas: '#2c344a',
    surface: '#343c52',
    overlay: '#3d4660',
    border: '#4a5470',
    text: '#f2f3f5',
    textSecondary: '#c5cad3',
    placeholder: '#86909c',
    selected: '#1d4f86',
    accent: '#3c9ae8',
    success: '#62d256',
    warning: '#f7ba1e',
    error: '#f76560',
    evidence: '#d7e0d0',
    evidenceBg: '#161b22',
    primaryButtonText: '#ffffff',
  },
  graphite: {
    id: 'graphite',
    label: '原石墨',
    dark: true,
    frame: '#12151c',
    canvas: '#161b22',
    surface: '#1c2330',
    overlay: '#242b3a',
    border: '#30363d',
    text: '#e6edf3',
    textSecondary: '#9aa3ae',
    placeholder: '#6e7681',
    selected: '#1f3a5f',
    accent: '#58a6ff',
    success: '#3fb950',
    warning: '#d29922',
    error: '#f85149',
    evidence: '#d7e0d0',
    evidenceBg: '#0d1117',
    primaryButtonText: '#ffffff',
  },
}

export const THEME_OPTIONS = THEME_IDS.map((id) => ({
  value: id,
  label: palettes[id].label,
}))

export function isThemeId(v: string | null | undefined): v is ThemeId {
  return !!v && (THEME_IDS as readonly string[]).includes(v)
}
