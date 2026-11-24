import { Maximize2, Minimize2, X } from 'lucide-react'

export interface ModuleStripItem {
  id: string
  title: string
  metric?: string
  dark?: boolean
}

export function ModuleZoomButton({
  expanded,
  onToggle,
  tone = 'light',
}: {
  expanded: boolean
  onToggle: () => void
  tone?: 'light' | 'dark'
}) {
  const cls =
    tone === 'dark'
      ? 'border-white/25 bg-white/10 text-white/85 hover:bg-white/20 hover:text-white'
      : 'border-line bg-surface-subtle text-fg-muted hover:bg-surface-hover hover:text-tidal'

  return (
    <button
      type="button"
      onClick={onToggle}
      title={expanded ? '收回原位 (Esc)' : '原位放大'}
      aria-label={expanded ? '收回原位' : '原位放大'}
      aria-pressed={expanded}
      className={`inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-lg border transition-colors ${cls}`}
    >
      {expanded ? <Minimize2 className="h-3.5 w-3.5" /> : <Maximize2 className="h-3.5 w-3.5" />}
    </button>
  )
}

/**
 * Floating bottom glass thumbnail bar rendered when any module card is expanded in-place.
 * Positioned directly above `<GlassDock />` (`bottom-[70px]`) so both remain crisp and non-overlapping.
 */
export function BottomModuleStrip({
  items,
  activeId,
  onSelect,
  onClose,
}: {
  items: ModuleStripItem[]
  activeId: string
  onSelect: (id: string) => void
  onClose: () => void
}) {
  if (!activeId) return null

  return (
    <div
      role="region"
      aria-label="已最小化模块缩略条"
      className="fixed bottom-[70px] left-1/2 -translate-x-1/2 z-30 max-w-[calc(100vw-2rem)]"
    >
      <div className="tg-dock-bar rounded-2xl px-2.5 py-1.5 flex items-center gap-1.5 overflow-x-auto tg-scroller">
        {items.map((item) => {
          const isCurrent = item.id === activeId
          return (
            <button
              key={item.id}
              type="button"
              onClick={() => onSelect(item.id)}
              aria-current={isCurrent ? 'true' : undefined}
              className={`shrink-0 rounded-xl px-3 py-1.5 border text-left transition-all flex items-center gap-2 ${
                item.dark
                  ? 'bg-[#07192e] text-white border-sky-400/35'
                  : 'bg-surface/85 text-fg border-line'
              } ${
                isCurrent
                  ? 'ring-2 ring-sky-500 shadow-sm scale-[1.02]'
                  : 'opacity-75 hover:opacity-100'
              }`}
            >
              <span
                className={`h-1.5 w-1.5 rounded-full shrink-0 ${
                  isCurrent ? 'bg-sky-400' : 'bg-slate-400/50'
                }`}
              />
              <span className="text-xs font-semibold whitespace-nowrap">{item.title}</span>
              {item.metric && (
                <span
                  className={`tg-tabular font-mono text-[11px] whitespace-nowrap ${
                    item.dark ? 'text-[#7dd3fc]' : 'text-tidal'
                  }`}
                >
                  {item.metric}
                </span>
              )}
            </button>
          )
        })}

        <button
          type="button"
          onClick={onClose}
          title="收回并还原分屏 (Esc)"
          aria-label="收回并还原分屏"
          className="ml-1 inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-xl border border-line bg-surface-subtle text-fg-muted hover:bg-surface-hover hover:text-fg"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>
    </div>
  )
}
