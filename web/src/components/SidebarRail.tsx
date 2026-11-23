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
  const linkDot = linkState === 'ok' ? 'bg-emerald-400' : linkState === 'idle' ? 'bg-amber-400' : 'bg-rose-400'
  const linkTitle = linkState === 'ok' ? '遥测已连接' : linkState === 'idle' ? '遥测已连接 · 无在线节点' : '遥测重连中'

  const rowCls = collapsed ? 'justify-center px-0 h-9' : 'px-2.5 h-9'

  return (
    <aside
      aria-label="主导航"
      className={`tg-sidebar relative shrink-0 flex flex-col border-r border-white/[0.06] select-none transition-[width] duration-200 ease-out ${
        collapsed ? 'w-[60px]' : 'w-[204px]'
      }`}
    >
      {/* Brand */}
      <div className={`h-11 flex items-center shrink-0 border-b border-white/[0.07] ${collapsed ? 'justify-center' : 'px-3 gap-2.5'}`}>
        <img src="/favicon.svg" alt="" className="w-7 h-7 rounded-md shrink-0 ring-1 ring-white/15" />
        {!collapsed && (
          <span className="min-w-0 flex-1 truncate text-[13px] font-semibold tracking-tight text-white">Tidegate</span>
        )}
      </div>

      {/* Primary navigation */}
      <nav className="flex-1 min-h-0 overflow-y-auto tg-scroller-dark px-2 py-2.5">
        <ul className="space-y-0.5">
          {navItems.map((item) => {
            const Icon = item.icon
            const active = activeTab === item.id
            return (
              <li key={item.id}>
                <button
                  type="button"
                  onClick={() => onChangeTab(item.id)}
                  title={collapsed ? item.label : undefined}
                  aria-label={collapsed ? item.label : undefined}
                  aria-current={active ? 'page' : undefined}
                  className={`group relative w-full flex items-center gap-2.5 rounded-md text-[13px] transition-colors focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-sky-400 ${rowCls} ${
                    active
                      ? 'bg-gradient-to-r from-sky-400/[0.18] to-sky-400/[0.04] text-white font-semibold'
                      : 'text-slate-300 hover:bg-white/[0.06] hover:text-white'
                  }`}
                >
                  <span
                    aria-hidden="true"
                    className={`absolute left-0 top-2 bottom-2 w-[3px] rounded-r-full bg-sky-400 shadow-[0_0_10px_2px_rgb(56_189_248/0.55)] transition-opacity ${
                      active ? 'opacity-100' : 'opacity-0'
                    }`}
                  />
                  <Icon
                    className={`w-4 h-4 shrink-0 transition-colors ${active ? 'text-sky-300' : 'text-slate-400 group-hover:text-slate-200'}`}
                  />
                  {!collapsed && <span className="min-w-0 flex-1 truncate text-left">{item.label}</span>}
                  {!collapsed && item.pulse && (
                    <span className="inline-flex items-center gap-1 rounded-full bg-emerald-400/15 px-1.5 py-0.5 text-[10px] font-semibold text-emerald-300">
                      <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
                      运行
                    </span>
                  )}
                  {!collapsed && !item.pulse && item.badge && (
                    <span
                      className={`tg-tabular min-w-5 rounded px-1 py-px text-center font-mono text-[10px] ${
                        active ? 'bg-sky-400/20 text-sky-100' : 'bg-white/[0.07] text-slate-300'
                      }`}
                    >
                      {item.badge}
                    </span>
                  )}
                  {collapsed && item.pulse && (
                    <span aria-hidden="true" className="absolute top-1.5 right-2 h-2 w-2 rounded-full bg-emerald-400 animate-pulse" />
                  )}
                </button>
              </li>
            )
          })}
        </ul>
      </nav>

      {/* Utilities */}
      <div className="shrink-0 border-t border-white/[0.07] px-2 py-2 space-y-0.5">
        <button
          type="button"
          onClick={onOpenCommandPalette}
          title={collapsed ? '检索 Ctrl+K' : undefined}
          aria-label="检索"
          className={`w-full flex items-center gap-2.5 rounded-md text-[13px] text-slate-300 hover:bg-white/[0.06] hover:text-white transition-colors ${rowCls}`}
        >
          <Search className="w-4 h-4 shrink-0 text-slate-400" />
          {!collapsed && (
            <>
              <span className="min-w-0 flex-1 truncate text-left">检索</span>
              <kbd className="shrink-0 rounded border border-white/15 bg-black/25 px-1 py-px font-mono text-[10px] text-slate-400">
                Ctrl K
              </kbd>
            </>
          )}
        </button>

        <button
          type="button"
          onClick={() => onOpenAccountModal('mcp')}
          title={collapsed ? 'MCP' : undefined}
          aria-label="MCP 接入"
          className={`w-full flex items-center gap-2.5 rounded-md text-[13px] text-slate-300 hover:bg-white/[0.06] hover:text-white transition-colors ${rowCls}`}
        >
          <Bot className="w-4 h-4 shrink-0 text-cyan-300" />
          {!collapsed && <span className="min-w-0 flex-1 truncate text-left">MCP</span>}
        </button>

        <button
          type="button"
          onClick={() => onOpenAccountModal('sessions')}
          title={`${user.username} · ${ROLE_LABEL[user.role] || user.role} · ${linkTitle}`}
          aria-label="账户"
          className={`w-full flex items-center gap-2.5 rounded-md text-[13px] hover:bg-white/[0.06] transition-colors ${
            collapsed ? 'justify-center h-10' : 'px-2 h-11'
          }`}
        >
          <span className="relative shrink-0">
            <span className="flex h-7 w-7 items-center justify-center rounded-md bg-gradient-to-br from-sky-500/40 to-cyan-400/20 text-[12px] font-semibold uppercase text-white ring-1 ring-white/15">
              {user.username.slice(0, 1)}
            </span>
            <span
              aria-hidden="true"
              className={`absolute -right-0.5 -bottom-0.5 h-2.5 w-2.5 rounded-full ring-2 ring-[#08203b] ${linkDot}`}
            />
          </span>
          {!collapsed && (
            <span className="min-w-0 flex-1 text-left leading-tight">
              <span className="block truncate font-medium text-white">{user.username}</span>
              <span className="block truncate text-[11px] text-slate-400">{ROLE_LABEL[user.role] || user.role}</span>
            </span>
          )}
        </button>

        <button
          type="button"
          onClick={onToggleCollapse}
          aria-label={collapsed ? '展开侧栏' : '收起侧栏'}
          title={collapsed ? '展开侧栏' : '收起侧栏'}
          aria-expanded={!collapsed}
          className={`w-full flex items-center gap-2.5 rounded-md text-[12px] text-slate-500 hover:bg-white/[0.06] hover:text-slate-200 transition-colors ${
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
