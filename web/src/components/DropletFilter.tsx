import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { ChevronDown } from 'lucide-react'
import { prefersReducedMotion } from '../lib/glassPhysics'

export interface DropletOption {
  value: string
  label: string
  dotClass?: string
}

/**
 * Mounts once at the root shell to provide the SVG gooey alpha bridge filter (`#tg-goo`)
 * used by `<DropletFilter />` across toolbars and filters.
 */
export function GooFilterDefs() {
  return (
    <svg aria-hidden="true" width="0" height="0" className="fixed pointer-events-none">
      <defs>
        <filter id="tg-goo" x="-800%" y="-50%" width="1700%" height="200%">
          <feGaussianBlur in="SourceGraphic" stdDeviation="6.5" result="blur" />
          <feColorMatrix
            in="blur"
            mode="matrix"
            values="1 0 0 0 0  0 1 0 0 0  0 0 1 0 0  0 0 0 22 -9"
            result="goo"
          />
          <feComposite in="SourceGraphic" in2="goo" operator="atop" />
        </filter>
      </defs>
    </svg>
  )
}

interface DropletFilterProps {
  options: DropletOption[]
  value: string
  onChange: (value: string) => void
  ariaLabel: string
  tone?: 'light' | 'dark'
  align?: 'left' | 'right'
  size?: 'sm' | 'md'
}

/**
 * Interactive liquid droplet filter pill:
 * Clicking splits option droplets outward with a gooey SVG bridge (up to 5 options inline when space permits)
 * or opens a Top-Layer glass popover menu when there are 6+ options or container space is narrow; selecting merges droplets back.
 */
