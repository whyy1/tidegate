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
  Gauge,
  Globe,
  KeyRound,
  ListChecks,
  Lock,
  PenLine,
  Plus,
  RefreshCw,
  RotateCcw,
  Save,
  ScanEye,
  Search,
  Trash2,
  TriangleAlert,
  Users,
  X,
} from 'lucide-react'
import { api, APIError } from '../lib/api'
import { copyText, downloadText, fileStamp, toCSV } from '../lib/download'
import type {
  AuditEntry,
  MCPCatalog,
  MCPToolCatalogItem,
  MCPToolCategory,
  MetricsSinkConfig,
  SecurityPolicy,
  User,
  UserRole,
} from '../lib/types'

interface Props {
  currentUser: User
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
}

const inputCls = 'w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface text-fg disabled:bg-surface-subtle disabled:text-fg-subtle'
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

export function GovernanceView({ currentUser, onToast }: Props) {
  const isAdmin = currentUser.role === 'admin'
  const [subTab, setSubTab] = useState<SubTab>(isAdmin ? 'users' : 'security')
  const confirm = useArmedConfirm()
  const { copied, mark: markCopied } = useCopied()

  // Users
  const [users, setUsers] = useState<User[]>([])
  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newRole, setNewRole] = useState<UserRole>('member')
  const [savingUser, setSavingUser] = useState(false)
  const [resetUid, setResetUid] = useState(0)
  const [resetPw, setResetPw] = useState('')
  const [resettingPw, setResettingPw] = useState(false)

  // Security policy + MCP catalog
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
      setUsers(await api.listUsers())
    } catch (err) {
      onToast('error', errMsg(err, '加载用户列表失败'))
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
      setCatalog(await api.getMCPCatalog())
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
      const page = await api.listAuditLogs({ limit: AUDIT_PAGE, before_id: lastId })
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

  /* ---------------- Users ---------------- */

  const handleCreateUser = async (e: FormEvent) => {
    e.preventDefault()
    if (!newUsername.trim() || newPassword.length < MIN_PASSWORD) {
      onToast('error', `请输入用户名和至少 ${MIN_PASSWORD} 个字符的初始密码`)
      return
    }
    setSavingUser(true)
    try {
      await api.createUser({ username: newUsername.trim(), password: newPassword, role: newRole })
      setNewUsername('')
      setNewPassword('')
      await loadUsers()
      onToast('success', '已创建用户')
    } catch (err) {
      onToast('error', errMsg(err, '创建用户失败'))
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

  return (
    <div className="flex-1 overflow-y-auto tg-scroller bg-canvas p-5 space-y-4 text-xs">
      <div className="tg-telemetry-strip rounded-xl px-3 py-2.5 shadow-xs flex flex-wrap items-center justify-between gap-3">
        <div role="tablist" aria-label="系统治理页签" className="tg-tablist items-center gap-1 bg-white/5 p-1 rounded-lg">
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
                  onClick={() => setSubTab(t.id)}
                  className={`inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md font-medium whitespace-nowrap transition-colors ${
                    selected
                      ? 'bg-white/15 text-white font-semibold ring-1 ring-white/25'
                      : 'text-slate-300 hover:bg-white/10 hover:text-white'
                  }`}
                >
                  <Icon className={`w-3.5 h-3.5 ${selected ? 'text-sky-300' : ''}`} /> {t.label}
                </button>
              )
            })}
        </div>
        <span className="text-[11px] text-slate-400 font-mono">
          {currentUser.username} · {currentUser.role}
        </span>
      </div>

      {subTab === 'users' && isAdmin && (
        <section aria-label="用户与角色" className="bg-surface rounded-xl border border-line shadow-xs">
          <form
            onSubmit={handleCreateUser}
            className="grid grid-cols-1 md:grid-cols-12 gap-2 items-end p-3 border-b border-line bg-surface-subtle rounded-t-xl"
          >
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
            <Field label="角色" className="md:col-span-3">
              <select
                name="new_role"
                value={newRole}
                onChange={(e) => setNewRole(e.target.value as UserRole)}
                className={inputCls}
              >
                <option value="member">member · 编辑与压测</option>
                <option value="viewer">viewer · 只读</option>
                <option value="admin">admin · 管理员</option>
              </select>
            </Field>
            <div className="md:col-span-2">
              <button
                type="submit"
                disabled={savingUser}
                className="w-full inline-flex items-center justify-center gap-1 px-3 py-1.5 rounded bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
              >
                <Plus className="w-3.5 h-3.5" /> 新建用户
              </button>
            </div>
          </form>

          <div className="overflow-x-auto">
            <table className="w-full text-left border-collapse">
              <thead className="border-b border-line text-fg-subtle">
                <tr>
                  <th className="px-3 py-2 font-medium">ID</th>
                  <th className="px-3 py-2 font-medium">用户名</th>
                  <th className="px-3 py-2 font-medium">角色</th>
                  <th className="px-3 py-2 font-medium">2FA</th>
                  <th className="px-3 py-2 font-medium">创建时间</th>
                  <th className="px-3 py-2 font-medium text-right">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {users.map((u) => {
                  const isSelf = u.id === currentUser.id
                  const resetOpen = resetUid === u.id
                  const delArmed = confirm.armed === `del-${u.id}`
                  const totpArmed = confirm.armed === `totp-${u.id}`
                  const pwArmed = confirm.armed === `pw-${u.id}`
                  return (
                    <Fragment key={u.id}>
                      <tr className={resetOpen ? 'bg-surface-subtle' : 'hover:bg-surface-hover'}>
                        <td className="px-3 py-2 font-mono tg-tabular text-fg-subtle">{u.id}</td>
                        <td className="px-3 py-2 font-semibold text-fg">
                          <span className="font-mono">{u.username}</span>
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
                          ) : (
                            <select
                              value={u.role}
                              aria-label={`${u.username} 的角色`}
                              onChange={async (e) => {
                                const role = e.target.value as UserRole
                                try {
                                  await api.patchUser(u.id, { role })
                                  await loadUsers()
                                  onToast('success', `已将 ${u.username} 设为 ${role}`)
                                } catch (err) {
                                  onToast('error', errMsg(err, '更新角色失败'))
                                }
                              }}
                              className="px-2 py-1 border border-line-strong rounded bg-surface text-fg font-mono"
                            >
                              <option value="admin">admin</option>
                              <option value="member">member</option>
                              <option value="viewer">viewer</option>
                            </select>
                          )}
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
                          {new Date(u.created_at).toLocaleString()}
                        </td>
                        <td className="px-3 py-2 text-right">
                          <div className="inline-flex items-center gap-1.5 justify-end">
                            <button
                              type="button"
                              onClick={() => openReset(u.id)}
                              aria-expanded={resetOpen}
                              className={`inline-flex items-center gap-1 px-2 py-1 rounded border ${
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
                                className={`inline-flex items-center gap-1 px-2 py-1 rounded border ${
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
                                    await loadUsers()
                                    onToast('success', `已删除用户「${u.username}」`)
                                  } catch (err) {
                                    onToast('error', errMsg(err, '删除用户失败'))
                                  }
                                }}
                                onBlur={confirm.reset}
                                className={`inline-flex items-center gap-1 px-2 py-1 rounded border ${
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
                      {resetOpen && (
                        <tr className="bg-surface-subtle">
                          <td colSpan={6} className="px-3 pb-3 pt-1">
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
                                className={`inline-flex items-center gap-1 px-3 py-1.5 rounded font-medium disabled:opacity-50 ${
                                  pwArmed
                                    ? 'bg-rose-600 hover:bg-rose-solid text-white'
                                    : 'bg-primary hover:bg-primary-hover text-white'
                                }`}
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
      )}

      {subTab === 'security' && (
        <form onSubmit={handleSavePolicy} className="bg-surface rounded-xl border border-line shadow-xs p-4 space-y-4">
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
            </fieldset>
          </div>

          <fieldset className="border border-line rounded-lg p-3 space-y-3 min-w-0">
            <legend className="font-semibold text-fg px-1.5 inline-flex items-center gap-1.5">
              <Bot className="w-4 h-4 text-sky-600" /> MCP 安全护栏
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
                <span className="font-medium">启用 MCP</span>
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
              <Field label="最高并发 VUs" className="w-32">
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
              <Field label="最长压测秒数" className="w-32">
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
              {catalog && (
                <span className="tg-tabular text-fg-subtle pb-2 font-mono text-[11px]">
                  生效 {catalog.effective_max_vus} VUs · {catalog.effective_max_duration_sec}s
                </span>
              )}
            </div>

            <div className="flex flex-wrap items-center justify-between gap-2 pt-3 border-t border-line">
              <span className="text-fg-muted">
                工具开关{' '}
                <span className="tg-tabular font-mono text-fg font-semibold">
                  {enabledCount}/{tools.length}
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
        <div className="space-y-4">
          <section aria-label="Prometheus" className="bg-surface rounded-xl border border-line p-4 shadow-xs space-y-3">
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

          <form onSubmit={handleSaveSink} className="bg-surface rounded-xl border border-line p-4 shadow-xs space-y-3">
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
                  className={inputCls}
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
        <section aria-label="审计日志" className="bg-surface rounded-xl border border-line shadow-xs">
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
                {visibleLogs.length} / {auditLogs.length}
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
                    <span className={`tg-tabular font-mono ${active ? 'text-sky-100' : 'text-fg-subtle'}`}>
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
                    const isMCP = log.action.startsWith('mcp.')
                    const isFail = /fail|denied|error/.test(log.action)
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
