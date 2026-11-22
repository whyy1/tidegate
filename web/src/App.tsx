import { useCallback, useEffect, useState } from 'react'
import { AccountModal } from './components/AccountModal'
import { AuthGate } from './components/AuthGate'
import { EnvModal } from './components/EnvModal'
import { ProjectModal } from './components/ProjectModal'
import { ToastContainer, type ToastItem } from './components/Toast'
import { TopBar, type NavTab } from './components/TopBar'
import { api, UNAUTHORIZED_EVENT } from './lib/api'
import type { API, AgentView, Environment, Project, SetupStatus, User } from './lib/types'
import { wsClient } from './lib/ws'
import { WorkbenchView } from './views/WorkbenchView'

let toastSeq = 1

export function App() {
  const [booting, setBooting] = useState(true)
  const [setupStatus, setSetupStatus] = useState<SetupStatus | null>(null)
  const [user, setUser] = useState<User | null>(null)

  const [projects, setProjects] = useState<Project[]>([])
  const [activeProjectId, setActiveProjectId] = useState('')
  const [environments, setEnvironments] = useState<Environment[]>([])
  const [activeEnvId, setActiveEnvId] = useState('')
  const [tempVars, setTempVars] = useState<Record<string, string>>({})

  const [agents, setAgents] = useState<AgentView[]>([])
  const [wsConnected, setWsConnected] = useState(false)
  const [activeRunId, setActiveRunId] = useState('')
  const [activeTab, setActiveTab] = useState<NavTab>('workbench')
  const [quickLoadAPI, setQuickLoadAPI] = useState<Partial<API> | null>(null)

  const [projectModalOpen, setProjectModalOpen] = useState(false)
  const [envModalOpen, setEnvModalOpen] = useState(false)
  const [accountModalOpen, setAccountModalOpen] = useState(false)
  const [toasts, setToasts] = useState<ToastItem[]>([])

  const pushToast = useCallback((kind: ToastItem['kind'], message: string) => {
    const id = toastSeq++
    setToasts((prev) => [...prev.slice(-4), { id, kind, message }])
    window.setTimeout(() => {
      setToasts((prev) => prev.filter((item) => item.id !== id))
    }, 4000)
  }, [])

  const bootstrap = useCallback(async () => {
    setBooting(true)
    try {
      const st = await api.setupStatus()
      setSetupStatus(st)
      if (!st.setup_required) {
        try {
          const meRes = await api.me()
          setUser(meRes.user)
        } catch {
          setUser(null)
        }
      } else {
        setUser(null)
      }
    } finally {
      setBooting(false)
    }
  }, [])

  useEffect(() => {
    void bootstrap()
    const onUnauth = () => {
      setUser(null)
      wsClient.stop()
      void api.setupStatus().then(setSetupStatus).catch(() => undefined)
    }
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauth)
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, onUnauth)
  }, [bootstrap])

  const loadProjects = useCallback(
    async (preferId?: string) => {
      const list = await api.listProjects()
      setProjects(list)
      setActiveProjectId((cur) => {
        if (preferId && list.some((p) => p.id === preferId)) return preferId
        if (cur && list.some((p) => p.id === cur)) return cur
        return list[0]?.id || ''
      })
    },
    []
  )

  const loadEnvironments = useCallback(async (pid: string) => {
    if (!pid) {
      setEnvironments([])
      setActiveEnvId('')
      return
    }
    const list = await api.listEnvironments(pid)
    setEnvironments(list)
    setActiveEnvId((cur) => {
      if (cur && list.some((e) => e.id === cur)) return cur
      return list[0]?.id || ''
    })
  }, [])

  useEffect(() => {
    if (!user) {
      wsClient.stop()
      return
    }
    void loadProjects()
    void api.listAgents().then(setAgents).catch(() => undefined)

    wsClient.start()
    const unsubStatus = wsClient.onStatus(setWsConnected)
    const unsubEv = wsClient.onEvent((ev) => {
      if (ev.type === 'agents' && ev.agents) {
        setAgents(ev.agents)
      } else if (ev.type === 'run_status' && ev.run_id) {
        if (ev.status === 'running' || ev.status === 'preparing' || ev.status === 'stopping') {
          setActiveRunId(ev.run_id)
        } else if (ev.status === 'finished' || ev.status === 'failed') {
          setActiveRunId((cur) => (cur === ev.run_id ? '' : cur))
        }
      }
    })
    return () => {
      unsubStatus()
      unsubEv()
      wsClient.stop()
    }
  }, [user, loadProjects])

  useEffect(() => {
    if (user && activeProjectId) {
      void loadEnvironments(activeProjectId)
    }
  }, [user, activeProjectId, loadEnvironments])

  const activeProject = projects.find((p) => p.id === activeProjectId) || null

  if (booting || !setupStatus) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-slate-100 text-slate-600 text-sm">
        正在连接 Tidegate 控制台...
      </div>
    )
  }

  if (!user) {
    return (
      <AuthGate
        status={setupStatus}
        onAuthenticated={async (u) => {
          setUser(u)
          const st = await api.setupStatus()
          setSetupStatus(st)
        }}
      />
    )
  }

  return (
    <div className="h-screen flex flex-col bg-slate-100 overflow-hidden">
      <TopBar
        user={user}
        projects={projects}
        activeProject={activeProject}
        environments={environments}
        activeEnvId={activeEnvId}
        tempVars={tempVars}
        agents={agents}
        wsConnected={wsConnected}
        activeTab={activeTab}
        activeRunId={activeRunId}
        onChangeTab={setActiveTab}
        onSelectProject={(pid) => {
          setActiveProjectId(pid)
          setTempVars({})
        }}
        onSelectEnv={setActiveEnvId}
        onTempVarsChange={setTempVars}
        onEnvironmentsChanged={async () => {
          if (activeProjectId) await loadEnvironments(activeProjectId)
        }}
        onOpenProjectModal={() => setProjectModalOpen(true)}
        onOpenEnvModal={() => setEnvModalOpen(true)}
        onOpenAccountModal={() => setAccountModalOpen(true)}
        onToast={pushToast}
      />

      {!activeProject ? (
        <div className="flex-1 flex items-center justify-center text-sm text-slate-500">
          暂无可用项目，请点击顶部「项目管理」新建或导入项目。
        </div>
      ) : activeTab === 'workbench' ? (
        <WorkbenchView
          projectId={activeProject.id}
          activeEnvId={activeEnvId}
          tempVars={tempVars}
          onTempVarsChange={setTempVars}
          onWorkspaceMutated={() => loadEnvironments(activeProject.id)}
          onQuickLoadTest={(apiDraft) => {
            setQuickLoadAPI(apiDraft)
            setActiveTab('run')
            pushToast('info', `已将「${apiDraft.name || '未命名接口'}」带入压测视图`)
          }}
          onToast={pushToast}
        />
      ) : (
        <div className="flex-1 flex items-center justify-center p-8 bg-slate-50">
          <div className="max-w-lg text-center space-y-2 border border-slate-200 bg-white rounded-xl p-6 shadow-xs">
            <h2 className="text-base font-semibold text-slate-900">
              {activeTab === 'scenarios'
                ? '场景编排、CSV 数据预览与功能试跑'
                : activeTab === 'run'
                  ? '实时压测控制台与 uPlot 多维监控'
                  : activeTab === 'history'
                    ? '压测历史报告、基线对比与 HTML/HAR 导出'
                    : '集群发压节点健康与带宽上限管理'}
            </h2>
            <p className="text-xs text-slate-500 leading-relaxed">
              当前阶段（F3A）已集成核心工作台与 API 调试全流程；本视图将在下一步里程碑（F3B）装配完整交互面板。
              {quickLoadAPI && activeTab === 'run' ? `（已预载接口：${quickLoadAPI.name || '草稿'}）` : ''}
            </p>
            <div>
              <button
                type="button"
                onClick={() => setActiveTab('workbench')}
                className="mt-2 px-4 py-1.5 rounded-md bg-sky-600 text-white text-xs font-medium hover:bg-sky-700"
              >
                返回接口工作台
              </button>
            </div>
          </div>
        </div>
      )}

      <ProjectModal
        open={projectModalOpen}
        projects={projects}
        activeProject={activeProject}
        onClose={() => setProjectModalOpen(false)}
        onSelectProject={setActiveProjectId}
        onProjectsChanged={loadProjects}
        onToast={pushToast}
      />

      {activeProject && (
        <EnvModal
          open={envModalOpen}
          projectId={activeProject.id}
          environments={environments}
          activeEnvId={activeEnvId}
          onClose={() => setEnvModalOpen(false)}
          onSelectEnv={setActiveEnvId}
          onEnvironmentsChanged={() => loadEnvironments(activeProject.id)}
          onToast={pushToast}
        />
      )}

      <AccountModal
        open={accountModalOpen}
        user={user}
        onClose={() => setAccountModalOpen(false)}
        onToast={pushToast}
        onLoggedOut={() => {
          setUser(null)
          wsClient.stop()
        }}
      />

      <ToastContainer
        items={toasts}
        onDismiss={(id) => setToasts((prev) => prev.filter((x) => x.id !== id))}
      />
    </div>
  )
}
