import { Bot, FolderKanban, Play, Search, Server, SlidersHorizontal } from 'lucide-react'
import type { AccountModalTab } from './AccountModal'
import { DropletFilter, RollingNumber } from './DropletFilter'
import type { AgentView, Environment, Project, User } from '../lib/types'
import { TempVarsPopover } from './TempVarsPopover'
import { ThemeToggle } from './ThemeToggle'

export type NavTab = 'workbench' | 'scenarios' | 'run' | 'history' | 'agents' | 'governance'

const VIEW_LABEL: Record<NavTab, string> = {
  workbench: '工作台',
  scenarios: '场景与数据',
  run: '实时压测',
  history: '历史对比',
  agents: '发压节点',
  governance: '系统治理',
}

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

const segCls = 'flex h-7 items-center rounded-full border border-line bg-surface-subtle text-xs min-w-0'
const segBtnCls =
  'flex h-full w-7 shrink-0 items-center justify-center border-l border-line text-fg-subtle hover:bg-surface-hover hover:text-fg transition-colors rounded-r-full'
const chipCls =
  'inline-flex h-7 items-center gap-1.5 rounded-full border border-line bg-surface-subtle px-2.5 text-xs text-fg-muted hover:bg-surface-hover hover:text-fg transition-colors'

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
  const linkState = wsConnected && agents.length > 0 ? 'ok' : wsConnected ? 'idle' : 'down'

  const envOptions = [
    { value: '', label: '无环境', dotClass: 'bg-slate-400' },
    ...environments.map((env) => ({ value: env.id, label: env.name, dotClass: 'bg-emerald-500' })),
  ]

  return (
    <header className="relative z-30 h-11 shrink-0 rounded-[18px] border border-line bg-surface/78 backdrop-blur-xl px-3 shadow-[0_12px_30px_-18px_var(--tg-shadow),inset_0_1px_0_var(--tg-specular)] flex items-center justify-between gap-2.5">
      <div className="flex items-center gap-2 min-w-0">
        <h1 className="hidden xl:block shrink-0 pr-1 text-[13px] font-bold text-fg">{VIEW_LABEL[activeTab]}</h1>
        <span aria-hidden="true" className="hidden xl:block h-4 w-px bg-line-strong" />

        <div className={segCls}>
          <FolderKanban className="ml-2.5 h-3.5 w-3.5 shrink-0 text-sky-600" aria-hidden="true" />
          <select
            name="active_project"
            aria-label="项目"
            value={activeProject?.id || ''}
            onChange={(e) => onSelectProject(e.target.value)}
            style={{ color: 'var(--tg-text)', colorScheme: 'inherit' }}
            className="h-full min-w-0 max-w-[148px] bg-surface-subtle pl-1.5 pr-1 text-xs font-medium text-fg focus:outline-none [&>option]:bg-surface [&>option]:text-fg"
          >
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
          <button type="button" onClick={onOpenProjectModal} aria-label="项目管理" title="项目管理" className={segBtnCls}>
            <SlidersHorizontal className="h-3.5 w-3.5" />
          </button>
        </div>

        {/* Liquid Droplet Split Filter for Environments */}
        <div className="flex items-center gap-1 min-w-0">
          <DropletFilter
            ariaLabel="切换环境"
            value={activeEnvId}
            onChange={onSelectEnv}
            options={envOptions}
          />
          {activeEnv?.base_url && (
            <span
              className="hidden md:inline-flex h-7 items-center min-w-0 max-w-[200px] truncate rounded-full border border-line bg-surface-subtle px-2.5 font-mono text-[11px] text-fg-subtle"
              title={activeEnv.base_url}
            >
              {activeEnv.base_url}
            </span>
          )}
          <button
            type="button"
            onClick={onOpenEnvModal}
            aria-label="环境管理"
            title="环境管理"
            className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-full border border-line bg-surface-subtle text-fg-subtle hover:bg-surface-hover hover:text-fg transition-colors"
          >
            <SlidersHorizontal className="h-3.5 w-3.5" />
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

      <div className="flex items-center gap-1.5 shrink-0">
        {activeRunId && activeTab !== 'run' && (
          <button
            type="button"
            onClick={() => onChangeTab('run')}
            title="打开实时压测"
            className="inline-flex h-7 items-center gap-1.5 rounded-full border border-emerald-300 bg-emerald-50 px-2.5 text-xs font-semibold text-emerald-800 hover:bg-emerald-100 transition-colors"
          >
            <span className="h-2 w-2 rounded-full bg-emerald-500 animate-pulse" />
            <Play className="h-3 w-3 fill-current" />
            运行中
          </button>
        )}

        <button type="button" onClick={onOpenCommandPalette} title="检索 Ctrl+K 或 /" className={`hidden sm:inline-flex ${chipCls}`}>
          <Search className="h-3.5 w-3.5 text-fg-subtle" />
          <span>检索</span>
          <kbd className="tg-kbd font-mono">Ctrl K</kbd>
        </button>

        <button
          type="button"
          onClick={() => onChangeTab('agents')}
          title={linkState === 'ok' ? '遥测已连接' : linkState === 'idle' ? '无在线节点' : '遥测重连中'}
          className={`tg-tabular ${chipCls}`}
        >
          <span
            className={`h-2 w-2 rounded-full ${
              linkState === 'ok' ? 'bg-emerald-500' : linkState === 'idle' ? 'bg-amber-500' : 'bg-rose-500'
            }`}
          />
          <Server className="h-3.5 w-3.5 text-fg-subtle" />
          <RollingNumber value={agents.length} className="font-semibold text-fg" />
          <span className="hidden md:inline">节点</span>
        </button>

        <button
          type="button"
          onClick={() => onOpenAccountModal('mcp')}
          className="hidden lg:inline-flex h-7 items-center gap-1.5 rounded-full border border-sky-200 bg-sky-50 px-2.5 text-xs font-medium text-sky-800 hover:bg-sky-100 transition-colors"
        >
          <Bot className="h-3.5 w-3.5 text-sky-600" />
          MCP
        </button>

        <span aria-hidden="true" className="mx-0.5 h-4 w-px bg-line-strong" />
        <ThemeToggle />
      </div>
    </header>
  )
}
