export interface SetupStatus {
  setup_required: boolean
  version: string
  tls: boolean
  runtime?: 'docker' | 'binary' | string
}

export type UserRole = 'admin' | 'member' | 'viewer'
export type ProjectAccessRole = '' | 'member' | 'viewer' | 'none'

export interface User {
  id: number
  username: string
  role: UserRole
  totp_enabled?: boolean
  totp_policy_required?: boolean
  disabled?: boolean
  disabled_at?: string
  active_sessions?: number
  last_seen_at?: string
  created_at: string
  password_changed_at: string
}

export interface UserProjectAccessItem {
  project_id: string
  project_name: string
  restricted: boolean
  member_role: ProjectAccessRole
  effective_role: UserRole | ''
  allowed: boolean
}

export interface UserProjectsResponse {
  user: User
  projects: UserProjectAccessItem[]
}

export interface TOTPEnrollment {
  secret: string
  otpauth_url: string
  issuer: string
  account: string
}

export interface TOTPConfirmResult {
  totp_enabled: boolean
  recovery_codes: string[]
}

export interface ProjectMember {
  user_id: number
  username?: string
  role: 'member' | 'viewer' | 'none'
}

export interface ProjectACL {
  project_id: string
  restricted: boolean
  members: ProjectMember[]
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

export type AuthType = 'inherit' | 'none' | 'bearer' | 'basic' | 'api_key' | 'oauth2'

export interface AuthConfig {
  type: AuthType | ''
  token?: string
  username?: string
  password?: string
  key?: string
  value?: string
  in?: 'header' | 'query' | ''
  token_url?: string
  client_id?: string
  client_secret?: string
  scope?: string
  client_auth?: 'basic' | 'body' | ''
}

export interface Project {
  id: string
  name: string
  description?: string
  variables?: KeyValue[]
  effective_role?: UserRole
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

export type APIProtocol = 'http' | 'ws' | 'grpc' | 'sse'

export interface WSMessage {
  type?: 'text' | 'binary' | ''
  data: string
}

export interface APIWebSocket {
  subprotocols?: string[]
  messages?: WSMessage[]
  recv_count?: number
  idle_timeout?: string
}

export interface APIGRPC {
  service: string
  method: string
  proto_file?: string
  use_reflection?: boolean
  tls?: boolean
  payload_encoding?: string
  response_encoding?: string
  expected_status?: number
  timeout?: string
}

export interface GRPCMethodInfo {
  name: string
  full_method: string
  input_type: string
  output_type: string
  client_streaming?: boolean
  server_streaming?: boolean
  sample_json?: string
}

export interface GRPCServiceInfo {
  name: string
  methods: GRPCMethodInfo[]
}

export interface GRPCSchemaCatalog {
  services: GRPCServiceInfo[]
}

export interface APISSE {
  max_events?: number
  stop_contains?: string
  timeout?: string
}

export interface APIResponseExample {
  name: string
  status: number
  content_type?: string
  headers?: KeyValue[]
  body?: string
}

export interface APIFieldDoc {
  path: string
  type?: string
  required?: boolean
  description?: string
}

export interface APIRequest {
  protocol?: APIProtocol | ''
  method: string
  url: string
  path_params?: KeyValue[]
  query?: KeyValue[]
  headers?: KeyValue[]
  body?: APIBody | null
  ws?: APIWebSocket | null
  grpc?: APIGRPC | null
  sse?: APISSE | null
}

export interface ExtractRule {
  name: string
  from: 'jsonpath' | 'regex' | 'header' | 'xpath'
  expr: string
  group?: number
  default?: string
  description?: string
  disabled?: boolean
}

export interface AssertRule {
  type: 'status' | 'duration' | 'body_contains' | 'jsonpath' | 'json_schema'
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
  pre_script?: string
  post_script?: string
  examples?: APIResponseExample[]
  response_fields?: APIFieldDoc[]
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
  type: 'fixed' | 'uniform' | 'normal' | ''
  min?: string
  max?: string
  mean?: string
  stddev?: string
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
  http_version?: 'auto' | 'http1' | 'http2' | ''
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
  dns_overrides?: Record<string, string>
  proxy_url?: string
  connect_backoff?: string
  max_dials_per_sec?: number
  on_error?: 'continue' | 'abort_iteration' | 'abort_vu' | ''
  allowed_targets?: string[]
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
  pre_script?: string
  post_script?: string
}

export interface CollectionTestRequest {
  folder_id?: string
  api_ids?: string[]
  environment_id?: string
  variables?: Record<string, string>
  stop_on_error?: boolean
  max_body_bytes?: number
}

export interface CollectionTestItem {
  step_id: string
  api_id: string
  api_name: string
  folder_path?: string
  protocol: string
  method: string
  passed: boolean
  skipped?: boolean
  status?: number
  duration_ms: number
  error_kind?: string
  error?: string
  assert_total: number
  assert_passed: number
  step: DebugStepResult
}

export interface CollectionTestResult {
  project_id: string
  folder_id?: string
  scope_name: string
  environment_id?: string
  passed: boolean
  total: number
  passed_count: number
  failed_count: number
  skipped_count: number
  duration_ms: number
  items: CollectionTestItem[]
  final_vars?: Record<string, string>
}

export type ScenarioStepType = 'http' | 'ws' | 'grpc' | 'sse' | 'script' | 'group' | 'loop' | 'if' | 'weighted'

export interface ScenarioWeightedBranch {
  name?: string
  weight: number
  steps?: ScenarioStep[]
}

export interface ScenarioStep {
  id: string
  name?: string
  type?: ScenarioStepType | ''
  api_id?: string
  script?: string
  on_error?: 'continue' | 'abort_iteration' | 'abort_vu' | ''
  disabled?: boolean
  override?: StepOverride | null
  think?: ThinkConfig | null
  loop_count?: number
  while?: string
  max_iterations?: number
  expr?: string
  children?: ScenarioStep[]
  else?: ScenarioStep[]
  branches?: ScenarioWeightedBranch[]
}

export interface ScenarioMixItem {
  scenario_id: string
  weight: number
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
  is_group?: boolean
  requests: number
  rps: number
  errors: number
  error_rate: number
  latency: LatencyStats
  latency_buckets?: number[]
  bytes_sent: number
  bytes_received: number
  sent_bps: number
  received_bps: number
  status?: Record<string, number>
  error_kinds?: Record<string, number>
  phases?: Record<string, PhaseView>
}

export interface NodeSnapshotView {
  agent_id: string
  agent_name: string
  active_vus: number
  active_conns: number
  target: number
  requests: number
  rps: number
  errors: number
  error_rate: number
  p50_ms: number
  p95_ms: number
  p99_ms: number
  sent_bps: number
  received_bps: number
}

export interface LatencyBucket {
  low_ms: number
  high_ms: number
  count: number
}

export interface StepHistogramView {
  id: string
  name: string
  is_group?: boolean
  latency: LatencyStats
  bands: number[]
  buckets: LatencyBucket[]
}

export interface RunHistogramResponse {
  run_id: string
  latency: LatencyStats
  bands: number[]
  buckets: LatencyBucket[]
  steps?: StepHistogramView[]
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
  nodes?: NodeSnapshotView[]
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
  labels?: Record<string, string>
  weight: number
  effective_weight: number
  load_share?: number
  cert_serial?: string
  stats?: SysmonStats
}

export interface AgentJoinToken {
  id: string
  name: string
  single_use: boolean
  used_count: number
  created_by?: string
  created_at: string
  expires_at: string
  used_at?: string
}

export interface JoinTokenCreated {
  token: string
  item: AgentJoinToken
}

export interface AgentCAInfo {
  fingerprint: string
  cert_pem: string
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
  changed_fields?: string[]
  preserved_fields?: string[]
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
  cluster_ca_fingerprint: string
  exe_path?: string
}

export interface APIToken {
  id: string
  user_id: number
  username?: string
  name: string
  prefix: string
  role: UserRole
  scopes?: string[]
  project_ids?: string[]
  /** Empty = inherit every MCP tool unlocked by scopes; non-empty = only these MCP tools. */
  mcp_tools?: string[]
  mcp_max_vus?: number
  mcp_max_duration_sec?: number
  rate_per_min?: number
  created_at: string
  last_used_at?: string
  last_used_addr?: string
  expires_at?: string
}

export interface CreateAPIToken {
  name: string
  role?: UserRole
  scopes?: string[]
  project_ids?: string[]
  mcp_tools?: string[]
  mcp_max_vus?: number
  mcp_max_duration_sec?: number
  rate_per_min?: number
  ttl_days?: number
  expires_in?: string
}

export type MCPToolCategory = 'read' | 'write' | 'debug' | 'run'

export interface MCPToolCatalogItem {
  name: string
  title: string
  description: string
  category: MCPToolCategory
  scope: string
  preview_read_only?: boolean
  read_only: boolean
  destructive: boolean
  open_world: boolean
  globally_enabled: boolean
}

export interface MCPResourceCatalogItem {
  uri_template: string
  name: string
  description: string
  globally_enabled: boolean
}

export interface MCPPromptCatalogItem {
  name: string
  title: string
  description: string
  globally_enabled: boolean
}

export interface MCPCatalog {
  tools: MCPToolCatalogItem[]
  resources?: MCPResourceCatalogItem[]
  prompts?: MCPPromptCatalogItem[]
  effective_max_vus: number
  effective_max_duration_sec: number
  effective_rate_per_min?: number
}

export interface CreateAPITokenResult {
  token: APIToken
  plaintext: string
}

export interface SecurityPolicy {
  allowlist_enabled: boolean
  allowed_targets?: string[]
  member_max_vus?: number
  member_max_rps?: number
  scripts_disabled?: boolean
  require_totp?: boolean
  token_max_ttl_days?: number
  mcp_disabled?: boolean
  mcp_disable_load_runs?: boolean
  mcp_disable_resources?: boolean
  mcp_disable_prompts?: boolean
  mcp_max_vus?: number
  mcp_max_duration_sec?: number
  mcp_rate_per_min?: number
  mcp_disabled_tools?: string[]
}

export interface MetricsSinkConfig {
  enabled: boolean
  type: 'influxdb' | 'prometheus_pushgateway' | string
  url: string
  org?: string
  bucket?: string
  database?: string
  username?: string
  token?: string
  job?: string
  extra_tags?: Record<string, string>
}

export interface AuditEntry {
  id: number
  created_at: string
  user_id: number
  username: string
  role: string
  action: string
  resource?: string
  resource_id?: string
  remote_addr?: string
  detail?: unknown
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
