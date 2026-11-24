import { useRef } from 'react'
import { Activity, Bot, Layers, Play, Search, Server, ShieldCheck, Workflow } from 'lucide-react'
import { prefersReducedMotion } from '../lib/glassPhysics'
import type { NavTab } from './TopBar'

interface Props {
  activeTab: NavTab
  activeRunId: string
  onChangeTab: (tab: NavTab) => void
  onOpenCommandPalette: () => void
  onOpenMCP: () => void
}

const DOCK_ITEMS: Array<{
  id: NavTab
  label: string
  icon: typeof Layers
}> = [
  { id: 'workbench', label: '工作台', icon: Layers },
  { id: 'scenarios', label: '场景与数据', icon: Workflow },
  { id: 'run', label: '实时压测', icon: Play },
  { id: 'history', label: '历史对比', icon: Activity },
  { id: 'agents', label: '发压节点', icon: Server },
  { id: 'governance', label: '系统治理', icon: ShieldCheck },
]

/**
 * Floating bottom-center frosted glass dock (icons only):
 * Smoothly magnifies hovered and adjacent icons via Gaussian falloff and floats the item name above.
 */
export function GlassDock({
  activeTab,
  activeRunId,
  onChangeTab,
  onOpenCommandPalette,
  onOpenMCP,
}: Props) {
  const dockRef = useRef<HTMLDivElement | null>(null)

  const handlePointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (e.pointerType === 'touch' || prefersReducedMotion() || !dockRef.current) return
    const items = dockRef.current.querySelectorAll<HTMLElement>('[data-dock-btn]')
    items.forEach((btn) => {
      const r = btn.getBoundingClientRect()
      const cx = r.left + r.width / 2
      const dist = Math.abs(e.clientX - cx)
      const mag = 1 + 0.42 * Math.exp(-(dist * dist) / (2 * 46 * 46))
      const lift = -((mag - 1) * 14)
      btn.style.transform = `translateY(${lift.toFixed(1)}px) scale(${mag.toFixed(3)})`
    })
  }

  const resetMag = () => {
    if (!dockRef.current) return
    const items = dockRef.current.querySelectorAll<HTMLElement>('[data-dock-btn]')
    items.forEach((btn) => {
      btn.style.transform = ''
    })
  }

  return (
    <div className="fixed bottom-2.5 left-1/2 -translate-x-1/2 z-30 max-w-[calc(100vw-1rem)]">
      <div
        ref={dockRef}
        role="toolbar"
        aria-label="快捷停靠栏"
        onPointerMove={handlePointerMove}
        onPointerLeave={resetMag}
        className="tg-dock-bar rounded-2xl px-2.5 py-1.5 flex items-end gap-1.5"
      >
        {DOCK_ITEMS.map(({ id, label, icon: Icon }) => {
          const active = activeTab === id
          const pulsing = id === 'run' && Boolean(activeRunId)
          return (
            <button
              key={id}
              type="button"
              data-dock-btn
              aria-label={label}
              aria-current={active ? 'page' : undefined}
              onClick={() => onChangeTab(id)}
              className={`tg-dock-btn relative h-9 w-9 rounded-xl border inline-flex items-center justify-center ${
                active
                  ? 'bg-gradient-to-br from-sky-500 to-sky-700 text-white border-sky-300/60 shadow-[0_8px_18px_-5px_rgb(2_132_199/0.55)]'
                  : 'bg-surface/85 border-line text-fg hover:text-tidal'
              }`}
            >
              <span className="tg-dock-tip pointer-events-none absolute bottom-[calc(100%+10px)] left-1/2 rounded-full border border-line bg-surface/95 px-2.5 py-0.5 text-[11px] font-semibold text-fg whitespace-nowrap shadow-md">
                {label}
              </span>
              <Icon className="h-4 w-4" />
              {pulsing && (
                <span
                  aria-hidden="true"
                  className="absolute top-1 right-1 h-2 w-2 rounded-full bg-emerald-400 animate-pulse"
                />
              )}
            </button>
          )
        })}

        <span aria-hidden="true" className="mx-0.5 h-6 w-px bg-line-strong self-center" />

        <button
          type="button"
          data-dock-btn
          aria-label="命令面板 Ctrl+K"
          onClick={onOpenCommandPalette}
          className="tg-dock-btn relative h-9 w-9 rounded-xl border border-line bg-surface/85 text-fg hover:text-tidal inline-flex items-center justify-center"
        >
          <span className="tg-dock-tip pointer-events-none absolute bottom-[calc(100%+10px)] left-1/2 rounded-full border border-line bg-surface/95 px-2.5 py-0.5 text-[11px] font-semibold text-fg whitespace-nowrap shadow-md">
            检索 Ctrl+K
          </span>
          <Search className="h-4 w-4" />
        </button>

        <button
          type="button"
          data-dock-btn
          aria-label="MCP 接入"
          onClick={onOpenMCP}
          className="tg-dock-btn relative h-9 w-9 rounded-xl border border-line bg-surface/85 text-cyan-600 dark:text-cyan-300 hover:bg-surface-hover inline-flex items-center justify-center"
        >
          <span className="tg-dock-tip pointer-events-none absolute bottom-[calc(100%+10px)] left-1/2 rounded-full border border-line bg-surface/95 px-2.5 py-0.5 text-[11px] font-semibold text-fg whitespace-nowrap shadow-md">
            MCP 接入
          </span>
          <Bot className="h-4 w-4" />
        </button>
      </div>
    </div>
  )
}
