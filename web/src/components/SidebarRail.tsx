import {
  Activity,
  Bot,
  ChevronLeft,
  ChevronRight,
  Layers,
  Play,
  Search,
  Server,
  ShieldCheck,
  UserCheck,
  Workflow,
} from 'lucide-react'
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
  onOpenAccountModal: (initialTab?: 'sessions' | 'tokens' | 'mcp' | 'password') => void
}

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
    { id: 'workbench', label: '接口工作台', icon: Layers },
    { id: 'scenarios', label: '场景与数据', icon: Workflow },
    { id: 'run', label: '实时压测', icon: Play, badge: activeRunId ? '运行中' : undefined, pulse: Boolean(activeRunId) },
    { id: 'history', label: '历史与对比', icon: Activity },
    { id: 'agents', label: '发压节点', icon: Server, badge: String(agents.length) },
    { id: 'governance', label: '系统治理', icon: ShieldCheck },
  ]

  return (
    <aside
      aria-label="主导航侧栏"
      className={`shrink-0 flex flex-col justify-between bg-gradient-to-b from-[#07192E] via-[#0B2A4A] to-[#08203B] text-slate-200 border-r border-slate-800/90 transition-[width] duration-150 select-none ${
        collapsed ? 'w-[60px]' : 'w-[204px]'
      }`}
    >
      <div className="flex flex-col min-h-0">
        {/* Brand Lockup */}
        <div className="h-11 px-3 flex items-center justify-between border-b border-white/10 shrink-0">
          <div className="flex items-center gap-2.5 min-w-0">
            <img src="/favicon.svg" alt="Tidegate" className="w-7 h-7 rounded-md shrink-0 ring-1 ring-white/15" />
            {!collapsed && (
              <div className="min-w-0">
                <div className="font-bold tracking-tight text-sm text-white leading-none">Tidegate</div>
                <div className="text-[10px] text-sky-300/80 font-mono mt-0.5 truncate">测控台 · IDE</div>
              </div>
            )}
          </div>
          <button
            type="button"
            onClick={onToggleCollapse}
            aria-label={collapsed ? '展开侧栏' : '折叠侧栏'}
            title={collapsed ? '展开导航栏' : '折叠导航栏'}
            className="p-1 rounded hover:bg-white/10 text-slate-300 hover:text-white transition-colors"
          >
            {collapsed ? <ChevronRight className="w-4 h-4" /> : <ChevronLeft className="w-4 h-4" />}
          </button>
        </div>

        {/* Navigation Items */}
        <nav className="p-2 space-y-1 flex-1 overflow-y-auto tg-scroller-dark">
          {navItems.map((item) => {
            const Icon = item.icon
            const active = activeTab === item.id
            return (
              <button
                key={item.id}
                type="button"
                onClick={() => onChangeTab(item.id)}
                title={collapsed ? item.label : undefined}
                aria-current={active ? 'page' : undefined}
                className={`relative w-full flex items-center gap-2.5 rounded-lg text-xs transition-colors ${
                  collapsed ? 'justify-center px-2 py-2.5' : 'px-3 py-2'
                } ${
                  active
                    ? 'bg-sky-500/20 text-white font-semibold ring-1 ring-sky-400/35'
                    : 'text-slate-300 hover:bg-white/8 hover:text-white font-medium'
                }`}
              >
                {active && (
                  <span
                    aria-hidden="true"
                    className="absolute left-0 top-1.5 bottom-1.5 w-[3px] rounded-r-full bg-[#38BDF8]"
                  />
                )}
                <Icon className={`w-4 h-4 shrink-0 ${active ? 'text-[#38BDF8]' : 'text-slate-300'}`} />
                {!collapsed && <span className="truncate flex-1 text-left">{item.label}</span>}
                {!collapsed && item.badge && (
                  <span
                    className={`tg-tabular px-1.5 py-0.5 rounded-full text-[10px] font-mono ${
                      item.pulse
                        ? 'bg-emerald-400 text-slate-950 font-bold animate-pulse'
                        : active
                          ? 'bg-sky-400/25 text-sky-100'
                          : 'bg-white/10 text-slate-200'
                    }`}
                  >
                    {item.badge}
                  </span>
                )}
                {collapsed && item.pulse && (
                  <span
                    aria-hidden="true"
                    className="absolute top-1.5 right-1.5 w-2 h-2 rounded-full bg-emerald-400 animate-pulse"
                  />
                )}
              </button>
            )
          })}
        </nav>
      </div>

      {/* Bottom Utility Actions */}
      <div className="p-2 border-t border-white/10 space-y-1 shrink-0">
        <button
          type="button"
          onClick={onOpenCommandPalette}
          title="全局检索接口、场景与命令 (Ctrl+K)"
          className={`w-full flex items-center gap-2 rounded-lg border border-white/10 bg-white/5 hover:bg-white/12 text-xs text-slate-200 transition-colors ${
            collapsed ? 'justify-center px-2 py-2' : 'px-2.5 py-1.5'
          }`}
        >
          <Search className="w-3.5 h-3.5 text-sky-300 shrink-0" />
          {!collapsed && (
            <>
              <span className="flex-1 text-left truncate text-slate-300">快速检索</span>
              <kbd className="px-1.5 py-0.5 rounded bg-slate-950/60 border border-white/15 font-mono text-[10px] text-slate-300">
                Ctrl+K
              </kbd>
            </>
          )}
        </button>

        <button
          type="button"
          onClick={() => onOpenAccountModal('mcp')}
          title="查看 MCP 智能体接入配置与访问令牌"
          className={`w-full flex items-center gap-2 rounded-lg text-xs text-sky-200 hover:bg-white/10 transition-colors ${
            collapsed ? 'justify-center px-2 py-2' : 'px-2.5 py-1.5'
          }`}
        >
          <Bot className="w-3.5 h-3.5 text-cyan-300 shrink-0" />
          {!collapsed && <span className="truncate text-left flex-1">MCP 智能体接入</span>}
        </button>

        <button
          type="button"
          onClick={() => onOpenAccountModal('sessions')}
          title={`当前账号：${user.username} (${user.role})`}
          className={`w-full flex items-center gap-2 rounded-lg bg-white/6 hover:bg-white/14 text-xs text-white transition-colors ${
            collapsed ? 'justify-center px-2 py-2' : 'px-2.5 py-2'
          }`}
        >
          <span
            title={wsConnected ? '实时遥测 WebSocket 已连接' : 'WebSocket 重连中'}
            className={`w-2 h-2 rounded-full shrink-0 ${
              wsConnected && agents.length > 0 ? 'bg-emerald-400' : wsConnected ? 'bg-amber-400' : 'bg-rose-400'
            }`}
          />
          {collapsed ? (
            <UserCheck className="w-3.5 h-3.5 text-sky-200 shrink-0" />
          ) : (
            <div className="min-w-0 flex-1 text-left">
              <div className="font-medium text-white truncate leading-tight">{user.username}</div>
              <div className="text-[10px] font-mono text-slate-400 leading-tight">{user.role}</div>
            </div>
          )}
        </button>
      </div>
    </aside>
  )
}
