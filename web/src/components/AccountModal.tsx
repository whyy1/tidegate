import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type FormEvent } from 'react'
import {
  Activity,
  ArrowRight,
  Ban,
  Bot,
  Check,
  Copy,
  Download,
  Eye,
  EyeOff,
  Key,
  KeyRound,
  Laptop,
  LogOut,
  Monitor,
  Moon,
  Palette,
  Plus,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
  Sun,
  Trash2,
  X,
} from 'lucide-react'
import { api, APIError } from '../lib/api'
import { copyText, downloadText, fileStamp } from '../lib/download'
import { qrToDataURL } from '../lib/qrcode'
import { getGlobalRoleLabel, ROLE_BADGE_TONE } from '../lib/roles'
import { setPreference, useColorScheme, type ResolvedScheme, type SchemePreference } from '../lib/theme'
import { useNativeDialog } from '../lib/useNativeDialog'
import type {
  APIToken,
  AuditEntry,
  MCPPromptCatalogItem,
  MCPResourceCatalogItem,
  MCPToolCatalogItem,
  MCPToolCategory,
  Project,
  SessionItem,
  TOTPEnrollment,
  User,
  UserRole,
} from '../lib/types'
import { GlassSelect } from './GlassSelect'
import { LiquidCapsule } from './LiquidCapsule'

export type AccountModalTab = 'sessions' | 'tokens' | 'totp' | 'mcp' | 'password' | 'appearance'

interface Props {
  open: boolean
  initialTab?: AccountModalTab
  user: User
  projects?: Project[]
  onUserUpdated?: (user: User) => void
  onOpenGovernance?: () => void
  onClose: () => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
  onLoggedOut: () => void
}

// ---------------------------------------------------------------------------
// Shared styles

const inputCls =
  'px-2.5 py-1.5 rounded-[10px] border border-line-strong bg-surface text-fg placeholder:text-fg-subtle focus:border-tidal focus:outline-none'
const btnSecondary = 'tg-btn-secondary'
const btnPrimary = 'tg-btn-primary'
const badge = 'inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full text-[11px] leading-4 border'

const SCOPES: { id: string; desc: string; adminOnly?: boolean }[] = [
  { id: 'read', desc: '只读查询' },
  { id: 'write', desc: '资产编排' },
  { id: 'debug', desc: '接口调试 · 集合回归' },
  { id: 'run', desc: '压测启停' },
  { id: 'mcp', desc: 'MCP 接入' },
  { id: 'admin', desc: '系统管理', adminOnly: true },
]

const CATEGORY_LABEL: Record<MCPToolCategory, string> = {
  read: '只读探查',
  write: '资产编排',
  debug: '调试回归',
  run: '压测控制',
}
const CATEGORY_ORDER: MCPToolCategory[] = ['read', 'write', 'debug', 'run']

type ToolState = 'on' | 'off' | 'role' | 'disabled'

/** Mirrors the server: empty scopes inherit everything; otherwise `mcp` gates the endpoint and the tool's own scope gates the tool. */
function scopeUnlocks(tool: MCPToolCatalogItem, scopes: string[]): boolean {
  if (scopes.length === 0) return true
  if (!scopes.includes('mcp')) return false
  return !tool.scope || scopes.includes(tool.scope)
}

function toolState(tool: MCPToolCatalogItem, scopes: string[], role: UserRole, whitelist: string[] | null): ToolState {
  if (!scopeUnlocks(tool, scopes)) return 'off'
  if (whitelist && !whitelist.includes(tool.name)) return 'off'
  if (!tool.globally_enabled) return 'disabled'
  if (role === 'viewer' && !tool.read_only) return 'role'
  return 'on'
}

const isExpired = (t: APIToken) => !!t.expires_at && new Date(t.expires_at).getTime() < Date.now()
const hasMCP = (t: APIToken) => !isExpired(t) && (!t.scopes || t.scopes.length === 0 || t.scopes.includes('mcp'))

// ---------------------------------------------------------------------------
// Small hooks

/** Copy with a ~1.5s "copied" icon swap keyed by `id`. */
function useCopyFlag(onFail: () => void) {
  const [copied, setCopied] = useState<string | null>(null)
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const copy = useCallback(
    async (id: string, text: string) => {
      const ok = await copyText(text)
      if (!ok) {
        onFail()
        return
      }
      setCopied(id)
      window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => setCopied(null), 1500)
    },
    [onFail]
  )
  return { copied, copy }
}

/** Inline two-step confirm: first click arms for 3s, second click runs. */
function useArm() {
  const [armed, setArmed] = useState<string | null>(null)
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const reset = useCallback(() => {
    window.clearTimeout(timer.current)
    setArmed(null)
  }, [])
  const trigger = useCallback(
    (id: string, run: () => void) => {
      if (armed === id) {
        reset()
        run()
        return
      }
      setArmed(id)
      window.clearTimeout(timer.current)
      timer.current = window.setTimeout(() => setArmed(null), 3000)
    },
    [armed, reset]
  )
  return { armed, trigger, reset }
}

const darkMQ = () => window.matchMedia('(prefers-color-scheme: dark)')
function useSystemScheme(): ResolvedScheme {
  return useSyncExternalStore(
    (cb) => {
      const mq = darkMQ()
      mq.addEventListener('change', cb)
      return () => mq.removeEventListener('change', cb)
    },
    () => (darkMQ().matches ? 'dark' : 'light'),
    () => 'light'
  )
}

function CopyIcon({ done, className = 'w-3.5 h-3.5' }: { done: boolean; className?: string }) {
  return done ? <Check className={`${className} text-emerald-600`} /> : <Copy className={className} />
}

