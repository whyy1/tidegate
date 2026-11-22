import type {
  API,
  AgentView,
  ApplyResult,
  DataFile,
  DebugAPIResponse,
  DebugRecord,
  DebugRunResult,
  EffectiveAPIPreview,
  Environment,
  FilePreview,
  Folder,
  HTTPOptions,
  KeyValue,
  LoadConfig,
  PreviewResult,
  Project,
  Run,
  RunComparisonResponse,
  RunDetail,
  RunEstimate,
  SampleRecord,
  Scenario,
  SessionItem,
  SetupStatus,
  SnapshotView,
  SystemInfo,
  Tree,
  User,
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
  api_id?: string
  api?: Partial<API>
  environment_id?: string
  variables?: Record<string, string>
  load?: LoadConfig
  http?: HTTPOptions
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
  login: (payload: { username: string; password: string }) =>
    request<{ user: User }>('/api/v1/auth/login', { method: 'POST', jsonBody: payload, skipUnauthEvent: true }),
  logout: () => request<void>('/api/v1/auth/logout', { method: 'POST', skipUnauthEvent: true }),
  me: () => request<{ user: User }>('/api/v1/auth/me', { skipUnauthEvent: true }),
  changePassword: (payload: { current_password: string; new_password: string }) =>
    request<void>('/api/v1/auth/password', { method: 'POST', jsonBody: payload }),
  listSessions: () => request<{ sessions: SessionItem[] }>('/api/v1/auth/sessions'),
  revokeSession: (id: string) => request<void>(`/api/v1/auth/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // Projects & Project Import/Export
  listProjects: async () => (await request<Project[] | null>('/api/v1/projects')) || [],
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
  exportRunURL: (id: string, format: 'json' | 'csv' | 'html' | 'har') =>
    `/api/v1/runs/${encodeURIComponent(id)}/export?format=${encodeURIComponent(format)}`,
  compareRuns: (ids: string[], maxPoints = 600) =>
    request<RunComparisonResponse>(
      `/api/v1/runs/compare?ids=${encodeURIComponent(ids.join(','))}&max_points=${maxPoints}`
    ),

  // Agents & System
  listAgents: async () => (await request<AgentView[] | null>('/api/v1/agents')) || [],
  patchAgent: (id: string, bandwidth_bps: number) =>
    request<AgentView>(`/api/v1/agents/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      jsonBody: { bandwidth_bps },
    }),
  systemInfo: () => request<SystemInfo>('/api/v1/system/info'),
}