export function DropletFilter({
  options,
  value,
  onChange,
  ariaLabel,
  tone = 'light',
  align = 'left',
  size = 'md',
}: DropletFilterProps) {
  const [open, setOpen] = useState(false)
  const [inlineFits, setInlineFits] = useState(options.length <= 5)
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const menuRef = useRef<HTMLDivElement | null>(null)
  const listRef = useRef<HTMLDivElement | null>(null)
  const listId = useId()
  const active = options.find((o) => o.value === value) || options[0]

  const slotW = size === 'sm' ? 68 : 78
  const mainW = size === 'sm' ? 78 : 96
  const expandedWidth = mainW + 8 + options.length * (slotW + 5) - 5

  const checkInlineFits = () => {
    if (options.length > 5) return false
    const el = wrapRef.current
    if (!el) return true
    const r = el.getBoundingClientRect()
    const vw = window.innerWidth
    const card = el.closest('.tg-glass-card, .tg-accent-card, aside, dialog')
    const cr = card ? card.getBoundingClientRect() : null
    const leftBound = cr ? Math.max(0, cr.left) : 0
    const rightBound = cr ? Math.min(vw, cr.right) : vw
    const avail = align === 'right' ? r.right - leftBound - 12 : rightBound - r.left - 12
    return avail >= expandedWidth
  }

  useLayoutEffect(() => {
    const updateFit = () => {
      setInlineFits(checkInlineFits())
    }
    updateFit()
    window.addEventListener('resize', updateFit)
    return () => window.removeEventListener('resize', updateFit)
  }, [open, options.length, align, size])

  const inlineSplit = options.length <= 5 && inlineFits

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as Node | null
      if (!target) return
      if (wrapRef.current?.contains(target) || menuRef.current?.contains(target)) return
      setOpen(false)
    }
    const onFocusIn = (e: FocusEvent) => {
      const target = e.target as Node | null
      if (!target) return
      if (wrapRef.current?.contains(target) || menuRef.current?.contains(target)) return
      setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        setOpen(false)
        triggerRef.current?.focus()
      }
    }
    document.addEventListener('pointerdown', onPointerDown, true)
    document.addEventListener('focusin', onFocusIn, true)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true)
      document.removeEventListener('focusin', onFocusIn, true)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open])

  useLayoutEffect(() => {
    if (!open || inlineSplit) return
    const el = menuRef.current
    if (!el) return

    if (typeof el.showPopover === 'function') {
      try {
        if (!el.matches(':popover-open')) el.showPopover()
      } catch {
        // popover unsupported or already promoted
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
      menu.style.maxHeight = ''
      const mr = menu.getBoundingClientRect()
      const spaceBelow = vh - tr.bottom - margin
      const spaceAbove = tr.top - margin
      const flipUp = spaceBelow < mr.height + gap && spaceAbove > spaceBelow
      const avail = flipUp ? spaceAbove : spaceBelow
      const maxH = Math.max(0, Math.min(260, Math.floor(avail - gap)))
      menu.style.maxHeight = `${maxH}px`

      const updatedMr = menu.getBoundingClientRect()
      const top = flipUp
        ? Math.max(margin, tr.top - gap - updatedMr.height)
        : Math.min(tr.bottom + gap, Math.max(margin, vh - margin - updatedMr.height))
      const desiredLeft = align === 'right' ? tr.right - updatedMr.width : tr.left
      const maxLeft = Math.max(margin, vw - margin - updatedMr.width)
      const left = Math.min(Math.max(margin, desiredLeft), maxLeft)

      menu.style.top = `${Math.round(top)}px`
      menu.style.left = `${Math.round(left)}px`
      menu.style.transformOrigin = `${flipUp ? 'bottom' : 'top'} ${align === 'right' ? 'right' : 'left'}`
    }

    const onScroll = (e: Event) => {
      const target = e.target as Node | null
      if (target && menuRef.current?.contains(target)) return
      updatePosition()
    }

    updatePosition()
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', onScroll, true)
    return () => {
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', onScroll, true)
      if (typeof el.hidePopover === 'function') {
        try {
          if (el.matches(':popover-open')) el.hidePopover()
        } catch {
          // ignore
        }
      }
    }
  }, [open, inlineSplit, align])

  const getOptionButtons = (): HTMLButtonElement[] => {
    const container = inlineSplit ? listRef.current : menuRef.current
    if (!container) return []
    return Array.from(container.querySelectorAll<HTMLButtonElement>('button[role="option"]'))
  }

  const focusOptionAt = (idx: number) => {
    const btns = getOptionButtons()
    if (!btns.length) return
    const wrapped = (idx + btns.length) % btns.length
    btns[wrapped]?.focus()
    btns[wrapped]?.scrollIntoView({ block: 'nearest' })
  }

  const handleTriggerKeyDown = (e: ReactKeyboardEvent<HTMLButtonElement>) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowRight') {
      e.preventDefault()
      if (!open) {
        setInlineFits(checkInlineFits())
        setOpen(true)
        requestAnimationFrame(() => {
          const activeIdx = Math.max(0, options.findIndex((o) => o.value === value))
          focusOptionAt(activeIdx)
        })
      } else {
        focusOptionAt(0)
      }
    } else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft') {
      e.preventDefault()
      if (!open) {
        setInlineFits(checkInlineFits())
        setOpen(true)
        requestAnimationFrame(() => focusOptionAt(options.length - 1))
      } else {
        focusOptionAt(options.length - 1)
      }
    }
  }

  const handleOptionKeyDown = (e: ReactKeyboardEvent<HTMLButtonElement>, idx: number) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowRight') {
      e.preventDefault()
      focusOptionAt(idx + 1)
    } else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft') {
      e.preventDefault()
      focusOptionAt(idx - 1)
    } else if (e.key === 'Home') {
      e.preventDefault()
      focusOptionAt(0)
    } else if (e.key === 'End') {
      e.preventDefault()
      focusOptionAt(options.length - 1)
    }
  }

  const heightCls = size === 'sm' ? 'h-6 text-[11px]' : 'h-7 text-xs'
  const blobBg =
    tone === 'dark'
      ? 'bg-slate-800/95'
      : 'bg-white/92 dark:bg-slate-800/95'
  const pillCls =
    tone === 'dark'
      ? 'bg-white/10 hover:bg-white/18 border-white/25 text-white'
      : 'bg-surface-subtle hover:bg-surface-hover border-line-strong text-fg'

  if (!inlineSplit) {
    return (
      <div ref={wrapRef} className="relative inline-block">
        <button
          ref={triggerRef}
          type="button"
          aria-label={ariaLabel}
          aria-expanded={open}
          aria-controls={open ? listId : undefined}
          onClick={() => {
            setInlineFits(checkInlineFits())
            setOpen((v) => !v)
          }}
          onKeyDown={handleTriggerKeyDown}
          className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 font-medium transition-colors ${heightCls} ${pillCls}`}
        >
          <span className={`h-1.5 w-1.5 rounded-full shrink-0 ${active?.dotClass || 'bg-sky-500'}`} />
          <span className="truncate max-w-[110px]">{active?.label || '全部'}</span>
          <ChevronDown className={`h-3 w-3 opacity-65 transition-transform ${open ? 'rotate-180' : ''}`} />
        </button>

        {open && (
          <div
            ref={menuRef}
            id={listId}
            popover="manual"
            role="listbox"
            aria-label={ariaLabel}
            className="tg-glass-popover fixed inset-auto m-0 z-50 min-w-[152px] overflow-y-auto tg-scroller p-1 space-y-0.5"
          >
            {options.map((opt, idx) => {
              const selected = opt.value === value
              return (
                <button
                  key={opt.value}
                  type="button"
                  role="option"
                  aria-selected={selected}
                  tabIndex={open ? 0 : -1}
                  onKeyDown={(e) => handleOptionKeyDown(e, idx)}
                  onClick={() => {
                    onChange(opt.value)
                    setOpen(false)
                    triggerRef.current?.focus()
                  }}
                  className={`w-full flex items-center gap-2 rounded-xl px-2.5 py-1.5 text-left text-xs font-medium transition-colors ${
                    selected
                      ? 'bg-sky-500/15 text-tidal font-semibold'
                      : 'text-fg-muted hover:bg-surface-hover hover:text-fg'
                  }`}
                >
                  <span className={`h-1.5 w-1.5 rounded-full shrink-0 ${opt.dotClass || 'bg-sky-500'}`} />
                  <span className="truncate">{opt.label}</span>
                </button>
              )
            })}
          </div>
        )}
      </div>
    )
  }

  const dir = align === 'right' ? -1 : 1

  return (
    <div ref={wrapRef} className={`relative inline-flex items-center ${heightCls}`}>
      <button
        ref={triggerRef}
        type="button"
        aria-label={ariaLabel}
        aria-expanded={open}
        aria-controls={listId}
        onClick={() => {
          setInlineFits(checkInlineFits())
          setOpen((v) => !v)
        }}
        onKeyDown={handleTriggerKeyDown}
        className={`relative z-20 inline-flex items-center justify-between gap-1.5 rounded-full border px-2.5 font-semibold transition-all shadow-xs ${heightCls} ${pillCls}`}
        style={{ minWidth: `${mainW}px` }}
      >
        <span className="inline-flex items-center gap-1.5 min-w-0">
          <span className={`h-1.5 w-1.5 rounded-full shrink-0 ${active?.dotClass || 'bg-sky-500'}`} />
          <span className="truncate max-w-[78px]">{active?.label || '全部'}</span>
        </span>
        <ChevronDown className={`h-3 w-3 shrink-0 opacity-65 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>

      {/* Gooey background droplets bridge layer */}
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 z-10 flex items-center"
        style={{
          filter: open ? 'url(#tg-goo)' : undefined,
          height: size === 'sm' ? '24px' : '28px',
        }}
      >
        <div className={`rounded-full w-full h-full ${blobBg}`} />
        {options.map((opt, idx) => {
          const offset = dir * (mainW + 8 + idx * (slotW + 5))
          return (
            <div
              key={opt.value}
              className={`absolute top-0 rounded-full ${blobBg}`}
              style={{
                left: align === 'right' ? 'auto' : 0,
                right: align === 'right' ? 0 : 'auto',
                width: `${slotW}px`,
                height: '100%',
                opacity: open ? 1 : 0,
                transform: open
                  ? `translateX(${offset}px) scale(1)`
                  : 'translateX(0px) scale(0.68)',
                transition:
                  'transform 420ms cubic-bezier(0.34, 1.48, 0.64, 1), opacity 200ms ease',
              }}
            />
          )
        })}
      </div>

      {/* Crisp text buttons synchronized cleanly over the gooey droplets */}
      <div
        ref={listRef}
        id={listId}
        role="listbox"
        aria-label={ariaLabel}
        className="pointer-events-none absolute inset-0 z-30 flex items-center"
        style={{ height: size === 'sm' ? '24px' : '28px' }}
      >
        {options.map((opt, idx) => {
          const selected = opt.value === value
          const offset = dir * (mainW + 8 + idx * (slotW + 5))
          const textCls =
            tone === 'dark'
              ? selected
                ? 'text-[#7dd3fc] ring-1 ring-sky-400/60 border-white/30'
                : 'text-white/90 hover:text-[#7dd3fc] border-white/20'
              : selected
                ? 'text-tidal ring-1 ring-sky-400/50 border-line/60'
                : 'text-fg hover:text-tidal border-line/60'
          return (
            <button
              key={opt.value}
              type="button"
              role="option"
              aria-selected={selected}
              tabIndex={open ? 0 : -1}
              onKeyDown={(e) => handleOptionKeyDown(e, idx)}
              onClick={() => {
                onChange(opt.value)
                setOpen(false)
                triggerRef.current?.focus()
              }}
              style={{
                left: align === 'right' ? 'auto' : 0,
                right: align === 'right' ? 0 : 'auto',
                width: `${slotW}px`,
                height: '100%',
                opacity: open ? 1 : 0,
                pointerEvents: open ? 'auto' : 'none',
                transform: open
                  ? `translateX(${offset}px) scale(1)`
                  : 'translateX(0px) scale(0.68)',
                transition:
                  'transform 420ms cubic-bezier(0.34, 1.48, 0.64, 1), opacity 180ms ease, color 150ms ease',
              }}
              className={`absolute top-0 rounded-full px-2 font-semibold whitespace-nowrap inline-flex items-center justify-center border shadow-sm ${heightCls} ${textCls}`}
            >
              {opt.label}
            </button>
          )
        })}
      </div>
    </div>
  )
}

