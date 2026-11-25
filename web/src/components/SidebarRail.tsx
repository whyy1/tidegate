import { useRef } from 'react'
import {
  Activity,
  Bot,
  ChevronsLeft,
  ChevronsRight,
  Layers,
  Play,
  Search,
  Server,
  ShieldCheck,
  Workflow,
} from 'lucide-react'
import type { AccountModalTab } from './AccountModal'
import { LiquidCapsule } from './LiquidCapsule'
import type { NavTab } from './TopBar'
import type { AgentView, User } from '../lib/types'

interface Props {
  user: User
  agents: AgentView[]
  wsConnected: boolean
  activeTab: NavTab
  activeRunId: string
  collapsed: boolean
  onToggleCollapse: () => void
  onChangeTab: (tab: NavTab) => void
  onOpenCommandPalette: () => void
  onOpenAccountModal: (initialTab?: AccountModalTab) => void
}

const ROLE_LABEL: Record<string, string> = { admin: '管理员', member: '成员', viewer: '只读' }

export function SidebarRail({
  user,
  agents,
  wsConnected,
  activeTab,
  activeRunId,
  collapsed,
  onToggleCollapse,
  onChangeTab,
  onOpenCommandPalette,
  onOpenAccountModal,
}: Props) {
  const navListRef = useRef<HTMLUListElement | null>(null)

  const navItems: Array<{
    id: NavTab
    label: string
    icon: typeof Workflow
    badge?: string
    pulse?: boolean
  }> = [
    { id: 'workbench', label: '工作台', icon: Layers },
    { id: 'scenarios', label: '场景与数据', icon: Workflow },
    { id: 'run', label: '实时压测', icon: Play, pulse: Boolean(activeRunId) },
    { id: 'history', label: '历史对比', icon: Activity },
    { id: 'agents', label: '发压节点', icon: Server, badge: String(agents.length) },
    { id: 'governance', label: '系统治理', icon: ShieldCheck },
  ]

  const linkState = wsConnected && agents.length > 0 ? 'ok' : wsConnected ? 'idle' : 'down'
  const linkDot = linkState === 'ok' ? 'bg-emerald-500' : linkState === 'idle' ? 'bg-amber-500' : 'bg-rose-500'
  const linkTitle = linkState === 'ok' ? '遥测已连接' : linkState === 'idle' ? '遥测已连接 · 无在线节点' : '遥测重连中'

  const rowCls = collapsed ? 'justify-center px-0 h-9' : 'px-2.5 h-9'

  return (
    <aside
      aria-label="主导航"
      className={`tg-glass-card shrink-0 flex flex-col select-none transition-[width] duration-200 ease-out ${
        collapsed
          ? 'relative w-[60px]'
          : 'relative w-[196px] max-sm:fixed max-sm:inset-y-2.5 max-sm:left-2.5 max-sm:z-40 max-sm:bg-surface/95 max-sm:shadow-2xl'
      }`}
    >
      {/* Brand */}
      <div
        className={`relative z-10 h-12 flex items-center shrink-0 border-b border-line ${
          collapsed ? 'justify-center' : 'px-3 gap-2.5'
        }`}
      >
        <img src="/favicon.svg" alt="" className="w-7 h-7 rounded-lg shrink-0 shadow-xs ring-1 ring-line" />
        {!collapsed && (
          <span className="min-w-0 flex-1 truncate text-[13px] font-bold tracking-tight text-fg">Tidegate</span>
        )}
      </div>

      {/* Primary vertical navigation with LiquidCapsule */}
      <nav className="relative z-10 flex-1 min-h-0 overflow-y-auto tg-scroller px-2 py-2.5">
        <ul ref={navListRef} className="relative space-y-1">
          <LiquidCapsule
            containerRef={navListRef}
            activeId={activeTab}
            orientation="vertical"
            tone="light"
            className="rounded-xl"
          />
          {navItems.map((item) => {
            const Icon = item.icon
            const active = activeTab === item.id
            return (
              <li key={item.id}>
                <button
                  type="button"
                  data-liq-id={item.id}
                  onClick={() => onChangeTab(item.id)}
                  title={collapsed ? item.label : undefined}
                  aria-label={collapsed ? item.label : undefined}
                  aria-current={active ? 'page' : undefined}
                  className={`group relative z-10 w-full flex items-center gap-2.5 rounded-xl text-[13px] transition-colors focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-sky-500 ${rowCls} ${
                    active ? 'text-tidal font-semibold' : 'text-fg-muted hover:text-fg'
                  }`}
                >
                  <Icon
                    className={`w-4 h-4 shrink-0 transition-colors ${
                      active ? 'text-tidal' : 'text-fg-subtle group-hover:text-fg'
                    }`}
                  />
                  {!collapsed && <span className="min-w-0 flex-1 truncate text-left">{item.label}</span>}
                  {!collapsed && item.pulse && (
                    <span className="inline-flex items-center gap-1 rounded-full bg-emerald-500/15 px-1.5 py-0.5 text-[10px] font-semibold text-emerald-700">
                      <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />
                      运行
                    </span>
                  )}
                  {!collapsed && !item.pulse && item.badge && (
                    <span
                      className={`tg-tabular min-w-5 rounded-md px-1 py-px text-center font-mono text-[10px] ${
                        active ? 'bg-sky-500/15 text-tidal' : 'bg-surface-subtle text-fg-muted border border-line'
                      }`}
                    >
                      {item.badge}
                    </span>
                  )}
                  {collapsed && item.pulse && (
                    <span aria-hidden="true" className="absolute top-1.5 right-2 h-2 w-2 rounded-full bg-emerald-500 animate-pulse" />
                  )}
                </button>
              </li>
            )
          })}
        </ul>
      </nav>

      {/* Utilities */}
      <div className="relative z-10 shrink-0 border-t border-line px-2 pt-2 pb-16 sm:pb-2 space-y-0.5">
        <button
          type="button"
          onClick={onOpenCommandPalette}
          title={collapsed ? '检索 Ctrl+K' : undefined}
          aria-label="检索"
          className={`w-full flex items-center gap-2.5 rounded-xl text-[13px] text-fg-muted hover:bg-surface-hover hover:text-fg transition-colors ${rowCls}`}
        >
          <Search className="w-4 h-4 shrink-0 text-fg-subtle" />
          {!collapsed && (
            <>
              <span className="min-w-0 flex-1 truncate text-left">检索</span>
              <kbd className="tg-kbd font-mono">Ctrl K</kbd>
            </>
          )}
        </button>

        <button
          type="button"
          onClick={() => onOpenAccountModal('mcp')}
          title={collapsed ? 'MCP' : undefined}
          aria-label="MCP 接入"
          className={`w-full flex items-center gap-2.5 rounded-xl text-[13px] text-fg-muted hover:bg-surface-hover hover:text-fg transition-colors ${rowCls}`}
        >
          <Bot className="w-4 h-4 shrink-0 text-tidal" />
          {!collapsed && <span className="min-w-0 flex-1 truncate text-left">MCP</span>}
        </button>

        <button
          type="button"
          onClick={() => onOpenAccountModal('sessions')}
          title={`${user.username} · ${ROLE_LABEL[user.role] || user.role} · ${linkTitle}`}
          aria-label="账户"
          className={`w-full flex items-center gap-2.5 rounded-xl text-[13px] hover:bg-surface-hover transition-colors ${
            collapsed ? 'justify-center h-10' : 'px-2 h-11'
          }`}
        >
          <span className="relative shrink-0">
            <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-gradient-to-br from-sky-500 to-sky-700 text-[12px] font-semibold uppercase text-white shadow-xs">
              {user.username.slice(0, 1)}
            </span>
            <span
              aria-hidden="true"
              className={`absolute -right-0.5 -bottom-0.5 h-2.5 w-2.5 rounded-full ring-2 ring-surface ${linkDot}`}
            />
          </span>
          {!collapsed && (
            <span className="min-w-0 flex-1 text-left leading-tight">
              <span className="block truncate font-semibold text-fg">{user.username}</span>
              <span className="block truncate text-[11px] text-fg-subtle">{ROLE_LABEL[user.role] || user.role}</span>
            </span>
          )}
        </button>

        <button
          type="button"
          onClick={onToggleCollapse}
          aria-label={collapsed ? '展开侧栏' : '收起侧栏'}
          title={collapsed ? '展开侧栏' : '收起侧栏'}
          aria-expanded={!collapsed}
          className={`w-full flex items-center gap-2.5 rounded-xl text-[12px] text-fg-subtle hover:bg-surface-hover hover:text-fg transition-colors ${
            collapsed ? 'justify-center h-8' : 'px-2.5 h-8'
          }`}
        >
          {collapsed ? <ChevronsRight className="w-4 h-4" /> : <ChevronsLeft className="w-4 h-4 shrink-0" />}
          {!collapsed && <span className="truncate">收起</span>}
        </button>
      </div>
    </aside>
  )
}
