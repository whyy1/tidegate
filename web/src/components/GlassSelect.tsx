import { Check, ChevronDown, Settings2 } from 'lucide-react'
import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'

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
 * (without `role="menu"`), Top-Layer `popover="manual"` viewport positioning
 * to escape ancestor `overflow` clipping (both on pages and inside modal `<dialog>`),
 * outside pointerdown dismissal, and Escape/Arrow keys.
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
  const menuRef = useRef<HTMLDivElement>(null)
  const listId = useId()
  const [highlightIdx, setHighlightIdx] = useState(-1)

  const latestRef = useRef({ options, value })
  latestRef.current = { options, value }

  const selected = options.find((o) => o.value === value)

  useEffect(() => {
    if (!open) return
    const { options: opts, value: val } = latestRef.current
    const selectedIdx = opts.findIndex((o) => o.value === val && !o.disabled)
    const idx = selectedIdx >= 0 ? selectedIdx : opts.findIndex((o) => !o.disabled)
    setHighlightIdx(idx)
  }, [open])

  useEffect(() => {
    if (disabled && open) setOpen(false)
  }, [disabled, open])

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as Node | null
      if (!target) return
      if (rootRef.current?.contains(target) || menuRef.current?.contains(target)) {
        return
      }
      setOpen(false)
    }
    const onFocusIn = (e: FocusEvent) => {
      const target = e.target as Node | null
      if (!target) return
      if (rootRef.current?.contains(target) || menuRef.current?.contains(target)) {
        return
      }
      setOpen(false)
    }
    const onKeyDown = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        setOpen(false)
        triggerRef.current?.focus()
      } else if (e.key === 'Tab') {
        if (menuRef.current?.contains(document.activeElement)) {
          triggerRef.current?.focus()
        }
        if (menuRef.current) {
          if (typeof menuRef.current.hidePopover === 'function') {
            try {
              if (menuRef.current.matches(':popover-open')) menuRef.current.hidePopover()
            } catch {
              // ignore
            }
          }
          menuRef.current.style.display = 'none'
        }
        setOpen(false)
      }
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('focusin', onFocusIn)
    document.addEventListener('keydown', onKeyDown, true)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('focusin', onFocusIn)
      document.removeEventListener('keydown', onKeyDown, true)
    }
  }, [open])

  useLayoutEffect(() => {
    if (!open) return
    const el = menuRef.current
    if (!el) return

    if (typeof el.showPopover === 'function') {
      try {
        if (!el.matches(':popover-open')) el.showPopover()
      } catch {
        // popover unsupported or already promoted: keep fixed positioning
      }
    }

    const updatePosition = () => {
      const trigger = triggerRef.current
      const menu = menuRef.current
      if (!trigger || !menu) return
      const tr = trigger.getBoundingClientRect()
      const vw = window.innerWidth
      const vh = window.innerHeight
      if (tr.bottom < 0 || tr.top > vh || tr.right < 0 || tr.left > vw) {
        setOpen(false)
        return
      }
      const gap = 6
      const margin = 8

      menu.style.minWidth = `${Math.round(tr.width)}px`
      menu.style.maxWidth = `${Math.max(160, vw - margin * 2)}px`
      menu.style.maxHeight = ''

      const mr = menu.getBoundingClientRect()
      const spaceBelow = vh - tr.bottom - margin
      const spaceAbove = tr.top - margin
      const flipUp = spaceBelow < mr.height + gap && spaceAbove > spaceBelow
      const avail = flipUp ? spaceAbove : spaceBelow
      const maxH = Math.max(120, Math.floor(avail - gap))
      menu.style.maxHeight = `${maxH}px`

      const updatedMr = menu.getBoundingClientRect()
      const top = flipUp
        ? Math.max(margin, tr.top - gap - updatedMr.height)
        : Math.min(tr.bottom + gap, Math.max(margin, vh - margin - updatedMr.height))
      const maxLeft = Math.max(margin, vw - margin - updatedMr.width)
      const left = Math.min(Math.max(margin, tr.left), maxLeft)

      menu.style.top = `${Math.round(top)}px`
      menu.style.left = `${Math.round(left)}px`
      menu.style.transformOrigin = `${flipUp ? 'bottom' : 'top'} ${left < tr.left ? 'right' : 'left'}`
    }

    updatePosition()
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    return () => {
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
      if (typeof el.hidePopover === 'function') {
        try {
          if (el.matches(':popover-open')) el.hidePopover()
        } catch {
          // ignore
        }
      }
    }
  }, [open])

  const commit = (val: string) => {
    onChange(val)
    setOpen(false)
    triggerRef.current?.focus()
  }

  const moveHighlight = (delta: number) => {
    if (options.length === 0) return
    let next = highlightIdx < 0 ? (delta > 0 ? -1 : 0) : highlightIdx
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
    <div
      ref={rootRef}
      onBlur={(e) => {
        if (!open) return
        const related = e.relatedTarget as Node | null
        if (related && !rootRef.current?.contains(related) && !menuRef.current?.contains(related)) {
          setOpen(false)
        }
      }}
      className={`relative ${variant === 'pill' ? 'inline-block' : 'w-full'} ${className}`}
    >
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
          ref={menuRef}
          id={listId}
          popover="manual"
          role="listbox"
          aria-label={ariaLabel}
          className={`tg-glass-popover fixed inset-auto m-0 z-50 ${menuClassName || 'w-64'} max-w-[85vw] flex flex-col overflow-hidden p-1.5 text-xs`}
        >
          <div className="min-h-0 flex-1 max-h-64 overflow-y-auto tg-scroller space-y-0.5">
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
                    tabIndex={-1}
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
            <div className="mt-1 pt-1 border-t border-line shrink-0">
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
