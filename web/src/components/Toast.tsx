import { CheckCircle2, AlertCircle, Info, X } from 'lucide-react'

export interface ToastItem {
  id: number
  kind: 'success' | 'error' | 'info'
  message: string
}

export function ToastContainer({
  items,
  onDismiss,
}: {
  items: ToastItem[]
  onDismiss: (id: number) => void
}) {
  if (items.length === 0) return null
  return (
    <div className="fixed bottom-4 right-4 z-50 flex flex-col gap-2 max-w-sm">
      {items.map((t) => (
        <div
          key={t.id}
          className={`flex items-start gap-2.5 rounded-lg border px-3.5 py-2.5 text-sm shadow-lg bg-white ${
            t.kind === 'error'
              ? 'border-rose-200 text-rose-800'
              : t.kind === 'success'
                ? 'border-emerald-200 text-emerald-800'
                : 'border-sky-200 text-sky-900'
          }`}
        >
          {t.kind === 'error' ? (
            <AlertCircle className="w-4 h-4 shrink-0 mt-0.5 text-rose-500" />
          ) : t.kind === 'success' ? (
            <CheckCircle2 className="w-4 h-4 shrink-0 mt-0.5 text-emerald-600" />
          ) : (
            <Info className="w-4 h-4 shrink-0 mt-0.5 text-sky-500" />
          )}
          <span className="flex-1 break-words leading-snug">{t.message}</span>
          <button
            type="button"
            onClick={() => onDismiss(t.id)}
            className="text-slate-400 hover:text-slate-600 shrink-0"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
      ))}
    </div>
  )
}
