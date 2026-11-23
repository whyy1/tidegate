import { useEffect, useSyncExternalStore } from 'react'

/** Persisted preference. "system" = no stored value, <meta> follows the OS. */
export type SchemePreference = 'system' | 'light' | 'dark'
export type ResolvedScheme = 'light' | 'dark'

const STORAGE_KEY = 'color-scheme'
const CHANGE_EVENT = 'tidegate:color-scheme'
const darkQuery = (): MediaQueryList | null =>
  typeof window !== 'undefined' && window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null

function metaEl(): HTMLMetaElement | null {
  return document.querySelector('meta[name="color-scheme"]')
}

export function readPreference(): SchemePreference {
  try {
    const v = window.localStorage.getItem(STORAGE_KEY)
    if (v === 'light' || v === 'dark') return v
  } catch {
    // storage blocked: fall back to whatever the <meta> currently pins
  }
  const content = metaEl()?.content
  return content === 'light' || content === 'dark' ? content : 'system'
}

export function systemScheme(): ResolvedScheme {
  return darkQuery()?.matches ? 'dark' : 'light'
}

export function resolvedScheme(): ResolvedScheme {
  const pref = readPreference()
  return pref === 'system' ? systemScheme() : pref
}

function applyMeta(pref: SchemePreference) {
  const val = pref === 'system' ? 'light dark' : pref
  const meta = metaEl()
  if (meta) meta.content = val
  if (typeof document !== 'undefined') {
    document.documentElement.style.colorScheme = val
  }
}

/** Explicitly set a preference (used by the three-state selector). */
export function setPreference(pref: SchemePreference) {
  try {
    if (pref === 'system') window.localStorage.removeItem(STORAGE_KEY)
    else window.localStorage.setItem(STORAGE_KEY, pref)
  } catch {
    // still apply visually even if it cannot be persisted
  }
  applyMeta(pref)
  window.dispatchEvent(new Event(CHANGE_EVENT))
}

/**
 * Two-state toggle: flip to the opposite of what is rendered right now.
 * If that equals the OS preference the user is undoing their override, so the stored value is removed.
 */
export function toggleScheme() {
  const target: ResolvedScheme = resolvedScheme() === 'dark' ? 'light' : 'dark'
  setPreference(target === systemScheme() ? 'system' : target)
}

function subscribe(cb: () => void) {
  const mq = darkQuery()
  const onStorage = (e: StorageEvent) => {
    if (e.key !== null && e.key !== STORAGE_KEY) return
    const v = e.newValue
    applyMeta(v === 'light' || v === 'dark' ? v : 'system')
    cb()
  }
  window.addEventListener(CHANGE_EVENT, cb)
  window.addEventListener('storage', onStorage)
  mq?.addEventListener('change', cb)
  return () => {
    window.removeEventListener(CHANGE_EVENT, cb)
    window.removeEventListener('storage', onStorage)
    mq?.removeEventListener('change', cb)
  }
}

const snapshot = () => `${readPreference()}|${resolvedScheme()}`

/** Live preference + rendered scheme; re-renders on toggle, other tabs and OS changes. */
export function useColorScheme(): { preference: SchemePreference; resolved: ResolvedScheme; system: ResolvedScheme } {
  const snap = useSyncExternalStore(subscribe, snapshot, () => 'system|light')
  const [preference, resolved] = snap.split('|') as [SchemePreference, ResolvedScheme]
  return { preference, resolved, system: systemScheme() }
}

/**
 * Observe the rendered scheme for imperative consumers (canvas charts).
 * Watches the <meta> (toggle / other tabs) and the OS media query.
 */
export function useSchemeObserver(onChange: (scheme: ResolvedScheme) => void) {
  useEffect(() => {
    let last = resolvedScheme()
    const fire = () => {
      const now = resolvedScheme()
      if (now !== last) {
        last = now
        onChange(now)
      }
    }
    const unsub = subscribe(fire)
    const meta = metaEl()
    const mo = meta ? new MutationObserver(fire) : null
    if (meta && mo) mo.observe(meta, { attributes: true, attributeFilter: ['content'] })
    return () => {
      unsub()
      mo?.disconnect()
    }
  }, [onChange])
}

/** Canvas palette for the currently rendered scheme. */
export function chartPalette(scheme: ResolvedScheme = resolvedScheme()) {
  return scheme === 'dark'
    ? {
        axis: '#94a3b8',
        grid: 'rgba(148,163,184,0.14)',
        text: '#e6ebf2',
        muted: '#94a3b8',
        surface: '#0f1623',
        canvas: '#090d16',
        border: 'rgba(148,163,184,0.18)',
      }
    : {
        axis: '#64748b',
        grid: '#e2e8f0',
        text: '#0f172a',
        muted: '#64748b',
        surface: '#ffffff',
        canvas: '#eef2f7',
        border: '#e2e8f0',
      }
}
