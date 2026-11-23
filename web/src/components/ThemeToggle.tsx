import { Moon, Sun } from 'lucide-react'
import { toggleScheme, useColorScheme } from '../lib/theme'

interface Props {
  /** "chrome" = inside light/dark adaptive top bar; "onDark" = over a fixed deep-sea surface. */
  tone?: 'chrome' | 'onDark'
  className?: string
}

/**
 * Two-state color scheme toggle (system ↔ opposite of system).
 * Icon visibility is driven by CSS (.tg-theme-icon-*) so it is correct before React hydrates.
 */
export function ThemeToggle({ tone = 'chrome', className = '' }: Props) {
  const { resolved } = useColorScheme()
  const nextLabel = resolved === 'dark' ? '切换到浅色外观' : '切换到深色外观'
  const toneCls =
    tone === 'onDark'
      ? 'border-white/15 text-slate-200 hover:bg-white/10 hover:text-white'
      : 'border-line text-fg-muted hover:bg-surface-hover hover:text-fg'
  return (
    <button
      type="button"
      onClick={toggleScheme}
      title={nextLabel}
      aria-label={nextLabel}
      className={`relative inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md border transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-sky-500 ${toneCls} ${className}`}
    >
      <Sun className="tg-theme-icon-light h-4 w-4" aria-hidden="true" />
      <Moon className="tg-theme-icon-dark h-4 w-4" aria-hidden="true" />
    </button>
  )
}
