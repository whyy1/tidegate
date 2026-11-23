import { Activity, Bot, FolderKanban, Globe, Play, Search, Server } from 'lucide-react'
import type { AccountModalTab } from './AccountModal'
import type { AgentView, Environment, Project, User } from '../lib/types'
import { TempVarsPopover } from './TempVarsPopover'

export type NavTab = 'workbench' | 'scenarios' | 'run' | 'history' | 'agents' | 'governance'

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
  onOpenAccountModal: (initialTab?: AccountModalTab) => void
  onOpenCommandPalette: () => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

export function TopBar({
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
  onOpenCommandPalette,
  onToast,
}: Props) {
  const activeEnv = environments.find((e) => e.id === activeEnvId) || null

  return (
    <header className="h-11 bg-white border-b border-slate-200 px-3.5 flex items-center justify-between gap-3 shrink-0">
      {/* Left: Project + Environment + Temp Vars */}
      <div className="flex items-center gap-2.5 min-w-0">
        <div className="flex items-center bg-slate-100 rounded-md border border-slate-200/90 text-xs min-w-0">
          <FolderKanban className="w-3.5 h-3.5 text-sky-700 ml-2 shrink-0" />
          <select
            name="active_project"
            aria-label="切换项目"
            value={activeProject?.id || ''}
            onChange={(e) => onSelectProject(e.target.value)}
            className="bg-transparent text-xs font-medium text-slate-800 px-2 py-1 focus:outline-none max-w-[140px]"
          >
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
          <button
            type="button"
            onClick={onOpenProjectModal}
            className="px-2 py-1 text-[11px] border-l border-slate-200 text-sky-700 hover:bg-slate-200/70 font-medium shrink-0"
          >
            管理
          </button>
        </div>

        <div className="flex items-center bg-slate-100 rounded-md border border-slate-200/90 text-xs min-w-0">
          <Globe className="w-3.5 h-3.5 text-emerald-700 ml-2 shrink-0" />
          <select
            name="active_env"
            aria-label="切换环境"
            value={activeEnvId}
            onChange={(e) => onSelectEnv(e.target.value)}
            className="bg-transparent text-xs font-medium text-slate-800 px-2 py-1 focus:outline-none max-w-[130px]"
          >
            <option value="">无环境</option>
            {environments.map((env) => (
              <option key={env.id} value={env.id}>
                {env.name}
              </option>
            ))}
          </select>
          {activeEnv?.base_url && (
            <span
              className="hidden md:inline-block min-w-0 max-w-[220px] truncate px-2 py-0.5 text-[11px] font-mono text-slate-500 border-l border-slate-200"
              title={activeEnv.base_url}
            >
              {activeEnv.base_url}
            </span>
          )}
          <button
            type="button"
            onClick={onOpenEnvModal}
            className="px-2 py-1 text-[11px] border-l border-slate-200 text-sky-700 hover:bg-slate-200/70 font-medium shrink-0"
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
      </div>

      {/* Right: Quick Search + Active Run + Nodes + MCP */}
      <div className="flex items-center gap-2 shrink-0">
        {activeRunId && activeTab !== 'run' && (
          <button
            type="button"
            onClick={() => onChangeTab('run')}
            className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-emerald-50 border border-emerald-300 text-emerald-800 hover:bg-emerald-100 text-xs font-semibold transition-colors"
            title="打开正在运行的实时压测"
          >
            <span className="w-2 h-2 rounded-full bg-emerald-500 animate-pulse" />
            <Play className="w-3 h-3 fill-current" />
            <span>运行中</span>
          </button>
        )}

        <button
          type="button"
          onClick={onOpenCommandPalette}
          className="hidden sm:inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md border border-slate-200 bg-slate-50 hover:bg-slate-100 text-xs text-slate-600 transition-colors"
          title="检索接口与命令 (Ctrl+K)"
        >
          <Search className="w-3.5 h-3.5 text-slate-400" />
          <span>检索</span>
          <kbd className="px-1 py-0.2 rounded border border-slate-300 bg-white font-mono text-[10px] text-slate-500">
            Ctrl+K
          </kbd>
        </button>

        <button
          type="button"
          onClick={() => onChangeTab('agents')}
          title={wsConnected ? 'WebSocket 已连接' : 'WebSocket 重连中'}
          className="tg-tabular inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md border border-slate-200 bg-slate-50 hover:bg-slate-100 text-xs text-slate-700 font-medium"
        >
          <span
            className={`w-2 h-2 rounded-full ${
              wsConnected && agents.length > 0 ? 'bg-emerald-500' : wsConnected ? 'bg-amber-500' : 'bg-rose-500'
            }`}
          />
          <Server className="w-3.5 h-3.5 text-slate-500" />
          <span>{agents.length} 节点</span>
        </button>

        <button
          type="button"
          onClick={() => onOpenAccountModal('mcp')}
          className="hidden lg:inline-flex items-center gap-1 px-2.5 py-1 rounded-md border border-sky-200 bg-sky-50 hover:bg-sky-100 text-xs text-sky-800 font-medium transition-colors"
        >
          <Bot className="w-3.5 h-3.5 text-sky-600" />
          <span>MCP</span>
        </button>

        {activeTab !== 'history' && (
          <button
            type="button"
            onClick={() => onChangeTab('history')}
            className="hidden 2xl:inline-flex items-center gap-1 px-2 py-1 rounded-md border border-slate-200 bg-white hover:bg-slate-50 text-xs text-slate-600"
          >
            <Activity className="w-3.5 h-3.5 text-slate-500" />
            <span>对比</span>
          </button>
        )}
      </div>
    </header>
  )
}
