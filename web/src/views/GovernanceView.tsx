import { Fragment, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react'
import {
  Activity,
  Bot,
  Bug,
  Check,
  ChevronRight,
  Copy,
  Dices,
  Eye,
  EyeOff,
  FileJson,
  FileSearch,
  FileSpreadsheet,
  FolderKey,
  Gauge,
  Globe,
  Key,
  KeyRound,
  ListChecks,
  Lock,
  LogOut,
  PenLine,
  Plus,
  RefreshCw,
  RotateCcw,
  Save,
  ScanEye,
  Search,
  ShieldCheck,
  Trash2,
  TriangleAlert,
  UserPlus,
  Users,
  X,
} from 'lucide-react'
import { RollingNumber } from '../components/DropletFilter'
import { GlassSelect } from '../components/GlassSelect'
import { LiquidCapsule } from '../components/LiquidCapsule'
import { api, APIError } from '../lib/api'
import { copyText, downloadText, fileStamp, toCSV } from '../lib/download'
import type {
  APIToken,
  AuditEntry,
  MCPCatalog,
  MCPToolCatalogItem,
  MCPToolCategory,
  MetricsSinkConfig,
  Project,
  ProjectAccessRole,
  SecurityPolicy,
  User,
  UserProjectAccessItem,
  UserRole,
} from '../lib/types'

interface Props {
  currentUser: User
  projects?: Project[]
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

type SubTab = 'security' | 'metrics' | 'users' | 'audit'

const AUDIT_PAGE = 100
const MIN_PASSWORD = 10

const CATEGORY_META: { id: MCPToolCategory; label: string; icon: typeof Bug; tone: string }[] = [
  { id: 'read', label: '只读探查', icon: ScanEye, tone: 'text-sky-600' },
  { id: 'write', label: '资产编排', icon: PenLine, tone: 'text-indigo-600' },
  { id: 'debug', label: '调试回归', icon: Bug, tone: 'text-amber-600' },
  { id: 'run', label: '压测控制', icon: Gauge, tone: 'text-rose-600' },
]

const AUTH_ACTION = /^user\.(login|login_failed|logout|setup|password_|totp_)/

const AUDIT_FILTERS: { id: string; label: string; mono: boolean; match: (action: string) => boolean }[] = [
  { id: 'all', label: '全部', mono: false, match: () => true },
  { id: 'mcp', label: 'mcp.tool.*', mono: true, match: (a) => a.startsWith('mcp.tool.') },
  { id: 'auth', label: '登录认证', mono: false, match: (a) => AUTH_ACTION.test(a) },
  { id: 'user', label: 'user.*', mono: true, match: (a) => a.startsWith('user.') },
  { id: 'token', label: 'token.*', mono: true, match: (a) => a.startsWith('token.') },
  { id: 'security', label: 'security.*', mono: true, match: (a) => a.startsWith('security.') },
  { id: 'run', label: 'run.*', mono: true, match: (a) => a.startsWith('run.') },
  { id: 'agent', label: 'agent.*', mono: true, match: (a) => a.startsWith('agent.') },
  { id: 'project', label: 'project.*', mono: true, match: (a) => a.startsWith('project.') },
]

const ROLE_BADGE: Record<string, string> = {
  admin: 'bg-rose-50 border-rose-200 text-rose-800',
  member: 'bg-sky-50 border-sky-200 text-sky-800',
  viewer: 'bg-slate-100 border-slate-200 text-slate-700',
  none: 'bg-rose-500/15 border-rose-300 text-rose-800',
}

const ACL_ROLE_LABELS: Record<ProjectAccessRole, string> = {
  '': '继承全局',
  member: '读写',
  viewer: '只读',
  none: '禁止访问',
}

const inputCls = 'w-full px-2.5 py-1.5 border border-line-strong rounded-[10px] bg-surface text-fg disabled:bg-surface-subtle disabled:text-fg-subtle'
const ghostBtn =
  'inline-flex items-center gap-1 px-2.5 py-1.5 rounded border border-line-strong bg-surface hover:bg-surface-hover text-fg-muted font-medium disabled:opacity-50'

/** Cryptographically random password without look-alike characters (rejection sampling, no modulo bias). */
function randomPassword(length = 20): string {
  const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789-_.!@#%+='
  const limit = 256 - (256 % alphabet.length)
  const out: string[] = []
  const buf = new Uint8Array(length * 2)
  while (out.length < length) {
    crypto.getRandomValues(buf)
    for (const b of buf) {
      if (b < limit) out.push(alphabet[b % alphabet.length])
      if (out.length === length) break
    }
  }
  return out.join('')
}

/** Inline two-step confirm: first call arms `key`, a second call within 3s returns true. */
function useArmedConfirm() {
  const [armed, setArmed] = useState('')
  const timer = useRef(0)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const arm = (key: string) => {
    window.clearTimeout(timer.current)
    if (armed === key) {
      setArmed('')
      return true
    }
    setArmed(key)
    timer.current = window.setTimeout(() => setArmed(''), 3000)
    return false
  }
  const reset = () => {
    window.clearTimeout(timer.current)
    setArmed('')
  }
  return { armed, arm, reset }
}

function useCopied() {
  const [copied, setCopied] = useState('')
  const timer = useRef(0)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const mark = (key: string) => {
    setCopied(key)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setCopied(''), 1500)
  }
  return { copied, mark }
}

function Switch({
  checked,
  onChange,
  disabled,
  label,
  name,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
  label: string
  name?: string
}) {
  return (
    <span className="relative inline-flex shrink-0">
      <input
        type="checkbox"
        role="switch"
        name={name}
        aria-label={label}
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        className="peer absolute inset-0 opacity-0 cursor-pointer disabled:cursor-not-allowed"
      />
      <span
        aria-hidden="true"
        className="w-8 h-[18px] rounded-full bg-line-strong peer-checked:bg-emerald-600 peer-disabled:opacity-50 peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-sky-500 transition-colors relative after:absolute after:top-[2px] after:left-[2px] after:w-[14px] after:h-[14px] after:rounded-full after:bg-white after:shadow-sm after:transition-transform peer-checked:after:translate-x-[14px]"
      />
    </span>
  )
}

function PasswordField({
  value,
  onChange,
  name,
  placeholder,
  ariaLabel,
  autoFocus,
  onCopy,
  copied,
}: {
  value: string
  onChange: (v: string) => void
  name: string
  placeholder?: string
  ariaLabel: string
  autoFocus?: boolean
  onCopy?: () => void
  copied?: boolean
}) {
  const [show, setShow] = useState(false)
  const short = value.length > 0 && value.length < MIN_PASSWORD
  return (
    <div
      className={`flex items-stretch rounded border bg-surface focus-within:border-sky-500 ${
        short ? 'border-amber-400' : 'border-line-strong'
      }`}
    >
      <input
        type={show ? 'text' : 'password'}
        name={name}
        aria-label={ariaLabel}
        autoComplete="new-password"
        autoCorrect="off"
        autoCapitalize="none"
        spellCheck={false}
        autoFocus={autoFocus}
        minLength={MIN_PASSWORD}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="flex-1 min-w-0 px-2.5 py-1.5 bg-transparent text-fg font-mono outline-none"
      />
      <span className="tg-tabular self-center px-1.5 text-[11px] text-fg-subtle font-mono" aria-hidden="true">
        {value.length > 0 ? value.length : ''}
      </span>
      <button
        type="button"
        onClick={() => setShow((s) => !s)}
        aria-label={show ? '隐藏密码' : '显示密码'}
        aria-pressed={show}
        className="px-2 text-fg-subtle hover:text-fg border-l border-line"
      >
        {show ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
      </button>
      <button
        type="button"
        onClick={() => {
          onChange(randomPassword())
          setShow(true)
        }}
        aria-label="生成随机密码"
        title="生成随机密码"
        className="px-2 text-fg-subtle hover:text-sky-600 border-l border-line"
      >
        <Dices className="w-3.5 h-3.5" />
      </button>
      {onCopy && (
        <button
          type="button"
          onClick={onCopy}
          disabled={!value}
          aria-label="复制密码"
          title="复制密码"
          className="px-2 text-fg-subtle hover:text-fg border-l border-line disabled:opacity-40"
        >
          {copied ? <Check className="w-3.5 h-3.5 text-emerald-600" /> : <Copy className="w-3.5 h-3.5" />}
        </button>
      )}
    </div>
  )
}

function Field({ label, children, className = '' }: { label: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={`block ${className}`}>
      <span className="block font-medium text-fg-muted mb-1">{label}</span>
      {children}
    </label>
  )
}

export function GovernanceView({ currentUser, projects = [], onToast }: Props) {
  const isAdmin = currentUser.role === 'admin'
  const [subTab, setSubTab] = useState<SubTab>(isAdmin ? 'users' : 'security')
  const govTabsRef = useRef<HTMLDivElement>(null)
  const confirm = useArmedConfirm()
  const { copied, mark: markCopied } = useCopied()

  // Users & Initial Project Permissions
  const [users, setUsers] = useState<User[]>([])
  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newRole, setNewRole] = useState<UserRole>('member')
  const [createAclMode, setCreateAclMode] = useState<'inherit' | 'custom'>('inherit')
  const [newProjectRoles, setNewProjectRoles] = useState<Record<string, ProjectAccessRole>>({})
  const [savingUser, setSavingUser] = useState(false)
  const [createdBanner, setCreatedBanner] = useState<{ username: string; password: string; role: UserRole } | null>(null)
  const [resetUid, setResetUid] = useState(0)
  const [resetPw, setResetPw] = useState('')
  const [resettingPw, setResettingPw] = useState(false)
  const [pendingDemote, setPendingDemote] = useState<{ user: User; nextRole: UserRole } | null>(null)
  const [permUid, setPermUid] = useState(0)
  const [permLoading, setPermLoading] = useState(false)
  const [permSaving, setPermSaving] = useState(false)
  const [permItems, setPermItems] = useState<UserProjectAccessItem[]>([])
  const [permDraft, setPermDraft] = useState<Record<string, ProjectAccessRole>>({})
  const [allTokens, setAllTokens] = useState<APIToken[]>([])

  // Security policy + MCP catalog & activity
  const [policy, setPolicy] = useState<SecurityPolicy>({
    allowlist_enabled: false,
    allowed_targets: [],
    member_max_vus: 0,
    member_max_rps: 0,
    scripts_disabled: false,
  })
  const [allowedTargetsText, setAllowedTargetsText] = useState('')
  const [savingPolicy, setSavingPolicy] = useState(false)
  const [catalog, setCatalog] = useState<MCPCatalog | null>(null)
  const [mcpActivity, setMcpActivity] = useState<AuditEntry[]>([])

  // Metrics sink & Prometheus
  const [sink, setSink] = useState<MetricsSinkConfig>({ enabled: false, type: 'influxdb', url: '' })
  const [savingSink, setSavingSink] = useState(false)
  const [testingSink, setTestingSink] = useState(false)
  const [promPreview, setPromPreview] = useState('')
  const [loadingProm, setLoadingProm] = useState(false)
  const [showSinkToken, setShowSinkToken] = useState(false)

  // Audit
  const [auditLogs, setAuditLogs] = useState<AuditEntry[]>([])
  const [auditQuery, setAuditQuery] = useState('')
  const [auditFilter, setAuditFilter] = useState('all')
  const [auditHasMore, setAuditHasMore] = useState(false)
  const [auditLoading, setAuditLoading] = useState(false)
  const [expandedLog, setExpandedLog] = useState<Set<number>>(new Set())

  const errMsg = (err: unknown, fallback: string) => (err instanceof APIError ? err.message : fallback)

  const loadUsers = async () => {
    if (!isAdmin) return
    try {
      const [uList, tokList] = await Promise.all([api.listUsers(), api.adminListTokens().catch(() => [])])
      setUsers(uList)
      setAllTokens(tokList)
    } catch (err) {
      onToast('error', errMsg(err, '加载用户列表失败'))
    }
  }

  const openPermEditor = async (uid: number) => {
    confirm.reset()
    if (permUid === uid) {
      setPermUid(0)
      return
    }
    setResetUid(0)
    setPermUid(uid)
    setPermLoading(true)
    try {
      const res = await api.getUserProjects(uid)
      setPermItems(res.projects || [])
      const draft: Record<string, ProjectAccessRole> = {}
      for (const p of res.projects || []) {
        draft[p.project_id] = p.member_role || ''
      }
      setPermDraft(draft)
    } catch (err) {
      onToast('error', errMsg(err, '加载用户项目权限失败'))
      setPermUid(0)
    } finally {
      setPermLoading(false)
    }
  }

  const handleSaveUserProjects = async (u: User) => {
    setPermSaving(true)
    try {
      const res = await api.putUserProjects(u.id, permDraft)
      setPermItems(res.projects || [])
      const draft: Record<string, ProjectAccessRole> = {}
      for (const p of res.projects || []) draft[p.project_id] = p.member_role || ''
      setPermDraft(draft)
      onToast('success', `已更新「${u.username}」的项目访问权限`)
    } catch (err) {
      onToast('error', errMsg(err, '保存项目权限失败'))
    } finally {
      setPermSaving(false)
    }
  }

  const loadPolicy = async () => {
    try {
      const p = await api.getSecurityPolicy()
      setPolicy(p)
      setAllowedTargetsText((p.allowed_targets || []).join('\n'))
    } catch (err) {
      onToast('error', errMsg(err, '加载安全策略失败'))
    }
  }

  const loadCatalog = async () => {
    try {
      const [cat, act] = await Promise.all([
        api.getMCPCatalog(),
        isAdmin ? api.mcpActivity(20).catch(() => []) : Promise.resolve([]),
      ])
      setCatalog(cat)
      setMcpActivity(act)
    } catch (err) {
      onToast('error', errMsg(err, '加载 MCP 工具目录失败'))
    }
  }

  const loadSink = async () => {
    try {
      setSink(await api.getMetricsSink())
    } catch (err) {
      onToast('error', errMsg(err, '加载指标外推配置失败'))
    }
  }

  const loadAudit = async (more = false) => {
    if (!isAdmin) return
    setAuditLoading(true)
    try {
      const lastId = more && auditLogs.length > 0 ? auditLogs[auditLogs.length - 1].id : undefined
      const prefixMap: Record<string, string> = {
        mcp: 'mcp.',
        user: 'user.',
        token: 'token.',
        security: 'security.',
        run: 'run.',
        agent: 'agent.',
        project: 'project.',
      }
      const page = await api.listAuditLogs({
        limit: AUDIT_PAGE,
        before_id: lastId,
        action_prefix: prefixMap[auditFilter] || undefined,
      })
      setAuditLogs((prev) => (more ? [...prev, ...page] : page))
      setAuditHasMore(page.length >= AUDIT_PAGE)
      if (!more) setExpandedLog(new Set())
    } catch (err) {
      onToast('error', errMsg(err, '加载审计日志失败'))
    } finally {
      setAuditLoading(false)
    }
  }

  useEffect(() => {
    confirm.reset()
    if (subTab === 'users') void loadUsers()
    if (subTab === 'security') {
      void loadPolicy()
      void loadCatalog()
    }
    if (subTab === 'metrics') void loadSink()
    if (subTab === 'audit') void loadAudit()
  }, [subTab])

  useEffect(() => {
    if (subTab === 'audit') void loadAudit(false)
  }, [auditFilter])

  /* ---------------- Users ---------------- */

  const handleCreateUser = async (e: FormEvent) => {
    e.preventDefault()
    if (!newUsername.trim() || newPassword.length < MIN_PASSWORD) {
      onToast('error', `请输入用户名和至少 ${MIN_PASSWORD} 个字符的初始密码`)
      return
    }
    setSavingUser(true)
    try {
      const createdU = await api.createUser({
        username: newUsername.trim(),
        password: newPassword,
        role: newRole,
      })
      if (newRole !== 'admin' && createAclMode === 'custom' && projects.length > 0) {
        const overrides: Record<string, ProjectAccessRole> = {}
        for (const p of projects) {
          overrides[p.id] = newProjectRoles[p.id] || 'none'
        }
        await api.putUserProjects(createdU.user.id, overrides)
      }
      setCreatedBanner({ username: createdU.user.username, password: newPassword, role: createdU.user.role })
      setNewUsername('')
      setNewPassword('')
      setCreateAclMode('inherit')
      setNewProjectRoles({})
      await loadUsers()
      onToast('success', `已开通账号「${createdU.user.username}」`)
    } catch (err) {
      onToast('error', errMsg(err, '开通账号失败'))
    } finally {
      setSavingUser(false)
    }
  }

  const openReset = (uid: number) => {
    confirm.reset()
    if (resetUid === uid) {
      setResetUid(0)
      return
    }
    setResetUid(uid)
    setResetPw('')
  }

  const handleResetPassword = async (u: User) => {
    if (resetPw.length < MIN_PASSWORD) {
      onToast('error', `新密码至少 ${MIN_PASSWORD} 个字符`)
      return
    }
    if (!confirm.arm(`pw-${u.id}`)) return
    setResettingPw(true)
    try {
      await api.adminResetUserPassword(u.id, resetPw)
      setResetUid(0)
      setResetPw('')
      onToast('success', `已重置「${u.username}」的密码`)
    } catch (err) {
      onToast('error', errMsg(err, '重置密码失败'))
    } finally {
      setResettingPw(false)
    }
  }

  const copyPassword = async (key: string, pw: string) => {
    if (await copyText(pw)) markCopied(key)
    else onToast('info', '复制失败，请手动选择')
  }

  /* ---------------- Security ---------------- */

  const disabledTools = useMemo(() => new Set(policy.mcp_disabled_tools || []), [policy.mcp_disabled_tools])
  const tools = catalog?.tools || []
  const enabledCount = tools.filter((t) => !disabledTools.has(t.name)).length

  const setToolEnabled = (name: string, enabled: boolean) => {
    const next = new Set(disabledTools)
    if (enabled) next.delete(name)
    else next.add(name)
    setPolicy({ ...policy, mcp_disabled_tools: [...next].sort() })
  }

  const handleSavePolicy = async (e: FormEvent) => {
    e.preventDefault()
    setSavingPolicy(true)
    try {
      const targets = allowedTargetsText
        .split(/\r?\n/)
        .map((s) => s.trim())
        .filter(Boolean)
      const saved = await api.putSecurityPolicy({ ...policy, allowed_targets: targets })
      setPolicy(saved)
      setAllowedTargetsText((saved.allowed_targets || []).join('\n'))
      void loadCatalog()
      onToast('success', '安全策略已保存')
    } catch (err) {
      onToast('error', errMsg(err, '保存安全策略失败'))
    } finally {
      setSavingPolicy(false)
    }
  }

  const optionalNumber = (v: number | undefined) => (v && v > 0 ? String(v) : '')
  const parseOptional = (raw: string) => (raw.trim() === '' ? 0 : Math.max(0, Math.floor(Number(raw) || 0)))

  /* ---------------- Metrics ---------------- */

  const handleSaveSink = async (e: FormEvent) => {
    e.preventDefault()
    setSavingSink(true)
    try {
      setSink(await api.putMetricsSink(sink))
      onToast('success', '指标外推配置已更新')
    } catch (err) {
      onToast('error', errMsg(err, '保存指标外推配置失败'))
    } finally {
      setSavingSink(false)
    }
  }

  const handleTestSink = async () => {
    setTestingSink(true)
    try {
      await api.testMetricsSink(sink)
      onToast('success', '连通性测试成功')
    } catch (err) {
      onToast('error', errMsg(err, '连通性测试失败'))
    } finally {
      setTestingSink(false)
    }
  }

  const handleLoadProm = async () => {
    setLoadingProm(true)
    try {
      setPromPreview(await api.fetchPrometheusMetrics())
    } catch (err) {
      onToast('error', errMsg(err, '读取 Prometheus 指标失败'))
    } finally {
      setLoadingProm(false)
    }
  }

  const promYaml = [
    'scrape_configs:',
    '  - job_name: tidegate',
    `    metrics_path: ${api.prometheusMetricsUrl()}`,
    `    scheme: ${window.location.protocol === 'https:' ? 'https' : 'http'}`,
    '    authorization:',
    '      type: Bearer',
    '      credentials: <read 权限 API 令牌>',
    '    static_configs:',
    `      - targets: ['${window.location.host}']`,
    '',
  ].join('\n')

  const copyPromYaml = async () => {
    if (await copyText(promYaml)) {
      markCopied('prom-yaml')
      onToast('success', '已复制 prometheus.yml 片段')
    } else {
      onToast('info', '复制失败，请手动选择')
    }
  }

  /* ---------------- Audit ---------------- */

  const auditFilterDef = AUDIT_FILTERS.find((f) => f.id === auditFilter) || AUDIT_FILTERS[0]
  const auditNeedle = auditQuery.trim().toLowerCase()
  const visibleLogs = useMemo(
    () =>
      auditLogs.filter((l) => {
        if (!auditFilterDef.match(l.action)) return false
        if (!auditNeedle) return true
        return (
          (l.username || '').toLowerCase().includes(auditNeedle) ||
          l.action.toLowerCase().includes(auditNeedle) ||
          `${l.resource || ''}:${l.resource_id || ''}`.toLowerCase().includes(auditNeedle) ||
          (l.remote_addr || '').toLowerCase().includes(auditNeedle)
        )
      }),
    [auditLogs, auditFilterDef, auditNeedle]
  )
  const filterCounts = useMemo(() => {
    const out: Record<string, number> = {}
    for (const f of AUDIT_FILTERS) out[f.id] = auditLogs.filter((l) => f.match(l.action)).length
    return out
  }, [auditLogs])

  const toggleLog = (id: number) =>
    setExpandedLog((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  const exportAudit = (fmt: 'csv' | 'json') => {
    if (visibleLogs.length === 0) return
    const name = `tidegate-audit-${fileStamp()}.${fmt}`
    if (fmt === 'json') {
      downloadText(name, JSON.stringify(visibleLogs, null, 2), 'application/json;charset=utf-8')
    } else {
      downloadText(
        name,
        toCSV([
          ['id', 'created_at', 'user_id', 'username', 'role', 'action', 'resource', 'resource_id', 'remote_addr', 'detail'],
          ...visibleLogs.map((l) => [
            l.id,
            l.created_at,
            l.user_id,
            l.username,
            l.role,
            l.action,
            l.resource ?? '',
            l.resource_id ?? '',
            l.remote_addr ?? '',
            l.detail ?? '',
          ]),
        ]),
        'text/csv;charset=utf-8'
      )
    }
  }

  /* ---------------- Render ---------------- */

  const tabs: { id: SubTab; label: string; icon: typeof Users; show: boolean }[] = [
    { id: 'users', label: '用户与角色', icon: Users, show: isAdmin },
    { id: 'security', label: '安全护栏', icon: Lock, show: true },
    { id: 'metrics', label: '指标导出', icon: Activity, show: true },
    { id: 'audit', label: '审计日志', icon: FileSearch, show: isAdmin },
  ]

  const totalEnabledAdmins = users.filter((u) => u.role === 'admin' && !u.disabled).length

  const applyRoleChange = async (u: User, role: UserRole) => {
    try {
      await api.patchUser(u.id, { role })
      setPendingDemote(null)
      await loadUsers()
      onToast('success', `已将 ${u.username} 设为 ${role}`)
    } catch (err) {
      onToast('error', errMsg(err, '更新角色失败'))
    }
  }

  return (
    <div className="flex-1 overflow-y-auto tg-scroller bg-transparent p-2.5 pb-[72px] space-y-3 text-xs">
      <div className="tg-accent-card px-3.5 py-2.5 flex flex-wrap items-center justify-between gap-3">
        <div
          ref={govTabsRef}
          role="tablist"
          aria-label="系统治理页签"
          className="relative tg-tablist items-center gap-1 bg-surface-subtle/80 border border-line p-1 rounded-xl"
        >
          <LiquidCapsule containerRef={govTabsRef} activeId={subTab} />
          {tabs
            .filter((t) => t.show)
            .map((t) => {
              const Icon = t.icon
              const selected = subTab === t.id
              return (
                <button
                  key={t.id}
                  type="button"
                  role="tab"
                  aria-selected={selected}
                  data-liq-id={t.id}
                  onClick={() => setSubTab(t.id)}
                  className={`relative z-10 inline-flex items-center gap-1.5 px-3 py-1.5 rounded-xl font-semibold whitespace-nowrap transition-colors ${
                    selected ? 'text-tidal' : 'text-fg-muted hover:text-fg'
                  }`}
                >
                  <Icon className={`w-3.5 h-3.5 ${selected ? 'text-tidal' : ''}`} /> {t.label}
                </button>
              )
            })}
        </div>
        <span className="text-[11px] text-fg-subtle font-mono">
          {currentUser.username} · {currentUser.role}
        </span>
      </div>

      {subTab === 'users' && isAdmin && (
        <div className="space-y-3">
          {/* Provision New Account + Immediate Project ACL Assignment */}
          <section aria-label="开通新账号" className="tg-glass-card !overflow-visible p-4 space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h2 className="font-semibold text-sm text-fg flex items-center gap-1.5">
                <UserPlus className="w-4 h-4 text-tidal" /> 开通新账号并分配项目权限
              </h2>
              {createdBanner && (
                <div className="flex items-center gap-2 px-3 py-1 rounded-full border border-emerald-300 bg-emerald-50 text-emerald-900">
                  <span className="font-mono">
                    {createdBanner.username} ({createdBanner.role})
                  </span>
                  <button
                    type="button"
                    onClick={() =>
                      void copyPassword(
                        'created-cred',
                        `账号: ${createdBanner.username}\n密码: ${createdBanner.password}\n角色: ${createdBanner.role}`
                      )
                    }
                    className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full bg-emerald-600 text-white font-medium"
                  >
                    {copied === 'created-cred' ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
                    {copied === 'created-cred' ? '已复制凭据' : '复制账号密码'}
                  </button>
                </div>
              )}
            </div>

            <form onSubmit={handleCreateUser} className="space-y-3">
              <div className="grid grid-cols-1 md:grid-cols-12 gap-2.5 items-end">
                <Field label="用户名" className="md:col-span-3">
                  <input
                    type="text"
                    name="new_username"
                    required
                    autoComplete="off"
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    value={newUsername}
                    onChange={(e) => setNewUsername(e.target.value)}
                    placeholder="qa-lead"
                    className={`${inputCls} font-mono`}
                  />
                </Field>
                <div className="md:col-span-4">
                  <span className="block font-medium text-fg-muted mb-1">初始密码</span>
                  <PasswordField
                    name="new_password"
                    ariaLabel="初始密码"
                    value={newPassword}
                    onChange={setNewPassword}
                    placeholder={`至少 ${MIN_PASSWORD} 位`}
                    onCopy={() => void copyPassword('new-pw', newPassword)}
                    copied={copied === 'new-pw'}
                  />
                </div>
                <div className="md:col-span-3">
                  <span className="block font-medium text-fg-muted mb-1">全局角色</span>
                  <GlassSelect
                    ariaLabel="新账号角色"
                    value={newRole}
                    onChange={(v) => setNewRole(v as UserRole)}
                    options={[
                      { value: 'member', label: 'member · 编辑与压测' },
                      { value: 'viewer', label: 'viewer · 全站只读' },
                      { value: 'admin', label: 'admin · 系统管理员' },
                    ]}
                  />
                </div>
                <div className="md:col-span-2">
                  <button
                    type="submit"
                    disabled={savingUser}
                    className="w-full tg-btn-primary"
                  >
                    <Plus className="w-3.5 h-3.5" /> {savingUser ? '开通中…' : '开通账号'}
                  </button>
                </div>
              </div>

              {newRole !== 'admin' && projects.length > 0 && (
                <div className="rounded-xl border border-line bg-surface-subtle/70 p-3 space-y-2.5">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="font-medium text-fg flex items-center gap-1.5">
                      <FolderKey className="w-3.5 h-3.5 text-tidal" /> 初始项目访问范围
                    </span>
                    <div role="radiogroup" aria-label="项目权限分配方式" className="inline-flex rounded-lg border border-line bg-surface p-0.5">
                      {(
                        [
                          ['inherit', '按全局角色访问开放项目'],
                          ['custom', '自定义每个项目的权限'],
                        ] as const
                      ).map(([id, label]) => (
                        <button
                          key={id}
                          type="button"
                          role="radio"
                          aria-checked={createAclMode === id}
                          onClick={() => setCreateAclMode(id)}
                          className={`px-2.5 py-1 rounded-md font-medium ${
                            createAclMode === id ? 'bg-sky-500/15 text-tidal' : 'text-fg-muted hover:text-fg'
                          }`}
                        >
                          {label}
                        </button>
                      ))}
                    </div>
                  </div>
                  {createAclMode === 'custom' && (
                    <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-2">
                      {projects.map((pr) => {
                        const val = newProjectRoles[pr.id] || 'none'
                        return (
                          <div
                            key={pr.id}
                            className="flex items-center justify-between gap-2 px-2.5 py-1.5 rounded-lg border border-line bg-surface"
                          >
                            <span className="truncate font-medium text-fg" title={pr.name}>
                              {pr.name}
                            </span>
                            <div className="inline-flex rounded-md border border-line bg-surface-subtle p-0.5 shrink-0">
                              {(['member', 'viewer', 'none'] as const).map((r) => (
                                <button
                                  key={r}
                                  type="button"
                                  onClick={() =>
                                    setNewProjectRoles((prev) => ({ ...prev, [pr.id]: r }))
                                  }
                                  className={`px-2 py-0.5 rounded text-[11px] font-medium ${
                                    val === r
                                      ? r === 'none'
                                        ? 'bg-rose-600 text-white'
                                        : 'bg-primary text-white'
                                      : 'text-fg-muted hover:text-fg'
                                  }`}
                                >
                                  {ACL_ROLE_LABELS[r]}
                                </button>
                              ))}
                            </div>
                          </div>
                        )
                      })}
                    </div>
                  )}
                </div>
              )}
            </form>
          </section>

          {/* Users & Per-User Project Access Matrix */}
          <section aria-label="团队账号与项目权限" className="tg-glass-card overflow-hidden">
            <div className="overflow-x-auto">
              <table className="w-full text-left border-collapse">
                <thead className="border-b border-line bg-surface-subtle/60 text-fg-subtle">
                  <tr>
                    <th className="px-3 py-2.5 font-medium">ID</th>
                    <th className="px-3 py-2.5 font-medium">用户名</th>
                    <th className="px-3 py-2.5 font-medium">全局角色</th>
                    <th className="px-3 py-2.5 font-medium">账号状态</th>
                    <th className="px-3 py-2.5 font-medium">2FA</th>
                    <th className="px-3 py-2.5 font-medium">在线会话 / 最近活跃</th>
                    <th className="px-3 py-2.5 font-medium text-right">操作</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {users.map((u) => {
                    const isSelf = u.id === currentUser.id
                    const resetOpen = resetUid === u.id
                    const permOpen = permUid === u.id
                    const delArmed = confirm.armed === `del-${u.id}`
                    const totpArmed = confirm.armed === `totp-${u.id}`
                    const pwArmed = confirm.armed === `pw-${u.id}`
                    const isLastAdmin = u.role === 'admin' && !u.disabled && totalEnabledAdmins <= 1
                    return (
                      <Fragment key={u.id}>
                        <tr className={resetOpen || permOpen ? 'bg-surface-subtle' : 'hover:bg-surface-hover'}>
                          <td className="px-3 py-2 font-mono tg-tabular text-fg-subtle">{u.id}</td>
                          <td className="px-3 py-2 font-semibold text-fg">
                            <span className={`font-mono ${u.disabled ? 'line-through text-fg-subtle' : ''}`}>
                              {u.username}
                            </span>
                            {isSelf && (
                              <span className="ml-1.5 px-1.5 py-px rounded bg-sky-50 border border-sky-200 text-sky-800 text-[11px] font-medium">
                                当前账号
                              </span>
                            )}
                          </td>
                          <td className="px-3 py-2">
                            {isSelf ? (
                              <span
                                className={`inline-flex px-2 py-0.5 rounded border font-mono text-[11px] ${ROLE_BADGE[u.role] || ROLE_BADGE.viewer}`}
                                title="不能修改自己的角色"
                              >
                                {u.role}
                              </span>
                            ) : pendingDemote?.user.id === u.id ? (
                              <div className="inline-flex items-center gap-1.5">
                                <button
                                  type="button"
                                  onClick={() => void applyRoleChange(u, pendingDemote.nextRole)}
                                  className="px-2 py-1 rounded-md bg-rose-600 text-white font-semibold text-[11px]"
                                >
                                  确认降级为 {pendingDemote.nextRole}？
                                </button>
                                <button
                                  type="button"
                                  onClick={() => setPendingDemote(null)}
                                  className="px-1.5 py-1 rounded border border-line text-fg-muted text-[11px]"
                                >
                                  取消
                                </button>
                              </div>
                            ) : (
                              <GlassSelect
                                ariaLabel={`${u.username} 的角色`}
                                value={u.role}
                                className="min-w-[140px] max-w-[200px]"
                                menuClassName="w-56"
                                onChange={(val) => {
                                  const nextRole = val as UserRole
                                  if (nextRole === u.role) return
                                  if (u.role === 'admin' && nextRole !== 'admin') {
                                    setPendingDemote({ user: u, nextRole })
                                    return
                                  }
                                  void applyRoleChange(u, nextRole)
                                }}
                                options={[
                                  { value: 'admin', label: 'admin', description: '系统管理员' },
                                  { value: 'member', label: 'member', description: '编辑与压测' },
                                  { value: 'viewer', label: 'viewer', description: '全站只读' },
                                ]}
                              />
                            )}
                          </td>
                          <td className="px-3 py-2">
                            <div className="inline-flex items-center gap-2">
                              <Switch
                                name={`user-enabled-${u.id}`}
                                label={`启用或停用 ${u.username}`}
                                checked={!u.disabled}
                                disabled={isSelf || isLastAdmin}
                                onChange={async (enabled) => {
                                  try {
                                    await api.patchUser(u.id, { disabled: !enabled })
                                    await loadUsers()
                                    onToast('success', `${enabled ? '已启用' : '已停用'}账号「${u.username}」`)
                                  } catch (err) {
                                    onToast('error', errMsg(err, '更新账号状态失败'))
                                  }
                                }}
                              />
                              <span className={u.disabled ? 'text-rose-700 font-medium' : 'text-emerald-700'}>
                                {u.disabled ? '已停用' : '正常'}
                              </span>
                            </div>
                          </td>
                          <td className="px-3 py-2">
                            {u.totp_enabled ? (
                              <span className="inline-flex px-2 py-0.5 rounded border bg-emerald-50 border-emerald-200 text-emerald-800 font-medium text-[11px]">
                                已启用
                              </span>
                            ) : (
                              <span className="inline-flex px-2 py-0.5 rounded border bg-surface-subtle border-line text-fg-subtle text-[11px]">
                                未启用
                              </span>
                            )}
                          </td>
                          <td className="px-3 py-2 text-fg-subtle tg-tabular whitespace-nowrap">
                            <div className="flex items-center gap-1.5">
                              <span className="font-mono text-fg font-medium">{u.active_sessions ?? 0}</span>
                              <span>个会话</span>
                              {(u.active_sessions ?? 0) > 0 && !isSelf && (
                                <button
                                  type="button"
                                  onClick={async () => {
                                    try {
                                      const res = await api.adminRevokeUserSessions(u.id)
                                      await loadUsers()
                                      onToast('success', `已强制下线 ${res.revoked} 个会话`)
                                    } catch (err) {
                                      onToast('error', errMsg(err, '强制下线失败'))
                                    }
                                  }}
                                  className="px-1.5 py-0.5 rounded border border-rose-200 text-rose-700 hover:bg-rose-50 text-[10px]"
                                  title="注销该用户的全部在线会话"
                                >
                                  强制下线
                                </button>
                              )}
                            </div>
                            <div className="text-[10px] text-fg-subtle">
                              {u.last_seen_at ? `活跃 ${new Date(u.last_seen_at).toLocaleString()}` : `创建 ${new Date(u.created_at).toLocaleDateString()}`}
                            </div>
                          </td>
                          <td className="px-3 py-2 text-right">
                            <div className="inline-flex items-center gap-1.5 justify-end">
                              {u.role !== 'admin' && (
                                <button
                                  type="button"
                                  onClick={() => void openPermEditor(u.id)}
                                  aria-expanded={permOpen}
                                  className={`inline-flex items-center gap-1 px-2 py-1 rounded-lg border ${
                                    permOpen
                                      ? 'border-sky-400 bg-sky-500/15 text-tidal font-semibold'
                                      : 'border-line-strong text-fg hover:bg-surface-hover'
                                  }`}
                                >
                                  <FolderKey className="w-3 h-3 text-tidal" /> 项目权限
                                </button>
                              )}
                              <button
                                type="button"
                                onClick={() => {
                                  setPermUid(0)
                                  openReset(u.id)
                                }}
                                aria-expanded={resetOpen}
                                className={`inline-flex items-center gap-1 px-2 py-1 rounded-lg border ${
                                  resetOpen
                                    ? 'border-sky-300 bg-sky-50 text-sky-800'
                                    : 'border-line-strong text-fg-muted hover:bg-surface-hover'
                                }`}
                              >
                                <KeyRound className="w-3 h-3" /> 重置密码
                              </button>
                              {u.totp_enabled && (
                                <button
                                  type="button"
                                  onClick={async () => {
                                    if (!confirm.arm(`totp-${u.id}`)) return
                                    try {
                                      await api.adminResetUserTOTP(u.id)
                                      await loadUsers()
                                      onToast('success', `已重置「${u.username}」的 2FA`)
                                    } catch (err) {
                                      onToast('error', errMsg(err, '重置 2FA 失败'))
                                    }
                                  }}
                                  onBlur={confirm.reset}
                                  className={`inline-flex items-center gap-1 px-2 py-1 rounded-lg border ${
                                    totpArmed
                                      ? 'bg-amber-600 border-amber-600 hover:bg-amber-solid text-white font-medium'
                                      : 'border-amber-300 bg-amber-50 text-amber-800 hover:bg-amber-100'
                                  }`}
                                >
                                  <RotateCcw className="w-3 h-3" /> {totpArmed ? '确认重置？' : '重置 2FA'}
                                </button>
                              )}
                              {!isSelf && (
                                <button
                                  type="button"
                                  onClick={async () => {
                                    if (!confirm.arm(`del-${u.id}`)) return
                                    try {
                                      await api.deleteUser(u.id)
                                      if (resetUid === u.id) setResetUid(0)
                                      if (permUid === u.id) setPermUid(0)
                                      await loadUsers()
                                      onToast('success', `已删除用户「${u.username}」`)
                                    } catch (err) {
                                      onToast('error', errMsg(err, '删除用户失败'))
                                    }
                                  }}
                                  onBlur={confirm.reset}
                                  className={`inline-flex items-center gap-1 px-2 py-1 rounded-lg border ${
                                    delArmed
                                      ? 'bg-rose-600 border-rose-600 hover:bg-rose-solid text-white font-medium'
                                      : 'border-rose-200 text-rose-700 hover:bg-rose-50'
                                  }`}
                                >
                                  <Trash2 className="w-3 h-3" /> {delArmed ? '确认删除？' : '删除'}
                                </button>
                              )}
                            </div>
                          </td>
                        </tr>
                        {permOpen && (
                          <tr className="bg-surface-subtle/90">
                            <td colSpan={7} className="px-4 py-3">
                              {permLoading ? (
                                <div className="py-3 text-center text-fg-subtle">正在加载项目权限矩阵…</div>
                              ) : (
                                <div className="space-y-3">
                                  <div className="flex flex-wrap items-center justify-between gap-2">
                                    <div className="font-semibold text-fg flex items-center gap-1.5">
                                      <FolderKey className="w-3.5 h-3.5 text-tidal" />
                                      <span>
                                        {u.username} · 项目权限管控（全局角色：<code className="font-mono">{u.role}</code>）
                                      </span>
                                    </div>
                                    <div className="flex items-center gap-2">
                                      <button
                                        type="button"
                                        onClick={() => {
                                          const allNone: Record<string, ProjectAccessRole> = {}
                                          for (const item of permItems) allNone[item.project_id] = 'none'
                                          setPermDraft(allNone)
                                        }}
                                        className="px-2 py-1 rounded-md border border-line bg-surface text-fg-muted hover:text-rose-700 text-[11px]"
                                      >
                                        全部禁止访问
                                      </button>
                                      <button
                                        type="button"
                                        onClick={() => {
                                          const cleared: Record<string, ProjectAccessRole> = {}
                                          for (const item of permItems) cleared[item.project_id] = ''
                                          setPermDraft(cleared)
                                        }}
                                        className="px-2 py-1 rounded-md border border-line bg-surface text-fg-muted hover:text-tidal text-[11px]"
                                      >
                                        全部恢复继承
                                      </button>
                                      <button
                                        type="button"
                                        disabled={permSaving}
                                        onClick={() => void handleSaveUserProjects(u)}
                                        className="tg-btn-primary !py-1"
                                      >
                                        <Save className="w-3 h-3" /> {permSaving ? '保存中…' : '保存项目权限'}
                                      </button>
                                    </div>
                                  </div>
                                  <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-2">
                                    {permItems.map((item) => {
                                      const val = permDraft[item.project_id] ?? ''
                                      return (
                                        <div
                                          key={item.project_id}
                                          className="flex items-center justify-between gap-2 p-2.5 rounded-xl border border-line bg-surface"
                                        >
                                          <div className="min-w-0">
                                            <div className="font-semibold text-fg truncate" title={item.project_name}>
                                              {item.project_name}
                                            </div>
                                            <div className="flex items-center gap-1.5 mt-0.5 text-[10px]">
                                              <span className="text-fg-subtle">
                                                {item.restricted ? '私有白名单' : '开放项目'}
                                              </span>
                                              <span
                                                className={`px-1.5 py-0.2 rounded-full border ${
                                                  item.allowed
                                                    ? ROLE_BADGE[item.effective_role] || ROLE_BADGE.viewer
                                                    : ROLE_BADGE.none
                                                }`}
                                              >
                                                生效: {item.allowed ? item.effective_role : '无权限'}
                                              </span>
                                            </div>
                                          </div>
                                          <div className="inline-flex rounded-lg border border-line bg-surface-subtle p-0.5 shrink-0">
                                            {(['', 'member', 'viewer', 'none'] as const).map((r) => (
                                              <button
                                                key={r || 'inherit'}
                                                type="button"
                                                onClick={() =>
                                                  setPermDraft((prev) => ({ ...prev, [item.project_id]: r }))
                                                }
                                                className={`px-2 py-0.5 rounded-md text-[11px] font-medium ${
                                                  val === r
                                                    ? r === 'none'
                                                      ? 'bg-rose-600 text-white'
                                                      : 'bg-primary text-white'
                                                    : 'text-fg-muted hover:text-fg'
                                                }`}
                                              >
                                                {r === '' ? (item.restricted ? '未授权' : '继承') : ACL_ROLE_LABELS[r]}
                                              </button>
                                            ))}
                                          </div>
                                        </div>
                                      )
                                    })}
                                  </div>
                                </div>
                              )}
                            </td>
                          </tr>
                        )}
                        {resetOpen && (
                          <tr className="bg-surface-subtle">
                            <td colSpan={7} className="px-3 pb-3 pt-1">
                              <form
                                onSubmit={(e) => {
                                  e.preventDefault()
                                  void handleResetPassword(u)
                                }}
                                className="flex flex-wrap items-center justify-end gap-2"
                              >
                                <span className="text-fg-muted">
                                  新密码 · <span className="font-mono text-fg">{u.username}</span>
                                </span>
                                <div className="w-full sm:w-80">
                                  <PasswordField
                                    name={`reset_password_${u.id}`}
                                    ariaLabel={`${u.username} 的新密码`}
                                    value={resetPw}
                                    onChange={(v) => {
                                      setResetPw(v)
                                      if (pwArmed) confirm.reset()
                                    }}
                                    placeholder={`至少 ${MIN_PASSWORD} 位`}
                                    autoFocus
                                    onCopy={() => void copyPassword('reset-pw', resetPw)}
                                    copied={copied === 'reset-pw'}
                                  />
                                </div>
                                <button
                                  type="submit"
                                  disabled={resettingPw || resetPw.length < MIN_PASSWORD}
                                  className={pwArmed ? 'tg-btn-primary !bg-rose-600' : 'tg-btn-primary'}
                                >
                                  <KeyRound className="w-3.5 h-3.5" /> {pwArmed ? '确认重置？' : '重置密码'}
                                </button>
                                <button
                                  type="button"
                                  onClick={() => {
                                    setResetUid(0)
                                    confirm.reset()
                                  }}
                                  aria-label="取消"
                                  className="p-1.5 rounded text-fg-subtle hover:text-fg hover:bg-surface-hover"
                                >
                                  <X className="w-3.5 h-3.5" />
                                </button>
                              </form>
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    )
                  })}
                </tbody>
              </table>
            </div>
          </section>

          {/* All-site API & MCP Token Audit */}
          <section aria-label="全站 API 与 MCP 令牌审计" className="tg-glass-card overflow-hidden">
            <div className="px-4 py-2.5 border-b border-line bg-surface-subtle/60 flex items-center justify-between gap-2">
              <h3 className="font-semibold text-fg flex items-center gap-1.5">
                <Key className="w-3.5 h-3.5 text-tidal" /> 全站 API 与 MCP 令牌审计
              </h3>
              <span className="tg-tabular font-mono text-[11px] text-fg-subtle">
                共 {allTokens.length} 个有效/已创建令牌
              </span>
            </div>
            <div className="overflow-x-auto">
              <table className="w-full text-left border-collapse">
                <thead className="border-b border-line text-fg-subtle">
                  <tr>
                    <th className="px-3 py-2 font-medium">令牌名</th>
                    <th className="px-3 py-2 font-medium">所属人</th>
                    <th className="px-3 py-2 font-medium">角色 / Scope</th>
                    <th className="px-3 py-2 font-medium">MCP 限额</th>
                    <th className="px-3 py-2 font-medium">最近使用</th>
                    <th className="px-3 py-2 font-medium text-right">操作</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {allTokens.length === 0 ? (
                    <tr>
                      <td colSpan={6} className="px-3 py-6 text-center text-fg-subtle">
                        暂无全站 API 令牌
                      </td>
                    </tr>
                  ) : (
                    allTokens.map((t) => {
                      const tokArmed = confirm.armed === `admtok-${t.id}`
                      return (
                        <tr key={t.id} className="hover:bg-surface-hover">
                          <td className="px-3 py-2 font-mono">
                            <span className="font-semibold text-fg">{t.name}</span>
                            <span className="ml-1.5 text-fg-subtle">{t.prefix}…</span>
                          </td>
                          <td className="px-3 py-2 font-mono text-fg">{t.username || `#${t.user_id}`}</td>
                          <td className="px-3 py-2 font-mono">
                            <span className="px-1.5 py-0.5 rounded bg-sky-50 border border-sky-200 text-sky-800 text-[10px]">
                              {t.role}
                            </span>{' '}
                            <span className="text-fg-muted text-[11px]">
                              {t.scopes?.length ? t.scopes.join(', ') : '全部 Scope'}
                            </span>
                          </td>
                          <td className="px-3 py-2 font-mono text-[11px] text-fg-muted tg-tabular">
                            {t.mcp_max_vus || t.rate_per_min || t.mcp_tools?.length ? (
                              <>
                                {t.mcp_tools?.length ? `${t.mcp_tools.length} 工具 ` : ''}
                                {t.mcp_max_vus ? `≤${t.mcp_max_vus} VU ` : ''}
                                {t.rate_per_min ? `${t.rate_per_min}/m` : ''}
                              </>
                            ) : (
                              '全局默认'
                            )}
                          </td>
                          <td className="px-3 py-2 text-fg-subtle tg-tabular">
                            {t.last_used_at ? new Date(t.last_used_at).toLocaleString() : '未使用'}
                            {t.last_used_addr ? ` · ${t.last_used_addr}` : ''}
                          </td>
                          <td className="px-3 py-2 text-right">
                            <button
                              type="button"
                              onClick={async () => {
                                if (!confirm.arm(`admtok-${t.id}`)) return
                                try {
                                  await api.revokeAPIToken(t.id)
                                  await loadUsers()
                                  onToast('success', `已吊销令牌「${t.name}」`)
                                } catch (err) {
                                  onToast('error', errMsg(err, '吊销令牌失败'))
                                }
                              }}
                              onBlur={confirm.reset}
                              className={tokArmed ? 'tg-btn-primary !bg-rose-600 !py-1' : 'tg-btn-danger !py-1'}
                            >
                              <LogOut className="w-3 h-3" /> {tokArmed ? '确认吊销？' : '吊销'}
                            </button>
                          </td>
                        </tr>
                      )
                    })
                  )}
                </tbody>
              </table>
            </div>
          </section>
        </div>
      )}

      {subTab === 'security' && (
        <form onSubmit={handleSavePolicy} className="tg-glass-card p-4 space-y-4">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <fieldset className="space-y-3 border border-line rounded-lg p-3 min-w-0">
              <legend className="font-semibold text-fg px-1.5">出站目标白名单</legend>
              <label className="flex items-center gap-2 text-fg cursor-pointer">
                <Switch
                  name="allowlist_enabled"
                  label="启用目标白名单"
                  disabled={!isAdmin}
                  checked={policy.allowlist_enabled}
                  onChange={(v) => setPolicy({ ...policy, allowlist_enabled: v })}
                />
                <span>启用域名 / 网段白名单</span>
              </label>
              <textarea
                rows={5}
                name="allowed_targets"
                aria-label="允许的目标"
                disabled={!isAdmin}
                autoCorrect="off"
                autoCapitalize="none"
                spellCheck={false}
                value={allowedTargetsText}
                onChange={(e) => setAllowedTargetsText(e.target.value)}
                placeholder={'*.staging.example.com\n10.20.0.0/16\napi.test.local:8443'}
                className={`${inputCls} font-mono p-2.5`}
              />
            </fieldset>

            <fieldset className="space-y-3 border border-line rounded-lg p-3 min-w-0">
              <legend className="font-semibold text-fg px-1.5">成员压测上限</legend>
              <div className="grid grid-cols-2 gap-3">
                <Field label="最高并发 VUs">
                  <input
                    type="number"
                    name="member_max_vus"
                    min={0}
                    disabled={!isAdmin}
                    value={optionalNumber(policy.member_max_vus)}
                    placeholder="不限"
                    onChange={(e) => setPolicy({ ...policy, member_max_vus: parseOptional(e.target.value) })}
                    className={`${inputCls} tg-tabular font-mono`}
                  />
                </Field>
                <Field label="最高目标 RPS">
                  <input
                    type="number"
                    name="member_max_rps"
                    min={0}
                    step="any"
                    disabled={!isAdmin}
                    value={policy.member_max_rps && policy.member_max_rps > 0 ? String(policy.member_max_rps) : ''}
                    placeholder="不限"
                    onChange={(e) =>
                      setPolicy({
                        ...policy,
                        member_max_rps: e.target.value.trim() === '' ? 0 : Math.max(0, Number(e.target.value) || 0),
                      })
                    }
                    className={`${inputCls} tg-tabular font-mono`}
                  />
                </Field>
              </div>
              <label className="flex items-center gap-2 pt-1 text-fg cursor-pointer">
                <Switch
                  name="scripts_disabled"
                  label="禁止脚本步骤"
                  disabled={!isAdmin}
                  checked={Boolean(policy.scripts_disabled)}
                  onChange={(v) => setPolicy({ ...policy, scripts_disabled: v })}
                />
                <span>禁止场景脚本步骤</span>
              </label>
              <div className="pt-2 border-t border-line grid grid-cols-1 sm:grid-cols-2 gap-3 items-end">
                <label className="flex items-center gap-2 text-fg cursor-pointer pb-1.5">
                  <Switch
                    name="require_totp"
                    label="强制全员绑定 2FA"
                    disabled={!isAdmin}
                    checked={Boolean(policy.require_totp)}
                    onChange={(v) => setPolicy({ ...policy, require_totp: v })}
                  />
                  <span className="inline-flex items-center gap-1">
                    <ShieldCheck className="w-3.5 h-3.5 text-emerald-600" /> 强制全员开启 2FA
                  </span>
                </label>
                <Field label="访问令牌最长有效期(天)">
                  <input
                    type="number"
                    name="token_max_ttl_days"
                    min={0}
                    disabled={!isAdmin}
                    value={optionalNumber(policy.token_max_ttl_days)}
                    placeholder="不限（允许长期）"
                    onChange={(e) => setPolicy({ ...policy, token_max_ttl_days: parseOptional(e.target.value) })}
                    className={`${inputCls} tg-tabular font-mono`}
                  />
                </Field>
              </div>
            </fieldset>
          </div>

          <fieldset className="border border-line rounded-lg p-3 space-y-3 min-w-0">
            <legend className="font-semibold text-fg px-1.5 inline-flex items-center gap-1.5">
              <Bot className="w-4 h-4 text-sky-600" /> MCP 安全护栏与调用治理
            </legend>

            <div className="flex flex-wrap items-end gap-x-5 gap-y-3">
              <label className="flex items-center gap-2 text-fg cursor-pointer pb-1.5">
                <Switch
                  name="mcp_enabled"
                  label="启用 MCP"
                  disabled={!isAdmin}
                  checked={!policy.mcp_disabled}
                  onChange={(v) => setPolicy({ ...policy, mcp_disabled: !v })}
                />
                <span className="font-medium">启用 MCP 服务</span>
              </label>
              <label className="flex items-center gap-2 text-fg cursor-pointer pb-1.5">
                <Switch
                  name="mcp_allow_load_runs"
                  label="允许 MCP 发起压测"
                  disabled={!isAdmin || Boolean(policy.mcp_disabled)}
                  checked={!policy.mcp_disable_load_runs}
                  onChange={(v) => setPolicy({ ...policy, mcp_disable_load_runs: !v })}
                />
                <span>允许发起压测</span>
              </label>
              <label className="flex items-center gap-2 text-fg cursor-pointer pb-1.5">
                <Switch
                  name="mcp_allow_resources"
                  label="开放 Resources"
                  disabled={!isAdmin || Boolean(policy.mcp_disabled)}
                  checked={!policy.mcp_disable_resources}
                  onChange={(v) => setPolicy({ ...policy, mcp_disable_resources: !v })}
                />
                <span>开放 Resources ({catalog?.resources?.length ?? 4})</span>
              </label>
              <label className="flex items-center gap-2 text-fg cursor-pointer pb-1.5">
                <Switch
                  name="mcp_allow_prompts"
                  label="开放 Prompts"
                  disabled={!isAdmin || Boolean(policy.mcp_disabled)}
                  checked={!policy.mcp_disable_prompts}
                  onChange={(v) => setPolicy({ ...policy, mcp_disable_prompts: !v })}
                />
                <span>开放 Prompts ({catalog?.prompts?.length ?? 4})</span>
              </label>
              <Field label="最高并发 VUs" className="w-28">
                <input
                  type="number"
                  name="mcp_max_vus"
                  min={0}
                  disabled={!isAdmin}
                  value={optionalNumber(policy.mcp_max_vus)}
                  placeholder="默认 200"
                  onChange={(e) => setPolicy({ ...policy, mcp_max_vus: parseOptional(e.target.value) })}
                  className={`${inputCls} tg-tabular font-mono`}
                />
              </Field>
              <Field label="最长时长(秒)" className="w-28">
                <input
                  type="number"
                  name="mcp_max_duration_sec"
                  min={0}
                  disabled={!isAdmin}
                  value={optionalNumber(policy.mcp_max_duration_sec)}
                  placeholder="默认 300"
                  onChange={(e) => setPolicy({ ...policy, mcp_max_duration_sec: parseOptional(e.target.value) })}
                  className={`${inputCls} tg-tabular font-mono`}
                />
              </Field>
              <Field label="单令牌限流(次/分)" className="w-32">
                <input
                  type="number"
                  name="mcp_rate_per_min"
                  min={0}
                  disabled={!isAdmin}
                  value={optionalNumber(policy.mcp_rate_per_min)}
                  placeholder="默认 120"
                  onChange={(e) => setPolicy({ ...policy, mcp_rate_per_min: parseOptional(e.target.value) })}
                  className={`${inputCls} tg-tabular font-mono`}
                />
              </Field>
              {catalog && (
                <span className="tg-tabular text-fg-subtle pb-2 font-mono text-[11px]">
                  生效 {catalog.effective_max_vus} VUs · {catalog.effective_max_duration_sec}s ·{' '}
                  {catalog.effective_rate_per_min || 120}/m
                </span>
              )}
            </div>

            <div className="flex flex-wrap items-center justify-between gap-2 pt-3 border-t border-line">
              <span className="text-fg-muted">
                工具开关{' '}
                <span className="tg-tabular font-mono text-fg font-semibold">
                  <RollingNumber value={enabledCount} />/{tools.length}
                </span>
              </span>
              {isAdmin && tools.length > 0 && (
                <div className="flex items-center gap-1.5">
                  <button
                    type="button"
                    onClick={() => setPolicy({ ...policy, mcp_disabled_tools: [] })}
                    className={ghostBtn}
                  >
                    <ListChecks className="w-3.5 h-3.5" /> 全部启用
                  </button>
                  <button
                    type="button"
                    onClick={() =>
                      setPolicy({
                        ...policy,
                        mcp_disabled_tools: tools
                          .filter((t) => t.category !== 'read')
                          .map((t) => t.name)
                          .sort(),
                      })
                    }
                    className={ghostBtn}
                  >
                    <ScanEye className="w-3.5 h-3.5" /> 仅限只读
                  </button>
                </div>
              )}
            </div>

            {!catalog ? (
              <p className="text-fg-subtle py-4 text-center">加载工具目录…</p>
            ) : (
              <div className="space-y-3">
                <div
                  className={`grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-3 transition-opacity ${
                    policy.mcp_disabled ? 'opacity-50' : ''
                  }`}
                >
                  {CATEGORY_META.map((cat) => {
                    const group = tools.filter((t) => t.category === cat.id)
                    const on = group.filter((t) => !disabledTools.has(t.name)).length
                    const Icon = cat.icon
                    return (
                      <section key={cat.id} aria-label={cat.label} className="rounded-lg border border-line bg-surface-subtle min-w-0">
                        <h3 className="flex items-center justify-between gap-2 px-3 py-2 border-b border-line font-semibold text-fg">
                          <span className="inline-flex items-center gap-1.5">
                            <Icon className={`w-3.5 h-3.5 ${cat.tone}`} /> {cat.label}
                          </span>
                          <span className="tg-tabular font-mono text-[11px] font-normal text-fg-subtle">
                            {on}/{group.length}
                          </span>
                        </h3>
                        <ul className="divide-y divide-line">
                          {group.length === 0 && <li className="px-3 py-2 text-fg-subtle">—</li>}
                          {group.map((t) => (
                            <ToolRow
                              key={t.name}
                              tool={t}
                              enabled={!disabledTools.has(t.name)}
                              disabled={!isAdmin}
                              onChange={(v) => setToolEnabled(t.name, v)}
                            />
                          ))}
                        </ul>
                      </section>
                    )
                  })}
                </div>

                {((catalog.resources?.length || 0) > 0 || (catalog.prompts?.length || 0) > 0 || mcpActivity.length > 0) && (
                  <div className="grid grid-cols-1 lg:grid-cols-3 gap-3 pt-2">
                    <div className="rounded-xl border border-line bg-surface-subtle p-3 space-y-1.5">
                      <div className="font-semibold text-fg">只读 Resources ({catalog.resources?.length || 0})</div>
                      {(catalog.resources || []).map((r) => (
                        <div key={r.uri_template} className="px-2 py-1 rounded bg-surface border border-line">
                          <div className="font-mono text-[11px] text-tidal truncate">{r.uri_template}</div>
                          <div className="text-[10px] text-fg-subtle truncate">{r.name} · {r.description}</div>
                        </div>
                      ))}
                    </div>
                    <div className="rounded-xl border border-line bg-surface-subtle p-3 space-y-1.5">
                      <div className="font-semibold text-fg">内置 Prompts ({catalog.prompts?.length || 0})</div>
                      {(catalog.prompts || []).map((p) => (
                        <div key={p.name} className="px-2 py-1 rounded bg-surface border border-line">
                          <div className="font-mono text-[11px] font-semibold text-fg truncate">/{p.name}</div>
                          <div className="text-[10px] text-fg-subtle truncate">{p.title} · {p.description}</div>
                        </div>
                      ))}
                    </div>
                    <div className="rounded-xl border border-line bg-surface-subtle p-3 space-y-1.5">
                      <div className="font-semibold text-fg flex items-center gap-1.5">
                        <Activity className="w-3.5 h-3.5 text-tidal" /> 最近 MCP 调用动态 ({mcpActivity.length})
                      </div>
                      <div className="max-h-40 overflow-y-auto tg-scroller space-y-1">
                        {mcpActivity.length === 0 ? (
                          <div className="text-fg-subtle py-4 text-center">暂无 MCP 调用</div>
                        ) : (
                          mcpActivity.map((act) => {
                            const fail = /fail|denied|error/.test(act.action)
                            return (
                              <div
                                key={act.id}
                                className={`flex items-center justify-between gap-2 px-2 py-1 rounded border text-[11px] font-mono ${
                                  fail ? 'bg-rose-50 border-rose-200 text-rose-800' : 'bg-surface border-line text-fg'
                                }`}
                              >
                                <span className="truncate">{act.username}: {act.action}</span>
                                <span className="shrink-0 text-fg-subtle tg-tabular">
                                  {new Date(act.created_at).toLocaleTimeString()}
                                </span>
                              </div>
                            )
                          })
                        )}
                      </div>
                    </div>
                  </div>
                )}
              </div>
            )}
          </fieldset>

          {isAdmin && (
            <div className="flex justify-end pt-3 border-t border-line">
              <button
                type="submit"
                disabled={savingPolicy}
                className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
              >
                <Save className="w-3.5 h-3.5" /> 保存安全策略
              </button>
            </div>
          )}
        </form>
      )}

      {subTab === 'metrics' && (
        <div className="space-y-3">
          <section aria-label="Prometheus" className="tg-glass-card p-4 space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="flex items-center gap-2 min-w-0">
                <span className="font-semibold text-fg">Prometheus</span>
                <code className="font-mono text-fg-muted truncate">{api.prometheusMetricsUrl()}</code>
              </h3>
              <div className="flex items-center gap-1.5">
                <button type="button" onClick={() => void copyPromYaml()} className={ghostBtn}>
                  {copied === 'prom-yaml' ? <Check className="w-3.5 h-3.5 text-emerald-600" /> : <Copy className="w-3.5 h-3.5" />}
                  复制 prometheus.yml 片段
                </button>
                <button type="button" disabled={loadingProm} onClick={() => void handleLoadProm()} className={ghostBtn}>
                  <RefreshCw className={`w-3.5 h-3.5 ${loadingProm ? 'animate-spin' : ''}`} /> 拉取快照
                </button>
              </div>
            </div>
            <pre className="tg-code-surface p-3 rounded-lg font-mono text-[11px] text-sky-100 overflow-auto tg-scroller-dark whitespace-pre">
              {promYaml}
            </pre>
            {promPreview && (
              <pre className="tg-code-surface p-3 rounded-lg text-emerald-300 font-mono text-[11px] overflow-auto tg-scroller-dark max-h-56 whitespace-pre-wrap">
                {promPreview}
              </pre>
            )}
          </section>

          <form onSubmit={handleSaveSink} className="tg-glass-card p-4 space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="font-semibold text-fg">实时指标外推</h3>
              <label className="inline-flex items-center gap-2 cursor-pointer text-fg">
                <Switch
                  name="sink_enabled"
                  label="启用实时外推"
                  disabled={!isAdmin}
                  checked={sink.enabled}
                  onChange={(v) => setSink({ ...sink, enabled: v })}
                />
                <span>启用</span>
              </label>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
              <Field label="类型">
                <select
                  name="sink_type"
                  disabled={!isAdmin}
                  value={sink.type || 'influxdb'}
                  onChange={(e) => setSink({ ...sink, type: e.target.value })}
                  className={`tg-select ${inputCls}`}
                >
                  <option value="influxdb">InfluxDB Line Protocol</option>
                  <option value="prometheus_pushgateway">Prometheus Pushgateway</option>
                </select>
              </Field>
              <Field label="写入 URL" className="md:col-span-2">
                <input
                  type="text"
                  name="sink_url"
                  disabled={!isAdmin}
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={sink.url}
                  onChange={(e) => setSink({ ...sink, url: e.target.value })}
                  placeholder={sink.type === 'prometheus_pushgateway' ? 'http://127.0.0.1:9091' : 'http://127.0.0.1:8086'}
                  className={`${inputCls} font-mono`}
                />
              </Field>
              {sink.type === 'influxdb' ? (
                <>
                  <Field label="Org · v2">
                    <input
                      type="text"
                      name="sink_org"
                      disabled={!isAdmin}
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      value={sink.org || ''}
                      onChange={(e) => setSink({ ...sink, org: e.target.value || undefined })}
                      className={`${inputCls} font-mono`}
                    />
                  </Field>
                  <Field label="Bucket / Database">
                    <input
                      type="text"
                      name="sink_bucket"
                      disabled={!isAdmin}
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      value={sink.bucket || sink.database || ''}
                      onChange={(e) => setSink({ ...sink, bucket: e.target.value || undefined })}
                      placeholder="tidegate"
                      className={`${inputCls} font-mono`}
                    />
                  </Field>
                  <Field label="Token">
                    <SecretInput
                      name="sink_token"
                      disabled={!isAdmin}
                      value={sink.token || ''}
                      show={showSinkToken}
                      onToggle={() => setShowSinkToken((s) => !s)}
                      onChange={(v) => setSink({ ...sink, token: v || undefined })}
                      placeholder="留空保留已存储"
                    />
                  </Field>
                </>
              ) : (
                <>
                  <Field label="Job">
                    <input
                      type="text"
                      name="sink_job"
                      disabled={!isAdmin}
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      value={sink.job || ''}
                      onChange={(e) => setSink({ ...sink, job: e.target.value || undefined })}
                      placeholder="tidegate"
                      className={`${inputCls} font-mono`}
                    />
                  </Field>
                  <Field label="Basic Auth 用户名">
                    <input
                      type="text"
                      name="sink_username"
                      disabled={!isAdmin}
                      autoComplete="off"
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      value={sink.username || ''}
                      onChange={(e) => setSink({ ...sink, username: e.target.value || undefined })}
                      placeholder="可选"
                      className={`${inputCls} font-mono`}
                    />
                  </Field>
                  <Field label="Token / 密码">
                    <SecretInput
                      name="sink_token"
                      disabled={!isAdmin}
                      value={sink.token || ''}
                      show={showSinkToken}
                      onToggle={() => setShowSinkToken((s) => !s)}
                      onChange={(v) => setSink({ ...sink, token: v || undefined })}
                      placeholder="可选"
                    />
                  </Field>
                </>
              )}
            </div>

            {isAdmin && (
              <div className="flex justify-end gap-2 pt-3 border-t border-line">
                <button type="button" disabled={testingSink} onClick={() => void handleTestSink()} className={ghostBtn}>
                  <Activity className="w-3.5 h-3.5" /> {testingSink ? '测试中…' : '测试连接'}
                </button>
                <button
                  type="submit"
                  disabled={savingSink}
                  className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
                >
                  <Save className="w-3.5 h-3.5" /> 保存外推配置
                </button>
              </div>
            )}
          </form>
        </div>
      )}

      {subTab === 'audit' && isAdmin && (
        <section aria-label="审计日志" className="tg-glass-card">
          <div className="p-3 border-b border-line space-y-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <search className="relative flex-1 min-w-[220px] max-w-md">
                <Search className="w-3.5 h-3.5 text-fg-subtle absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
                <input
                  type="search"
                  name="audit_query"
                  aria-label="检索审计日志"
                  autoComplete="off"
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={auditQuery}
                  onChange={(e) => setAuditQuery(e.target.value)}
                  placeholder="用户名 / 动作 / 资源 / IP"
                  className={`${inputCls} pl-8`}
                />
              </search>
              <span className="tg-tabular text-fg-subtle font-mono text-[11px]">
                <RollingNumber value={visibleLogs.length} /> / {auditLogs.length}
              </span>
              <div className="ml-auto flex items-center gap-1.5">
                <button type="button" disabled={auditLoading} onClick={() => void loadAudit()} className={ghostBtn}>
                  <RefreshCw className={`w-3.5 h-3.5 ${auditLoading ? 'animate-spin' : ''}`} /> 刷新
                </button>
                <button type="button" disabled={visibleLogs.length === 0} onClick={() => exportAudit('csv')} className={ghostBtn}>
                  <FileSpreadsheet className="w-3.5 h-3.5" /> 导出 CSV
                </button>
                <button type="button" disabled={visibleLogs.length === 0} onClick={() => exportAudit('json')} className={ghostBtn}>
                  <FileJson className="w-3.5 h-3.5" /> 导出 JSON
                </button>
              </div>
            </div>
            <div role="radiogroup" aria-label="动作筛选" className="flex flex-wrap items-center gap-1.5">
              {AUDIT_FILTERS.map((f) => {
                const active = auditFilter === f.id
                return (
                  <button
                    key={f.id}
                    type="button"
                    role="radio"
                    aria-checked={active}
                    onClick={() => setAuditFilter(f.id)}
                    className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full border text-[11px] transition-colors ${
                      active
                        ? 'bg-sky-600 border-sky-600 text-white'
                        : 'border-line-strong text-fg-muted hover:bg-surface-hover'
                    } ${f.mono ? 'font-mono' : 'font-medium'}`}
                  >
                    {f.label}
                    <span className={`tg-tabular font-mono ${active ? 'text-[#e0f2fe]' : 'text-fg-subtle'}`}>
                      {filterCounts[f.id] ?? 0}
                    </span>
                  </button>
                )
              })}
            </div>
          </div>

          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse">
              <thead className="border-b border-line text-fg-subtle">
                <tr>
                  <th className="w-7" aria-label="展开" />
                  <th className="px-3 py-2 font-medium">时间</th>
                  <th className="px-3 py-2 font-medium">操作人</th>
                  <th className="px-3 py-2 font-medium">动作</th>
                  <th className="px-3 py-2 font-medium">资源</th>
                  <th className="px-3 py-2 font-medium">来源 IP</th>
                  <th className="px-3 py-2 font-medium">详情</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line font-mono text-[11px]">
                {visibleLogs.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="px-3 py-8 text-center text-fg-subtle font-sans text-xs">
                      {auditLoading ? '加载中…' : '无匹配记录'}
                    </td>
                  </tr>
                ) : (
                  visibleLogs.map((log) => {
                    const open = expandedLog.has(log.id)
                    const detailText = log.detail == null ? '' : JSON.stringify(log.detail)
                    const detailObj =
                      log.detail && typeof log.detail === 'object' ? (log.detail as Record<string, unknown>) : null
                    const hasDetailErr = Boolean(detailObj?.is_error || detailObj?.error)
                    const isMCP = log.action.startsWith('mcp.')
                    const isFail = hasDetailErr || /fail|denied|error/.test(log.action)
                    return (
                      <Fragment key={log.id}>
                        <tr
                          onClick={() => toggleLog(log.id)}
                          className={`cursor-pointer ${open ? 'bg-surface-subtle' : 'hover:bg-surface-hover'}`}
                        >
                          <td className="pl-2">
                            <button
                              type="button"
                              aria-expanded={open}
                              aria-label={open ? '收起详情' : '展开详情'}
                              onClick={(e) => {
                                e.stopPropagation()
                                toggleLog(log.id)
                              }}
                              className="p-0.5 rounded text-fg-subtle hover:text-fg"
                            >
                              <ChevronRight className={`w-3.5 h-3.5 transition-transform ${open ? 'rotate-90' : ''}`} />
                            </button>
                          </td>
                          <td className="px-3 py-2 text-fg-subtle whitespace-nowrap tg-tabular">
                            {new Date(log.created_at).toLocaleString()}
                          </td>
                          <td className="px-3 py-2 font-sans whitespace-nowrap">
                            <span className="font-semibold text-fg">{log.username || '-'}</span>{' '}
                            <span className="text-fg-subtle">{log.role || ''}</span>
                          </td>
                          <td className="px-3 py-2 whitespace-nowrap">
                            <span
                              className={`px-1.5 py-0.5 rounded border font-semibold ${
                                isFail
                                  ? 'bg-rose-50 border-rose-200 text-rose-800'
                                  : isMCP
                                    ? 'bg-cyan-50 border-cyan-200 text-cyan-800'
                                    : 'bg-surface-subtle border-line text-fg'
                              }`}
                            >
                              {log.action}
                            </span>
                          </td>
                          <td className="px-3 py-2 text-fg-muted">
                            {log.resource}
                            {log.resource_id ? `:${log.resource_id}` : ''}
                          </td>
                          <td className="px-3 py-2 text-fg-subtle tg-tabular">{log.remote_addr || '-'}</td>
                          <td className="px-3 py-2 max-w-xs truncate text-fg-subtle">{detailText || '-'}</td>
                        </tr>
                        {open && (
                          <tr className="bg-surface-subtle">
                            <td />
                            <td colSpan={6} className="px-3 pb-3 pt-1">
                              <div className="flex flex-wrap gap-x-4 gap-y-1 mb-2 text-fg-subtle tg-tabular">
                                <span>
                                  id <span className="text-fg">{log.id}</span>
                                </span>
                                <span>
                                  user_id <span className="text-fg">{log.user_id}</span>
                                </span>
                                <span>
                                  时间 <span className="text-fg">{log.created_at}</span>
                                </span>
                              </div>
                              <pre className="tg-code-surface p-3 rounded-lg text-sky-100 overflow-auto tg-scroller-dark max-h-80 whitespace-pre-wrap break-all cursor-text select-text" onClick={(e) => e.stopPropagation()}>
                                {log.detail == null ? 'null' : JSON.stringify(log.detail, null, 2)}
                              </pre>
                            </td>
                          </tr>
                        )}
                      </Fragment>
                    )
                  })
                )}
              </tbody>
            </table>
          </div>

          {auditHasMore && (
            <div className="flex justify-center p-3 border-t border-line">
              <button type="button" disabled={auditLoading} onClick={() => void loadAudit(true)} className={ghostBtn}>
                <RefreshCw className={`w-3.5 h-3.5 ${auditLoading ? 'animate-spin' : ''}`} /> 加载更多
              </button>
            </div>
          )}
        </section>
      )}
    </div>
  )
}

function ToolRow({
  tool,
  enabled,
  disabled,
  onChange,
}: {
  tool: MCPToolCatalogItem
  enabled: boolean
  disabled: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <li>
      <label className={`flex items-start gap-2.5 px-3 py-2 ${disabled ? '' : 'cursor-pointer hover:bg-surface-hover'}`} title={tool.description}>
        <span className="pt-0.5">
          <Switch checked={enabled} disabled={disabled} label={tool.title || tool.name} onChange={onChange} />
        </span>
        <span className="min-w-0 flex-1">
          <span className={`block font-medium truncate ${enabled ? 'text-fg' : 'text-fg-subtle line-through decoration-1'}`}>
            {tool.title || tool.name}
          </span>
          <span className="flex flex-wrap items-center gap-1 mt-0.5">
            <code className="font-mono text-[11px] text-fg-muted truncate max-w-full">{tool.name}</code>
            <span className="px-1 rounded border border-line bg-surface font-mono text-[10px] text-fg-muted">{tool.scope}</span>
            {tool.destructive && (
              <span
                className="inline-flex items-center gap-0.5 px-1 rounded border border-rose-200 bg-rose-50 text-rose-800 text-[10px]"
                title="破坏性操作"
              >
                <TriangleAlert className="w-2.5 h-2.5" /> 破坏性
              </span>
            )}
            {tool.open_world && (
              <span
                className="inline-flex items-center gap-0.5 px-1 rounded border border-amber-200 bg-amber-50 text-amber-800 text-[10px]"
                title="会访问外部目标"
              >
                <Globe className="w-2.5 h-2.5" /> 外部
              </span>
            )}
          </span>
        </span>
      </label>
    </li>
  )
}

function SecretInput({
  name,
  value,
  onChange,
  show,
  onToggle,
  disabled,
  placeholder,
}: {
  name: string
  value: string
  onChange: (v: string) => void
  show: boolean
  onToggle: () => void
  disabled?: boolean
  placeholder?: string
}) {
  return (
    <div className="flex items-stretch rounded border border-line-strong bg-surface focus-within:border-sky-500">
      <input
        type={show ? 'text' : 'password'}
        name={name}
        disabled={disabled}
        autoComplete="new-password"
        autoCorrect="off"
        autoCapitalize="none"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="flex-1 min-w-0 px-2.5 py-1.5 bg-transparent text-fg font-mono outline-none disabled:text-fg-subtle"
      />
      <button
        type="button"
        onClick={onToggle}
        aria-label={show ? '隐藏' : '显示'}
        aria-pressed={show}
        className="px-2 text-fg-subtle hover:text-fg border-l border-line"
      >
        {show ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
      </button>
    </div>
  )
}
