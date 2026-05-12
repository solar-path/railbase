import { signal } from '@preact/signals'

export type Theme = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'air-theme'

export const theme = signal<Theme>(readStored())

function readStored(): Theme {
  if (typeof localStorage === 'undefined') return 'system'
  const v = localStorage.getItem(STORAGE_KEY)
  return v === 'light' || v === 'dark' || v === 'system' ? v : 'system'
}

function resolveSystem(): 'light' | 'dark' {
  if (typeof matchMedia === 'undefined') return 'light'
  return matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function apply(t: Theme) {
  const resolved = t === 'system' ? resolveSystem() : t
  const root = document.documentElement
  root.classList.toggle('dark', resolved === 'dark')
  root.style.colorScheme = resolved
}

export function setTheme(t: Theme) {
  theme.value = t
  localStorage.setItem(STORAGE_KEY, t)
  apply(t)
}

export function initTheme() {
  apply(theme.value)
  if (typeof matchMedia !== 'undefined') {
    matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      if (theme.value === 'system') apply('system')
    })
  }
}