function summarizeUA(ua: string): string {
  const s = ua || ''
  let os = '未知系统'
  if (/Windows NT/i.test(s)) os = 'Windows'
  else if (/Mac OS X|Macintosh/i.test(s)) os = 'macOS'
  else if (/Android/i.test(s)) os = 'Android'
  else if (/iPhone|iPad/i.test(s)) os = 'iOS'
  else if (/Linux/i.test(s)) os = 'Linux'

  let browser = '浏览器 / 客户端'
  if (/Edg\//i.test(s)) browser = 'Microsoft Edge'
  else if (/Chrome\//i.test(s) && !/Edg\//i.test(s)) browser = 'Chrome / Chromium'
  else if (/Firefox\//i.test(s)) browser = 'Firefox'
  else if (/Safari\//i.test(s) && !/Chrome\//i.test(s)) browser = 'Safari'
  else if (/curl/i.test(s)) browser = 'cURL'
  return `${os} · ${browser}`
}

// ---------------------------------------------------------------------------

export function AccountModal({
  open,
  initialTab = 'sessions',
  user,
  projects = [],
  onUserUpdated,
  onOpenGovernance,
  onClose,
  onToast,
  onLoggedOut,
}: Props) {
  const dialogRef = useNativeDialog(open, onClose)
  const [tab, setTab] = useState<AccountModalTab>(initialTab)
  const tabsRef = useRef<HTMLDivElement | null>(null)

  const [sessions, setSessions] = useState<SessionItem[]>([])
  const [loading, setLoading] = useState(false)
  const [revokingOthers, setRevokingOthers] = useState(false)

  const [tokens, setTokens] = useState<APIToken[]>([])
  const [loadingTokens, setLoadingTokens] = useState(false)
  const [catalog, setCatalog] = useState<MCPToolCatalogItem[] | null>(null)
  const [resources, setResources] = useState<MCPResourceCatalogItem[]>([])
  const [prompts, setPrompts] = useState<MCPPromptCatalogItem[]>([])
  const [effectiveMaxVUs, setEffectiveMaxVUs] = useState(0)
  const [effectiveMaxDurSec, setEffectiveMaxDurSec] = useState(0)
  const [effectiveRatePerMin, setEffectiveRatePerMin] = useState(0)
  const [catalogError, setCatalogError] = useState(false)
  const [tokName, setTokName] = useState('')
  const [tokRole, setTokRole] = useState<UserRole>(user.role)
  const [tokTTLDays, setTokTTLDays] = useState<number>(30)
  const [tokCustomDays, setTokCustomDays] = useState<string>('')
  const [tokMcpMaxVUs, setTokMcpMaxVUs] = useState<string>('')
  const [tokMcpMaxDur, setTokMcpMaxDur] = useState<string>('')
  const [tokRatePerMin, setTokRatePerMin] = useState<string>('')
  const [tokScopes, setTokScopes] = useState<string[]>([])
  const [tokProjectIDs, setTokProjectIDs] = useState<string[]>([])
  const [restrictTools, setRestrictTools] = useState(false)
  const [toolPick, setToolPick] = useState<string[]>([])
  const [creatingTok, setCreatingTok] = useState(false)
  const [newToken, setNewToken] = useState<{ token: APIToken; plaintext: string } | null>(null)
  /** Plaintexts minted while this dialog is open; never persisted. */
  const [plaintexts, setPlaintexts] = useState<Record<string, string>>({})

  const [mcpClient, setMcpClient] = useState<'standard' | 'cursor' | 'stdio'>('standard')
  const [mcpTokenId, setMcpTokenId] = useState('')
  const [mcpQuickRW, setMcpQuickRW] = useState(false)
  const [probingMcp, setProbingMcp] = useState(false)
  const [mcpProbeRes, setMcpProbeRes] = useState<{
    ok: boolean
    latencyMs: number
    serverName?: string
    toolCount?: number
    error?: string
  } | null>(null)
  const [mcpActivity, setMcpActivity] = useState<AuditEntry[]>([])

  const [enrollment, setEnrollment] = useState<TOTPEnrollment | null>(null)
  const [totpConfirmCode, setTotpConfirmCode] = useState('')
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([])
  const [disablePwd, setDisablePwd] = useState('')
  const [disableCode, setDisableCode] = useState('')
  const [totpBusy, setTotpBusy] = useState(false)

  const [curPwd, setCurPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')
  const [confirmPwd, setConfirmPwd] = useState('')
  const [showPwd, setShowPwd] = useState(false)
  const [savingPwd, setSavingPwd] = useState(false)

  const scheme = useColorScheme()
  const systemScheme = useSystemScheme()
  const { copied, copy } = useCopyFlag(useCallback(() => onToast('info', '复制失败，请手动选择文本复制'), [onToast]))
  const { armed, trigger, reset: disarm } = useArm()
  const wasOpenRef = useRef(false)
  const sessionsReqRef = useRef(0)
  const tokensReqRef = useRef(0)
  const [revokingId, setRevokingId] = useState('')

  useEffect(() => {
    if (!open) {
      wasOpenRef.current = false
      return
    }
    if (wasOpenRef.current) return
    wasOpenRef.current = true
    setTab(initialTab)
    setEnrollment(null)
    setTotpConfirmCode('')
    setRecoveryCodes([])
    setDisablePwd('')
    setDisableCode('')
    setNewToken(null)
    setPlaintexts({})
    setMcpTokenId('')
    setMcpProbeRes(null)
    setCatalog(null)
    setCatalogError(false)
    setRestrictTools(false)
    setToolPick([])
    setCurPwd('')
    setNewPwd('')
    setConfirmPwd('')
    setShowPwd(false)
    setTokName('')
    setTokScopes([])
    setTokProjectIDs([])
    setTokCustomDays('')
    setTokMcpMaxVUs('')
    setTokMcpMaxDur('')
    setTokRatePerMin('')
    setRevokingId('')
  }, [open, initialTab])

  useEffect(() => {
    setTokRole(user.role)
  }, [user.role])

  const loadSessions = useCallback(async () => {
    const seq = ++sessionsReqRef.current
    setLoading(true)
    try {
      const res = await api.listSessions()
      if (seq !== sessionsReqRef.current) return
      setSessions(res.sessions || [])
    } catch (err) {
      if (seq !== sessionsReqRef.current) return
      onToast('error', err instanceof APIError ? err.message : '读取会话列表失败')
    } finally {
      if (seq === sessionsReqRef.current) setLoading(false)
    }
  }, [onToast])

  const loadTokens = useCallback(async () => {
    const seq = ++tokensReqRef.current
    setLoadingTokens(true)
    try {
      const list = await api.listAPITokens()
      if (seq !== tokensReqRef.current) return
      setTokens(list)
    } catch (err) {
      if (seq !== tokensReqRef.current) return
      onToast('error', err instanceof APIError ? err.message : '读取 API 令牌失败')
    } finally {
      if (seq === tokensReqRef.current) setLoadingTokens(false)
    }
  }, [onToast])

  useEffect(() => {
    if (!open) return
    disarm()
    if (tab === 'sessions') void loadSessions()
    if (tab === 'tokens' || tab === 'mcp') {
      void loadTokens()
      void api.mcpActivity(12).then(setMcpActivity).catch(() => undefined)
    }
  }, [open, tab])

  useEffect(() => {
    if (!open || (tab !== 'tokens' && tab !== 'mcp') || catalog || catalogError) return
    let cancelled = false
    api
      .getMCPCatalog()
      .then((c) => {
        if (cancelled) return
        setCatalog(c.tools || [])
        setResources(c.resources || [])
        setPrompts(c.prompts || [])
        setEffectiveMaxVUs(c.effective_max_vus || 0)
        setEffectiveMaxDurSec(c.effective_max_duration_sec || 0)
        setEffectiveRatePerMin(c.effective_rate_per_min || 0)
      })
      .catch(() => !cancelled && setCatalogError(true))
    return () => {
      cancelled = true
    }
  }, [open, tab, catalog, catalogError])

  // ----- derived -----

  const allowedTokenRoles: UserRole[] =
    user.role === 'admin' ? ['viewer', 'member', 'admin'] : user.role === 'member' ? ['viewer', 'member'] : ['viewer']
  const scopeOptions = SCOPES.filter((s) => !s.adminOnly || user.role === 'admin')

  const sortedCatalog = useMemo(
    () =>
      (catalog || [])
        .slice()
        .sort((a, b) => CATEGORY_ORDER.indexOf(a.category) - CATEGORY_ORDER.indexOf(b.category)),
    [catalog]
  )
  const scopeUnlocked = sortedCatalog.filter((t) => scopeUnlocks(t, tokScopes)).map((t) => t.name)
  const whitelist = restrictTools ? toolPick.filter((n) => scopeUnlocked.includes(n)) : null
  const toolStates = sortedCatalog.map((t) => ({ tool: t, state: toolState(t, tokScopes, tokRole, whitelist) }))
  const usableCount = toolStates.filter((t) => t.state === 'on').length
  const mcpBlocked = tokScopes.length > 0 && !tokScopes.includes('mcp')

  const mcpTokens = useMemo(() => tokens.filter(hasMCP), [tokens])
  const selectedMcpToken = mcpTokens.find((t) => t.id === mcpTokenId) || null

  // Default MCP token: prefer one minted in this dialog (plaintext known), then the newest.
  useEffect(() => {
    if (selectedMcpToken || mcpTokens.length === 0) return
    const minted = mcpTokens.find((t) => plaintexts[t.id])
    const newest = mcpTokens.slice().sort((a, b) => b.created_at.localeCompare(a.created_at))[0]
    setMcpTokenId((minted || newest).id)
  }, [mcpTokens, selectedMcpToken, plaintexts])

  const qrSrc = useMemo(() => {
    if (!enrollment) return ''
    try {
      return qrToDataURL(enrollment.otpauth_url, { scale: 4 })
    } catch {
      return ''
    }
  }, [enrollment])

  const origin = typeof window !== 'undefined' ? window.location.origin : ''
  const mcpPlaintext = selectedMcpToken ? plaintexts[selectedMcpToken.id] : undefined
  const tokenValue = mcpPlaintext || (selectedMcpToken ? `<${selectedMcpToken.prefix}… 令牌明文>` : '<tg_ 令牌明文>')
  const mcpEndpoint = `${origin}/api/v1/mcp`
  const mcpSnippet = JSON.stringify(
    {
      mcpServers: {
        tidegate:
          mcpClient === 'standard'
            ? { url: mcpEndpoint, headers: { Authorization: `Bearer ${tokenValue}` } }
            : mcpClient === 'cursor'
              ? { serverUrl: mcpEndpoint, headers: { Authorization: `Bearer ${tokenValue}` } }
              : {
                  command: 'tidegate',
                  args: ['mcp', '--url', origin],
                  env: { TIDEGATE_TOKEN: tokenValue },
                },
      },
    },
    null,
    2
  )
  const enabledToolCount = (catalog || []).filter((t) => t.globally_enabled).length
  const projectNameMap = useMemo(() => new Map(projects.map((p) => [p.id, p.name])), [projects])

  const uniqueTokenName = (base: string) => {
    const taken = new Set(tokens.map((t) => t.name))
    if (!taken.has(base)) return base
    for (let i = 2; ; i++) if (!taken.has(`${base} ${i}`)) return `${base} ${i}`
  }

  // ----- actions -----

  const toggleTokScope = (scope: string) =>
    setTokScopes((prev) => (prev.includes(scope) ? prev.filter((x) => x !== scope) : [...prev, scope]))

  const toggleTokProject = (pid: string) =>
    setTokProjectIDs((prev) => (prev.includes(pid) ? prev.filter((x) => x !== pid) : [...prev, pid]))

  const toggleRestrict = () => {
    if (!restrictTools) setToolPick(scopeUnlocked)
    setRestrictTools((v) => !v)
  }

  const togglePick = (name: string) =>
    setToolPick((prev) => (prev.includes(name) ? prev.filter((x) => x !== name) : [...prev, name]))

  const rememberMinted = (token: APIToken, plaintext: string) =>
    setPlaintexts((prev) => ({ ...prev, [token.id]: plaintext }))

  const handleCreateToken = async (e: FormEvent) => {
    e.preventDefault()
    const name = tokName.trim()
    if (!name) {
      onToast('error', '请输入令牌名称')
      return
    }
    let mcpTools: string[] | undefined
    if (whitelist) {
      if (whitelist.length === 0) {
        onToast('error', '限定 MCP 工具时至少勾选一个')
        return
      }
      if (whitelist.length < scopeUnlocked.length) mcpTools = whitelist
    }
    setCreatingTok(true)
    const ttlDaysResolved = tokTTLDays === -1 ? Math.max(1, Number(tokCustomDays) || 30) : tokTTLDays
    const maxVUs = Number(tokMcpMaxVUs) > 0 ? Number(tokMcpMaxVUs) : undefined
    const maxDur = Number(tokMcpMaxDur) > 0 ? Number(tokMcpMaxDur) : undefined
    const rateMin = Number(tokRatePerMin) > 0 ? Number(tokRatePerMin) : undefined
    try {
      const res = await api.createAPIToken({
        name,
        role: tokRole,
        scopes: tokScopes.length > 0 ? tokScopes : undefined,
        project_ids: tokProjectIDs.length > 0 ? tokProjectIDs : undefined,
        mcp_tools: mcpTools,
        mcp_max_vus: maxVUs,
        mcp_max_duration_sec: maxDur,
        rate_per_min: rateMin,
        ttl_days: ttlDaysResolved > 0 ? ttlDaysResolved : undefined,
      })
      setNewToken(res)
      rememberMinted(res.token, res.plaintext)
      setTokName('')
      setTokScopes([])
      setTokProjectIDs([])
      setRestrictTools(false)
      setToolPick([])
      setTokCustomDays('')
      setTokMcpMaxVUs('')
      setTokMcpMaxDur('')
      setTokRatePerMin('')
      await loadTokens()
      onToast('success', `已生成令牌「${res.token.name}」`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '生成 API 令牌失败')
    } finally {
      setCreatingTok(false)
    }
  }

  const handleProbeMCP = async () => {
    setProbingMcp(true)
    try {
      const res = await api.mcpProbe(mcpPlaintext)
      setMcpProbeRes({
        ok: res.ok,
        latencyMs: res.latencyMs,
        serverName: res.serverName,
        toolCount: res.toolCount,
      })
      onToast('success', `MCP 握手成功：发现 ${res.toolCount} 个可用工具（${res.latencyMs} ms）`)
    } catch (err) {
      const msg = err instanceof APIError ? err.message : 'MCP 自检连接失败'
      setMcpProbeRes({ ok: false, latencyMs: 0, error: msg })
      onToast('error', msg)
    } finally {
      setProbingMcp(false)
    }
  }

  const handleRevokeOtherSessions = async () => {
    setRevokingOthers(true)
    try {
      const res = await api.revokeOtherSessions()
      onToast('success', `已注销其他设备会话 ${res.revoked} 个`)
      await loadSessions()
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '注销其他设备失败')
    } finally {
      setRevokingOthers(false)
    }
  }

  const handleQuickMCPToken = async () => {
    const readwrite = mcpQuickRW && user.role !== 'viewer'
    setCreatingTok(true)
    try {
      const res = await api.createAPIToken({
        name: uniqueTokenName(readwrite ? 'MCP 读写' : 'MCP 只读'),
        role: readwrite ? 'member' : 'viewer',
        scopes: readwrite ? ['mcp', 'read', 'write', 'debug', 'run'] : ['mcp', 'read'],
        ttl_days: 30,
      })
      rememberMinted(res.token, res.plaintext)
      setMcpTokenId(res.token.id)
      await loadTokens()
      onToast('success', `已生成令牌「${res.token.name}」并填入配置`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '生成 MCP 令牌失败')
    } finally {
      setCreatingTok(false)
    }
  }

  const handleRevokeToken = async (tok: APIToken) => {
    setRevokingId(`token:${tok.id}`)
    try {
      await api.revokeAPIToken(tok.id)
      onToast('success', `已吊销令牌「${tok.name}」`)
      if (newToken?.token.id === tok.id) setNewToken(null)
      await loadTokens()
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '吊销 API 令牌失败')
    } finally {
      setRevokingId((cur) => (cur === `token:${tok.id}` ? '' : cur))
    }
  }

  const handleStartEnrollTOTP = async () => {
    setTotpBusy(true)
    try {
      const res = await api.enrollTOTP()
      setEnrollment(res)
      setTotpConfirmCode('')
      setRecoveryCodes([])
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '初始化 2FA 密钥失败')
    } finally {
      setTotpBusy(false)
    }
  }

  const handleConfirmTOTP = async (e: FormEvent) => {
    e.preventDefault()
    const code = totpConfirmCode.trim()
    if (!/^\d{6}$/.test(code)) {
      onToast('error', '请输入 6 位数字验证码')
      return
    }
    setTotpBusy(true)
    try {
      const res = await api.confirmTOTP(code)
      setRecoveryCodes(res.recovery_codes || [])
      setEnrollment(null)
      setTotpConfirmCode('')
      onUserUpdated?.({ ...user, totp_enabled: true })
      onToast('success', '已启用双因素认证，请保存恢复码')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '绑定验证器失败')
    } finally {
      setTotpBusy(false)
    }
  }

  const handleDisableTOTP = async (e: FormEvent) => {
    e.preventDefault()
    if (!disablePwd || !disableCode.trim()) {
      onToast('error', '请输入当前密码与验证码或恢复码')
      return
    }
    setTotpBusy(true)
    try {
      await api.disableTOTP({ password: disablePwd, code: disableCode.trim() })
      setDisablePwd('')
      setDisableCode('')
      setRecoveryCodes([])
      onUserUpdated?.({ ...user, totp_enabled: false })
      onToast('success', '已关闭双因素认证')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '关闭 2FA 失败')
    } finally {
      setTotpBusy(false)
    }
  }

  const downloadRecovery = () => {
    const body = [`Tidegate 恢复码 · ${user.username} · ${new Date().toLocaleString()}`, '', ...recoveryCodes, ''].join('\r\n')
    downloadText(`tidegate-recovery-codes-${user.username}-${fileStamp()}.txt`, body)
  }

  const handleRevokeSession = async (s: SessionItem) => {
    setRevokingId(`session:${s.id}`)
    try {
      await api.revokeSession(s.id)
      onToast('success', s.current ? '已注销当前会话' : '已踢出该设备会话')
      if (s.current) {
        onLoggedOut()
        onClose()
      } else {
        await loadSessions()
      }
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '注销会话失败')
    } finally {
      setRevokingId((cur) => (cur === `session:${s.id}` ? '' : cur))
    }
  }

  const handleChangePwd = async (e: FormEvent) => {
    e.preventDefault()
    if (newPwd.length < 10) {
      onToast('error', '新密码长度不能少于 10 个字符')
      return
    }
    if (newPwd !== confirmPwd) {
      onToast('error', '两次输入的新密码不一致')
      return
    }
    setSavingPwd(true)
    try {
      await api.changePassword({ current_password: curPwd, new_password: newPwd })
      onToast('success', '密码已修改，其他设备会话已失效')
      setCurPwd('')
      setNewPwd('')
      setConfirmPwd('')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '修改密码失败')
    } finally {
      setSavingPwd(false)
    }
  }

  const handleLogout = async () => {
    try {
      await api.logout()
    } catch {
      // session may already be gone
    }
    onLoggedOut()
    onClose()
  }

  // ----- render helpers -----

  const dangerBtn = (id: string, label: string, confirmLabel: string, run: () => void) => {
    const isArmed = armed === id
    const isBusy = revokingId === id
    return (
      <button
        type="button"
        disabled={isBusy}
        onClick={() => trigger(id, run)}
        onBlur={() => isArmed && disarm()}
        className={`shrink-0 inline-flex items-center gap-1 px-2.5 py-1 rounded-md border font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed ${
          isArmed
            ? 'border-rose-600 bg-rose-600 hover:bg-rose-solid text-white'
            : 'border-rose-200 text-rose-700 hover:bg-rose-50'
        }`}
      >
        <Trash2 className="w-3.5 h-3.5" /> {isBusy ? '处理中…' : isArmed ? confirmLabel : label}
      </button>
    )
  }

  const tabs = [
    { id: 'sessions' as const, label: '会话', icon: Laptop },
    { id: 'tokens' as const, label: 'API 令牌', icon: Key },
    { id: 'mcp' as const, label: 'MCP 接入', icon: Bot },
    { id: 'totp' as const, label: '双因素认证', icon: ShieldCheck },
    { id: 'password' as const, label: '密码', icon: KeyRound },
    { id: 'appearance' as const, label: '外观', icon: Palette },
  ]

  return (
    <dialog
      ref={dialogRef}
      {...({ closedby: 'closerequest' } as Record<string, string>)}
      aria-label="账号与令牌"
      className="tg-dialog w-[min(52rem,calc(100vw-2rem))] h-[min(44rem,88dvh)]"
    >
      {open && (
        <>
          <header className="tg-dialog-header">
            <div className="flex items-center gap-2.5 min-w-0">
              <h2 className="text-sm font-semibold text-fg shrink-0">账号与令牌</h2>
              <span className="text-xs font-mono text-fg-muted truncate">{user.username}</span>
              <span className={`px-2 py-0.5 rounded-full border text-[11px] font-medium ${ROLE_BADGE_TONE[user.role]}`}>
                {getGlobalRoleLabel(user.role)}
              </span>
              {user.totp_enabled && (
                <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full bg-emerald-500/12 text-emerald-700 text-[11px] font-medium">
                  <ShieldCheck className="w-3 h-3" /> 2FA
                </span>
              )}
            </div>
            <div className="flex items-center gap-2 shrink-0">
              {user.role === 'admin' && onOpenGovernance && (
                <button
                  type="button"
                  onClick={() => {
                    onClose()
                    onOpenGovernance()
                  }}
                  className="tg-btn-secondary !py-1 !px-2.5 !text-[11px]"
                >
                  <span>团队账号与项目权限</span>
                  <ArrowRight className="w-3 h-3 text-tidal" />
                </button>
              )}
              <button
                type="button"
                onClick={onClose}
                aria-label="关闭"
                className="p-1 text-fg-subtle hover:text-fg rounded-lg hover:bg-surface-hover"
              >
                <X className="w-4 h-4" />
              </button>
            </div>
          </header>

          <div
            ref={tabsRef}
            role="tablist"
            aria-label="账号与安全"
            className="relative tg-tablist px-5 border-b border-line bg-surface-subtle/60 gap-2 py-1.5 text-xs font-medium shrink-0"
          >
            <LiquidCapsule containerRef={tabsRef} activeId={tab} orientation="horizontal" className="rounded-lg" />
            {tabs.map((t) => {
              const Icon = t.icon
              const selected = tab === t.id
              return (
                <button
                  key={t.id}
                  data-liq-id={t.id}
                  type="button"
                  role="tab"
                  aria-selected={selected}
                  onClick={() => setTab(t.id)}
                  className={`relative z-10 inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg font-semibold transition-colors whitespace-nowrap ${
                    selected ? 'text-tidal' : 'text-fg-muted hover:text-fg'
                  }`}
                >
                  <Icon className="w-3.5 h-3.5" />
                  <span>{t.label}</span>
                </button>
              )
            })}
          </div>

          <div role="tabpanel" className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 text-xs text-fg">
            {/* ================= Sessions ================= */}
            {tab === 'sessions' &&
              (loading && sessions.length === 0 ? (
                <p className="text-fg-subtle">正在加载会话…</p>
              ) : sessions.length === 0 ? (
                <p className="text-fg-subtle">暂无活动会话</p>
              ) : (
                <div className="space-y-3">
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-fg-muted">
                      共 <strong className="text-fg tg-tabular">{sessions.length}</strong> 台已登录设备
                    </span>
                    {sessions.length > 1 && (
                      <button
                        type="button"
                        disabled={revokingOthers}
                        onClick={() => void handleRevokeOtherSessions()}
                        className="tg-btn-danger !py-1 !px-2.5"
                      >
                        <LogOut className="w-3 h-3" /> {revokingOthers ? '处理中…' : '注销其他设备'}
                      </button>
                    )}
                  </div>
                  <ul className="rounded-xl border border-line bg-surface divide-y divide-line overflow-hidden">
                    {sessions.map((s) => (
                      <li key={s.id} className="flex items-center justify-between gap-3 px-3.5 py-2.5">
                        <div className="min-w-0">
                          <div className="flex items-center gap-2 font-medium text-fg">
                            <span>{summarizeUA(s.user_agent)}</span>
                            <span className="font-mono text-[11px] text-fg-muted">· {s.remote_addr || '本机'}</span>
                            {s.current && (
                              <span className={`${badge} bg-emerald-50 text-emerald-700 border-emerald-200`}>当前设备</span>
                            )}
                          </div>
                          <p className="text-[11px] text-fg-subtle truncate mt-0.5" title={s.user_agent}>
                            {s.user_agent || '未知 User-Agent'}
                          </p>
                          <p className="tg-tabular text-[11px] text-fg-subtle mt-0.5">
                            活跃 {new Date(s.last_seen_at).toLocaleString()} · 过期 {new Date(s.expires_at).toLocaleString()}
                          </p>
                        </div>
                        {dangerBtn(`session:${s.id}`, s.current ? '退出' : '注销', s.current ? '确认退出？' : '确认注销？', () =>
                          void handleRevokeSession(s)
                        )}
                      </li>
                    ))}
                  </ul>
                </div>
              ))}

            {/* ================= API tokens ================= */}
            {tab === 'tokens' && (
              <div className="space-y-4">
                <form onSubmit={handleCreateToken} className="rounded-xl border border-line bg-surface-subtle">
                  <div className="flex flex-wrap items-center gap-2 p-3">
                    <input
                      type="text"
                      name="token-name"
                      aria-label="令牌名称"
                      autoComplete="off"
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      value={tokName}
                      onChange={(e) => setTokName(e.target.value)}
                      placeholder="自动化流水线 / MCP 客户端"
                      className={`${inputCls} flex-1 min-w-[12rem]`}
                    />
                    <GlassSelect
                      ariaLabel="令牌角色"
                      value={tokRole}
                      onChange={(v) => setTokRole(v as UserRole)}
                      className="min-w-[118px]"
                      options={allowedTokenRoles.map((r) => ({
                        value: r,
                        label: getGlobalRoleLabel(r, true),
                      }))}
                    />
                    <GlassSelect
                      ariaLabel="令牌有效期"
                      value={String(tokTTLDays)}
                      onChange={(v) => setTokTTLDays(Number(v))}
                      className="min-w-[110px]"
                      options={[
                        { value: '7', label: '7 天' },
                        { value: '30', label: '30 天' },
                        { value: '90', label: '90 天' },
                        { value: '365', label: '365 天' },
                        { value: '-1', label: '自定义天数' },
                        { value: '0', label: '长期有效' },
                      ]}
                    />
                    {tokTTLDays === -1 && (
                      <input
                        type="number"
                        min={1}
                        max={3650}
                        aria-label="自定义有效天数"
                        value={tokCustomDays}
                        onChange={(e) => setTokCustomDays(e.target.value)}
                        placeholder="天数"
                        className={`${inputCls} w-20 font-mono tg-tabular`}
                      />
                    )}
                    <button type="submit" disabled={creatingTok} className={btnPrimary}>
                      <Plus className="w-3.5 h-3.5" /> 创建
                    </button>
                  </div>

                  <div className="px-3 pb-3 grid grid-cols-1 sm:grid-cols-3 gap-2">
                    <label className="block">
                      <span className="block text-[11px] text-fg-muted mb-1">单令牌 MCP 最大 VU</span>
                      <input
                        type="number"
                        min={0}
                        value={tokMcpMaxVUs}
                        onChange={(e) => setTokMcpMaxVUs(e.target.value)}
                        placeholder={effectiveMaxVUs > 0 ? `全局上限 ${effectiveMaxVUs}` : '继承全局'}
                        className={`${inputCls} w-full font-mono tg-tabular`}
                      />
                    </label>
                    <label className="block">
                      <span className="block text-[11px] text-fg-muted mb-1">单令牌 MCP 最长时长(秒)</span>
                      <input
                        type="number"
                        min={0}
                        value={tokMcpMaxDur}
                        onChange={(e) => setTokMcpMaxDur(e.target.value)}
                        placeholder={effectiveMaxDurSec > 0 ? `全局上限 ${effectiveMaxDurSec}s` : '继承全局'}
                        className={`${inputCls} w-full font-mono tg-tabular`}
                      />
                    </label>
                    <label className="block">
                      <span className="block text-[11px] text-fg-muted mb-1">单令牌调用速率(次/分)</span>
                      <input
                        type="number"
                        min={0}
                        value={tokRatePerMin}
                        onChange={(e) => setTokRatePerMin(e.target.value)}
                        placeholder={effectiveRatePerMin > 0 ? `全局上限 ${effectiveRatePerMin}` : '继承全局'}
                        className={`${inputCls} w-full font-mono tg-tabular`}
                      />
                    </label>
                  </div>

                  <fieldset className="px-3 pb-3 pt-2.5 border-t border-line">
                    <legend className="sr-only">Scope</legend>
                    <div className="flex items-center justify-between gap-2 mb-1.5">
                      <span className="text-[11px] font-medium text-fg-muted">Scope</span>
                      {tokScopes.length === 0 && (
                        <span className={`${badge} bg-surface text-fg-muted border-line`}>未勾选 · 继承角色全部权限</span>
                      )}
                    </div>
                    <div className="flex flex-wrap gap-1.5">
                      {scopeOptions.map((sc) => {
                        const on = tokScopes.includes(sc.id)
                        return (
                          <label
                            key={sc.id}
                            className={`inline-flex items-center gap-1.5 px-2 py-1 rounded-md border cursor-pointer transition-colors ${
                              on ? 'border-sky-300 bg-sky-50' : 'border-line bg-surface hover:border-line-strong'
                            }`}
                          >
                            <input type="checkbox" checked={on} onChange={() => toggleTokScope(sc.id)} />
                            <span className="font-mono text-[11px] text-fg">{sc.id}</span>
                            <span className="text-[11px] text-fg-muted">{sc.desc}</span>
                          </label>
                        )
                      })}
                    </div>
                  </fieldset>

                  <fieldset className="px-3 pb-3 pt-2.5 border-t border-line">
                    <legend className="sr-only">MCP 工具</legend>
                    <div className="flex flex-wrap items-center justify-between gap-2 mb-1.5">
                      <div className="flex items-center gap-2">
                        <span className="text-[11px] font-medium text-fg-muted">MCP 工具</span>
                        {catalog && (
                          <span className="tg-tabular text-[11px] text-fg-subtle">
                            {usableCount}/{catalog.length}
                          </span>
                        )}
                        {mcpBlocked && (
                          <span className={`${badge} bg-amber-50 text-amber-800 border-amber-200`}>
                            <Ban className="w-3 h-3" /> 缺少 mcp Scope
                          </span>
                        )}
                      </div>
                      {catalog && catalog.length > 0 && !mcpBlocked && (
                        <button
                          type="button"
                          aria-pressed={restrictTools}
                          onClick={toggleRestrict}
                          className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-md border text-[11px] font-medium ${
                            restrictTools
                              ? 'border-sky-300 bg-sky-50 text-sky-700'
                              : 'border-line bg-surface text-fg-muted hover:text-fg'
                          }`}
                        >
                          <SlidersHorizontal className="w-3 h-3" /> 限定 MCP 工具
                        </button>
                      )}
                    </div>
                    {catalogError ? (
                      <p className="text-fg-subtle">工具目录读取失败</p>
                    ) : !catalog ? (
                      <p className="text-fg-subtle">正在读取工具目录…</p>
                    ) : (
                      <div className="grid sm:grid-cols-2 gap-x-4 gap-y-2">
                        {CATEGORY_ORDER.map((cat) => {
                          const items = toolStates.filter((t) => t.tool.category === cat)
                          if (items.length === 0) return null
                          return (
                            <div key={cat} className="min-w-0">
                              <div className="text-[11px] text-fg-subtle mb-1">
                                {CATEGORY_LABEL[cat]} <span className="tg-tabular">{items.length}</span>
                              </div>
                              <div className="flex flex-wrap gap-1">
                                {items.map(({ tool, state }) => {
                                  const pickable = restrictTools && scopeUnlocks(tool, tokScopes)
                                  const tone =
                                    state === 'on'
                                      ? 'border-sky-200 bg-sky-50 text-sky-800'
                                      : state === 'role'
                                        ? 'border-dashed border-amber-300 bg-amber-50 text-amber-800'
                                        : state === 'disabled'
                                          ? 'border-line bg-surface text-fg-subtle line-through'
                                          : 'border-dashed border-line bg-transparent text-fg-subtle'
                                  const hint =
                                    state === 'disabled'
                                      ? '已被管理员全局禁用'
                                      : state === 'role'
                                        ? 'viewer 角色仅在项目授权后可调用'
                                        : `scope: ${tool.scope || '—'}`
                                  const body = (
                                    <>
                                      {pickable && (
                                        <input
                                          type="checkbox"
                                          className="w-3 h-3"
                                          checked={toolPick.includes(tool.name)}
                                          onChange={() => togglePick(tool.name)}
                                        />
                                      )}
                                      {state === 'disabled' && <Ban className="w-3 h-3 no-underline" />}
                                      <span className="font-mono">{tool.name}</span>
                                    </>
                                  )
                                  return pickable ? (
                                    <label
                                      key={tool.name}
                                      title={`${tool.title} · ${hint}`}
                                      className={`${badge} ${tone} cursor-pointer`}
                                    >
                                      {body}
                                    </label>
                                  ) : (
                                    <span key={tool.name} title={`${tool.title} · ${hint}`} className={`${badge} ${tone}`}>
                                      {body}
                                    </span>
                                  )
                                })}
                              </div>
                            </div>
                          )
                        })}
                      </div>
                    )}
                  </fieldset>

                  {projects.length > 0 && (
                    <fieldset className="px-3 pb-3 pt-2.5 border-t border-line">
                      <legend className="sr-only">限定项目</legend>
                      <div className="flex items-center justify-between gap-2 mb-1.5">
                        <span className="text-[11px] font-medium text-fg-muted">项目</span>
                        {tokProjectIDs.length === 0 && (
                          <span className={`${badge} bg-surface text-fg-muted border-line`}>未勾选 · 全部项目</span>
                        )}
                      </div>
                      <div className="flex flex-wrap gap-1.5">
                        {projects.map((p) => {
                          const on = tokProjectIDs.includes(p.id)
                          return (
                            <label
                              key={p.id}
                              className={`inline-flex items-center gap-1.5 px-2 py-1 rounded-md border cursor-pointer ${
                                on ? 'border-sky-300 bg-sky-50' : 'border-line bg-surface hover:border-line-strong'
                              }`}
                            >
                              <input type="checkbox" checked={on} onChange={() => toggleTokProject(p.id)} />
                              <span className="text-[11px] text-fg">{p.name}</span>
                            </label>
                          )
                        })}
                      </div>
                    </fieldset>
                  )}
                </form>

                {newToken && (
                  <div className="rounded-lg border border-emerald-300 bg-emerald-50 p-3 space-y-2">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="font-semibold text-emerald-800">
                        {newToken.token.name}
                        <span className="ml-2 font-normal text-emerald-700">明文仅显示一次</span>
                      </span>
                      <div className="flex items-center gap-1.5">
                        {hasMCP(newToken.token) && (
                          <button
                            type="button"
                            onClick={() => {
                              setMcpTokenId(newToken.token.id)
                              setTab('mcp')
                            }}
                            className={btnSecondary}
                          >
                            MCP 配置 <ArrowRight className="w-3.5 h-3.5" />
                          </button>
                        )}
                        <button
                          type="button"
                          onClick={() => void copy('new-token', newToken.plaintext)}
                          className="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-md bg-emerald-600 hover:bg-emerald-solid text-white font-medium"
                        >
                          {copied === 'new-token' ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />} 复制
                        </button>
                      </div>
                    </div>
                    <code className="block font-mono text-[11px] break-all bg-surface border border-emerald-200 rounded px-2 py-1.5 text-fg select-all">
                      {newToken.plaintext}
                    </code>
                  </div>
                )}

                {loadingTokens && tokens.length === 0 ? (
                  <p className="text-fg-subtle">正在加载令牌…</p>
                ) : tokens.length === 0 ? (
                  <p className="text-fg-subtle">暂无 API 令牌</p>
                ) : (
                  <ul className="rounded-lg border border-line divide-y divide-line">
                    {tokens.map((t) => {
                      const expired = isExpired(t)
                      return (
                        <li key={t.id} className="flex items-center justify-between gap-3 px-3 py-2.5">
                          <div className="min-w-0">
                            <div className="flex flex-wrap items-center gap-1.5">
                              <span className={`font-medium ${expired ? 'text-fg-subtle line-through' : 'text-fg'}`}>
                                {t.name}
                              </span>
                              <span className={`${badge} font-mono bg-surface-subtle text-fg-muted border-line`}>
                                {t.prefix}…
                              </span>
                              <span className={`${badge} font-mono bg-sky-50 text-sky-700 border-sky-200`}>{t.role}</span>
                              {t.scopes && t.scopes.length > 0 ? (
                                t.scopes.map((sc) => (
                                  <span key={sc} className={`${badge} font-mono bg-indigo-50 text-indigo-700 border-indigo-200`}>
                                    {sc}
                                  </span>
                                ))
                              ) : (
                                <span className={`${badge} bg-surface-subtle text-fg-muted border-line`}>全部 Scope</span>
                              )}
                              {t.mcp_tools && t.mcp_tools.length > 0 && (
                                <span
                                  title={t.mcp_tools.join('\n')}
                                  className={`${badge} bg-violet-50 text-violet-700 border-violet-200`}
                                >
                                  <SlidersHorizontal className="w-3 h-3" />
                                  MCP 限定 <span className="tg-tabular">{t.mcp_tools.length}</span>
                                </span>
                              )}
                              {t.project_ids && t.project_ids.length > 0 && (
                                <span
                                  title={t.project_ids.map((pid) => projectNameMap.get(pid) || pid).join('、')}
                                  className={`${badge} bg-amber-50 text-amber-800 border-amber-200`}
                                >
                                  项目：
                                  {t.project_ids
                                    .slice(0, 2)
                                    .map((pid) => projectNameMap.get(pid) || pid)
                                    .join('、')}
                                  {t.project_ids.length > 2 ? ` +${t.project_ids.length - 2}` : ''}
                                </span>
                              )}
                              {((t.mcp_max_vus || 0) > 0 || (t.rate_per_min || 0) > 0) && (
                                <span className={`${badge} bg-cyan-50 text-cyan-800 border-cyan-200 font-mono`}>
                                  {t.mcp_max_vus ? `≤${t.mcp_max_vus} VU ` : ''}
                                  {t.rate_per_min ? `${t.rate_per_min}/m` : ''}
                                </span>
                              )}
                              {expired && <span className={`${badge} bg-rose-50 text-rose-700 border-rose-200`}>已过期</span>}
                            </div>
                            <div className="tg-tabular text-[11px] text-fg-subtle mt-0.5">
                              创建 {new Date(t.created_at).toLocaleString()}
                              {t.expires_at ? ` · 过期 ${new Date(t.expires_at).toLocaleDateString()}` : ' · 长期有效'}
                              {t.last_used_at ? ` · 最近使用 ${new Date(t.last_used_at).toLocaleString()}` : ''}
                              {t.last_used_addr ? ` (${t.last_used_addr})` : ''}
                            </div>
                          </div>
                          {dangerBtn(`token:${t.id}`, '吊销', '确认吊销？', () => void handleRevokeToken(t))}
                        </li>
                      )
                    })}
                  </ul>
                )}
              </div>
            )}

            {/* ================= MCP ================= */}
            {tab === 'mcp' && (
              <div className="space-y-4">
                <div className="flex flex-wrap items-center justify-between gap-2 p-3 rounded-xl border border-line bg-surface-subtle">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="inline-flex items-center gap-1.5 font-semibold text-fg">
                      <Bot className="w-4 h-4 text-tidal" />
                      MCP 状态：
                    </span>
                    <span className="px-2 py-0.5 rounded-full bg-sky-500/15 text-tidal text-[11px] font-mono tg-tabular">
                      可用工具 {enabledToolCount}/{catalog?.length ?? 16}
                    </span>
                    <span className="px-2 py-0.5 rounded-full bg-surface border border-line text-fg-muted text-[11px] font-mono tg-tabular">
                      {effectiveMaxVUs > 0 ? `并发上限 ${effectiveMaxVUs} VU` : 'VU 不限'}
                      {effectiveMaxDurSec > 0 ? ` · 时长 ≤${effectiveMaxDurSec}s` : ''}
                      {effectiveRatePerMin > 0 ? ` · ${effectiveRatePerMin} 次/分` : ''}
                    </span>
                    {mcpProbeRes && (
                      <span
                        className={`px-2 py-0.5 rounded-full text-[11px] font-medium ${
                          mcpProbeRes.ok
                            ? 'bg-emerald-500/15 text-emerald-700'
                            : 'bg-rose-500/15 text-rose-700'
                        }`}
                      >
                        {mcpProbeRes.ok
                          ? `握手正常 · ${mcpProbeRes.toolCount} 工具 · ${mcpProbeRes.latencyMs}ms`
                          : mcpProbeRes.error || '握手失败'}
                      </span>
                    )}
                  </div>
                  <button
                    type="button"
                    disabled={probingMcp}
                    onClick={() => void handleProbeMCP()}
                    className="tg-btn-secondary !py-1"
                  >
                    <Sparkles className="w-3.5 h-3.5 text-tidal" />
                    {probingMcp ? '检测中…' : '自检连接'}
                  </button>
                </div>

                <div className="flex flex-wrap items-center gap-2">
                  <div role="radiogroup" aria-label="接入客户端格式" className="inline-flex rounded-xl border border-line bg-surface-subtle p-0.5">
                    {(
                      [
                        ['standard', 'Streamable HTTP (url)'],
                        ['cursor', 'Cursor / Antigravity (serverUrl)'],
                        ['stdio', 'CLI Stdio (env)'],
                      ] as const
                    ).map(([id, label]) => (
                      <button
                        key={id}
                        type="button"
                        role="radio"
                        aria-checked={mcpClient === id}
                        onClick={() => setMcpClient(id)}
                        className={`px-2.5 py-1 rounded-lg font-medium ${
                          mcpClient === id ? 'bg-surface text-tidal shadow-xs' : 'text-fg-muted hover:text-fg'
                        }`}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                  <div className="flex items-center gap-1.5 flex-1 min-w-[14rem]">
                    <span className="text-fg-muted shrink-0">令牌</span>
                    <GlassSelect
                      ariaLabel="选择 MCP 令牌"
                      value={selectedMcpToken?.id || ''}
                      disabled={mcpTokens.length === 0}
                      onChange={setMcpTokenId}
                      placeholder={loadingTokens ? '加载中…' : '无可用 MCP 令牌'}
                      className="flex-1 min-w-0"
                      options={mcpTokens.map((t) => ({
                        value: t.id,
                        label: `${t.name} · ${t.prefix}…`,
                        badge: getGlobalRoleLabel(t.role, true),
                      }))}
                    />
                  </div>
                </div>

                <div className="flex flex-wrap items-center gap-2 rounded-xl border border-line bg-surface-subtle px-3 py-2">
                  <span className="text-fg-muted">快捷签发令牌</span>
                  {user.role !== 'viewer' && (
                    <div role="radiogroup" aria-label="权限" className="inline-flex rounded-md border border-line bg-surface p-0.5">
                      {(
                        [
                          [false, '只读'],
                          [true, '读写'],
                        ] as const
                      ).map(([rw, label]) => (
                        <button
                          key={label}
                          type="button"
                          role="radio"
                          aria-checked={mcpQuickRW === rw}
                          onClick={() => setMcpQuickRW(rw)}
                          className={`px-2 py-0.5 rounded font-medium ${
                            mcpQuickRW === rw ? 'bg-sky-50 text-sky-700' : 'text-fg-muted hover:text-fg'
                          }`}
                        >
                          {label}
                        </button>
                      ))}
                    </div>
                  )}
                  <button
                    type="button"
                    disabled={creatingTok}
                    onClick={() => void handleQuickMCPToken()}
                    className={`ml-auto ${mcpTokens.length === 0 ? btnPrimary : btnSecondary}`}
                  >
                    <Plus className="w-3.5 h-3.5" /> {creatingTok ? '生成中…' : '一键生成并填入'}
                  </button>
                </div>

                <div className="rounded-xl border tg-code-surface overflow-hidden">
                  <div className="flex items-center justify-between gap-2 px-3 py-1.5 border-b border-white/10">
                    <div className="flex items-center gap-2 min-w-0">
                      <span className="font-mono text-[11px] text-slate-400 truncate">mcp_config.json</span>
                      {selectedMcpToken &&
                        (mcpPlaintext ? (
                          <span className="px-1.5 py-0.5 rounded text-[11px] bg-emerald-400/15 text-emerald-300">
                            已填入明文 · 仅本次可见
                          </span>
                        ) : (
                          <span className="px-1.5 py-0.5 rounded text-[11px] bg-amber-400/15 text-amber-300">
                            需替换为令牌明文
                          </span>
                        ))}
                    </div>
                    <button
                      type="button"
                      onClick={() => void copy('mcp', mcpSnippet)}
                      className="inline-flex items-center gap-1 px-2 py-1 rounded text-slate-200 hover:bg-white/10"
                    >
                      <CopyIcon done={copied === 'mcp'} /> 复制
                    </button>
                  </div>
                  <pre className="p-3.5 font-mono text-[11px] leading-relaxed text-sky-100 overflow-x-auto tg-scroller-dark">
                    {mcpSnippet}
                  </pre>
                </div>

                {(resources.length > 0 || prompts.length > 0) && (
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                    <div className="rounded-xl border border-line bg-surface-subtle p-3 space-y-2">
                      <div className="font-semibold text-fg flex items-center justify-between">
                        <span>MCP Resources ({resources.length})</span>
                      </div>
                      <div className="space-y-1.5">
                        {resources.map((r) => (
                          <div key={r.uri_template} className="flex items-center justify-between gap-2 rounded-lg bg-surface border border-line px-2 py-1.5">
                            <div className="min-w-0">
                              <div className="font-mono text-[11px] text-tidal truncate">{r.uri_template}</div>
                              <div className="text-[10px] text-fg-subtle truncate">{r.name} · {r.description}</div>
                            </div>
                            <button
                              type="button"
                              onClick={() => void copy(`res:${r.uri_template}`, r.uri_template)}
                              className="p-1 text-fg-subtle hover:text-fg shrink-0"
                              title="复制 URI 模板"
                            >
                              <CopyIcon done={copied === `res:${r.uri_template}`} />
                            </button>
                          </div>
                        ))}
                      </div>
                    </div>

                    <div className="rounded-xl border border-line bg-surface-subtle p-3 space-y-2">
                      <div className="font-semibold text-fg flex items-center justify-between">
                        <span>MCP Prompts ({prompts.length})</span>
                      </div>
                      <div className="space-y-1.5">
                        {prompts.map((p) => (
                          <div key={p.name} className="flex items-center justify-between gap-2 rounded-lg bg-surface border border-line px-2 py-1.5">
                            <div className="min-w-0">
                              <div className="font-mono text-[11px] text-fg font-semibold truncate">/{p.name}</div>
                              <div className="text-[10px] text-fg-subtle truncate">{p.title} · {p.description}</div>
                            </div>
                            <button
                              type="button"
                              onClick={() => void copy(`prm:${p.name}`, p.name)}
                              className="p-1 text-fg-subtle hover:text-fg shrink-0"
                              title="复制 Prompt 名称"
                            >
                              <CopyIcon done={copied === `prm:${p.name}`} />
                            </button>
                          </div>
                        ))}
                      </div>
                    </div>
                  </div>
                )}

                {mcpActivity.length > 0 && (
                  <div className="rounded-xl border border-line bg-surface-subtle p-3 space-y-2">
                    <div className="font-semibold text-fg flex items-center gap-1.5">
                      <Activity className="w-3.5 h-3.5 text-tidal" /> 最近 MCP 调用记录
                    </div>
                    <div className="max-h-36 overflow-y-auto tg-scroller space-y-1">
                      {mcpActivity.slice(0, 8).map((a) => (
                        <div key={a.id} className="flex items-center justify-between gap-2 px-2 py-1 rounded bg-surface border border-line text-[11px] font-mono">
                          <span className="truncate text-fg">{a.action} {a.resource_id || ''}</span>
                          <span className="text-fg-subtle shrink-0 tg-tabular">
                            {new Date(a.created_at).toLocaleTimeString()}
                          </span>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            )}

            {/* ================= TOTP ================= */}
            {tab === 'totp' && (
              <div className="space-y-4">
                <div className="flex items-center justify-between">
                  <h3 className="font-semibold text-fg">TOTP 双因素认证</h3>
                  <span
                    className={`${badge} rounded-full px-2 font-semibold ${
                      user.totp_enabled
                        ? 'bg-emerald-50 text-emerald-700 border-emerald-200'
                        : 'bg-surface-subtle text-fg-muted border-line'
                    }`}
                  >
                    {user.totp_enabled ? '已启用' : '未启用'}
                  </span>
                </div>

                {recoveryCodes.length > 0 && (
                  <div className="rounded-lg border border-amber-300 bg-amber-50 p-3 space-y-2.5">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="font-semibold text-amber-900">恢复码 · 仅显示一次</span>
                      <div className="flex items-center gap-1.5">
                        <button type="button" onClick={downloadRecovery} className={btnSecondary}>
                          <Download className="w-3.5 h-3.5" /> 下载 .txt
                        </button>
                        <button
                          type="button"
                          onClick={() => void copy('recovery', recoveryCodes.join('\n'))}
                          className="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-md bg-amber-600 hover:bg-amber-solid text-white font-medium"
                        >
                          {copied === 'recovery' ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />} 复制全部
                        </button>
                      </div>
                    </div>
                    <ol className="grid grid-cols-2 sm:grid-cols-4 gap-2">
                      {recoveryCodes.map((rc) => (
                        <li
                          key={rc}
                          className="tg-tabular px-2 py-1.5 rounded bg-surface border border-amber-200 font-mono text-center text-fg select-all"
                        >
                          {rc}
                        </li>
                      ))}
                    </ol>
                  </div>
                )}

                {!user.totp_enabled ? (
                  enrollment ? (
                    <div className="grid sm:grid-cols-[auto_1fr] gap-5 rounded-lg border border-line p-4">
                      <figure className="justify-self-center sm:justify-self-start">
                        {qrSrc ? (
                          <img
                            src={qrSrc}
                            width={184}
                            height={184}
                            alt="验证器绑定二维码"
                            className="block rounded-md border border-line bg-white [image-rendering:pixelated]"
                          />
                        ) : (
                          <div className="w-[184px] h-[184px] grid place-items-center rounded-md border border-dashed border-line text-fg-subtle">
                            使用右侧密钥手动添加
                          </div>
                        )}
                      </figure>

                      <div className="min-w-0 space-y-4">
                        <div>
                          <div className="text-[11px] text-fg-muted mb-1">Base32 密钥</div>
                          <div className="flex items-center gap-2">
                            <code className="flex-1 min-w-0 px-2.5 py-1.5 rounded-md bg-surface-subtle border border-line font-mono tracking-wider text-fg break-all select-all">
                              {enrollment.secret.replace(/(.{4})(?=.)/g, '$1 ')}
                            </code>
                            <button
                              type="button"
                              onClick={() => void copy('secret', enrollment.secret)}
                              aria-label="复制密钥"
                              className={btnSecondary}
                            >
                              <CopyIcon done={copied === 'secret'} /> 复制
                            </button>
                          </div>
                          <details className="mt-1.5 group">
                            <summary className="cursor-pointer text-[11px] text-fg-subtle hover:text-fg-muted select-none">
                              otpauth URI
                            </summary>
                            <div className="mt-1 flex items-start gap-2">
                              <code className="flex-1 min-w-0 px-2 py-1 rounded bg-surface-subtle border border-line font-mono text-[11px] text-fg-muted break-all select-all">
                                {enrollment.otpauth_url}
                              </code>
                              <button
                                type="button"
                                onClick={() => void copy('otpauth', enrollment.otpauth_url)}
                                aria-label="复制 otpauth URI"
                                className="p-1.5 rounded-md border border-line hover:bg-surface-hover text-fg-muted"
                              >
                                <CopyIcon done={copied === 'otpauth'} />
                              </button>
                            </div>
                          </details>
                        </div>

                        <form onSubmit={handleConfirmTOTP} className="pt-3 border-t border-line">
                          <label htmlFor="tg-totp-confirm" className="block text-[11px] text-fg-muted mb-1">
                            6 位验证码
                          </label>
                          <div className="flex items-center gap-2">
                            <input
                              id="tg-totp-confirm"
                              name="totp-code"
                              type="text"
                              inputMode="numeric"
                              autoComplete="one-time-code"
                              autoCorrect="off"
                              autoCapitalize="none"
                              pattern="[0-9]{6}"
                              maxLength={6}
                              spellCheck={false}
                              required
                              value={totpConfirmCode}
                              onChange={(e) => setTotpConfirmCode(e.target.value.replace(/\D/g, ''))}
                              placeholder="000000"
                              className={`${inputCls} tg-tabular w-36 font-mono text-sm tracking-[0.3em]`}
                            />
                            <button type="submit" disabled={totpBusy || totpConfirmCode.length !== 6} className={btnPrimary}>
                              {totpBusy ? '验证中…' : '确认启用'}
                            </button>
                            <button type="button" onClick={() => setEnrollment(null)} className={btnSecondary}>
                              取消
                            </button>
                          </div>
                        </form>
                      </div>
                    </div>
                  ) : (
                    <button
                      type="button"
                      disabled={totpBusy}
                      onClick={() => void handleStartEnrollTOTP()}
                      className={btnPrimary}
                    >
                      <ShieldCheck className="w-3.5 h-3.5" /> {totpBusy ? '生成中…' : '绑定验证器'}
                    </button>
                  )
                ) : (
                  <form onSubmit={handleDisableTOTP} className="rounded-lg border border-line p-4 max-w-md">
                    <fieldset className="space-y-3">
                      <legend className="font-semibold text-fg mb-2">停用 2FA</legend>
                      <div>
                        <label htmlFor="tg-totp-disable-pwd" className="block text-[11px] text-fg-muted mb-1">
                          当前密码
                        </label>
                        <input
                          id="tg-totp-disable-pwd"
                          name="current-password"
                          type="password"
                          autoComplete="current-password"
                          required
                          value={disablePwd}
                          onChange={(e) => setDisablePwd(e.target.value)}
                          className={`${inputCls} w-full`}
                        />
                      </div>
                      <div>
                        <label htmlFor="tg-totp-disable-code" className="block text-[11px] text-fg-muted mb-1">
                          验证码或恢复码
                        </label>
                        <input
                          id="tg-totp-disable-code"
                          name="totp-code"
                          type="text"
                          inputMode="numeric"
                          autoComplete="one-time-code"
                          autoCorrect="off"
                          autoCapitalize="none"
                          pattern="[0-9A-Za-z-]*"
                          maxLength={12}
                          spellCheck={false}
                          required
                          value={disableCode}
                          onChange={(e) => setDisableCode(e.target.value)}
                          placeholder="000000 或 XXXX-XXXX"
                          className={`${inputCls} tg-tabular w-full font-mono tracking-wider`}
                        />
                      </div>
                      <button
                        type="submit"
                        disabled={totpBusy}
                        className="px-3 py-1.5 rounded-md border border-rose-300 bg-rose-50 hover:bg-rose-100 text-rose-700 font-medium disabled:opacity-50"
                      >
                        {totpBusy ? '处理中…' : '停用 2FA'}
                      </button>
                    </fieldset>
                  </form>
                )}
              </div>
            )}

            {/* ================= Password ================= */}
            {tab === 'password' && (
              <form onSubmit={handleChangePwd} className="space-y-3 max-w-sm">
                <input type="text" name="username" autoComplete="username" value={user.username} readOnly hidden />
                <div>
                  <label htmlFor="tg-pwd-current" className="block text-[11px] text-fg-muted mb-1">
                    当前密码
                  </label>
                  <input
                    id="tg-pwd-current"
                    name="current-password"
                    type="password"
                    autoComplete="current-password"
                    required
                    value={curPwd}
                    onChange={(e) => setCurPwd(e.target.value)}
                    className={`${inputCls} w-full`}
                  />
                </div>
                <div>
                  <div className="flex items-center justify-between mb-1">
                    <label htmlFor="tg-pwd-new" className="text-[11px] text-fg-muted">
                      新密码
                    </label>
                    <span
                      className={`tg-tabular text-[11px] ${newPwd.length >= 10 ? 'text-emerald-600' : 'text-fg-subtle'}`}
                    >
                      {newPwd.length}/10
                    </span>
                  </div>
                  <div className="relative">
                    <input
                      id="tg-pwd-new"
                      name="new-password"
                      type={showPwd ? 'text' : 'password'}
                      autoComplete="new-password"
                      minLength={10}
                      required
                      value={newPwd}
                      onChange={(e) => setNewPwd(e.target.value)}
                      className={`${inputCls} w-full pr-8`}
                    />
                    <button
                      type="button"
                      aria-pressed={showPwd}
                      aria-label={showPwd ? '隐藏密码' : '显示密码'}
                      onClick={() => setShowPwd((v) => !v)}
                      className="absolute right-1.5 top-1/2 -translate-y-1/2 p-1 rounded text-fg-subtle hover:text-fg"
                    >
                      {showPwd ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                    </button>
                  </div>
                </div>
                <div>
                  <label htmlFor="tg-pwd-confirm" className="block text-[11px] text-fg-muted mb-1">
                    确认新密码
                  </label>
                  <input
                    id="tg-pwd-confirm"
                    name="confirm-password"
                    type={showPwd ? 'text' : 'password'}
                    autoComplete="new-password"
                    required
                    value={confirmPwd}
                    onChange={(e) => setConfirmPwd(e.target.value)}
                    aria-invalid={confirmPwd.length > 0 && confirmPwd !== newPwd}
                    className={`${inputCls} w-full ${
                      confirmPwd.length > 0 && confirmPwd !== newPwd ? 'border-rose-400 focus:border-rose-500' : ''
                    }`}
                  />
                </div>
                <button type="submit" disabled={savingPwd} className={btnPrimary}>
                  {savingPwd ? '保存中…' : '修改密码'}
                </button>
              </form>
            )}

            {/* ================= Appearance ================= */}
            {tab === 'appearance' && (
              <fieldset>
                <legend className="font-semibold text-fg mb-3">外观</legend>
                <div className="grid grid-cols-3 gap-3 max-w-xl">
                  {(
                    [
                      { id: 'system', label: '跟随系统', icon: Monitor },
                      { id: 'light', label: '浅色', icon: Sun },
                      { id: 'dark', label: '深色', icon: Moon },
                    ] as { id: SchemePreference; label: string; icon: typeof Monitor }[]
                  ).map((opt) => {
                    const Icon = opt.icon
                    const selected = scheme.preference === opt.id
                    return (
                      <label
                        key={opt.id}
                        className={`group relative flex flex-col gap-2 rounded-lg border p-2 cursor-pointer transition-colors has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-sky-400 ${
                          selected ? 'border-tidal ring-1 ring-tidal' : 'border-line hover:border-line-strong'
                        }`}
                      >
                        <input
                          type="radio"
                          name="tg-color-scheme"
                          value={opt.id}
                          checked={selected}
                          onChange={() => setPreference(opt.id)}
                          className="sr-only"
                        />
                        <SchemePreview kind={opt.id} />
                        <span className="flex items-center gap-1.5 font-medium text-fg">
                          <Icon className="w-3.5 h-3.5" />
                          {opt.label}
                          {selected && <Check className="w-3.5 h-3.5 ml-auto text-tidal" />}
                        </span>
                        {opt.id === 'system' && (
                          <span className="text-[11px] text-fg-subtle -mt-1">
                            当前{systemScheme === 'dark' ? '深色' : '浅色'}
                          </span>
                        )}
                      </label>
                    )
                  })}
                </div>
              </fieldset>
            )}
          </div>

          <footer className="tg-dialog-footer justify-between text-xs">
            <button
              type="button"
              onClick={() => void handleLogout()}
              className="inline-flex items-center gap-1.5 text-rose-600 hover:text-rose-700 font-medium"
            >
              <LogOut className="w-3.5 h-3.5" /> 退出登录
            </button>
            <button type="button" onClick={onClose} className={btnSecondary}>
              关闭
            </button>
          </footer>
        </>
      )}
    </dialog>
  )
}

/** Tiny fixed-colour UI mock; intentionally not theme-adaptive so each card shows its own scheme. */
function SchemePreview({ kind }: { kind: SchemePreference }) {
  const pane = (dark: boolean) => (
    <div className={`h-full flex ${dark ? 'bg-[#090d16]' : 'bg-[#eef2f7]'}`}>
      <div className="w-1/4 bg-[#0b2a4a]" />
      <div className="flex-1 p-1.5 space-y-1">
        <div className={`h-1.5 w-3/4 rounded-sm ${dark ? 'bg-[#2a3548]' : 'bg-[#cbd5e1]'}`} />
        <div className={`h-6 rounded-sm border ${dark ? 'bg-[#0f1623] border-[#1e293b]' : 'bg-[#ffffff] border-[#e2e8f0]'}`} />
        <div className="h-1.5 w-1/3 rounded-sm bg-[#0284c7]" />
      </div>
    </div>
  )
  return (
    <div aria-hidden className="h-16 rounded-md overflow-hidden border border-line" style={{ colorScheme: 'normal' }}>
      {kind === 'system' ? (
        <div className="grid grid-cols-2 h-full">
          <div className="overflow-hidden">{pane(false)}</div>
          <div className="overflow-hidden">{pane(true)}</div>
        </div>
      ) : (
        pane(kind === 'dark')
      )}
    </div>
  )
}
