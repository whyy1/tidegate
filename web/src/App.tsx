import { useCallback, useEffect, useMemo, useState } from 'react'
import { AccountModal, type AccountModalTab } from './components/AccountModal'
import { AuthGate } from './components/AuthGate'
import { CommandPalette } from './components/CommandPalette'
import { EnvModal } from './components/EnvModal'
import { ProjectModal } from './components/ProjectModal'
import { SidebarRail } from './components/SidebarRail'
import { ToastContainer, type ToastItem } from './components/Toast'
import { TopBar, type NavTab } from './components/TopBar'
import { api, UNAUTHORIZED_EVENT } from './lib/api'
import type {
  API,
  APIBody,
  AgentView,
  Environment,
  KeyValue,
  Project,
  SampleView,
  SetupStatus,
  SysmonStats,
  User,
} from './lib/types'
import { wsClient } from './lib/ws'
import { AgentsView } from './views/AgentsView'
import { GovernanceView } from './views/GovernanceView'
import { HistoryCompareView } from './views/HistoryCompareView'
import { LiveRunView } from './views/LiveRunView'
import { ScenariosView } from './views/ScenariosView'
import { WorkbenchView } from './views/WorkbenchView'

let toastSeq = 1
let replaySeq = 1

function sampleToDraftAPI(sample: SampleView): Partial<API> {
  const headers: KeyValue[] = []
  let contentType = ''
  for (const h of sample.request_headers || []) {
    const low = h.name.toLowerCase()
    if (low === 'host' || low === 'content-length') continue
    if (low === 'content-type') contentType = h.value
    headers.push({ name: h.name, value: h.value })
  }

  let body: APIBody = { type: 'none' }
  if (sample.request_body) {
    const ctLow = contentType.toLowerCase()
    const trimmed = sample.request_body.trim()
    if (ctLow.includes('application/json') || trimmed.startsWith('{') || trimmed.startsWith('[')) {
      body = { type: 'json', content: sample.request_body }
    } else if (ctLow.includes('application/x-www-form-urlencoded')) {
      const params = new URLSearchParams(sample.request_body)
      const fields: KeyValue[] = []
      params.forEach((v, k) => fields.push({ name: k, value: v }))
      body = fields.length > 0 ? { type: 'form', fields } : { type: 'raw', content_type: contentType, content: sample.request_body }
    } else {
      body = { type: 'raw', content_type: contentType || 'text/plain', content: sample.request_body }
    }
  }

  return {
    name: sample.step ? `复现: ${sample.step}` : `复现 ${sample.method || 'GET'}`,
    folder_id: '',
    request: {
      method: sample.method || 'GET',
      url: sample.url || '',
      path_params: [],
      query: [],
      headers,
      body,
    },
    auth: { type: 'none' },
    extract: [],
    assert: [],
  }
}

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
  const [sysByAgent, setSysByAgent] = useState<Record<string, SysmonStats>>({})
  const [wsConnected, setWsConnected] = useState(false)
  const [activeRunId, setActiveRunId] = useState('')
  const [activeTab, setActiveTab] = useState<NavTab>('workbench')
  const [quickLoadAPI, setQuickLoadAPI] = useState<Partial<API> | null>(null)
  const [preloadedScenarioId, setPreloadedScenarioId] = useState('')
  const [inspectRunId, setInspectRunId] = useState('')
  const [incomingReplay, setIncomingReplay] = useState<{ seq: number; api: Partial<API> } | null>(null)
  const [openAPIRequest, setOpenAPIRequest] = useState<{ seq: number; apiId?: string; scratch?: boolean } | null>(null)

  const [sidebarCollapsed, setSidebarCollapsed] = useState<boolean>(() => {
    try {
      return window.localStorage.getItem('tg.sidebar.collapsed') === '1'
    } catch {
      return false
    }
  })
  const [cmdOpen, setCmdOpen] = useState(false)
  const [projectModalOpen, setProjectModalOpen] = useState(false)
  const [envModalOpen, setEnvModalOpen] = useState(false)
  const [accountModalOpen, setAccountModalOpen] = useState(false)
  const [accountInitialTab, setAccountInitialTab] = useState<AccountModalTab>('sessions')
  const [toasts, setToasts] = useState<ToastItem[]>([])

  const toggleSidebar = useCallback(() => {
    setSidebarCollapsed((cur) => {
      const next = !cur
      try {
        window.localStorage.setItem('tg.sidebar.collapsed', next ? '1' : '0')
      } catch {
        // ignore storage errors
      }
      return next
    })
  }, [])

  const openAccountWithTab = useCallback((initialTab: AccountModalTab = 'sessions') => {
    setAccountInitialTab(initialTab)
    setAccountModalOpen(true)
  }, [])

  useEffect(() => {
    const onGlobalKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setCmdOpen((v) => !v)
      }
    }
    window.addEventListener('keydown', onGlobalKey)
    return () => window.removeEventListener('keydown', onGlobalKey)
  }, [])

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
      } else if (ev.type === 'sys' && ev.agent_id && ev.sys) {
        const aid = ev.agent_id
        const sys = ev.sys
        setSysByAgent((prev) => ({ ...prev, [aid]: sys }))
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

  const liveAgents = useMemo(
    () => agents.map((ag) => (sysByAgent[ag.id] ? { ...ag, stats: sysByAgent[ag.id] } : ag)),
    [agents, sysByAgent]
  )

  const selectProject = useCallback((pid: string) => {
    setActiveProjectId(pid)
    setTempVars({})
    setQuickLoadAPI(null)
    setPreloadedScenarioId('')
    setInspectRunId('')
  }, [])

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
    <div className="h-dvh flex bg-[var(--tg-canvas)] overflow-hidden">
      <SidebarRail
        user={user}
        agents={liveAgents}
        wsConnected={wsConnected}
        activeTab={activeTab}
        activeRunId={activeRunId}
        collapsed={sidebarCollapsed}
        onToggleCollapse={toggleSidebar}
        onChangeTab={setActiveTab}
        onOpenCommandPalette={() => setCmdOpen(true)}
        onOpenAccountModal={openAccountWithTab}
      />

      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <TopBar
          user={user}
          projects={projects}
          activeProject={activeProject}
          environments={environments}
          activeEnvId={activeEnvId}
          tempVars={tempVars}
          agents={liveAgents}
          wsConnected={wsConnected}
          activeTab={activeTab}
          activeRunId={activeRunId}
          onChangeTab={setActiveTab}
          onSelectProject={selectProject}
          onSelectEnv={setActiveEnvId}
          onTempVarsChange={setTempVars}
          onEnvironmentsChanged={async () => {
            if (activeProjectId) await loadEnvironments(activeProjectId)
          }}
          onOpenProjectModal={() => setProjectModalOpen(true)}
          onOpenEnvModal={() => setEnvModalOpen(true)}
          onOpenAccountModal={openAccountWithTab}
          onOpenCommandPalette={() => setCmdOpen(true)}
          onToast={pushToast}
        />

        {!activeProject ? (
          <div className="flex-1 flex items-center justify-center text-sm text-slate-500">
            暂无可用项目，请点击顶部「项目管理」新建或导入项目。
          </div>
        ) : (
          <div className="flex-1 flex flex-col min-h-0 overflow-hidden">
            <div className={activeTab === 'workbench' ? 'flex-1 flex min-h-0 overflow-hidden' : 'hidden'}>
              <WorkbenchView
                projectId={activeProject.id}
                activeEnvId={activeEnvId}
                tempVars={tempVars}
                canWrite={user.role !== 'viewer'}
                onTempVarsChange={setTempVars}
                onWorkspaceMutated={() => loadEnvironments(activeProject.id)}
                onQuickLoadTest={(apiDraft) => {
                  setPreloadedScenarioId('')
                  setInspectRunId('')
                  setQuickLoadAPI(apiDraft)
                  setActiveTab('run')
                  pushToast('info', `已将「${apiDraft.name || '未命名接口'}」带入压测视图`)
                }}
                incomingReplay={incomingReplay}
                openRequest={openAPIRequest}
                isActive={activeTab === 'workbench'}
                onToast={pushToast}
              />
            </div>

            {activeTab === 'scenarios' && (
              <ScenariosView
                projectId={activeProject.id}
                environments={environments}
                activeEnvId={activeEnvId}
                tempVars={tempVars}
                canWrite={user.role !== 'viewer'}
                onLaunchScenario={(scenarioId) => {
                  setQuickLoadAPI(null)
                  setInspectRunId('')
                  setPreloadedScenarioId(scenarioId)
                  setActiveTab('run')
                }}
                onToast={pushToast}
              />
            )}

            {activeTab === 'run' && (
              <LiveRunView
                projectId={activeProject.id}
                environments={environments}
                activeEnvId={activeEnvId}
                tempVars={tempVars}
                agents={liveAgents}
                canWrite={user.role !== 'viewer'}
                preloadedAPI={quickLoadAPI}
                preloadedScenarioId={preloadedScenarioId}
                inspectRunId={inspectRunId}
                onClearPreloads={() => {
                  setQuickLoadAPI(null)
                  setPreloadedScenarioId('')
                  setInspectRunId('')
                }}
                onReplaySample={(sample) => {
                  setIncomingReplay({ seq: replaySeq++, api: sampleToDraftAPI(sample) })
                  setActiveTab('workbench')
                  pushToast('info', '已将异常采样带入工作台，可直接调整并重发调试')
                }}
                onToast={pushToast}
              />
            )}

            {activeTab === 'history' && (
              <HistoryCompareView
                projectId={activeProject.id}
                canWrite={user.role !== 'viewer'}
                onInspectRun={(runId) => {
                  setQuickLoadAPI(null)
                  setPreloadedScenarioId('')
                  setInspectRunId(runId)
                  setActiveTab('run')
                }}
                onToast={pushToast}
              />
            )}

            {activeTab === 'agents' && (
              <AgentsView
                agents={liveAgents}
                isAdmin={user.role === 'admin'}
                onAgentsRefresh={async () => {
                  const list = await api.listAgents()
                  setAgents(list)
                }}
                onToast={pushToast}
              />
            )}

            {activeTab === 'governance' && (
              <GovernanceView currentUser={user} onToast={pushToast} />
            )}
          </div>
        )}
      </div>

      <CommandPalette
        open={cmdOpen}
        projectId={activeProject?.id || ''}
        environments={environments}
        activeEnvId={activeEnvId}
        onClose={() => setCmdOpen(false)}
        onChangeTab={setActiveTab}
        onSelectEnv={setActiveEnvId}
        onOpenAPI={(apiId) => {
          setActiveTab('workbench')
          setOpenAPIRequest({ seq: replaySeq++, apiId })
        }}
        onNewScratchAPI={() => {
          setActiveTab('workbench')
          setOpenAPIRequest({ seq: replaySeq++, scratch: true })
        }}
        onOpenMCPConfig={() => openAccountWithTab('mcp')}
      />

      <ProjectModal
        open={projectModalOpen}
        projects={projects}
        activeProject={activeProject}
        onClose={() => setProjectModalOpen(false)}
        onSelectProject={selectProject}
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
        initialTab={accountInitialTab}
        user={user}
        onClose={() => setAccountModalOpen(false)}
        onToast={pushToast}
        onLoggedOut={() => {
          setUser(null)
          setSysByAgent({})
          setActiveRunId('')
          setQuickLoadAPI(null)
          setPreloadedScenarioId('')
          setInspectRunId('')
          setIncomingReplay(null)
          setOpenAPIRequest(null)
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
