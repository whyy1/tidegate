import { useLayoutEffect, useRef } from 'react'
import { CheckCircle2, AlertCircle, Info, X } from 'lucide-react'

export interface ToastItem {
  id: number
  kind: 'success' | 'error' | 'info'
  message: string
}

/**
 * Toasts live in a manual popover so they render in the top layer, above any open modal <dialog>.
 * Re-showing on every change moves the popover to the top of the top-layer stack.
 */
export function ToastContainer({
  items,
  onDismiss,
}: {
  items: ToastItem[]
  onDismiss: (id: number) => void
}) {
  const ref = useRef<HTMLDivElement | null>(null)

  useLayoutEffect(() => {
    const el = ref.current
    if (!el || typeof el.showPopover !== 'function') return
    try {
      if (el.matches(':popover-open')) el.hidePopover()
      if (items.length > 0) el.showPopover()
    } catch {
      // popover unsupported: element stays a fixed-position block
    }
  }, [items])

  return (
    <div
      ref={ref}
      popover="manual"
      aria-live="polite"
      className={`fixed inset-auto bottom-4 right-4 z-50 m-0 flex max-w-sm flex-col gap-2 overflow-visible border-0 bg-transparent p-0 ${
        items.length === 0 ? 'hidden' : ''
      }`}
    >
      {items.map((t) => (
        <div
          key={t.id}
          role={t.kind === 'error' ? 'alert' : 'status'}
          className={`tg-glass-popover flex items-start gap-2.5 px-3.5 py-2.5 text-sm text-fg ${
            t.kind === 'error'
              ? '!border-rose-400/55'
              : t.kind === 'success'
                ? '!border-emerald-400/55'
                : '!border-sky-400/55'
          }`}
        >
          {t.kind === 'error' ? (
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-rose-500" />
          ) : t.kind === 'success' ? (
            <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-emerald-500" />
          ) : (
            <Info className="mt-0.5 h-4 w-4 shrink-0 text-tidal" />
          )}
          <span className="flex-1 break-words leading-snug">{t.message}</span>
          <button
            type="button"
            onClick={() => onDismiss(t.id)}
            aria-label="关闭"
            className="shrink-0 text-fg-subtle hover:text-fg"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
      ))}
    </div>
  )
}
