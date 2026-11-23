import { useEffect, useState, type FormEvent } from 'react'
import { Bot, Check, Copy, Key, KeyRound, Laptop, LogOut, Plus, ShieldCheck, Trash2, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { APIToken, Project, SessionItem, TOTPEnrollment, User, UserRole } from '../lib/types'

export type AccountModalTab = 'sessions' | 'tokens' | 'totp' | 'mcp' | 'password'

interface Props {
  open: boolean
  initialTab?: AccountModalTab
  user: User
  projects?: Project[]
  onUserUpdated?: (user: User) => void
  onClose: () => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
  onLoggedOut: () => void
}

export function AccountModal({
  open,
  initialTab = 'sessions',
  user,
  projects = [],
  onUserUpdated,
  onClose,
  onToast,
  onLoggedOut,
}: Props) {
  const [tab, setTab] = useState<AccountModalTab>(initialTab)
  const [sessions, setSessions] = useState<SessionItem[]>([])
  const [loading, setLoading] = useState(false)
  const [tokens, setTokens] = useState<APIToken[]>([])
  const [loadingTokens, setLoadingTokens] = useState(false)
  const [tokName, setTokName] = useState('')
  const [tokRole, setTokRole] = useState<UserRole>(user.role)
  const [tokTTLDays, setTokTTLDays] = useState<number>(30)
  const [tokScopes, setTokScopes] = useState<string[]>([])
  const [tokProjectIDs, setTokProjectIDs] = useState<string[]>([])
  const [creatingTok, setCreatingTok] = useState(false)
  const [newPlaintext, setNewPlaintext] = useState('')
  const [copiedTok, setCopiedTok] = useState(false)
  const [enrollment, setEnrollment] = useState<TOTPEnrollment | null>(null)
  const [totpConfirmCode, setTotpConfirmCode] = useState('')
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([])
  const [copiedRecovery, setCopiedRecovery] = useState(false)
  const [disablePwd, setDisablePwd] = useState('')
  const [disableCode, setDisableCode] = useState('')
  const [totpBusy, setTotpBusy] = useState(false)
  const [mcpMode, setMcpMode] = useState<'http' | 'stdio'>('http')
  const [copiedMcp, setCopiedMcp] = useState(false)
  const [curPwd, setCurPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')
  const [confirmPwd, setConfirmPwd] = useState('')
  const [savingPwd, setSavingPwd] = useState(false)

  useEffect(() => {
    if (open) {
      setTab(initialTab)
      setCopiedMcp(false)
      setEnrollment(null)
      setTotpConfirmCode('')
      setRecoveryCodes([])
      setCopiedRecovery(false)
      setDisablePwd('')
      setDisableCode('')
    }
  }, [open, initialTab])

  const loadSessions = async () => {
    setLoading(true)
    try {
      const res = await api.listSessions()
      setSessions(res.sessions || [])
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '读取会话列表失败')
    } finally {
      setLoading(false)
    }
  }

  const loadTokens = async () => {
    setLoadingTokens(true)
    try {
      setTokens(await api.listAPITokens())
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '读取 API 令牌失败')
    } finally {
      setLoadingTokens(false)
    }
  }

  useEffect(() => {
    if (!open) return
    if (tab === 'sessions') void loadSessions()
    if (tab === 'tokens' || tab === 'mcp') void loadTokens()
  }, [open, tab])

  useEffect(() => {
    setTokRole(user.role)
  }, [user.role])

  if (!open) return null

  const allowedTokenRoles: UserRole[] =
    user.role === 'admin' ? ['viewer', 'member', 'admin'] : user.role === 'member' ? ['viewer', 'member'] : ['viewer']

  const origin = typeof window !== 'undefined' ? window.location.origin : 'https://127.0.0.1:8080'
  const tokenPlaceholder = newPlaintext || '<使用上方按钮生成或填入 tg_ 令牌>'
  const httpConfigSnippet = JSON.stringify(
    {
      mcpServers: {
        tidegate: {
          serverUrl: `${origin}/api/v1/mcp`,
          headers: {
            Authorization: `Bearer ${tokenPlaceholder}`,
          },
        },
      },
    },
    null,
    2
  )
  const stdioConfigSnippet = JSON.stringify(
    {
      mcpServers: {
        tidegate: {
          command: 'tidegate',
          args: ['mcp', '--url', origin, '--token', tokenPlaceholder],
        },
      },
    },
    null,
    2
  )
  const activeSnippet = mcpMode === 'http' ? httpConfigSnippet : stdioConfigSnippet

  const availableTokenScopes =
    user.role === 'admin'
      ? [
          { id: 'read', label: 'read (只读)' },
          { id: 'write', label: 'write (写入)' },
          { id: 'debug', label: 'debug (调试)' },
          { id: 'run', label: 'run (压测/集合)' },
          { id: 'mcp', label: 'mcp (MCP 协议)' },
          { id: 'admin', label: 'admin (系统管理)' },
        ]
      : [
          { id: 'read', label: 'read (只读)' },
          { id: 'write', label: 'write (写入)' },
          { id: 'debug', label: 'debug (调试)' },
          { id: 'run', label: 'run (压测/集合)' },
          { id: 'mcp', label: 'mcp (MCP 协议)' },
        ]

  const toggleTokScope = (scope: string) => {
    setTokScopes((prev) => (prev.includes(scope) ? prev.filter((x) => x !== scope) : [...prev, scope]))
  }

  const toggleTokProject = (pid: string) => {
    setTokProjectIDs((prev) => (prev.includes(pid) ? prev.filter((x) => x !== pid) : [...prev, pid]))
  }

  const handleCreateToken = async (e: FormEvent) => {
    e.preventDefault()
    if (!tokName.trim()) {
      onToast('error', '请输入 API 令牌名称')
      return
    }
    setCreatingTok(true)
    try {
      const res = await api.createAPIToken({
        name: tokName.trim(),
        role: tokRole,
        scopes: tokScopes.length > 0 ? tokScopes : undefined,
        project_ids: tokProjectIDs.length > 0 ? tokProjectIDs : undefined,
        ttl_days: tokTTLDays > 0 ? tokTTLDays : undefined,
      })
      setNewPlaintext(res.plaintext)
      setCopiedTok(false)
      setTokName('')
      setTokScopes([])
      setTokProjectIDs([])
      await loadTokens()
      onToast('success', '已生成新的 API 访问令牌，可直接用于 CLI 或 MCP 接入')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '生成 API 令牌失败')
    } finally {
      setCreatingTok(false)
    }
  }

  const handleQuickMCPToken = async (readonly: boolean) => {
    setCreatingTok(true)
    try {
      const res = await api.createAPIToken({
        name: readonly ? 'mcp-readonly' : 'mcp-readwrite',
        role: readonly ? 'viewer' : 'member',
        scopes: readonly ? ['mcp', 'read'] : ['mcp', 'read', 'write', 'debug', 'run'],
        ttl_days: 30,
      })
      setNewPlaintext(res.plaintext)
      setCopiedTok(false)
      setCopiedMcp(false)
      await loadTokens()
      onToast('success', readonly ? '已生成只读 MCP 令牌并填入配置' : '已生成读写 MCP 令牌并填入配置')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '生成 MCP 令牌失败')
    } finally {
      setCreatingTok(false)
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
    if (!totpConfirmCode.trim()) {
      onToast('error', '请输入 6 位动态验证码')
      return
    }
    setTotpBusy(true)
    try {
      const res = await api.confirmTOTP(totpConfirmCode.trim())
      setRecoveryCodes(res.recovery_codes || [])
      setCopiedRecovery(false)
      setEnrollment(null)
      setTotpConfirmCode('')
      onUserUpdated?.({ ...user, totp_enabled: true })
      onToast('success', '已启用 TOTP 双因素认证，请妥善保存恢复码')
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
      onToast('success', '已关闭双因素认证 (2FA)')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '关闭 2FA 失败')
    } finally {
      setTotpBusy(false)
    }
  }

  const handleRevokeToken = async (tok: APIToken) => {
    try {
      await api.revokeAPIToken(tok.id)
      onToast('success', `已吊销令牌「${tok.name}」`)
      await loadTokens()
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '吊销 API 令牌失败')
    }
  }

  const handleRevoke = async (s: SessionItem) => {
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

  return (
    <div className="fixed inset-0 z-40 bg-slate-950/50 backdrop-blur-[2px] flex items-center justify-center p-4">
      <div className="w-full max-w-2xl bg-white rounded-xl border border-slate-200 shadow-2xl overflow-hidden">
        <div className="px-5 py-3 tg-telemetry-strip flex items-center justify-between">
          <div className="flex items-center gap-3 min-w-0">
            <h2 className="text-sm font-semibold text-white">账号与令牌</h2>
            <span className="text-xs text-sky-200/80 font-mono truncate">
              {user.username} · {user.role}
            </span>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭弹窗"
            className="p-1 text-slate-300 hover:text-white rounded hover:bg-white/10"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <div role="tablist" aria-label="账号与安全页签" className="tg-tablist px-5 border-b border-slate-200 bg-slate-50 gap-5 text-xs font-medium">
          {(
            [
              { id: 'sessions' as const, label: '活动会话', icon: Laptop },
              { id: 'tokens' as const, label: 'API 访问令牌', icon: Key },
              { id: 'totp' as const, label: '双因素认证 2FA', icon: ShieldCheck },
              { id: 'mcp' as const, label: 'MCP 客户端接入', icon: Bot },
              { id: 'password' as const, label: '修改密码', icon: KeyRound },
            ] as const
          ).map((t) => {
            const Icon = t.icon
            const selected = tab === t.id
            return (
              <button
                key={t.id}
                type="button"
                role="tab"
                aria-selected={selected}
                onClick={() => setTab(t.id)}
                className={`tg-tab inline-flex items-center gap-1.5 py-2.5 transition-colors ${
                  selected ? 'text-sky-700' : 'text-slate-500 hover:text-slate-900'
                }`}
              >
                <Icon className="w-3.5 h-3.5" />
                <span>{t.label}</span>
              </button>
            )
          })}
        </div>

        <div className="p-5 max-h-[65vh] overflow-y-auto tg-scroller">
          {tab === 'sessions' && (
            <div className="space-y-3">
              {loading ? (
                <p className="text-xs text-slate-500">正在加载会话...</p>
              ) : sessions.length === 0 ? (
                <p className="text-xs text-slate-500">暂无活动会话记录。</p>
              ) : (
                sessions.map((s) => (
                  <div
                    key={s.id}
                    className="flex items-center justify-between gap-3 p-3 rounded-lg border border-slate-200 bg-slate-50/50 text-xs"
                  >
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 font-medium text-slate-800">
                        <span className="font-mono">{s.remote_addr || '本机'}</span>
                        {s.current && (
                          <span className="px-1.5 py-0.5 text-[10px] rounded bg-emerald-100 text-emerald-800 font-semibold">
                            当前会话
                          </span>
                        )}
                      </div>
                      <p className="text-slate-500 truncate mt-0.5">{s.user_agent || '未知终端'}</p>
                      <p className="tg-tabular text-[11px] text-slate-400 mt-0.5">
                        最近活跃：{new Date(s.last_seen_at).toLocaleString()} · 过期时间：
                        {new Date(s.expires_at).toLocaleString()}
                      </p>
                    </div>
                    <button
                      type="button"
                      onClick={() => void handleRevoke(s)}
                      className="shrink-0 inline-flex items-center gap-1 px-2.5 py-1.5 rounded border border-rose-200 text-rose-700 hover:bg-rose-50"
                    >
                      <Trash2 className="w-3.5 h-3.5" /> {s.current ? '退出' : '注销'}
                    </button>
                  </div>
                ))
              )}
            </div>
          )}

          {tab === 'tokens' && (
            <div className="space-y-4 text-xs">
              <form onSubmit={handleCreateToken} className="p-3 rounded-lg border border-slate-200 bg-slate-50/60 space-y-3">
                <div className="font-medium text-slate-800">生成长期 CLI / CI / MCP Bearer 令牌（tg_ 前缀）</div>
                <div className="grid grid-cols-12 gap-2 items-center">
                  <input
                    type="text"
                    value={tokName}
                    onChange={(e) => setTokName(e.target.value)}
                    placeholder="令牌用途（如 Claude MCP / GitLab CI）"
                    className="col-span-5 px-2.5 py-1.5 border border-slate-300 rounded bg-white"
                  />
                  <select
                    value={tokRole}
                    onChange={(e) => setTokRole(e.target.value as UserRole)}
                    className="col-span-3 px-2 py-1.5 border border-slate-300 rounded bg-white"
                  >
                    {allowedTokenRoles.map((r) => (
                      <option key={r} value={r}>
                        权限：{r}
                      </option>
                    ))}
                  </select>
                  <select
                    value={tokTTLDays}
                    onChange={(e) => setTokTTLDays(Number(e.target.value))}
                    className="col-span-2 px-2 py-1.5 border border-slate-300 rounded bg-white"
                  >
                    <option value={7}>7 天</option>
                    <option value={30}>30 天</option>
                    <option value={90}>90 天</option>
                    <option value={365}>365 天</option>
                    <option value={0}>长期有效</option>
                  </select>
                  <button
                    type="submit"
                    disabled={creatingTok}
                    className="col-span-2 inline-flex items-center justify-center gap-1 px-2.5 py-1.5 rounded bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
                  >
                    <Plus className="w-3.5 h-3.5" /> 创建
                  </button>
                </div>

                <fieldset className="space-y-1.5 pt-2 border-t border-slate-200/80">
                  <legend className="text-[11px] font-medium text-slate-600 px-1">
                    权限范围 Scope（不选则继承角色）
                  </legend>
                  <div className="flex flex-wrap gap-2">
                    {availableTokenScopes.map((sc) => (
                      <label
                        key={sc.id}
                        className="inline-flex items-center gap-1.5 px-2 py-1 rounded border border-slate-200 bg-white cursor-pointer hover:border-sky-300"
                      >
                        <input
                          type="checkbox"
                          checked={tokScopes.includes(sc.id)}
                          onChange={() => toggleTokScope(sc.id)}
                        />
                        <span className="font-mono text-[11px] text-slate-700">{sc.label}</span>
                      </label>
                    ))}
                  </div>
                </fieldset>

                {projects.length > 0 && (
                  <fieldset className="space-y-1.5 pt-2 border-t border-slate-200/80">
                    <legend className="text-[11px] font-medium text-slate-600 px-1">
                      限定项目（不选则全部可访问）
                    </legend>
                    <div className="flex flex-wrap gap-2">
                      {projects.map((p) => (
                        <label
                          key={p.id}
                          className="inline-flex items-center gap-1.5 px-2 py-1 rounded border border-slate-200 bg-white cursor-pointer hover:border-sky-300"
                        >
                          <input
                            type="checkbox"
                            checked={tokProjectIDs.includes(p.id)}
                            onChange={() => toggleTokProject(p.id)}
                          />
                          <span className="text-[11px] text-slate-700">{p.name}</span>
                        </label>
                      ))}
                    </div>
                  </fieldset>
                )}
              </form>

              {newPlaintext && (
                <div className="p-3 rounded-lg border border-emerald-300 bg-emerald-50 space-y-1.5">
                  <div className="flex items-center justify-between text-emerald-900 font-semibold">
                    <span>新令牌明文（仅本次展示，关闭后不可再次查看）：</span>
                    <button
                      type="button"
                      onClick={async () => {
                        try {
                          await navigator.clipboard.writeText(newPlaintext)
                          setCopiedTok(true)
                          onToast('success', '已复制 API 令牌')
                        } catch {
                          onToast('info', '请手动复制上方令牌明文')
                        }
                      }}
                      className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-emerald-600 text-white"
                    >
                      {copiedTok ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />} 复制
                    </button>
                  </div>
                  <div className="font-mono text-[11px] break-all bg-white border border-emerald-200 rounded px-2 py-1.5 text-slate-900">
                    {newPlaintext}
                  </div>
                </div>
              )}

              {loadingTokens ? (
                <p className="text-slate-500">正在加载 API 令牌...</p>
              ) : tokens.length === 0 ? (
                <p className="text-slate-400">当前账号尚未创建任何 API 访问令牌。</p>
              ) : (
                <div className="space-y-2">
                  {tokens.map((t) => (
                    <div
                      key={t.id}
                      className="flex items-center justify-between gap-3 p-2.5 rounded-lg border border-slate-200 bg-white"
                    >
                      <div className="min-w-0">
                        <div className="flex flex-wrap items-center gap-1.5 font-medium text-slate-800">
                          <span>{t.name}</span>
                          <span className="px-1.5 py-0.5 rounded bg-slate-100 text-slate-700 font-mono text-[10px]">
                            {t.prefix}...
                          </span>
                          <span className="px-1.5 py-0.5 rounded bg-sky-50 text-sky-700 font-mono text-[10px]">
                            {t.role}
                          </span>
                          {t.scopes && t.scopes.length > 0 ? (
                            t.scopes.map((sc) => (
                              <span
                                key={sc}
                                className="px-1.5 py-0.5 rounded bg-indigo-50 text-indigo-700 border border-indigo-200 font-mono text-[10px]"
                              >
                                scope:{sc}
                              </span>
                            ))
                          ) : (
                            <span className="px-1.5 py-0.5 rounded bg-slate-50 text-slate-500 text-[10px]">
                              全量 Scope
                            </span>
                          )}
                          {t.project_ids && t.project_ids.length > 0 && (
                            <span className="px-1.5 py-0.5 rounded bg-amber-50 text-amber-800 border border-amber-200 text-[10px]">
                              限定 {t.project_ids.length} 个项目
                            </span>
                          )}
                        </div>
                        <div className="tg-tabular text-[11px] text-slate-400 mt-0.5">
                          创建：{new Date(t.created_at).toLocaleString()}
                          {t.expires_at ? ` · 过期：${new Date(t.expires_at).toLocaleDateString()}` : ' · 永不过期'}
                          {t.last_used_at ? ` · 最近使用：${new Date(t.last_used_at).toLocaleString()}` : ''}
                        </div>
                      </div>
                      <button
                        type="button"
                        onClick={() => void handleRevokeToken(t)}
                        className="shrink-0 inline-flex items-center gap-1 px-2.5 py-1 rounded border border-rose-200 text-rose-700 hover:bg-rose-50"
                      >
                        <Trash2 className="w-3.5 h-3.5" /> 吊销
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}

          {tab === 'totp' && (
            <div className="space-y-4 text-xs">
              <div className="flex items-center justify-between p-3 rounded-lg border border-slate-200 bg-slate-50">
                <div className="font-semibold text-slate-800">TOTP 双因素认证（2FA）</div>
                <span
                  className={`px-2.5 py-1 rounded-full font-semibold text-[11px] ${
                    user.totp_enabled
                      ? 'bg-emerald-100 text-emerald-800 border border-emerald-200'
                      : 'bg-slate-200 text-slate-700'
                  }`}
                >
                  {user.totp_enabled ? '已启用 2FA' : '未启用'}
                </span>
              </div>

              {recoveryCodes.length > 0 && (
                <div className="p-3.5 rounded-lg border border-amber-300 bg-amber-50 space-y-2.5">
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-semibold text-amber-950">
                      一次性恢复码（仅展示一次，丢失验证器时可用于紧急登录）：
                    </span>
                    <button
                      type="button"
                      onClick={async () => {
                        try {
                          await navigator.clipboard.writeText(recoveryCodes.join('\n'))
                          setCopiedRecovery(true)
                          onToast('success', '已复制全部 8 个恢复码')
                        } catch {
                          onToast('info', '请手动记录下方恢复码')
                        }
                      }}
                      className="inline-flex items-center gap-1 px-2.5 py-1 rounded bg-amber-700 hover:bg-amber-800 text-white font-medium"
                    >
                      {copiedRecovery ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />} 复制全部
                    </button>
                  </div>
                  <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
                    {recoveryCodes.map((rc) => (
                      <div
                        key={rc}
                        className="px-2 py-1.5 rounded bg-white border border-amber-200 font-mono text-center text-slate-900 select-all"
                      >
                        {rc}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {!user.totp_enabled ? (
                enrollment ? (
                  <form onSubmit={handleConfirmTOTP} className="space-y-3 p-3.5 rounded-lg border border-slate-200 bg-white">
                    <div className="font-semibold text-slate-800">步骤 1：在验证器 App 中添加账号密钥</div>
                    <div className="space-y-2">
                      <div>
                        <div className="text-[11px] text-slate-500 mb-1">Base32 手动密钥（Secret）：</div>
                        <div className="flex items-center gap-2">
                          <code className="flex-1 px-2.5 py-1.5 rounded bg-slate-50 border border-slate-200 font-mono text-slate-900 select-all">
                            {enrollment.secret}
                          </code>
                          <button
                            type="button"
                            onClick={async () => {
                              try {
                                await navigator.clipboard.writeText(enrollment.secret)
                                onToast('success', '已复制 Base32 密钥')
                              } catch {
                                onToast('info', '请手动复制密钥')
                              }
                            }}
                            className="px-2.5 py-1.5 rounded border border-slate-300 bg-white hover:bg-slate-50"
                          >
                            复制密钥
                          </button>
                        </div>
                      </div>
                      <div>
                        <div className="text-[11px] text-slate-500 mb-1">otpauth:// 配置 URI：</div>
                        <div className="px-2.5 py-1.5 rounded bg-slate-50 border border-slate-200 font-mono text-[11px] break-all text-slate-700 select-all">
                          {enrollment.otpauth_url}
                        </div>
                      </div>
                    </div>

                    <div className="pt-2 border-t border-slate-200">
                      <label htmlFor="tg-totp-confirm" className="block font-semibold text-slate-800 mb-1">
                        步骤 2：输入验证器动态码以完成绑定
                      </label>
                      <p className="text-[11px] text-slate-500 mb-1.5">
                        请输入验证器当前显示的 6 位数字动态验证码。
                      </p>
                      <div className="flex items-center gap-2 max-w-sm">
                        <input
                          id="tg-totp-confirm"
                          type="text"
                          inputMode="numeric"
                          autoComplete="one-time-code"
                          pattern="[0-9]*"
                          maxLength={6}
                          spellCheck={false}
                          required
                          value={totpConfirmCode}
                          onChange={(e) => setTotpConfirmCode(e.target.value)}
                          placeholder="6 位数字验证码"
                          className="tg-tabular flex-1 px-3 py-1.5 border border-slate-300 rounded-md font-mono tracking-widest focus:border-sky-600 focus:outline-none"
                        />
                        <button
                          type="submit"
                          disabled={totpBusy}
                          className="px-3.5 py-1.5 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
                        >
                          {totpBusy ? '验证中...' : '确认启用'}
                        </button>
                      </div>
                    </div>
                  </form>
                ) : (
                  <div className="p-4 rounded-lg border border-dashed border-slate-300 bg-slate-50/70 space-y-2.5">
                    <p className="text-slate-600 leading-relaxed">
                      启用双因素认证后，Web 控制台登录除用户名密码外还需提供 6 位 TOTP 动态码（不影响已签发的 Bearer API 令牌自动化调用）。
                    </p>
                    <button
                      type="button"
                      disabled={totpBusy}
                      onClick={() => void handleStartEnrollTOTP()}
                      className="inline-flex items-center gap-1.5 px-3.5 py-1.5 rounded-md bg-[#0B2A4A] hover:bg-[#123862] text-white font-medium disabled:opacity-50"
                    >
                      <ShieldCheck className="w-3.5 h-3.5" /> {totpBusy ? '生成中...' : '开始绑定验证器'}
                    </button>
                  </div>
                )
              ) : (
                <form onSubmit={handleDisableTOTP} className="space-y-3 p-3.5 rounded-lg border border-slate-200 bg-white max-w-md">
                  <div className="font-semibold text-slate-800">停用双因素认证（2FA）</div>
                  <div>
                    <label htmlFor="tg-totp-disable-pwd" className="block font-medium text-slate-700 mb-1">
                      当前登录密码
                    </label>
                    <input
                      id="tg-totp-disable-pwd"
                      type="password"
                      required
                      value={disablePwd}
                      onChange={(e) => setDisablePwd(e.target.value)}
                      className="w-full px-3 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                    />
                  </div>
                  <div>
                    <label htmlFor="tg-totp-disable-code" className="block font-medium text-slate-700 mb-1">
                      当前动态验证码或恢复码
                    </label>
                    <p className="text-[11px] text-slate-500 mb-1">
                      输入当前 6 位动态验证码或任意一个尚未使用的 8 位恢复码（XXXX-XXXX）。
                    </p>
                    <input
                      id="tg-totp-disable-code"
                      type="text"
                      inputMode="numeric"
                      autoComplete="one-time-code"
                      pattern="[0-9A-Za-z-]*"
                      maxLength={12}
                      spellCheck={false}
                      required
                      value={disableCode}
                      onChange={(e) => setDisableCode(e.target.value)}
                      placeholder="6 位数字验证码或 XXXX-XXXX"
                      className="tg-tabular w-full px-3 py-1.5 border border-slate-300 rounded-md font-mono tracking-wider focus:border-sky-500 focus:outline-none"
                    />
                  </div>
                  <button
                    type="submit"
                    disabled={totpBusy}
                    className="px-3.5 py-1.5 rounded-md border border-rose-300 bg-rose-50 hover:bg-rose-100 text-rose-800 font-medium disabled:opacity-50"
                  >
                    {totpBusy ? '处理中...' : '停用 2FA'}
                  </button>
                </form>
              )}
            </div>
          )}

          {tab === 'mcp' && (
            <div className="space-y-3 text-xs">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="inline-flex rounded-lg border border-slate-200 bg-slate-100 p-0.5">
                  <button
                    type="button"
                    onClick={() => setMcpMode('http')}
                    className={`px-3 py-1 rounded-md font-medium ${
                      mcpMode === 'http' ? 'bg-white text-slate-900 shadow-xs' : 'text-slate-600'
                    }`}
                  >
                    Streamable HTTP
                  </button>
                  <button
                    type="button"
                    onClick={() => setMcpMode('stdio')}
                    className={`px-3 py-1 rounded-md font-medium ${
                      mcpMode === 'stdio' ? 'bg-white text-slate-900 shadow-xs' : 'text-slate-600'
                    }`}
                  >
                    CLI Stdio
                  </button>
                </div>

                <div className="flex flex-wrap items-center gap-1.5">
                  <button
                    type="button"
                    disabled={creatingTok}
                    onClick={() => void handleQuickMCPToken(true)}
                    className="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-md border border-slate-300 bg-white hover:bg-slate-50 text-slate-700 font-medium disabled:opacity-50"
                  >
                    <Plus className="w-3.5 h-3.5 text-sky-600" /> 生成只读 MCP 令牌
                  </button>
                  {user.role !== 'viewer' && (
                    <button
                      type="button"
                      disabled={creatingTok}
                      onClick={() => void handleQuickMCPToken(false)}
                      className="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-md border border-sky-300 bg-sky-50 hover:bg-sky-100 text-sky-800 font-medium disabled:opacity-50"
                    >
                      <Plus className="w-3.5 h-3.5" /> 生成读写 MCP 令牌
                    </button>
                  )}
                  <button
                    type="button"
                    onClick={async () => {
                      try {
                        await navigator.clipboard.writeText(activeSnippet)
                        setCopiedMcp(true)
                        onToast('success', '已复制 mcp_config.json 配置片段')
                      } catch {
                        onToast('info', '请手动复制下方 JSON 配置')
                      }
                    }}
                    className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-[#0B2A4A] hover:bg-[#123862] text-white font-medium"
                  >
                    {copiedMcp ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />} 复制 JSON
                  </button>
                </div>
              </div>

              <pre className="p-3.5 rounded-lg bg-[#07192E] text-sky-100 font-mono text-[11px] overflow-x-auto tg-scroller-dark border border-slate-800">
                {activeSnippet}
              </pre>
            </div>
          )}

          {tab === 'password' && (
            <form onSubmit={handleChangePwd} className="space-y-3 text-xs max-w-sm">
              <div>
                <label className="block font-medium text-slate-700 mb-1">当前密码</label>
                <input
                  type="password"
                  required
                  value={curPwd}
                  onChange={(e) => setCurPwd(e.target.value)}
                  className="w-full px-3 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                />
              </div>
              <div>
                <label className="block font-medium text-slate-700 mb-1">新密码（至少 10 个字符）</label>
                <input
                  type="password"
                  required
                  value={newPwd}
                  onChange={(e) => setNewPwd(e.target.value)}
                  className="w-full px-3 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                />
              </div>
              <div>
                <label className="block font-medium text-slate-700 mb-1">确认新密码</label>
                <input
                  type="password"
                  required
                  value={confirmPwd}
                  onChange={(e) => setConfirmPwd(e.target.value)}
                  className="w-full px-3 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                />
              </div>
              <button
                type="submit"
                disabled={savingPwd}
                className="px-4 py-2 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
              >
                {savingPwd ? '保存中...' : '确认修改密码'}
              </button>
            </form>
          )}
        </div>

        <div className="px-5 py-3 bg-slate-50 border-t border-slate-200 flex items-center justify-between text-xs">
          <button
            type="button"
            onClick={async () => {
              try {
                await api.logout()
              } catch {
                // ignore
              }
              onLoggedOut()
              onClose()
            }}
            className="inline-flex items-center gap-1.5 text-rose-600 hover:text-rose-700 font-medium"
          >
            <LogOut className="w-3.5 h-3.5" /> 退出当前账号
          </button>
          <button
            type="button"
            onClick={onClose}
            className="px-3 py-1.5 rounded-md border border-slate-300 bg-white text-slate-700 hover:bg-slate-50"
          >
            关闭
          </button>
        </div>
      </div>
    </div>
  )
}
