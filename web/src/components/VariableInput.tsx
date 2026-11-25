import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type ClipboardEventHandler, type KeyboardEvent } from 'react'
import type { ResolvedVariable } from '../lib/types'

interface Props {
  value: string
  onChange: (next: string) => void
  onPaste?: ClipboardEventHandler<HTMLInputElement>
  variables: ResolvedVariable[]
  placeholder?: string
  className?: string
  disabled?: boolean
}

const sourceBadge: Record<ResolvedVariable['source'], { label: string; cls: string }> = {
  override: { label: '临时', cls: 'bg-amber-500/15 text-amber-700 border-amber-500/30' },
  environment: { label: '环境', cls: 'bg-sky-500/15 text-tidal border-sky-500/30' },
  project: { label: '项目', cls: 'bg-surface-subtle text-fg-muted border-line' },
}

export function VariableInput({
  value,
  onChange,
  onPaste,
  variables,
  placeholder,
  className = '',
  disabled = false,
}: Props) {
  const rootRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const keyboardScrollRef = useRef(false)
  const listId = useId()
  const [cursor, setCursor] = useState(0)
  const [focused, setFocused] = useState(false)
  const [dismissedAt, setDismissedAt] = useState<string | null>(null)
  const [selectedIdx, setSelectedIdx] = useState(0)

  const match = useMemo(() => {
    const pos = Math.max(0, Math.min(cursor, value.length))
    const before = value.slice(0, pos)
    const m = /\{\{([a-zA-Z0-9_-]*)$/.exec(before)
    if (!m) return null
    return {
      start: m.index,
      end: pos,
      prefix: m[1].toLowerCase(),
      tokenKey: `${m.index}:${before}`,
    }
  }, [value, cursor])

  const matchRef = useRef(match)
  matchRef.current = match

  const candidates = useMemo(() => {
    if (!match) return []
    const q = match.prefix
    if (!q) return variables.slice(0, 12)
    return variables.filter((v) => v.name.toLowerCase().includes(q)).slice(0, 12)
  }, [variables, match])

  const open = Boolean(focused && match && candidates.length > 0 && dismissedAt !== match.tokenKey)

  useEffect(() => {
    setSelectedIdx(0)
  }, [match?.prefix])

  useEffect(() => {
    if (!open || !keyboardScrollRef.current) return
    keyboardScrollRef.current = false
    const activeEl = menuRef.current?.querySelector<HTMLElement>(`[data-var-idx="${selectedIdx}"]`)
    activeEl?.scrollIntoView({ block: 'nearest' })
  }, [open, selectedIdx])

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as Node | null
      if (!target) return
      if (rootRef.current?.contains(target) || menuRef.current?.contains(target)) return
      setFocused(false)
    }
    const onFocusIn = (e: FocusEvent) => {
      const target = e.target as Node | null
      if (!target) return
      if (rootRef.current?.contains(target) || menuRef.current?.contains(target)) return
      setFocused(false)
    }
    const onWindowBlur = () => setFocused(false)
    const onKeyDown = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape' && !e.isComposing) {
        e.preventDefault()
        e.stopPropagation()
        if (matchRef.current) setDismissedAt(matchRef.current.tokenKey)
      }
    }
    document.addEventListener('pointerdown', onPointerDown, true)
    document.addEventListener('focusin', onFocusIn, true)
    document.addEventListener('keydown', onKeyDown, true)
    window.addEventListener('blur', onWindowBlur)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true)
      document.removeEventListener('focusin', onFocusIn, true)
      document.removeEventListener('keydown', onKeyDown, true)
      window.removeEventListener('blur', onWindowBlur)
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
        // popover unsupported or already promoted
      }
    }

    const updatePosition = () => {
      const anchor = inputRef.current
      const menu = menuRef.current
      if (!anchor || !menu) return
      const tr = anchor.getBoundingClientRect()
      const vw = window.innerWidth
      const vh = window.innerHeight
      if (tr.bottom < 0 || tr.top > vh || tr.right < 0 || tr.left > vw) {
        setFocused(false)
        return
      }
      const gap = 4
      const margin = 8
      const availWidth = Math.max(160, vw - margin * 2)
      const desiredWidth = Math.min(availWidth, Math.max(Math.round(tr.width), 280))

      menu.style.width = `${desiredWidth}px`
      menu.style.maxWidth = `${availWidth}px`
      menu.style.maxHeight = ''

      const mr = menu.getBoundingClientRect()
      const spaceBelow = vh - tr.bottom - margin
      const spaceAbove = tr.top - margin
      const flipUp = spaceBelow < mr.height + gap && spaceAbove > spaceBelow
      const avail = flipUp ? spaceAbove : spaceBelow
      const maxH = Math.max(0, Math.min(264, Math.floor(avail - gap)))
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
  }, [open, candidates.length])

  const applyCandidate = (name: string) => {
    if (!match) return
    const before = value.slice(0, match.start)
    const after = value.slice(match.end)
    const cleanAfter = after.startsWith('}}') ? after.slice(2) : after
    const inserted = `{{${name}}}`
    const nextVal = `${before}${inserted}${cleanAfter}`
    const nextPos = before.length + inserted.length
    onChange(nextVal)
    setDismissedAt(null)
    requestAnimationFrame(() => {
      const el = inputRef.current
      if (el) {
        el.focus()
        el.setSelectionRange(nextPos, nextPos)
        setCursor(nextPos)
      }
    })
  }

  const syncCursor = () => {
    const el = inputRef.current
    if (el && el.selectionStart !== null) {
      setCursor(el.selectionStart)
    }
  }

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    // Guard against IME composition confirmation (including Safari's out-of-order keyCode 229).
    if (e.nativeEvent.isComposing || e.keyCode === 229) {
      return
    }
    if (!open) return

    if (e.key === 'ArrowDown') {
      e.preventDefault()
      keyboardScrollRef.current = true
      setSelectedIdx((i) => (i + 1) % candidates.length)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      keyboardScrollRef.current = true
      setSelectedIdx((i) => (i - 1 + candidates.length) % candidates.length)
    } else if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault()
      const picked = candidates[selectedIdx] || candidates[0]
      if (picked) applyCandidate(picked.name)
    } else if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      if (match) setDismissedAt(match.tokenKey)
    }
  }

  return (
    <div ref={rootRef} className="relative flex-1 min-w-0">
      <input
        ref={inputRef}
        type="text"
        value={value}
        disabled={disabled}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        onFocus={() => {
          setFocused(true)
          syncCursor()
        }}
        onClick={syncCursor}
        onKeyUp={syncCursor}
        onKeyDown={handleKeyDown}
        onPaste={onPaste}
        onChange={(e) => {
          const pos = e.target.selectionStart ?? e.target.value.length
          setCursor(pos)
          setDismissedAt(null)
          onChange(e.target.value)
        }}
        placeholder={placeholder}
        className={`w-full ${className}`}
      />

      {open && (
        <div
          ref={menuRef}
          id={listId}
          popover="manual"
          role="listbox"
          aria-label="变量模板自动补全"
          onMouseDown={(e) => e.preventDefault()}
          className="tg-glass-popover fixed inset-auto m-0 z-50 flex flex-col overflow-hidden text-xs"
        >
          <div className="px-2.5 py-1 bg-surface-subtle border-b border-line text-[10px] text-fg-subtle flex items-center justify-end shrink-0">
            <span className="tg-tabular">{candidates.length} 项</span>
          </div>
          <div className="min-h-0 flex-1 max-h-60 overflow-y-auto tg-scroller divide-y divide-line p-1">
            {candidates.map((v, idx) => {
              const badge = sourceBadge[v.source] || sourceBadge.project
              const active = idx === selectedIdx
              return (
                <button
                  key={`${v.source}:${v.name}`}
                  data-var-idx={idx}
                  type="button"
                  tabIndex={-1}
                  role="option"
                  aria-selected={active}
                  onMouseDown={(e) => e.preventDefault()}
                  onMouseEnter={() => setSelectedIdx(idx)}
                  onClick={() => applyCandidate(v.name)}
                  className={`w-full text-left px-2.5 py-1.5 rounded-xl flex items-center justify-between gap-2 text-xs transition-colors ${
                    active ? 'bg-sky-500/15 text-tidal' : 'hover:bg-surface-hover text-fg'
                  }`}
                >
                  <div className="flex items-center gap-2 min-w-0">
                    <code className="font-mono font-semibold text-tidal truncate">{`{{${v.name}}}`}</code>
                    <span className={`px-1.5 py-px rounded border text-[10px] shrink-0 ${badge.cls}`}>
                      {badge.label}
                    </span>
                  </div>
                  <span className="font-mono text-[11px] text-fg-subtle truncate max-w-[220px]">
                    {v.secret ? '******' : v.value || '（空值）'}
                  </span>
                </button>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}
