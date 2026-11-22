import { Activity, Cpu, FolderKanban, Globe, Layers, Play, Server, UserCheck, Workflow } from 'lucide-react'
import type { AgentView, Environment, Project, User } from '../lib/types'
import { TempVarsPopover } from './TempVarsPopover'

export type NavTab = 'workbench' | 'scenarios' | 'run' | 'history' | 'agents'

interface Props {
  user: User
  projects: Project[]
  activeProject: Project | null
  environments: Environment[]
  activeEnvId: string
  tempVars: Record<string, string>
  agents: AgentView[]
  wsConnected: boolean
  activeTab: NavTab
  activeRunId: string
  onChangeTab: (tab: NavTab) => void
  onSelectProject: (id: string) => void
  onSelectEnv: (id: string) => void
  onTempVarsChange: (next: Record<string, string>) => void
  onEnvironmentsChanged: () => Promise<void>
  onOpenProjectModal: () => void
  onOpenEnvModal: () => void
  onOpenAccountModal: () => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

export function TopBar({
  user,
  projects,
  activeProject,
  environments,
  activeEnvId,
  tempVars,
  agents,
  wsConnected,
  activeTab,
  activeRunId,
  onChangeTab,
  onSelectProject,
  onSelectEnv,
  onTempVarsChange,
  onEnvironmentsChanged,
  onOpenProjectModal,
  onOpenEnvModal,
  onOpenAccountModal,
  onToast,
}: Props) {
  const activeEnv = environments.find((e) => e.id === activeEnvId) || null
  const navItems: Array<{ id: NavTab; label: string; icon: typeof Workflow; badge?: string }> = [
    { id: 'workbench', label: '接口工作台', icon: Layers },
    { id: 'scenarios', label: '场景与数据', icon: Workflow },
    { id: 'run', label: '实时压测', icon: Play, badge: activeRunId ? '运行中' : undefined },
    { id: 'history', label: '历史与对比', icon: Activity },
    { id: 'agents', label: '发压节点', icon: Server, badge: String(agents.length) },
  ]

  return (
    <header className="bg-[#0B2A4A] text-white border-b border-slate-800 px-3 py-2 flex flex-wrap items-center justify-between gap-2 shrink-0">
      <div className="flex items-center gap-3">
        <div className="flex items-center gap-2 pr-2">
          <img src="/favicon.svg" alt="Tidegate" className="w-7 h-7 rounded-md" />
          <span className="font-bold tracking-tight text-sm text-white">Tidegate</span>
        </div>

        <nav className="flex items-center gap-1 bg-white/8 p-0.5 rounded-lg">
          {navItems.map((item) => {
            const Icon = item.icon
            const active = activeTab === item.id
            return (
              <button
                key={item.id}
                type="button"
                onClick={() => onChangeTab(item.id)}
                className={`inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs font-medium transition-colors ${
                  active ? 'bg-[#3BA7FF] text-slate-950 font-semibold shadow-xs' : 'text-slate-200 hover:bg-white/10'
                }`}
              >
                <Icon className="w-3.5 h-3.5" />
                <span>{item.label}</span>
                {item.badge && (
                  <span
                    className={`px-1.5 py-0.2 rounded-full text-[10px] font-mono ${
                      item.id === 'run'
                        ? 'bg-emerald-400 text-slate-950 font-bold animate-pulse'
                        : active
                          ? 'bg-slate-900/20 text-slate-950'
                          : 'bg-white/15 text-slate-100'
                    }`}
                  >
                    {item.badge}
                  </span>
                )}
              </button>
            )
          })}
        </nav>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <div className="flex items-center bg-white/10 rounded-md border border-white/15">
          <FolderKanban className="w-3.5 h-3.5 text-sky-300 ml-2 shrink-0" />
          <select
            aria-label="切换项目"
            value={activeProject?.id || ''}
            onChange={(e) => onSelectProject(e.target.value)}
            className="bg-transparent text-xs text-white px-2 py-1 focus:outline-none max-w-[140px]"
          >
            {projects.map((p) => (
              <option key={p.id} value={p.id} className="text-slate-900">
                {p.name}
              </option>
            ))}
          </select>
          <button
            type="button"
            onClick={onOpenProjectModal}
            className="px-2 py-1 text-[11px] border-l border-white/15 text-sky-200 hover:text-white hover:bg-white/10"
          >
            管理
          </button>
        </div>

        <div className="flex items-center bg-white/10 rounded-md border border-white/15">
          <Globe className="w-3.5 h-3.5 text-emerald-300 ml-2 shrink-0" />
          <select
            aria-label="切换环境"
            value={activeEnvId}
            onChange={(e) => onSelectEnv(e.target.value)}
            className="bg-transparent text-xs text-white px-2 py-1 focus:outline-none max-w-[145px]"
          >
            <option value="" className="text-slate-900">
              无环境（直填完整URL）
            </option>
            {environments.map((env) => (
              <option key={env.id} value={env.id} className="text-slate-900">
                {env.name}
              </option>
            ))}
          </select>
          <button
            type="button"
            onClick={onOpenEnvModal}
            className="px-2 py-1 text-[11px] border-l border-white/15 text-sky-200 hover:text-white hover:bg-white/10"
          >
            环境
          </button>
        </div>

        {activeProject && (
          <TempVarsPopover
            projectId={activeProject.id}
            activeEnv={activeEnv}
            tempVars={tempVars}
            onChange={onTempVarsChange}
            onEnvironmentsChanged={onEnvironmentsChanged}
            onToast={onToast}
          />
        )}

        <button
          type="button"
          onClick={() => onChangeTab('agents')}
          title={wsConnected ? 'WebSocket 实时连接正常' : 'WebSocket 重连中'}
          className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-white/10 hover:bg-white/20 border border-white/15 text-xs"
        >
          <span
            className={`w-2 h-2 rounded-full ${
              wsConnected && agents.length > 0 ? 'bg-emerald-400' : wsConnected ? 'bg-amber-400' : 'bg-rose-400'
            }`}
          />
          <Cpu className="w-3.5 h-3.5 text-sky-300" />
          <span>{agents.length} 节点</span>
        </button>

        <button
          type="button"
          onClick={onOpenAccountModal}
          className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-white/10 hover:bg-white/20 border border-white/15 text-xs"
        >
          <UserCheck className="w-3.5 h-3.5 text-sky-200" />
          <span>{user.username}</span>
        </button>
      </div>
    </header>
  )
}
