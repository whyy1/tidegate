export interface SetupStatus {
  setup_required: boolean
  version: string
  tls: boolean
}

export interface User {
  id: number
  username: string
  role: string
  created_at: string
  password_changed_at: string
}

export interface SessionItem {
  id: string
  created_at: string
  last_seen_at: string
  expires_at: string
  remote_addr: string
  user_agent: string
  current: boolean
}

export interface KeyValue {
  name: string
  value: string
  description?: string
  disabled?: boolean
  secret?: boolean
}

export type AuthType = 'inherit' | 'none' | 'bearer' | 'basic' | 'api_key'

export interface AuthConfig {
  type: AuthType | ''
  token?: string
  username?: string
  password?: string
  key?: string
  value?: string
  in?: 'header' | 'query' | ''
}

export interface Project {
  id: string
  name: string
  description?: string
  variables?: KeyValue[]
  version: string
}

export interface Folder {
  id: string
  name: string
  order: number
  headers?: KeyValue[]
  auth?: AuthConfig | null
  parent_id: string
  version: string
}

export interface BodyFilePart {
  field: string
  path: string
  filename?: string
  content_type?: string
  disabled?: boolean
}

export interface APIBody {
  type: 'none' | 'json' | 'form' | 'multipart' | 'raw' | 'binary' | ''
  content?: string
  fields?: KeyValue[]
  files?: BodyFilePart[]
  file?: string
  content_type?: string
}

export interface APIRequest {
  method: string
  url: string
  path_params?: KeyValue[]
  query?: KeyValue[]
  headers?: KeyValue[]
  body?: APIBody | null
}

export interface ExtractRule {
  name: string
  from: 'jsonpath' | 'regex' | 'header'
  expr: string
  group?: number
  default?: string
  description?: string
  disabled?: boolean
}

export interface AssertRule {
  type: 'status' | 'duration' | 'body_contains' | 'jsonpath'
  equals?: string
  in?: number[]
  max?: string
  value?: string
  expr?: string
  exists?: boolean
  contains?: string
  description?: string
  disabled?: boolean
}

export interface API {
  id: string
  name: string
  order: number
  description?: string
  request: APIRequest
  auth?: AuthConfig | null
  extract?: ExtractRule[]
  assert?: AssertRule[]
  folder_id: string
  version: string
}

export interface TreeNode {
  type: 'folder' | 'api'
  id: string
  name: string
  order: number
  version: string
  method?: string
  url?: string
  headers?: KeyValue[]
  auth?: AuthConfig | null
  children?: TreeNode[]
}

export interface TreeProblem {
  path: string
  message: string
}

export interface Tree {
  children: TreeNode[]
  problems?: TreeProblem[]
}

export interface Environment {
  id: string
  name: string
  base_url?: string
  variables?: KeyValue[]
  version: string
}

export interface DataFile {
  name: string
  size: number
  mod_time: string
  version: string
}

export interface FilePreview {
  name: string
  size: number
  is_csv: boolean
  columns?: string[]
  rows?: string[][]
  total_rows?: number
  text?: string
  truncated?: boolean
}

export interface InheritedHeader {
  name: string
  value: string
  description?: string
  source_folder_id: string
  source_folder_name: string
  overridden: boolean
}

export interface EffectiveAuth {
  auth?: AuthConfig | null
  source: 'api' | 'folder' | 'none'
  source_folder_id?: string
  source_folder_name?: string
}

export interface ResolvedVariable {
  name: string
  value: string
  source: 'project' | 'environment' | 'override'
  description?: string
  secret?: boolean
}

export interface EffectiveAPIPreview {
  method: string
  url: string
  base_url?: string
  inherited_headers: InheritedHeader[]
  effective_auth: EffectiveAuth
  variables: ResolvedVariable[]
}

export interface HeaderKV {
  name: string
  value: string
}

export interface DebugActualRequest {
  method: string
  url: string
  headers: HeaderKV[]
  body?: string
  body_truncated?: boolean
}

export interface DebugTiming {
  dns_ms: number
  connect_ms: number
  tls_ms: number
  first_byte_ms: number
  download_ms: number
  total_ms: number
  reused: boolean
}

export interface DebugHop {
  method: string
  url: string
  status: number
  location?: string
  duration_ms: number
  timing: DebugTiming
}

export interface DebugExtractResult {
  name: string
  from: string
  expr: string
  value?: string
  matched: boolean
  description?: string
  error?: string
}

export interface DebugAssertResult {
  type: string
  desc: string
  description?: string
  passed: boolean
  message?: string
}

