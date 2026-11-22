import type {
  API,
  APIToken,
  AgentCAInfo,
  AgentJoinToken,
  AgentView,
  ApplyResult,
  AuditEntry,
  CollectionTestRequest,
  CollectionTestResult,
  CreateAPITokenResult,
  DataFile,
  DebugAPIResponse,
  DebugRecord,
  DebugRunResult,
  EffectiveAPIPreview,
  Environment,
  FilePreview,
  Folder,
  GRPCSchemaCatalog,
  HTTPOptions,
  JoinTokenCreated,
  KeyValue,
  LoadConfig,
  MetricsSinkConfig,
  PreviewResult,
  Project,
  ProjectACL,
  Run,
  RunComparisonResponse,
  RunDetail,
  RunEstimate,
  RunHistogramResponse,
  SampleRecord,
  Scenario,
  ScenarioMixItem,
  SecurityPolicy,
  SessionItem,
  SetupStatus,
  SnapshotView,
  SystemInfo,
  TOTPConfirmResult,
  TOTPEnrollment,
  Tree,
  User,
  UserRole,
} from './types'

export class APIError extends Error {
  status: number
  code: string
  field?: string
  currentVersion?: string

  constructor(status: number, code: string, message: string, field?: string, currentVersion?: string) {
    super(message)
    this.status = status
    this.code = code
    this.field = field
    this.currentVersion = currentVersion
  }
}

export const UNAUTHORIZED_EVENT = 'tidegate:unauthorized'

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

async function request<T>(
  path: string,
  init: RequestInit & { jsonBody?: unknown; skipUnauthEvent?: boolean } = {}
): Promise<T> {
  const method = (init.method || 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  if (!SAFE_METHODS.has(method)) {
    headers.set('X-Tidegate-Request', '1')
  }

  let body = init.body
  if (init.jsonBody !== undefined) {
    headers.set('Content-Type', 'application/json')
    body = JSON.stringify(init.jsonBody)
  }

  const resp = await fetch(path, {
    ...init,
    method,
    headers,
    body,
    credentials: 'same-origin',
  })

  if (resp.status === 204) {
    return undefined as T
  }

  const ct = resp.headers.get('content-type') || ''
  if (!resp.ok) {
    let code = 'http_error'
    let message = `请求失败 (${resp.status})`
    let field: string | undefined
    let currentVersion: string | undefined
    if (ct.includes('application/json')) {
      try {
        const parsed = (await resp.json()) as {
          error?: { code?: string; message?: string; field?: string; current_version?: string }
        }
        if (parsed?.error) {
          code = parsed.error.code || code
          message = parsed.error.message || message
          field = parsed.error.field
          currentVersion = parsed.error.current_version
        }
      } catch {
        // ignore JSON parse failure on error body
      }
    }
    if (resp.status === 401 && !init.skipUnauthEvent) {
      window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT))
    }
    throw new APIError(resp.status, code, message, field, currentVersion)
  }

  if (ct.includes('application/json')) {
    return (await resp.json()) as T
  }
  return (await resp.text()) as unknown as T
}

export interface StartRunPayload {
  scenario_id?: string
  mix?: ScenarioMixItem[]
  api_id?: string
  api?: Partial<API>
  environment_id?: string
  variables?: Record<string, string>
  load?: LoadConfig
  http?: HTTPOptions
  node_selector?: Record<string, string>
  node_failure_policy?: 'redistribute' | 'abort'
}

export interface ImportJSONInput {
  format?: string
  filename?: string
  content: string
  strategy?: 'merge' | 'overwrite' | 'skip'
  target_folder_id?: string
  name?: string
}

