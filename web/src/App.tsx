import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { AccountModal, type AccountModalTab } from './components/AccountModal'
import { AuthGate } from './components/AuthGate'
import { CommandPalette } from './components/CommandPalette'
import { GooFilterDefs } from './components/DropletFilter'
import { EnvModal } from './components/EnvModal'
import { GlassDock } from './components/GlassDock'
import { ProjectModal } from './components/ProjectModal'
import { SidebarRail } from './components/SidebarRail'
import { ToastContainer, type ToastItem } from './components/Toast'
import { TopBar, type NavTab } from './components/TopBar'
import { api, UNAUTHORIZED_EVENT } from './lib/api'
import { useGlassStagePhysics } from './lib/glassPhysics'
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
import { WorkbenchView } from './views/WorkbenchView'

const AgentsView = lazy(() => import('./views/AgentsView').then((m) => ({ default: m.AgentsView })))
const GovernanceView = lazy(() => import('./views/GovernanceView').then((m) => ({ default: m.GovernanceView })))
const HistoryCompareView = lazy(() =>
  import('./views/HistoryCompareView').then((m) => ({ default: m.HistoryCompareView }))
)
const LiveRunView = lazy(() => import('./views/LiveRunView').then((m) => ({ default: m.LiveRunView })))
const ScenariosView = lazy(() => import('./views/ScenariosView').then((m) => ({ default: m.ScenariosView })))

let toastSeq = 1
let replaySeq = 1

