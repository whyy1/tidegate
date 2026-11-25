import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import { Pin, Plus, Trash2, X, Zap } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { Environment } from '../lib/types'

interface Props {
  projectId: string
  activeEnv: Environment | null
  tempVars: Record<string, string>
  onChange: (next: Record<string, string>) => void
  onEnvironmentsChanged: () => Promise<void>
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

export function TempVarsPopover({
  projectId,
  activeEnv,
  tempVars,
  onChange,
  onEnvironmentsChanged,
  onToast,
}: Props) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')
  const [pinning, setPinning] = useState<string>('')
  const [confirmClear, setConfirmClear] = useState(false)

  const entries = Object.entries(tempVars)

  useEffect(() => {
    if (!open) {
      setConfirmClear(false)
      return
    }
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as Node | null
      if (!target) return
      if (rootRef.current?.contains(target) || panelRef.current?.contains(target)) return
      setOpen(false)
    }
    const onFocusIn = (e: FocusEvent) => {
      const target = e.target as Node | null
      if (!target) return
      if (rootRef.current?.contains(target) || panelRef.current?.contains(target)) return
      setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (document.querySelector('dialog[open]') && !panelRef.current?.contains(document.activeElement)) {
          return
        }
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
    if (!open) return
    const el = panelRef.current
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
      const panel = panelRef.current
      if (!trigger || !panel) return
      const tr = trigger.getBoundingClientRect()
      const vw = window.innerWidth
      const vh = window.innerHeight
      const margin = 8
      const gap = 8

      const pr = panel.getBoundingClientRect()
      const top = Math.min(tr.bottom + gap, Math.max(margin, vh - margin - pr.height))
      const maxLeft = Math.max(margin, vw - margin - pr.width)
      const left = Math.min(Math.max(margin, tr.left), maxLeft)

      panel.style.top = `${Math.round(top)}px`
      panel.style.left = `${Math.round(left)}px`
      panel.style.maxHeight = `${Math.max(160, Math.floor(vh - margin * 2))}px`
    }

    const onScroll = (e: Event) => {
      const target = e.target as Node | null
      if (target && panelRef.current?.contains(target)) return
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
  }, [open])

  useEffect(() => {
    if (!confirmClear) return
    const t = window.setTimeout(() => setConfirmClear(false), 3000)
    return () => window.clearTimeout(t)
  }, [confirmClear])

  const handleAdd = (e: FormEvent) => {
    e.preventDefault()
    const k = newKey.trim()
    if (!k) return
    onChange({ ...tempVars, [k]: newVal })
    setNewKey('')
    setNewVal('')
  }

  const handlePinToEnv = async (k: string, val: string) => {
    if (!activeEnv) {
      onToast('info', '请先在顶部选择一个环境')
      return
    }
    setPinning(k)
    try {
      const existing = activeEnv.variables || []
      const idx = existing.findIndex((v) => v.name === k)
      const nextVars =
        idx >= 0
          ? existing.map((v, i) => (i === idx ? { ...v, value: val, disabled: false } : v))
          : [...existing, { name: k, value: val, description: '由临时变量固定' }]

      await api.updateEnvironment(projectId, activeEnv.id, {
        id: activeEnv.id,
        name: activeEnv.name,
        base_url: activeEnv.base_url || '',
        variables: nextVars,
        version: activeEnv.version,
      })
      await onEnvironmentsChanged()
      onToast('success', `已将 {{${k}}} 固定到环境「${activeEnv.name}」`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '固定到环境失败')
    } finally {
      setPinning('')
    }
  }

  const has = entries.length > 0