/**
 * Smooth cubic-out numeric counter for KPI metrics (`QPS/RPS`, `TPS`, latency, filtered counts).
 */
export function RollingNumber({
  value,
  decimals = 0,
  suffix = '',
  prefix = '',
  locale = true,
  className = '',
}: {
  value: number
  decimals?: number
  suffix?: string
  prefix?: string
  locale?: boolean
  className?: string
}) {
  const [display, setDisplay] = useState(value)
  const curRef = useRef(value)

  useEffect(() => {
    const target = Number.isFinite(value) ? value : 0
    const start = curRef.current
    if (Math.abs(target - start) < 1e-6 || prefersReducedMotion()) {
      curRef.current = target
      setDisplay(target)
      return
    }
    const t0 = performance.now()
    let raf = 0
    const step = (now: number) => {
      const k = Math.min(1, (now - t0) / 420)
      const ease = 1 - Math.pow(1 - k, 3)
      const next = start + (target - start) * ease
      curRef.current = next
      setDisplay(next)
      if (k < 1) raf = window.requestAnimationFrame(step)
      else curRef.current = target
    }
    raf = window.requestAnimationFrame(step)
    return () => window.cancelAnimationFrame(raf)
  }, [value])

  const formatted =
    decimals > 0
      ? display.toFixed(decimals)
      : locale
        ? Math.round(display).toLocaleString('en-US')
        : String(Math.round(display))

  return (
    <span className={`tg-tabular ${className}`}>
      {prefix}
      {formatted}
      {suffix}
    </span>
  )
}
