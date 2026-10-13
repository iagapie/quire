import { useEffect } from 'react'
import { create } from 'zustand'
import { Events } from '@wailsio/runtime'
import { Preferences } from './api'
import { readLocal, writeLocal } from './utils'

export type ThemePref = 'system' | 'light' | 'dark'

const query = '(prefers-color-scheme: dark)'
const KEY = 'quire.theme'

interface ThemeState {
  pref: ThemePref
  systemDark: boolean
  setPref(pref: ThemePref): void
}

export const useTheme = create<ThemeState>()((set) => ({
  pref: readLocal<ThemePref>(KEY, 'system'),
  systemDark: window.matchMedia(query).matches,
  setPref(pref) {
    set({ pref })
    writeLocal(KEY, pref)
    // The backend persists it and updates the native window and menu.
    void Preferences.SetTheme(pref)
  },
}))

export const isDark = (s: Pick<ThemeState, 'pref' | 'systemDark'>) => s.pref === 'dark' || (s.pref === 'system' && s.systemDark)

/** Keeps <html class="dark"> in sync with the preference, the OS and the app menu. */
export function useThemeSync() {
  const dark = useTheme(isDark)

  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
  }, [dark])

  useEffect(() => {
    const apply = (pref: string) => {
      if (pref === 'system' || pref === 'light' || pref === 'dark') {
        useTheme.setState({ pref })
        writeLocal(KEY, pref)
      }
    }
    Preferences.GetTheme().then(apply).catch(() => {})
    const off = Events.On('theme:changed', (e: { data: string }) => apply(e.data))
    const mq = window.matchMedia(query)
    const onChange = () => useTheme.setState({ systemDark: mq.matches })
    mq.addEventListener('change', onChange)
    return () => {
      off()
      mq.removeEventListener('change', onChange)
    }
  }, [])

  return dark
}