export interface DebugStepResult {
  step_id: string
  step_name: string
  passed: boolean
  error_kind?: string
  error?: string
  actual_request: DebugActualRequest
  redirects?: DebugHop[]
  status: number
  response_headers?: HeaderKV[]
  body?: string
  body_base64?: string
  is_binary?: boolean
  body_truncated?: boolean
  content_type?: string
  bytes_sent: number
  bytes_received: number
  timing: DebugTiming
  extracts?: DebugExtractResult[]
  asserts?: DebugAssertResult[]
  extracted_vars?: Record<string, string>
}

export interface DebugAPIResponse {
  record_id?: string
  result: DebugStepResult
  final_vars?: Record<string, string>
}

export interface DebugRunResult {
  passed: boolean
  duration_ms: number
  steps: DebugStepResult[]
  data_rows?: Record<string, Record<string, string>>
  final_vars?: Record<string, string>
}

export interface DebugRecord {
  id: string
  project_id: string
  api_id?: string
  method: string
  url: string
  status: number
  duration_ms: number
  created_at: string
  data?: {
    environment_id?: string
    result: DebugStepResult
    final_vars?: Record<string, string>
  }
}

export interface DataSource {
  name: string
  file: string
  mode?: 'sequential' | 'random' | 'unique' | ''
}

export interface ThinkConfig {
  type: 'fixed' | 'uniform' | ''
  min?: string
  max?: string
}

export interface Stage {
  duration: string
  target: number
}

export interface Threshold {
  metric: string
  op?: '<' | '<=' | '>' | '>=' | ''
  value: number
  step?: string
  abort_on_fail?: boolean
  grace_period?: string
}

export interface ThresholdResult {
  metric: string
  op: string
  value: number
  step?: string
  abort_on_fail?: boolean
  actual: number
  passed: boolean
  message: string
}

export interface HTTPOptions {
  conn_mode?: 'per_vu' | 'shared' | ''
  keep_alive?: boolean
  max_requests_per_conn?: number
  follow_redirects?: boolean
  max_redirects?: number
  insecure_skip_verify?: boolean
  cookies?: boolean
  timeout?: {
    connect?: string
    tls?: string
    first_byte?: string
    total?: string
  }
  max_body_read?: number
  dns_refresh?: string
  connect_backoff?: string
}

export interface LoadConfig {
  mode: 'vus' | 'rate'
  stages: Stage[]
  max_vus?: number
  max_requests?: number
  graceful_stop?: string
  thresholds?: Threshold[]
}

export interface StepOverride {
  method?: string
  url?: string
  path_params?: KeyValue[]
  headers?: KeyValue[]
  query?: KeyValue[]
  body?: APIBody | null
  auth?: AuthConfig | null
  extract?: ExtractRule[]
  assert?: AssertRule[]
}

export interface ScenarioStep {
  id: string
  name?: string
  api_id: string
  disabled?: boolean
  override?: StepOverride | null
  think?: ThinkConfig | null
}

export interface Scenario {
  id: string
  name: string
  description?: string
  environment_id?: string
  variables?: KeyValue[]
  http?: HTTPOptions
  data?: DataSource[]
  steps: ScenarioStep[]
  load: LoadConfig
  version: string
}

export interface LatencyStats {
  count: number
  p50_ms: number
  p90_ms: number
  p95_ms: number
  p99_ms: number
  max_ms: number
  mean_ms: number
}

export interface PhaseView {
  count: number
  avg_ms: number
}

export interface StepView {
  id?: string
  name?: string
  requests: number
  rps: number
  errors: number
  error_rate: number
  latency: LatencyStats
  bytes_sent: number
  bytes_received: number
  sent_bps: number
  received_bps: number
  status?: Record<string, number>
  error_kinds?: Record<string, number>
  phases?: Record<string, PhaseView>
}

export interface SampleView {
  kind: string
  step: string
  time: string
  method: string
  url: string
  status?: number
  error?: string
  duration_ms: number
  request_headers?: HeaderKV[]
  response_headers?: HeaderKV[]
  request_body?: string
  request_body_truncated?: boolean
  body?: string
  body_truncated?: boolean
}

export interface SnapshotView extends StepView {
  seq: number
  time: string
  duration_ms: number
  active_vus: number
  active_conns: number
  target: number
  iterations: number
  dropped_iterations: number
  sched_delay?: LatencyStats
  overhead_bytes_sent: number
  overhead_bytes_received: number
  steps: StepView[]
  samples?: SampleView[]
}

