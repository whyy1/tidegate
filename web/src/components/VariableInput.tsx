import { useEffect, useMemo, useRef, useState, type ClipboardEventHandler, type KeyboardEvent } from 'react'
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
  override: { label: '临时', cls: 'bg-amber-100 text-amber-800 border-amber-200' },
  environment: { label: '环境', cls: 'bg-sky-100 text-sky-800 border-sky-200' },
  project: { label: '项目', cls: 'bg-slate-100 text-slate-700 border-slate-200' },
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
  const inputRef = useRef<HTMLInputElement>(null)
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
      setSelectedIdx((i) => (i + 1) % candidates.length)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
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
    <div className="relative flex-1 min-w-[240px]">
      <input
        ref={inputRef}
        type="text"
        value={value}
        disabled={disabled}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        onFocus={() => {
          setFocused(true)
          syncCursor()
        }}
        onBlur={() => {
          window.setTimeout(() => setFocused(false), 120)
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
          role="listbox"
          aria-label="变量模板自动补全"
          className="absolute left-0 right-0 top-full mt-1 z-40 rounded-lg border border-slate-200 bg-white shadow-xl max-h-60 overflow-y-auto divide-y divide-slate-100"
        >
          <div className="px-2.5 py-1 bg-slate-50 text-[10px] text-slate-500 flex items-center justify-between">
            <span>可用变量补全（↑↓ 选择，Enter / Tab 插入，Esc 收起）</span>
            <span className="font-mono">{candidates.length} 项</span>
          </div>
          {candidates.map((v, idx) => {
            const badge = sourceBadge[v.source] || sourceBadge.project
            const active = idx === selectedIdx
            return (
              <button
                key={`${v.source}:${v.name}`}
                type="button"
                role="option"
                aria-selected={active}
                onMouseDown={(e) => {
                  e.preventDefault()
                  applyCandidate(v.name)
                }}
                className={`w-full text-left px-2.5 py-1.5 flex items-center justify-between gap-2 text-xs transition-colors ${
                  active ? 'bg-sky-50 text-sky-950' : 'hover:bg-slate-50 text-slate-700'
                }`}
              >
                <div className="flex items-center gap-2 min-w-0">
                  <code className="font-mono font-semibold text-sky-700 truncate">{`{{${v.name}}}`}</code>
                  <span className={`px-1.5 py-0.2 rounded border text-[10px] shrink-0 ${badge.cls}`}>
                    {badge.label}
                  </span>
                </div>
                <span className="font-mono text-[11px] text-slate-500 truncate max-w-[220px]">
                  {v.secret ? '******' : v.value || '（空值）'}
                </span>
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