function sampleToDraftAPI(sample: SampleView): Partial<API> {
  const headers: KeyValue[] = []
  let contentType = ''
  for (const h of sample.request_headers || []) {
    const low = h.name.toLowerCase()
    if (low === 'host' || low === 'content-length') continue
    // Redacted secrets would replay as literal "******" and trigger 401s; let auth/env re-apply them.
    if (/^\*{3,}$/.test(h.value.trim())) continue
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
      if (window.innerWidth < 768) return true
      return window.localStorage.getItem('tg.sidebar.collapsed') === '1'
    } catch {
      return typeof window !== 'undefined' ? window.innerWidth < 768 : false
    }
  })
  const [cmdOpen, setCmdOpen] = useState(false)
  const cmdOpenRef = useRef(false)
  cmdOpenRef.current = cmdOpen
  const [projectModalOpen, setProjectModalOpen] = useState(false)
  const [envModalOpen, setEnvModalOpen] = useState(false)
  const [accountModalOpen, setAccountModalOpen] = useState(false)
  const [accountInitialTab, setAccountInitialTab] = useState<AccountModalTab>('sessions')
  const [toasts, setToasts] = useState<ToastItem[]>([])
  const stageRef = useRef<HTMLDivElement | null>(null)
  useGlassStagePhysics(stageRef, !booting && Boolean(user))

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
        if (!cmdOpenRef.current && document.querySelector('dialog[open]')) return
        e.preventDefault()
        setCmdOpen((v) => !v)
        return
      }
      if (e.key === '/' && !e.ctrlKey && !e.metaKey && !e.altKey) {
        const tgt = e.target instanceof Element ? e.target : null
        const inField = Boolean(tgt?.closest('input, textarea, select, [contenteditable="true"]'))
        if (!inField && !document.querySelector('dialog[open]')) {
          e.preventDefault()
          setCmdOpen(true)
        }
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
  const canWriteProject = activeProject?.effective_role ? activeProject.effective_role !== 'viewer' : user?.role !== 'viewer'
  const isProjectTab = activeTab === 'workbench' || activeTab === 'scenarios' || activeTab === 'run' || activeTab === 'history'

  if (booting || !setupStatus) {
    return (
      <div className="min-h-dvh flex items-center justify-center gap-2.5 bg-canvas text-fg-muted text-sm">
        <span className="h-2 w-2 rounded-full bg-sky-500 animate-pulse" />
        正在连接 Tidegate…
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
    <div ref={stageRef} className="h-dvh flex gap-2.5 p-2.5 tg-mesh-canvas overflow-hidden">
      <GooFilterDefs />
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

      <div className="flex-1 min-w-0 flex flex-col gap-2.5 overflow-hidden">
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

        {user.totp_policy_required && !user.totp_enabled && (
          <div className="shrink-0 flex items-center justify-between gap-3 px-3.5 py-2 rounded-[14px] border border-amber-300/80 bg-amber-50/85 text-amber-900 text-xs">
            <span className="font-medium">
              管理员已启用「全员强制开启 2FA」安全策略，请尽快为当前账号绑定双因素验证。
            </span>
            <button
              type="button"
              onClick={() => openAccountWithTab('totp')}
              className="shrink-0 px-2.5 py-1 rounded-lg bg-amber-600 hover:bg-amber-700 text-white font-semibold"
            >
              立即绑定 2FA →
            </button>
          </div>
        )}

        <div className="flex-1 flex flex-col min-h-0 overflow-hidden">
          {!activeProject && isProjectTab ? (
            <div className="flex-1 tg-glass-card flex flex-col items-center justify-center gap-3 text-sm text-fg-subtle">
              <span>暂无可用项目</span>
              <div className="flex items-center gap-2">
                {user.role !== 'viewer' && (
                  <button
                    type="button"
                    onClick={() => setProjectModalOpen(true)}
                    className="tg-btn-primary"
                  >
                    新建或导入项目
                  </button>
                )}
                {user.role === 'admin' && (
                  <button
                    type="button"
                    onClick={() => setActiveTab('governance')}
                    className="tg-btn-secondary"
                  >
                    前往系统治理
                  </button>
                )}
              </div>
            </div>
          ) : (
            <>
              {activeProject && (
                <div className={activeTab === 'workbench' ? 'flex-1 flex min-h-0 overflow-hidden' : 'hidden'}>
                  <WorkbenchView
                    projectId={activeProject.id}
                    environments={environments}
                    projectVariables={activeProject.variables || []}
                    activeEnvId={activeEnvId}
                    tempVars={tempVars}
                    canWrite={canWriteProject}
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
              )}

              <Suspense
                fallback={
                  <div className="flex-1 flex items-center justify-center text-xs text-fg-subtle">加载中…</div>
                }
              >
                {activeProject && (
                  <div className={activeTab === 'scenarios' ? 'flex-1 flex min-h-0 overflow-hidden' : 'hidden'}>
                    <ScenariosView
                      projectId={activeProject.id}
                      environments={environments}
                      activeEnvId={activeEnvId}
                      tempVars={tempVars}
                      canWrite={canWriteProject}
                      isActive={activeTab === 'scenarios'}
                      onLaunchScenario={(scenarioId) => {
                        setQuickLoadAPI(null)
                        setInspectRunId('')
                        setPreloadedScenarioId(scenarioId)
                        setActiveTab('run')
                      }}
                      onEditAPI={(apiId) => {
                        setActiveTab('workbench')
                        setOpenAPIRequest({ seq: replaySeq++, apiId })
                      }}
                      onToast={pushToast}
                    />
                  </div>
                )}

                {activeProject && activeTab === 'run' && (
                  <LiveRunView
                    projectId={activeProject.id}
                    environments={environments}
                    activeEnvId={activeEnvId}
                    tempVars={tempVars}
                    agents={liveAgents}
                    canWrite={canWriteProject}
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

                {activeProject && activeTab === 'history' && (
                  <HistoryCompareView
                    projectId={activeProject.id}
                    canWrite={canWriteProject}
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
                  <GovernanceView
                    currentUser={user}
                    projects={projects}
                    onToast={pushToast}
                  />
                )}
              </Suspense>
            </>
          )}
        </div>
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
        onOpenProjects={() => setProjectModalOpen(true)}
        onOpenEnvs={() => setEnvModalOpen(true)}
      />

      <ProjectModal
        open={projectModalOpen}
        projects={projects}
        activeProject={activeProject}
        isAdmin={user.role === 'admin'}
        onClose={() => setProjectModalOpen(false)}
        onSelectProject={selectProject}
        onProjectsChanged={loadProjects}
        onOpenGovernanceUsers={() => setActiveTab('governance')}
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
        projects={projects}
        onUserUpdated={(u) => setUser(u)}
        onClose={() => setAccountModalOpen(false)}
        onOpenGovernance={() => setActiveTab('governance')}
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

      <GlassDock
        activeTab={activeTab}
        activeRunId={activeRunId}
        onChangeTab={setActiveTab}
        onOpenCommandPalette={() => setCmdOpen(true)}
        onOpenMCP={() => openAccountWithTab('mcp')}
      />

      <ToastContainer
        items={toasts}
        onDismiss={(id) => setToasts((prev) => prev.filter((x) => x.id !== id))}
      />
    </div>
  )
}