export interface SampleRecord {
  id: number
  seq: number
  kind: string
  step: string
  time: string
  data: SampleView
}

export type RunStatus = 'preparing' | 'running' | 'stopping' | 'finished' | 'failed'

export interface Run {
  id: string
  project_id: string
  scenario_id?: string
  api_id?: string
  environment_id?: string
  scenario_name: string
  scenario?: {
    name?: string
    steps?: Array<{ id: string; name?: string }>
    load?: LoadConfig
  }
  status: RunStatus
  created_at: string
  started_at?: string
  finished_at?: string
  error?: string
  summary?: SnapshotView
}

export interface Alert {
  id: number
  run_id: string
  agent?: string
  kind: string
  level: 'warning' | 'critical' | string
  message: string
  value: number
  threshold: number
  started_at: string
  ended_at?: string
}

export interface RunDetail extends Run {
  alerts: Alert[]
  thresholds?: ThresholdResult[]
  thresholds_passed?: boolean
}

export interface SysmonStats {
  time: string
  elapsed_ms: number
  cpu: {
    machine: number
    process: number
    cores: number
    scope: string
  }
  memory: {
    used_bytes: number
    total_bytes: number
    process_bytes: number
    scope: string
  }
  nics: Array<{
    name: string
    index: number
    up: boolean
    loopback: boolean
    link_speed_bits_per_sec: number
    rx_bytes_per_sec: number
    tx_bytes_per_sec: number
    rx_bytes: number
    tx_bytes: number
  }>
  tcp: {
    in_segs: number
    out_segs: number
    retrans_segs: number
    out_segs_per_sec: number
    retrans_per_sec: number
    established: number
  }
  ports: {
    ephemeral_start: number
    ephemeral_count: number
    tcp_conns: number
  }
  runtime: {
    goroutines: number
    gc_cycles: number
    gc_pause_ns: number
    gc_pause_max_ns: number
  }
  unavailable?: string[]
}

export interface AgentView {
  id: string
  name: string
  version: string
  os: string
  arch: string
  cpu_cores: number
  memory_bytes: number
  interfaces?: Array<{ name: string; link_speed_bps: number }>
  remote_addr?: string
  connected_at: string
  last_seen: string
  rtt_ms: number
  clock_offset_ms: number
  bandwidth_bps: number
  stats?: SysmonStats
}

export interface RunEstimate {
  load_mode: string
  peak_target: number
  max_vus: number
  step_count: number
  avg_request_bytes: number
  estimated_peak_rps: number
  estimated_uplink_bps: number
  online_agents: number
  total_cpu_cores: number
  available_uplink_bps: number
  warnings?: string[]
}

export interface StepDelta {
  id?: string
  name?: string
  rps_pct?: number
  p50_pct?: number
  p95_pct?: number
  p99_pct?: number
  max_pct?: number
  error_rate_diff: number
}

export interface RunDelta {
  rps_pct?: number
  p50_pct?: number
  p95_pct?: number
  p99_pct?: number
  max_pct?: number
  error_rate_diff: number
  step_deltas?: Record<string, StepDelta>
}

export interface RunComparisonItem {
  run: Run
  summary?: SnapshotView
  alerts: Alert[]
  snapshots: SnapshotView[]
  delta?: RunDelta
}

export interface RunComparisonResponse {
  baseline_id: string
  runs: RunComparisonItem[]
}

export interface ConversionReport {
  format: string
  detected_version?: string
  apis_found: number
  envs_found?: number
  folders_found?: number
  scenarios_found?: number
  files_found?: number
  apis_created?: number
  apis_updated?: number
  apis_skipped?: number
  folders_created?: number
  envs_created?: number
  scenarios_created?: number
  files_imported?: number
  warnings?: string[]
  skipped?: string[]
}

export interface ImportDiff {
  action: 'create' | 'update' | 'skip'
  folder_path?: string
  method: string
  url: string
  name: string
  existing_id?: string
}

export interface PreviewResult {
  report: ConversionReport
  diffs: ImportDiff[]
}

export interface ApplyResult {
  report: ConversionReport
  diffs: ImportDiff[]
}

export interface SystemInfo {
  version: string
  tls: boolean
  cluster_addr: string
}

export interface WSEvent {
  type: 'snapshot' | 'sys' | 'run_status' | 'alert' | 'agents'
  run_id?: string
  agent_id?: string
  status?: RunStatus
  error?: string
  snapshot?: SnapshotView
  sys?: SysmonStats
  alert?: Alert
  agents?: AgentView[]
}
