import { useEffect, useState, type FormEvent } from 'react'
import { Check, Copy, Key, KeyRound, Laptop, LogOut, Plus, Trash2, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { APIToken, SessionItem, User, UserRole } from '../lib/types'

interface Props {
  open: boolean
  user: User
  onClose: () => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
  onLoggedOut: () => void
}

export function AccountModal({ open, user, onClose, onToast, onLoggedOut }: Props) {
  const [tab, setTab] = useState<'sessions' | 'tokens' | 'password'>('sessions')
  const [sessions, setSessions] = useState<SessionItem[]>([])
  const [loading, setLoading] = useState(false)
  const [tokens, setTokens] = useState<APIToken[]>([])
  const [loadingTokens, setLoadingTokens] = useState(false)
  const [tokName, setTokName] = useState('')
  const [tokRole, setTokRole] = useState<UserRole>(user.role)
  const [tokTTLDays, setTokTTLDays] = useState<number>(30)
  const [creatingTok, setCreatingTok] = useState(false)
  const [newPlaintext, setNewPlaintext] = useState('')
  const [copiedTok, setCopiedTok] = useState(false)
  const [curPwd, setCurPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')
  const [confirmPwd, setConfirmPwd] = useState('')
  const [savingPwd, setSavingPwd] = useState(false)

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
    if (tab === 'tokens') void loadTokens()
  }, [open, tab])

  useEffect(() => {
    setTokRole(user.role)
  }, [user.role])

  if (!open) return null

  const allowedTokenRoles: UserRole[] =
    user.role === 'admin' ? ['viewer', 'member', 'admin'] : user.role === 'member' ? ['viewer', 'member'] : ['viewer']

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
        ttl_days: tokTTLDays > 0 ? tokTTLDays : undefined,
      })
      setNewPlaintext(res.plaintext)
      setCopiedTok(false)
      setTokName('')
      await loadTokens()
      onToast('success', '已生成新的 API 访问令牌，请妥善保存明文')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '生成 API 令牌失败')
    } finally {
      setCreatingTok(false)
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
    <div className="fixed inset-0 z-40 bg-slate-900/40 flex items-center justify-center p-4">
      <div className="w-full max-w-xl bg-white rounded-xl border border-slate-200 shadow-xl overflow-hidden">
        <div className="px-5 py-3.5 border-b border-slate-200 flex items-center justify-between bg-slate-50">
          <div>
            <h2 className="text-sm font-semibold text-slate-900">账号与会话安全</h2>
            <p className="text-xs text-slate-500">当前用户：{user.username}（{user.role}）</p>
          </div>
          <button type="button" onClick={onClose} className="p-1 text-slate-400 hover:text-slate-600 rounded">
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="px-5 pt-3 border-b border-slate-200 flex gap-4 text-xs font-medium">
          <button
            type="button"
            onClick={() => setTab('sessions')}
            className={`pb-2.5 border-b-2 transition-colors ${
              tab === 'sessions' ? 'border-sky-500 text-sky-700' : 'border-transparent text-slate-500 hover:text-slate-800'
            }`}
          >
            <Laptop className="w-3.5 h-3.5 inline mr-1" /> 活动会话
          </button>
          <button
            type="button"
            onClick={() => setTab('tokens')}
            className={`pb-2.5 border-b-2 transition-colors ${
              tab === 'tokens' ? 'border-sky-500 text-sky-700' : 'border-transparent text-slate-500 hover:text-slate-800'
            }`}
          >
            <Key className="w-3.5 h-3.5 inline mr-1" /> API 访问令牌
          </button>
          <button
            type="button"
            onClick={() => setTab('password')}
            className={`pb-2.5 border-b-2 transition-colors ${
              tab === 'password' ? 'border-sky-500 text-sky-700' : 'border-transparent text-slate-500 hover:text-slate-800'
            }`}
          >
            <KeyRound className="w-3.5 h-3.5 inline mr-1" /> 修改密码
          </button>
        </div>

        <div className="p-5 max-h-[65vh] overflow-y-auto">
          {tab === 'sessions' ? (
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
                      <p className="text-[11px] text-slate-400 mt-0.5">
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
          ) : tab === 'tokens' ? (
            <div className="space-y-4 text-xs">
              <form onSubmit={handleCreateToken} className="p-3 rounded-lg border border-slate-200 bg-slate-50/60 space-y-2.5">
                <div className="font-medium text-slate-800">生成长期 CLI / CI Bearer 令牌（tg_ 前缀）</div>
                <div className="grid grid-cols-12 gap-2 items-center">
                  <input
                    type="text"
                    value={tokName}
                    onChange={(e) => setTokName(e.target.value)}
                    placeholder="令牌用途（如 GitLab CI 每日压测）"
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
                        <div className="flex items-center gap-2 font-medium text-slate-800">
                          <span>{t.name}</span>
                          <span className="px-1.5 py-0.5 rounded bg-slate-100 text-slate-700 font-mono text-[10px]">
                            {t.prefix}...
                          </span>
                          <span className="px-1.5 py-0.5 rounded bg-sky-50 text-sky-700 font-mono text-[10px]">
                            {t.role}
                          </span>
                        </div>
                        <div className="text-[11px] text-slate-400 mt-0.5">
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
          ) : (
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
