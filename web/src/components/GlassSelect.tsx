import { Check, ChevronDown, Settings2 } from 'lucide-react'
import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'

export interface GlassSelectOption {
  value: string
  label: string
  badge?: string
  description?: string
  disabled?: boolean
}

interface GlassSelectProps {
  value: string
  options: GlassSelectOption[]
  onChange: (value: string) => void
  placeholder?: string
  ariaLabel: string
  icon?: ReactNode
  variant?: 'pill' | 'field'
  className?: string
  menuClassName?: string
  disabled?: boolean
  footerAction?: {
    label: string
    icon?: ReactNode
    onClick: () => void
  }
}

/**
 * Unified frosted-glass selector replacing inconsistent native `<select>` menus
 * in navigation pills, forms, and toolbars. Uses WAI-ARIA `listbox` semantics
 * (without `role="menu"`), outside pointerdown dismissal, and Escape/Arrow keys.
 */
export function GlassSelect({
  value,
  options,
  onChange,
  placeholder = '请选择…',
  ariaLabel,
  icon,
  variant = 'field',
  className = '',
  menuClassName = '',
  disabled = false,
  footerAction,
}: GlassSelectProps) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const listId = useId()
  const [highlightIdx, setHighlightIdx] = useState(-1)

  const selected = options.find((o) => o.value === value)

  useEffect(() => {
    if (!open) return
    const idx = Math.max(
      0,
      options.findIndex((o) => o.value === value && !o.disabled)
    )
    setHighlightIdx(idx)

    const onPointerDown = (e: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    const onKeyDown = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        triggerRef.current?.focus()
      }
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open, options, value])

  const commit = (val: string) => {
    onChange(val)
    setOpen(false)
    triggerRef.current?.focus()
  }

  const moveHighlight = (delta: number) => {
    if (options.length === 0) return
    let next = highlightIdx
    for (let step = 0; step < options.length; step++) {
      next = (next + delta + options.length) % options.length
      if (!options[next].disabled) {
        setHighlightIdx(next)
        return
      }
    }
  }

  const handleKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (disabled) return
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      if (!open) setOpen(true)
      else moveHighlight(1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      if (!open) setOpen(true)
      else moveHighlight(-1)
    } else if (e.key === 'Enter' || e.key === ' ') {
      if (open && highlightIdx >= 0 && options[highlightIdx] && !options[highlightIdx].disabled) {
        e.preventDefault()
        commit(options[highlightIdx].value)
      }
    }
  }

  const triggerClasses =
    variant === 'pill'
      ? 'inline-flex items-center gap-1.5 rounded-full border border-line bg-surface-subtle hover:bg-surface-hover px-3 py-1 text-xs font-medium text-fg transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-sky-500/40'
      : 'inline-flex items-center justify-between gap-2 w-full rounded-[10px] border border-line-strong bg-surface hover:bg-surface-subtle px-2.5 py-1.5 text-xs text-fg transition-colors focus:outline-none focus-visible:border-sky-500 focus-visible:ring-2 focus-visible:ring-sky-500/20'

  return (
    <div ref={rootRef} className={`relative ${variant === 'pill' ? 'inline-block' : 'w-full'} ${className}`}>
      <button
        ref={triggerRef}
        type="button"
        disabled={disabled}
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        onClick={() => !disabled && setOpen((v) => !v)}
        onKeyDown={handleKeyDown}
        className={`${triggerClasses} ${disabled ? 'opacity-50 cursor-not-allowed' : ''}`}
      >
        {icon && <span className="shrink-0 text-tidal flex items-center">{icon}</span>}
        <span className="truncate font-medium">
          {selected ? selected.label : <span className="text-fg-subtle">{placeholder}</span>}
        </span>
        {selected?.badge && (
          <span className="shrink-0 px-1.5 py-0.2 rounded-full bg-sky-500/12 text-tidal text-[10px] font-mono">
            {selected.badge}
          </span>
        )}
        <ChevronDown
          className={`w-3.5 h-3.5 text-fg-subtle shrink-0 ml-auto transition-transform duration-150 ${
            open ? 'rotate-180' : ''
          }`}
        />
      </button>

      {open && (
        <div
          id={listId}
          role="listbox"
          aria-label={ariaLabel}
          className={`tg-glass-popover absolute left-0 top-full mt-1.5 z-50 min-w-full w-64 max-w-[85vw] overflow-hidden p-1.5 text-xs ${menuClassName}`}
        >
          <div className="max-h-64 overflow-y-auto tg-scroller space-y-0.5">
            {options.length === 0 ? (
              <div className="px-2.5 py-3 text-center text-fg-subtle">暂无可选项</div>
            ) : (
              options.map((opt, idx) => {
                const isSelected = opt.value === value
                const isHighlighted = idx === highlightIdx
                return (
                  <button
                    key={opt.value}
                    type="button"
                    role="option"
                    aria-selected={isSelected}
                    disabled={opt.disabled}
                    onMouseEnter={() => setHighlightIdx(idx)}
                    onClick={() => !opt.disabled && commit(opt.value)}
                    className={`w-full text-left px-2.5 py-1.5 rounded-xl flex items-center justify-between gap-2 transition-colors ${
                      opt.disabled
                        ? 'opacity-45 cursor-not-allowed'
                        : isSelected
                          ? 'bg-sky-500/15 text-tidal font-semibold'
                          : isHighlighted
                            ? 'bg-surface-hover text-fg'
                            : 'text-fg hover:bg-surface-hover'
                    }`}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="truncate">{opt.label}</div>
                      {opt.description && (
                        <div className="text-[10px] text-fg-subtle truncate">{opt.description}</div>
                      )}
                    </div>
                    <div className="flex items-center gap-1.5 shrink-0">
                      {opt.badge && (
                        <span className="px-1.5 py-0.5 rounded-full bg-surface-subtle border border-line text-[10px] font-mono text-fg-muted">
                          {opt.badge}
                        </span>
                      )}
                      {isSelected && <Check className="w-3.5 h-3.5 text-tidal shrink-0" />}
                    </div>
                  </button>
                )
              })
            )}
          </div>
          {footerAction && (
            <div className="mt-1 pt-1 border-t border-line">
              <button
                type="button"
                onClick={() => {
                  setOpen(false)
                  footerAction.onClick()
                }}
                className="w-full px-2.5 py-1.5 rounded-xl flex items-center gap-2 text-xs font-medium text-tidal hover:bg-sky-500/10 transition-colors"
              >
                {footerAction.icon || <Settings2 className="w-3.5 h-3.5" />}
                <span>{footerAction.label}</span>
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