export const api = {
  // Setup & Auth
  setupStatus: () => request<SetupStatus>('/api/v1/setup/status', { skipUnauthEvent: true }),
  setup: (payload: { code: string; username: string; password: string }) =>
    request<{ user: User }>('/api/v1/setup', { method: 'POST', jsonBody: payload, skipUnauthEvent: true }),
  login: (payload: { username: string; password: string; totp_code?: string }) =>
    request<{ user: User }>('/api/v1/auth/login', { method: 'POST', jsonBody: payload, skipUnauthEvent: true }),
  logout: () => request<void>('/api/v1/auth/logout', { method: 'POST', skipUnauthEvent: true }),
  me: () => request<{ user: User }>('/api/v1/auth/me', { skipUnauthEvent: true }),
  changePassword: (payload: { current_password: string; new_password: string }) =>
    request<void>('/api/v1/auth/password', { method: 'POST', jsonBody: payload }),
  enrollTOTP: () => request<TOTPEnrollment>('/api/v1/auth/totp/enroll', { method: 'POST' }),
  confirmTOTP: (code: string) =>
    request<TOTPConfirmResult>('/api/v1/auth/totp/confirm', { method: 'POST', jsonBody: { code } }),
  disableTOTP: (payload: { password: string; code: string }) =>
    request<{ totp_enabled: boolean }>('/api/v1/auth/totp/disable', { method: 'POST', jsonBody: payload }),
  listSessions: () => request<{ sessions: SessionItem[] }>('/api/v1/auth/sessions'),
  revokeSession: (id: string) => request<void>(`/api/v1/auth/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // Projects & Project Import/Export
  listProjects: async () => (await request<Project[] | null>('/api/v1/projects')) || [],
  getProjectACL: (pid: string) => request<ProjectACL>(`/api/v1/projects/${encodeURIComponent(pid)}/acl`),
  putProjectACL: (
    pid: string,
    payload: { restricted: boolean; members: Array<{ user_id: number; role: 'member' | 'viewer' }> }
  ) =>
    request<ProjectACL>(`/api/v1/projects/${encodeURIComponent(pid)}/acl`, {
      method: 'PUT',
      jsonBody: payload,
    }),
  createProject: (payload: { name: string; description?: string; variables?: KeyValue[] }) =>
    request<Project>('/api/v1/projects', { method: 'POST', jsonBody: payload }),
  getProject: (pid: string) => request<Project>(`/api/v1/projects/${encodeURIComponent(pid)}`),
  patchProject: (
    pid: string,
    payload: { name?: string; description?: string; variables?: KeyValue[]; version: string }
  ) => request<Project>(`/api/v1/projects/${encodeURIComponent(pid)}`, { method: 'PATCH', jsonBody: payload }),
  deleteProject: (pid: string, version: string) =>
    request<void>(`/api/v1/projects/${encodeURIComponent(pid)}?version=${encodeURIComponent(version)}`, {
      method: 'DELETE',
    }),
  exportProjectURL: (pid: string, format: 'tidegate' | 'openapi-json' | 'openapi-yaml' | 'postman') =>
    `/api/v1/projects/${encodeURIComponent(pid)}/export?format=${encodeURIComponent(format)}`,
  importProjectArchive: (form: FormData) =>
    request<{ project: Project; report: ApplyResult['report'] }>('/api/v1/projects/import', {
      method: 'POST',
      body: form,
    }),
  importPreview: (pid: string, input: FormData | ImportJSONInput) =>
    input instanceof FormData
      ? request<PreviewResult>(`/api/v1/projects/${encodeURIComponent(pid)}/import/preview`, {
          method: 'POST',
          body: input,
        })
      : request<PreviewResult>(`/api/v1/projects/${encodeURIComponent(pid)}/import/preview`, {
          method: 'POST',
          jsonBody: input,
        }),
  importApply: (pid: string, input: FormData | ImportJSONInput) =>
    input instanceof FormData
      ? request<ApplyResult>(`/api/v1/projects/${encodeURIComponent(pid)}/import/apply`, {
          method: 'POST',
          body: input,
        })
      : request<ApplyResult>(`/api/v1/projects/${encodeURIComponent(pid)}/import/apply`, {
          method: 'POST',
          jsonBody: input,
        }),

  // API Tree, Folders, APIs
  getTree: (pid: string) => request<Tree>(`/api/v1/projects/${encodeURIComponent(pid)}/tree`),
  createFolder: (pid: string, payload: Partial<Folder>) =>
    request<Folder>(`/api/v1/projects/${encodeURIComponent(pid)}/folders`, { method: 'POST', jsonBody: payload }),
  getFolder: (pid: string, id: string) =>
    request<Folder>(`/api/v1/projects/${encodeURIComponent(pid)}/folders/${encodeURIComponent(id)}`),
  patchFolder: (pid: string, id: string, payload: Partial<Folder> & { version: string }) =>
    request<Folder>(`/api/v1/projects/${encodeURIComponent(pid)}/folders/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      jsonBody: payload,
    }),
  deleteFolder: (pid: string, id: string, version: string) =>
    request<void>(
      `/api/v1/projects/${encodeURIComponent(pid)}/folders/${encodeURIComponent(id)}?version=${encodeURIComponent(version)}`,
      { method: 'DELETE' }
    ),

  createAPI: (pid: string, payload: Partial<API>) =>
    request<API>(`/api/v1/projects/${encodeURIComponent(pid)}/apis`, { method: 'POST', jsonBody: payload }),
  getAPI: (pid: string, id: string) =>
    request<API>(`/api/v1/projects/${encodeURIComponent(pid)}/apis/${encodeURIComponent(id)}`),
  updateAPI: (pid: string, id: string, payload: Partial<API>) =>
    request<API>(`/api/v1/projects/${encodeURIComponent(pid)}/apis/${encodeURIComponent(id)}`, {
      method: 'PUT',
      jsonBody: payload,
    }),
  deleteAPI: (pid: string, id: string, version: string) =>
    request<void>(
      `/api/v1/projects/${encodeURIComponent(pid)}/apis/${encodeURIComponent(id)}?version=${encodeURIComponent(version)}`,
      { method: 'DELETE' }
    ),
  copyAPI: (pid: string, id: string) =>
    request<API>(`/api/v1/projects/${encodeURIComponent(pid)}/apis/${encodeURIComponent(id)}/copy`, {
      method: 'POST',
    }),
  moveNode: (pid: string, payload: { id: string; version: string; parent_id: string; index: number }) =>
    request<{ type: string; id: string; version: string; parent_id: string; order: number }>(
      `/api/v1/projects/${encodeURIComponent(pid)}/move`,
      { method: 'POST', jsonBody: payload }
    ),
  resolvePreviewAPI: (
    pid: string,
    payload: { api_id?: string; api?: Partial<API>; environment_id?: string; variables?: Record<string, string> }
  ) =>
    request<EffectiveAPIPreview>(`/api/v1/projects/${encodeURIComponent(pid)}/apis/resolve-preview`, {
      method: 'POST',
      jsonBody: payload,
    }),
  apiToCurl: (
    pid: string,
    payload: {
      api_id?: string
      api?: Partial<API>
      environment_id?: string
      variables?: Record<string, string>
      mode?: 'expanded' | 'template'
      shell?: 'bash' | 'powershell'
      redact_secrets?: boolean
    }
  ) =>
    request<{ curl: string }>(`/api/v1/projects/${encodeURIComponent(pid)}/apis/to-curl`, {
      method: 'POST',
      jsonBody: payload,
    }),
  parseCurl: (command: string) =>
    request<{ api: API }>('/api/v1/tools/curl/parse', { method: 'POST', jsonBody: { command } }),
  discoverGRPC: (
    pid: string,
    payload: {
      environment_id?: string
      target?: string
      tls?: boolean
      insecure_skip_verify?: boolean
      proto_file?: string
      metadata?: Record<string, string>
    }
  ) =>
    request<GRPCSchemaCatalog>(`/api/v1/projects/${encodeURIComponent(pid)}/grpc/discover`, {
      method: 'POST',
      jsonBody: payload,
    }),

  // Environments
  listEnvironments: async (pid: string) =>
    (await request<Environment[] | null>(`/api/v1/projects/${encodeURIComponent(pid)}/environments`)) || [],
  createEnvironment: (pid: string, payload: Partial<Environment>) =>
    request<Environment>(`/api/v1/projects/${encodeURIComponent(pid)}/environments`, {
      method: 'POST',
      jsonBody: payload,
    }),
  updateEnvironment: (pid: string, id: string, payload: Partial<Environment>) =>
    request<Environment>(`/api/v1/projects/${encodeURIComponent(pid)}/environments/${encodeURIComponent(id)}`, {
      method: 'PUT',
      jsonBody: payload,
    }),
  deleteEnvironment: (pid: string, id: string, version: string) =>
    request<void>(
      `/api/v1/projects/${encodeURIComponent(pid)}/environments/${encodeURIComponent(id)}?version=${encodeURIComponent(version)}`,
      { method: 'DELETE' }
    ),

  // Data Files
  listFiles: async (pid: string) =>
    (await request<DataFile[] | null>(`/api/v1/projects/${encodeURIComponent(pid)}/files`)) || [],
  uploadFile: (pid: string, file: File, version = '') => {
    const fd = new FormData()
    if (version) fd.set('version', version)
    fd.set('name', file.name)
    fd.set('file', file)
    return request<DataFile>(`/api/v1/projects/${encodeURIComponent(pid)}/files`, { method: 'POST', body: fd })
  },
  previewFile: (pid: string, name: string, rows = 20) =>
    request<FilePreview>(
      `/api/v1/projects/${encodeURIComponent(pid)}/files/${encodeURIComponent(name)}/preview?rows=${rows}`
    ),
  deleteFile: (pid: string, name: string, version: string) =>
    request<void>(
      `/api/v1/projects/${encodeURIComponent(pid)}/files/${encodeURIComponent(name)}?version=${encodeURIComponent(version)}`,
      { method: 'DELETE' }
    ),

  // Debug & Debug History
  debugAPI: (
    pid: string,
    payload: {
      api_id?: string
      api?: Partial<API>
      environment_id?: string
      variables?: Record<string, string>
      http?: HTTPOptions
      max_body_bytes?: number
      save_history?: boolean
    }
  ) =>
    request<DebugAPIResponse>(`/api/v1/projects/${encodeURIComponent(pid)}/debug`, {
      method: 'POST',
      jsonBody: payload,
    }),
  debugScenario: (
    pid: string,
    payload: {
      scenario_id?: string
      scenario?: Partial<Scenario>
      environment_id?: string
      variables?: Record<string, string>
      skip_think?: boolean
      stop_on_error?: boolean
      max_body_bytes?: number
    }
  ) =>
    request<DebugRunResult>(`/api/v1/projects/${encodeURIComponent(pid)}/scenarios/debug`, {
      method: 'POST',
      jsonBody: payload,
    }),
  runCollectionTest: (pid: string, body: CollectionTestRequest) =>
    request<CollectionTestResult>(`/api/v1/projects/${encodeURIComponent(pid)}/tests/run`, {
      method: 'POST',
      jsonBody: body,
    }),
  listDebugHistory: async (pid: string, limit = 50, includeData = false) =>
    (await request<DebugRecord[] | null>(
      `/api/v1/projects/${encodeURIComponent(pid)}/debug-history?limit=${limit}&include_data=${includeData}`
    )) || [],
  getDebugRecord: (pid: string, id: string) =>
    request<DebugRecord>(`/api/v1/projects/${encodeURIComponent(pid)}/debug-history/${encodeURIComponent(id)}`),
  clearDebugHistory: (pid: string) =>
    request<void>(`/api/v1/projects/${encodeURIComponent(pid)}/debug-history`, { method: 'DELETE' }),

  // Scenarios
  listScenarios: async (pid: string) =>
    (await request<Scenario[] | null>(`/api/v1/projects/${encodeURIComponent(pid)}/scenarios`)) || [],
  createScenario: (pid: string, payload: Partial<Scenario>) =>
    request<Scenario>(`/api/v1/projects/${encodeURIComponent(pid)}/scenarios`, {
      method: 'POST',
      jsonBody: payload,
    }),
  getScenario: (pid: string, id: string) =>
    request<Scenario>(`/api/v1/projects/${encodeURIComponent(pid)}/scenarios/${encodeURIComponent(id)}`),
  updateScenario: (pid: string, id: string, payload: Partial<Scenario>) =>
    request<Scenario>(`/api/v1/projects/${encodeURIComponent(pid)}/scenarios/${encodeURIComponent(id)}`, {
      method: 'PUT',
      jsonBody: payload,
    }),
  deleteScenario: (pid: string, id: string, version: string) =>
    request<void>(
      `/api/v1/projects/${encodeURIComponent(pid)}/scenarios/${encodeURIComponent(id)}?version=${encodeURIComponent(version)}`,
      { method: 'DELETE' }
    ),
  exportScenarioURL: (pid: string, id: string, format: 'k6' = 'k6', envId = '') => {
    const qs = new URLSearchParams({ format })
    if (envId) qs.set('env_id', envId)
    return `/api/v1/projects/${encodeURIComponent(pid)}/scenarios/${encodeURIComponent(id)}/export?${qs.toString()}`
  },

  // Runs, Estimator, Snapshots, Samples, Compare
  estimateRun: (pid: string, payload: StartRunPayload) =>
    request<RunEstimate>(`/api/v1/projects/${encodeURIComponent(pid)}/runs/estimate`, {
      method: 'POST',
      jsonBody: payload,
    }),
  startRun: (pid: string, payload: StartRunPayload) =>
    request<Run>(`/api/v1/projects/${encodeURIComponent(pid)}/runs`, { method: 'POST', jsonBody: payload }),
  listRuns: async (pid: string, limit = 50, offset = 0) =>
    (await request<Run[] | null>(`/api/v1/projects/${encodeURIComponent(pid)}/runs?limit=${limit}&offset=${offset}`)) ||
    [],
  getRun: (id: string) => request<RunDetail>(`/api/v1/runs/${encodeURIComponent(id)}`),
  runSnapshots: async (id: string, from = 0, limit = 0) =>
    (await request<SnapshotView[] | null>(
      `/api/v1/runs/${encodeURIComponent(id)}/snapshots?from=${from}&limit=${limit}`
    )) || [],
  runSamples: async (id: string, query: { kind?: string; step?: string; after_id?: number; limit?: number } = {}) => {
    const qs = new URLSearchParams()
    if (query.kind) qs.set('kind', query.kind)
    if (query.step) qs.set('step', query.step)
    if (query.after_id) qs.set('after_id', String(query.after_id))
    if (query.limit) qs.set('limit', String(query.limit))
    const q = qs.toString()
    return (await request<SampleRecord[] | null>(`/api/v1/runs/${encodeURIComponent(id)}/samples${q ? '?' + q : ''}`)) || []
  },
  stopRun: (id: string, mode: 'graceful' | 'abort' = 'graceful') =>
    request<Run>(`/api/v1/runs/${encodeURIComponent(id)}/stop`, { method: 'POST', jsonBody: { mode } }),
  setRunTarget: (id: string, target: number) =>
    request<void>(`/api/v1/runs/${encodeURIComponent(id)}/target`, { method: 'POST', jsonBody: { target } }),
  deleteRun: (id: string) => request<void>(`/api/v1/runs/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  exportRunURL: (id: string, format: 'json' | 'csv' | 'html' | 'har' | 'jtl') =>
    `/api/v1/runs/${encodeURIComponent(id)}/export?format=${encodeURIComponent(format)}`,
  runHistogram: (id: string) => request<RunHistogramResponse>(`/api/v1/runs/${encodeURIComponent(id)}/histogram`),
  compareRuns: (ids: string[], maxPoints = 600) =>
    request<RunComparisonResponse>(
      `/api/v1/runs/compare?ids=${encodeURIComponent(ids.join(','))}&max_points=${maxPoints}`
    ),

  // Agents, Enrollment & System
  listAgents: async () => (await request<AgentView[] | null>('/api/v1/agents')) || [],
  patchAgent: (id: string, patch: { bandwidth_bps?: number; weight?: number }) =>
    request<AgentView>(`/api/v1/agents/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      jsonBody: patch,
    }),
  revokeAgent: (id: string) => request<void>(`/api/v1/agents/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  getAgentCA: () => request<AgentCAInfo>('/api/v1/agents/ca'),
  exportAgentCAPEMUrl: () => '/api/v1/agents/ca?format=pem',
  listJoinTokens: async () => (await request<AgentJoinToken[] | null>('/api/v1/agents/join-tokens')) || [],
  createJoinToken: (body: { name: string; ttl_hours?: number; single_use?: boolean }) =>
    request<JoinTokenCreated>('/api/v1/agents/join-tokens', { method: 'POST', jsonBody: body }),
  deleteJoinToken: (id: string) =>
    request<void>(`/api/v1/agents/join-tokens/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  systemInfo: () => request<SystemInfo>('/api/v1/system/info'),

  // Personal API Tokens
  listAPITokens: async () => (await request<APIToken[] | null>('/api/v1/auth/tokens')) || [],
  createAPIToken: (body: {
    name: string
    role?: UserRole
    scopes?: string[]
    project_ids?: string[]
    ttl_days?: number
    expires_in?: string
  }) => request<CreateAPITokenResult>('/api/v1/auth/tokens', { method: 'POST', jsonBody: body }),
  revokeAPIToken: (id: string) =>
    request<void>(`/api/v1/auth/tokens/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // Governance: Users, Security Policy, Metrics Sink & Audit Logs
  listUsers: async () => (await request<User[] | null>('/api/v1/users')) || [],
  createUser: (body: { username: string; password: string; role: UserRole }) =>
    request<{ user: User }>('/api/v1/users', { method: 'POST', jsonBody: body }),
  patchUser: (id: number, body: { role: UserRole }) =>
    request<{ user: User }>(`/api/v1/users/${id}`, { method: 'PATCH', jsonBody: body }),
  deleteUser: (id: number) => request<void>(`/api/v1/users/${id}`, { method: 'DELETE' }),
  adminResetUserTOTP: (id: number) => request<void>(`/api/v1/users/${id}/totp`, { method: 'DELETE' }),
  getSecurityPolicy: () => request<SecurityPolicy>('/api/v1/settings/security'),
  putSecurityPolicy: (pol: SecurityPolicy) =>
    request<SecurityPolicy>('/api/v1/settings/security', { method: 'PUT', jsonBody: pol }),
  getMetricsSink: () => request<MetricsSinkConfig>('/api/v1/settings/metrics-sink'),
  putMetricsSink: (cfg: MetricsSinkConfig) =>
    request<MetricsSinkConfig>('/api/v1/settings/metrics-sink', { method: 'PUT', jsonBody: cfg }),
  testMetricsSink: (cfg?: MetricsSinkConfig) =>
    request<{ ok: boolean }>('/api/v1/settings/metrics-sink/test', {
      method: 'POST',
      jsonBody: cfg,
    }),
  prometheusMetricsUrl: () => '/api/v1/metrics/prometheus',
  fetchPrometheusMetrics: () => request<string>('/api/v1/metrics/prometheus'),
  listAuditLogs: async (params: { action?: string; username?: string; limit?: number; before_id?: number } = {}) => {
    const qs = new URLSearchParams()
    if (params.action) qs.set('action', params.action)
    if (params.username) qs.set('username', params.username)
    if (params.limit) qs.set('limit', String(params.limit))
    if (params.before_id) qs.set('before_id', String(params.before_id))
    const q = qs.toString()
    return (await request<AuditEntry[] | null>(`/api/v1/audit-logs${q ? '?' + q : ''}`)) || []
  },
}