  return (
    <div ref={rootRef} className="relative">
      <button
        ref={triggerRef}
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-haspopup="dialog"
        title="临时变量"
        className={`inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium transition-colors ${
          has
            ? 'border-amber-300 bg-amber-50 text-amber-800 hover:bg-amber-100'
            : 'border-line bg-surface-subtle text-fg-muted hover:bg-surface-hover hover:text-fg'
        }`}
      >
        <Zap className={`h-3.5 w-3.5 ${has ? 'text-amber-500' : 'text-fg-subtle'}`} />
        <span className="hidden lg:inline">临时变量</span>
        <span
          className={`tg-tabular min-w-4 rounded-full px-1 text-center font-mono text-[11px] ${
            has ? 'bg-amber-200/70 text-amber-900' : 'bg-surface-hover text-fg-subtle'
          }`}
        >
          {entries.length}
        </span>
      </button>

      {open && (
        <div
          ref={panelRef}
          popover="manual"
          role="dialog"
          aria-label="临时变量"
          className="tg-glass-popover fixed inset-auto m-0 z-50 w-[min(24rem,calc(100vw-1rem))] overflow-hidden text-fg flex flex-col"
        >
          <div className="flex items-center justify-between gap-2 border-b border-line bg-surface-subtle px-3.5 py-2">
            <div className="flex items-center gap-2 text-xs font-semibold">
              <Zap className="h-3.5 w-3.5 text-amber-500" />
              临时变量
              <span className="rounded-full border border-amber-200 bg-amber-50 px-1.5 text-[10px] font-medium text-amber-800">
                最高优先级
              </span>
            </div>
            <button
              type="button"
              onClick={() => setOpen(false)}
              aria-label="关闭"
              className="rounded p-1 text-fg-subtle hover:bg-surface-hover hover:text-fg"
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          <div className="min-h-0 flex-1 max-h-72 space-y-1.5 overflow-y-auto p-2.5 text-xs tg-scroller">
            {!has ? (
              <div className="py-5 text-center text-fg-subtle">暂无</div>
            ) : (
              entries.map(([k, val]) => (
                <div key={k} className="flex items-center gap-1.5 rounded-md border border-line bg-surface-subtle p-1.5">
                  <span className="max-w-[110px] truncate px-1 font-mono font-semibold text-sky-700" title={k}>
                    {k}
                  </span>
                  <input
                    type="text"
                    name={`temp_${k}`}
                    aria-label={`${k} 的值`}
                    value={val}
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    onChange={(e) => onChange({ ...tempVars, [k]: e.target.value })}
                    className="min-w-0 flex-1 rounded border border-line bg-surface px-2 py-1 font-mono text-xs text-fg focus:border-sky-500 focus:outline-none"
                  />
                  <button
                    type="button"
                    disabled={pinning === k}
                    title={activeEnv ? `固定到环境「${activeEnv.name}」` : '需先选择环境'}
                    aria-label="固定到环境"
                    onClick={() => void handlePinToEnv(k, val)}
                    className="rounded p-1 text-fg-subtle hover:bg-surface-hover hover:text-sky-600 disabled:opacity-40"
                  >
                    <Pin className="h-3.5 w-3.5" />
                  </button>
                  <button
                    type="button"
                    title="移除"
                    aria-label="移除"
                    onClick={() => {
                      const next = { ...tempVars }
                      delete next[k]
                      onChange(next)
                    }}
                    className="rounded p-1 text-fg-subtle hover:bg-surface-hover hover:text-rose-600"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                </div>
              ))
            )}
          </div>

          <form onSubmit={handleAdd} className="flex items-center gap-1.5 border-t border-line bg-surface-subtle p-2.5 text-xs">
            <input
              type="text"
              name="temp_new_key"
              aria-label="变量名"
              placeholder="变量名"
              value={newKey}
              autoCorrect="off"
              autoCapitalize="none"
              spellCheck={false}
              onChange={(e) => setNewKey(e.target.value)}
              className="w-28 rounded border border-line-strong bg-surface px-2 py-1 font-mono text-fg focus:border-sky-500 focus:outline-none"
            />
            <input
              type="text"
              name="temp_new_value"
              aria-label="变量值"
              placeholder="值"
              value={newVal}
              autoCorrect="off"
              autoCapitalize="none"
              spellCheck={false}
              onChange={(e) => setNewVal(e.target.value)}
              className="min-w-0 flex-1 rounded border border-line-strong bg-surface px-2 py-1 font-mono text-fg focus:border-sky-500 focus:outline-none"
            />
            <button
              type="submit"
              aria-label="添加"
              className="inline-flex items-center gap-1 rounded bg-sky-600 px-2.5 py-1 font-medium text-white hover:bg-sky-solid"
            >
              <Plus className="h-3 w-3" /> 添加
            </button>
            {has && (
              <button
                type="button"
                onClick={() => {
                  if (confirmClear) {
                    onChange({})
                    setConfirmClear(false)
                  } else setConfirmClear(true)
                }}
                onBlur={() => setConfirmClear(false)}
                title="清空"
                aria-label={confirmClear ? '确认清空' : '清空'}
                className={`inline-flex items-center gap-1 rounded px-2 py-1 font-medium transition-colors ${
                  confirmClear ? 'bg-rose-600 text-white hover:bg-rose-solid' : 'text-fg-subtle hover:bg-surface-hover hover:text-rose-600'
                }`}
              >
                <Trash2 className="h-3 w-3" />
                {confirmClear && '确认'}
              </button>
            )}
          </form>
        </div>
      )}
    </div>
  )
}
