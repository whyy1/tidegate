import { useMemo, useRef, type KeyboardEvent } from 'react'
import { Plus } from 'lucide-react'

export interface CodeSnippet {
  label: string
  code: string
}

interface Props {
  value: string
  onChange: (next: string) => void
  snippets?: CodeSnippet[]
  placeholder?: string
  rows?: number
  fill?: boolean
  disabled?: boolean
  ariaLabel?: string
}

export function CodeEditor({
  value,
  onChange,
  snippets = [],
  placeholder = '',
  rows = 7,
  fill = false,
  disabled = false,
  ariaLabel,
}: Props) {
  const areaRef = useRef<HTMLTextAreaElement>(null)
  const gutterRef = useRef<HTMLDivElement>(null)

  const lineCount = useMemo(() => {
    if (!value) return 1
    return Math.max(1, value.split('\n').length)
  }, [value])

  const lineNumbers = useMemo(() => {
    const out: number[] = []
    for (let i = 1; i <= lineCount; i++) out.push(i)
    return out
  }, [lineCount])

  const insertSnippet = (code: string) => {
    const trimmed = value.trimEnd()
    const next = trimmed ? `${trimmed}\n${code}` : code
    onChange(next)
    requestAnimationFrame(() => {
      const el = areaRef.current
      if (el) {
        el.focus()
        const end = next.length
        el.setSelectionRange(end, end)
      }
    })
  }

  const handleKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing || e.keyCode === 229) {
      return
    }
    if (e.key === 'Tab') {
      e.preventDefault()
      const el = e.currentTarget
      const start = el.selectionStart
      const end = el.selectionEnd
      const next = value.slice(0, start) + '  ' + value.slice(end)
      onChange(next)
      requestAnimationFrame(() => {
        el.setSelectionRange(start + 2, start + 2)
      })
    }
  }

  return (
    <div className={fill ? 'flex-1 min-h-[200px] flex flex-col gap-1.5' : 'space-y-1.5'}>
      {snippets.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 shrink-0">
          <span className="text-[11px] text-fg-subtle">片段</span>
          {snippets.map((s) => (
            <button
              key={s.label}
              type="button"
              disabled={disabled}
              onClick={() => insertSnippet(s.code)}
              className="inline-flex items-center gap-0.5 px-2 py-0.5 rounded border border-line bg-surface-subtle hover:bg-sky-50 hover:border-sky-300 text-[11px] text-fg font-medium transition-colors disabled:opacity-45"
            >
              <Plus className="w-3 h-3" aria-hidden="true" />
              {s.label}
            </button>
          ))}
        </div>
      )}

      <div
        className={`flex rounded-lg border border-line-strong bg-surface overflow-hidden focus-within:border-sky-500 focus-within:ring-1 focus-within:ring-sky-500/30 ${
          fill ? 'flex-1 min-h-0' : ''
        }`}
      >
        <div
          ref={gutterRef}
          aria-hidden="true"
          className="select-none bg-surface-subtle border-r border-line px-2 py-2 text-right font-mono text-[11px] leading-[1.5rem] text-fg-subtle tg-tabular min-w-[2.25rem] overflow-hidden shrink-0"
        >
          {lineNumbers.map((n) => (
            <div key={n}>{n}</div>
          ))}
        </div>
        <textarea
          ref={areaRef}
          rows={fill ? undefined : rows}
          value={value}
          disabled={disabled}
          aria-label={ariaLabel}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          onKeyDown={handleKeyDown}
          onScroll={(e) => {
            if (gutterRef.current) gutterRef.current.scrollTop = e.currentTarget.scrollTop
          }}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          className={`flex-1 min-w-0 px-2.5 py-2 font-mono text-xs leading-[1.5rem] bg-surface text-fg focus:outline-none ${
            fill ? 'h-full resize-none overflow-auto tg-scroller' : 'resize-y'
          }`}
        />
      </div>
    </div>
  )
}
