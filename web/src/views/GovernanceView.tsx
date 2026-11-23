import { useEffect, useState, type FormEvent } from 'react'
import { Activity, Bot, FileSearch, Lock, Plus, RefreshCw, Save, Trash2, Users } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { AuditEntry, MetricsSinkConfig, SecurityPolicy, User, UserRole } from '../lib/types'

interface Props {
  currentUser: User
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

export function GovernanceView({ currentUser, onToast }: Props) {
  const isAdmin = currentUser.role === 'admin'
  const [subTab, setSubTab] = useState<'security' | 'metrics' | 'users' | 'audit'>(isAdmin ? 'users' : 'security')

  // Users CRUD state
  const [users, setUsers] = useState<User[]>([])
  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [newRole, setNewRole] = useState<UserRole>('member')
  const [savingUser, setSavingUser] = useState(false)
  const [confirmDelUid, setConfirmDelUid] = useState<number>(0)

  // Security Policy state
  const [policy, setPolicy] = useState<SecurityPolicy>({
    allowlist_enabled: false,
    allowed_targets: [],
    member_max_vus: 0,
    member_max_rps: 0,
    scripts_disabled: false,
  })
  const [allowedTargetsText, setAllowedTargetsText] = useState('')
  const [savingPolicy, setSavingPolicy] = useState(false)

  // Metrics Sink & Prometheus state
  const [sink, setSink] = useState<MetricsSinkConfig>({
    enabled: false,
    type: 'influxdb',
    url: '',
  })
  const [savingSink, setSavingSink] = useState(false)
  const [testingSink, setTestingSink] = useState(false)
  const [promPreview, setPromPreview] = useState('')
  const [loadingProm, setLoadingProm] = useState(false)

  // Audit logs state
  const [auditLogs, setAuditLogs] = useState<AuditEntry[]>([])
  const [auditUserFilter, setAuditUserFilter] = useState('')
  const [auditActionFilter, setAuditActionFilter] = useState('')
  const [auditLimit, setAuditLimit] = useState(50)

  const loadUsers = async () => {
    if (!isAdmin) return
    try {
      setUsers(await api.listUsers())
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '加载用户列表失败')
    }
  }

  const loadPolicy = async () => {
    try {
      const p = await api.getSecurityPolicy()
      setPolicy(p)
      setAllowedTargetsText((p.allowed_targets || []).join('\n'))
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '加载安全策略失败')
    }
  }

  const loadSink = async () => {
    try {
      setSink(await api.getMetricsSink())
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '加载监控外推配置失败')
    }
  }

  const loadAudit = async () => {
    if (!isAdmin) return
    try {
      setAuditLogs(
        await api.listAuditLogs({
          username: auditUserFilter.trim() || undefined,
          action: auditActionFilter.trim() || undefined,
          limit: auditLimit,
        })
      )
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '加载审计日志失败')
    }
  }

  useEffect(() => {
    if (subTab === 'users') void loadUsers()
    if (subTab === 'security') void loadPolicy()
    if (subTab === 'metrics') void loadSink()
    if (subTab === 'audit') void loadAudit()
  }, [subTab])

  const handleCreateUser = async (e: FormEvent) => {
    e.preventDefault()
    if (!newUsername.trim() || newPassword.length < 10) {
      onToast('error', '请输入用户名和至少 10 个字符的初始密码')
      return
    }
    setSavingUser(true)
    try {
      await api.createUser({ username: newUsername.trim(), password: newPassword, role: newRole })
      setNewUsername('')
      setNewPassword('')
      await loadUsers()
      onToast('success', '已创建用户账号')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '创建用户失败')
    } finally {
      setSavingUser(false)
    }
  }

  const handleSavePolicy = async (e: FormEvent) => {
    e.preventDefault()
    setSavingPolicy(true)
    try {
      const targets = allowedTargetsText
        .split(/\r?\n/)
        .map((s) => s.trim())
        .filter(Boolean)
      const saved = await api.putSecurityPolicy({
        ...policy,
        allowed_targets: targets,
      })
      setPolicy(saved)
      setAllowedTargetsText((saved.allowed_targets || []).join('\n'))
      onToast('success', '安全围栏与规模限制策略已保存生效')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '保存安全策略失败')
    } finally {
      setSavingPolicy(false)
    }
  }

  const handleSaveSink = async (e: FormEvent) => {
    e.preventDefault()
    setSavingSink(true)
    try {
      const saved = await api.putMetricsSink(sink)
      setSink(saved)
      onToast('success', '实时指标外推配置已更新')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '保存指标外推配置失败')
    } finally {
      setSavingSink(false)
    }
  }

  const handleTestSink = async () => {
    setTestingSink(true)
    try {
      await api.testMetricsSink(sink)
      onToast('success', '连通性测试成功：目标接收端点可达')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '连通性测试失败')
    } finally {
      setTestingSink(false)
    }
  }

  const handleLoadProm = async () => {
    setLoadingProm(true)
    try {
      setPromPreview(await api.fetchPrometheusMetrics())
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '读取 Prometheus 指标失败')
    } finally {
      setLoadingProm(false)
    }
  }

  return (
    <div className="flex-1 overflow-y-auto tg-scroller bg-[var(--tg-canvas)] p-5 space-y-4 text-xs">
      <div className="tg-telemetry-strip rounded-xl border border-slate-800 p-3 shadow-xs flex flex-wrap items-center justify-between gap-3">
        <div role="tablist" aria-label="系统治理页签" className="tg-tablist items-center gap-1 bg-white/10 p-1 rounded-lg">
          {isAdmin && (
            <button
              type="button"
              role="tab"
              aria-selected={subTab === 'users'}
              onClick={() => setSubTab('users')}
              className={`tg-tab inline-flex items-center gap-1 px-3 py-1.5 rounded-md font-medium ${
                subTab === 'users' ? 'bg-white text-slate-950 font-semibold shadow-xs' : 'text-slate-200 hover:bg-white/10'
              }`}
            >
              <Users className="w-3.5 h-3.5" /> 用户与角色
            </button>
          )}
          <button
            type="button"
            role="tab"
            aria-selected={subTab === 'security'}
            onClick={() => setSubTab('security')}
            className={`tg-tab inline-flex items-center gap-1 px-3 py-1.5 rounded-md font-medium ${
              subTab === 'security' ? 'bg-white text-slate-950 font-semibold shadow-xs' : 'text-slate-200 hover:bg-white/10'
            }`}
          >
            <Lock className="w-3.5 h-3.5" /> 安全围栏与 MCP
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={subTab === 'metrics'}
            onClick={() => setSubTab('metrics')}
            className={`tg-tab inline-flex items-center gap-1 px-3 py-1.5 rounded-md font-medium ${
              subTab === 'metrics' ? 'bg-white text-slate-950 font-semibold shadow-xs' : 'text-slate-200 hover:bg-white/10'
            }`}
          >
            <Activity className="w-3.5 h-3.5" /> 实时指标导出
          </button>
          {isAdmin && (
            <button
              type="button"
              role="tab"
              aria-selected={subTab === 'audit'}
              onClick={() => setSubTab('audit')}
              className={`tg-tab inline-flex items-center gap-1 px-3 py-1.5 rounded-md font-medium ${
                subTab === 'audit' ? 'bg-white text-slate-950 font-semibold shadow-xs' : 'text-slate-200 hover:bg-white/10'
              }`}
            >
              <FileSearch className="w-3.5 h-3.5" /> 安全审计日志
            </button>
          )}
        </div>
      </div>

      {subTab === 'users' && isAdmin && (
        <div className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-4">
          <form onSubmit={handleCreateUser} className="grid grid-cols-1 md:grid-cols-12 gap-2 items-end p-3 rounded-lg bg-slate-50 border border-slate-200">
            <div className="md:col-span-3">
              <label className="block font-medium text-slate-700 mb-1">用户名</label>
              <input
                type="text"
                required
                value={newUsername}
                onChange={(e) => setNewUsername(e.target.value)}
                placeholder="例如 qa-lead"
                className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white"
              />
            </div>
            <div className="md:col-span-4">
              <label className="block font-medium text-slate-700 mb-1">初始密码（至少 10 位）</label>
              <input
                type="password"
                required
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                placeholder="至少 10 个字符"
                className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white"
              />
            </div>
            <div className="md:col-span-3">
              <label className="block font-medium text-slate-700 mb-1">角色权限 (RBAC)</label>
              <select
                value={newRole}
                onChange={(e) => setNewRole(e.target.value as UserRole)}
                className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white"
              >
                <option value="member">member（项目编辑与压测执行）</option>
                <option value="viewer">viewer（只读查看结果）</option>
                <option value="admin">admin（系统管理员）</option>
              </select>
            </div>
            <div className="md:col-span-2">
              <button
                type="submit"
                disabled={savingUser}
                className="w-full inline-flex items-center justify-center gap-1 px-3 py-1.5 rounded bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
              >
                <Plus className="w-3.5 h-3.5" /> 新建用户
              </button>
            </div>
          </form>

          <div className="overflow-x-auto border border-slate-200 rounded-lg">
            <table className="w-full text-left border-collapse">
              <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
                <tr>
                  <th className="px-3 py-2">ID</th>
                  <th className="px-3 py-2">用户名</th>
                  <th className="px-3 py-2">角色</th>
                  <th className="px-3 py-2">双因素 (2FA)</th>
                  <th className="px-3 py-2">创建时间</th>
                  <th className="px-3 py-2 text-right">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {users.map((u) => (
                  <tr key={u.id}>
                    <td className="px-3 py-2 font-mono">{u.id}</td>
                    <td className="px-3 py-2 font-semibold text-slate-800">
                      {u.username}{' '}
                      {u.id === currentUser.id && (
                        <span className="ml-1 px-1.5 py-0.2 rounded bg-sky-100 text-sky-800 text-[10px]">当前账号</span>
                      )}
                    </td>
                    <td className="px-3 py-2">
                      <select
                        value={u.role}
                        onChange={async (e) => {
                          const role = e.target.value as UserRole
                          try {
                            await api.patchUser(u.id, { role })
                            await loadUsers()
                            onToast('success', `已更新 ${u.username} 角色为 ${role}`)
                          } catch (err) {
                            onToast('error', err instanceof APIError ? err.message : '更新角色失败')
                          }
                        }}
                        className="px-2 py-1 border border-slate-300 rounded bg-white font-mono"
                      >
                        <option value="admin">admin</option>
                        <option value="member">member</option>
                        <option value="viewer">viewer</option>
                      </select>
                    </td>
                    <td className="px-3 py-2">
                      {u.totp_enabled ? (
                        <span className="inline-flex items-center px-2 py-0.5 rounded bg-emerald-50 border border-emerald-200 text-emerald-800 font-medium text-[11px]">
                          已启用
                        </span>
                      ) : (
                        <span className="inline-flex items-center px-2 py-0.5 rounded bg-slate-100 text-slate-500 text-[11px]">
                          未启用
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-2 text-slate-500">{new Date(u.created_at).toLocaleString()}</td>
                    <td className="px-3 py-2 text-right">
                      <div className="inline-flex items-center gap-1.5 justify-end">
                        {u.totp_enabled && (
                          <button
                            type="button"
                            onClick={async () => {
                              try {
                                await api.adminResetUserTOTP(u.id)
                                await loadUsers()
                                onToast('success', `已重置「${u.username}」的双因素认证 (2FA)`)
                              } catch (err) {
                                onToast('error', err instanceof APIError ? err.message : '重置 2FA 失败')
                              }
                            }}
                            className="inline-flex items-center gap-1 px-2 py-1 rounded border border-amber-300 bg-amber-50 text-amber-800 hover:bg-amber-100"
                          >
                            重置 2FA
                          </button>
                        )}
                        {u.id !== currentUser.id && (
                          <button
                            type="button"
                            onClick={async () => {
                              if (confirmDelUid !== u.id) {
                                setConfirmDelUid(u.id)
                                return
                              }
                              try {
                                await api.deleteUser(u.id)
                                setConfirmDelUid(0)
                                await loadUsers()
                                onToast('success', `已删除用户「${u.username}」`)
                              } catch (err) {
                                onToast('error', err instanceof APIError ? err.message : '删除用户失败')
                              }
                            }}
                            className={`inline-flex items-center gap-1 px-2 py-1 rounded border ${
                              confirmDelUid === u.id
                                ? 'bg-rose-600 border-rose-600 text-white'
                                : 'border-rose-200 text-rose-700 hover:bg-rose-50'
                            }`}
                          >
                            <Trash2 className="w-3 h-3" /> {confirmDelUid === u.id ? '确认删除' : '删除'}
                          </button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {subTab === 'security' && (
        <form onSubmit={handleSavePolicy} className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-4">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <fieldset className="space-y-3 border border-slate-200 rounded-lg p-3">
              <legend className="text-xs font-semibold text-slate-800 px-1.5">出站目标白名单</legend>
              <label className="flex items-center gap-2 font-medium text-slate-800 cursor-pointer">
                <input
                  type="checkbox"
                  name="allowlist_enabled"
                  disabled={!isAdmin}
                  checked={policy.allowlist_enabled}
                  onChange={(e) => setPolicy({ ...policy, allowlist_enabled: e.target.checked })}
                />
                <span>启用目标域名 / 网段白名单</span>
              </label>
              <textarea
                rows={5}
                name="allowed_targets"
                disabled={!isAdmin}
                value={allowedTargetsText}
                onChange={(e) => setAllowedTargetsText(e.target.value)}
                placeholder={'*.staging.example.com\n10.20.0.0/16\napi.test.local:8443'}
                className="w-full p-2.5 border border-slate-300 rounded font-mono bg-white disabled:bg-slate-50"
              />
            </fieldset>

            <fieldset className="space-y-3 border border-slate-200 rounded-lg p-3">
              <legend className="text-xs font-semibold text-slate-800 px-1.5">成员压测上限</legend>
              <div>
                <label className="block font-medium text-slate-700 mb-1">普通成员最高并发 VUs（0 为不限）</label>
                <input
                  type="number"
                  name="member_max_vus"
                  min={0}
                  disabled={!isAdmin}
                  value={policy.member_max_vus ?? 0}
                  onChange={(e) => setPolicy({ ...policy, member_max_vus: Math.max(0, Number(e.target.value) || 0) })}
                  className="tg-tabular w-full px-2.5 py-1.5 border border-slate-300 rounded font-mono disabled:bg-slate-50"
                />
              </div>
              <div>
                <label className="block font-medium text-slate-700 mb-1">普通成员最高目标 RPS（0 为不限）</label>
                <input
                  type="number"
                  name="member_max_rps"
                  min={0}
                  step="any"
                  disabled={!isAdmin}
                  value={policy.member_max_rps ?? 0}
                  onChange={(e) => setPolicy({ ...policy, member_max_rps: Math.max(0, Number(e.target.value) || 0) })}
                  className="tg-tabular w-full px-2.5 py-1.5 border border-slate-300 rounded font-mono disabled:bg-slate-50"
                />
              </div>
              <label className="flex items-center gap-2 pt-2 text-slate-800 cursor-pointer">
                <input
                  type="checkbox"
                  name="scripts_disabled"
                  disabled={!isAdmin}
                  checked={Boolean(policy.scripts_disabled)}
                  onChange={(e) => setPolicy({ ...policy, scripts_disabled: e.target.checked })}
                />
                <span>禁止场景自定义脚本步骤</span>
              </label>
            </fieldset>
          </div>

          <fieldset className="border border-slate-200 rounded-lg p-3 space-y-3 bg-slate-50/70">
            <legend className="font-semibold text-slate-900 px-1.5 inline-flex items-center gap-1.5">
              <Bot className="w-4 h-4 text-sky-600" />
              <span>MCP 安全护栏</span>
            </legend>
            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-3">
              <label className="flex items-center gap-2 text-slate-800 cursor-pointer">
                <input
                  type="checkbox"
                  name="mcp_enabled"
                  disabled={!isAdmin}
                  checked={!policy.mcp_disabled}
                  onChange={(e) => setPolicy({ ...policy, mcp_disabled: !e.target.checked })}
                />
                <span>启用 MCP</span>
              </label>
              <label className="flex items-center gap-2 text-slate-800 cursor-pointer">
                <input
                  type="checkbox"
                  name="mcp_allow_load_runs"
                  disabled={!isAdmin}
                  checked={!policy.mcp_disable_load_runs}
                  onChange={(e) => setPolicy({ ...policy, mcp_disable_load_runs: !e.target.checked })}
                />
                <span>允许 MCP 发起压测</span>
              </label>
              <div>
                <label className="block text-[11px] font-medium text-slate-700 mb-1">
                  MCP 最高并发 VUs
                </label>
                <input
                  type="number"
                  name="mcp_max_vus"
                  min={0}
                  disabled={!isAdmin}
                  value={policy.mcp_max_vus ?? 0}
                  placeholder="200"
                  onChange={(e) => setPolicy({ ...policy, mcp_max_vus: Math.max(0, Number(e.target.value) || 0) })}
                  className="tg-tabular w-full px-2.5 py-1 border border-slate-300 rounded font-mono bg-white disabled:bg-slate-100"
                />
              </div>
              <div>
                <label className="block text-[11px] font-medium text-slate-700 mb-1">
                  MCP 最长压测秒数
                </label>
                <input
                  type="number"
                  name="mcp_max_duration_sec"
                  min={0}
                  disabled={!isAdmin}
                  value={policy.mcp_max_duration_sec ?? 0}
                  placeholder="300"
                  onChange={(e) =>
                    setPolicy({ ...policy, mcp_max_duration_sec: Math.max(0, Number(e.target.value) || 0) })
                  }
                  className="tg-tabular w-full px-2.5 py-1 border border-slate-300 rounded font-mono bg-white disabled:bg-slate-100"
                />
              </div>
            </div>
          </fieldset>

          {isAdmin && (
            <div className="flex justify-end pt-2 border-t border-slate-100">
              <button
                type="submit"
                disabled={savingPolicy}
                className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded bg-[#0B2A4A] hover:bg-[#123d69] text-white font-medium disabled:opacity-50"
              >
                <Save className="w-3.5 h-3.5" /> 保存安全策略
              </button>
            </div>
          )}
        </form>
      )}

      {subTab === 'metrics' && (
        <div className="space-y-4">
          <div className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="font-semibold text-slate-800 font-mono text-xs">/api/v1/metrics/prometheus</div>
              <button
                type="button"
                disabled={loadingProm}
                onClick={() => void handleLoadProm()}
                className="inline-flex items-center gap-1 px-3 py-1.5 rounded border border-slate-300 bg-white hover:bg-slate-50 text-slate-700 font-medium"
              >
                <RefreshCw className={`w-3.5 h-3.5 ${loadingProm ? 'animate-spin' : ''}`} /> 拉取快照
              </button>
            </div>
            {promPreview && (
              <pre className="p-3 rounded-lg bg-slate-900 text-emerald-300 font-mono text-[11px] overflow-auto max-h-56 whitespace-pre-wrap">
                {promPreview}
              </pre>
            )}
          </div>

          <form onSubmit={handleSaveSink} className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-3">
            <div className="flex items-center justify-between">
              <span className="font-semibold text-slate-800">实时异步指标外推 (InfluxDB v1/v2 或 Prometheus Pushgateway)</span>
              <label className="inline-flex items-center gap-1.5 cursor-pointer">
                <input
                  type="checkbox"
                  disabled={!isAdmin}
                  checked={sink.enabled}
                  onChange={(e) => setSink({ ...sink, enabled: e.target.checked })}
                />
                <span>启用实时外推</span>
              </label>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
              <div>
                <label className="block text-slate-600 mb-1">导出器类型</label>
                <select
                  disabled={!isAdmin}
                  value={sink.type || 'influxdb'}
                  onChange={(e) => setSink({ ...sink, type: e.target.value })}
                  className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white disabled:bg-slate-50"
                >
                  <option value="influxdb">InfluxDB Line Protocol</option>
                  <option value="prometheus_pushgateway">Prometheus Pushgateway</option>
                </select>
              </div>
              <div className="md:col-span-2">
                <label className="block text-slate-600 mb-1">目标写入 URL</label>
                <input
                  type="text"
                  disabled={!isAdmin}
                  value={sink.url}
                  onChange={(e) => setSink({ ...sink, url: e.target.value })}
                  placeholder={sink.type === 'prometheus_pushgateway' ? 'http://127.0.0.1:9091' : 'http://127.0.0.1:8086'}
                  className="w-full px-2.5 py-1.5 border border-slate-300 rounded font-mono disabled:bg-slate-50"
                />
              </div>
              {sink.type === 'influxdb' ? (
                <>
                  <div>
                    <label className="block text-slate-600 mb-1">Org（InfluxDB v2）</label>
                    <input
                      type="text"
                      disabled={!isAdmin}
                      value={sink.org || ''}
                      onChange={(e) => setSink({ ...sink, org: e.target.value || undefined })}
                      className="w-full px-2.5 py-1.5 border border-slate-300 rounded font-mono disabled:bg-slate-50"
                    />
                  </div>
                  <div>
                    <label className="block text-slate-600 mb-1">Bucket / Database</label>
                    <input
                      type="text"
                      disabled={!isAdmin}
                      value={sink.bucket || sink.database || ''}
                      onChange={(e) => setSink({ ...sink, bucket: e.target.value || undefined })}
                      placeholder="tidegate"
                      className="w-full px-2.5 py-1.5 border border-slate-300 rounded font-mono disabled:bg-slate-50"
                    />
                  </div>
                  <div>
                    <label className="block text-slate-600 mb-1">认证 Token（留空保留已存储密钥）</label>
                    <input
                      type="password"
                      disabled={!isAdmin}
                      value={sink.token || ''}
                      onChange={(e) => setSink({ ...sink, token: e.target.value || undefined })}
                      placeholder="******"
                      className="w-full px-2.5 py-1.5 border border-slate-300 rounded font-mono disabled:bg-slate-50"
                    />
                  </div>
                </>
              ) : (
                <>
                  <div>
                    <label className="block text-slate-600 mb-1">Job 名称</label>
                    <input
                      type="text"
                      disabled={!isAdmin}
                      value={sink.job || ''}
                      onChange={(e) => setSink({ ...sink, job: e.target.value || undefined })}
                      placeholder="tidegate"
                      className="w-full px-2.5 py-1.5 border border-slate-300 rounded font-mono disabled:bg-slate-50"
                    />
                  </div>
                  <div>
                    <label className="block text-slate-600 mb-1">Basic Auth 用户名（可选）</label>
                    <input
                      type="text"
                      disabled={!isAdmin}
                      value={sink.username || ''}
                      onChange={(e) => setSink({ ...sink, username: e.target.value || undefined })}
                      className="w-full px-2.5 py-1.5 border border-slate-300 rounded font-mono disabled:bg-slate-50"
                    />
                  </div>
                  <div>
                    <label className="block text-slate-600 mb-1">Token / 密码（可选）</label>
                    <input
                      type="password"
                      disabled={!isAdmin}
                      value={sink.token || ''}
                      onChange={(e) => setSink({ ...sink, token: e.target.value || undefined })}
                      className="w-full px-2.5 py-1.5 border border-slate-300 rounded font-mono disabled:bg-slate-50"
                    />
                  </div>
                </>
              )}
            </div>

            {isAdmin && (
              <div className="flex justify-end gap-2 pt-2 border-t border-slate-100">
                <button
                  type="button"
                  disabled={testingSink}
                  onClick={() => void handleTestSink()}
                  className="px-3 py-1.5 rounded border border-slate-300 bg-white hover:bg-slate-50 text-slate-700 font-medium"
                >
                  {testingSink ? '测试中...' : '测试连接'}
                </button>
                <button
                  type="submit"
                  disabled={savingSink}
                  className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded bg-[#0B2A4A] hover:bg-[#123d69] text-white font-medium disabled:opacity-50"
                >
                  <Save className="w-3.5 h-3.5" /> 保存外推配置
                </button>
              </div>
            )}
          </form>
        </div>
      )}

      {subTab === 'audit' && isAdmin && (
        <div className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <input
              type="text"
              value={auditUserFilter}
              onChange={(e) => setAuditUserFilter(e.target.value)}
              placeholder="按用户名过滤..."
              className="px-2.5 py-1.5 border border-slate-300 rounded bg-white"
            />
            <input
              type="text"
              value={auditActionFilter}
              onChange={(e) => setAuditActionFilter(e.target.value)}
              placeholder="按动作过滤（如 run.start, user.create）..."
              className="w-64 px-2.5 py-1.5 border border-slate-300 rounded font-mono bg-white"
            />
            <select
              value={auditLimit}
              onChange={(e) => setAuditLimit(Number(e.target.value))}
              className="px-2 py-1.5 border border-slate-300 rounded bg-white"
            >
              <option value={50}>最近 50 条</option>
              <option value={100}>最近 100 条</option>
              <option value={200}>最近 200 条</option>
            </select>
            <button
              type="button"
              onClick={() => void loadAudit()}
              className="inline-flex items-center gap-1 px-3 py-1.5 rounded bg-sky-600 hover:bg-sky-700 text-white font-medium"
            >
              <RefreshCw className="w-3.5 h-3.5" /> 查询日志
            </button>
          </div>

          <div className="overflow-x-auto border border-slate-200 rounded-lg">
            <table className="w-full text-left border-collapse">
              <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
                <tr>
                  <th className="px-3 py-2">时间</th>
                  <th className="px-3 py-2">操作人 (角色)</th>
                  <th className="px-3 py-2">动作</th>
                  <th className="px-3 py-2">资源对象</th>
                  <th className="px-3 py-2">来源 IP</th>
                  <th className="px-3 py-2">详情</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100 font-mono text-[11px]">
                {auditLogs.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="px-3 py-6 text-center text-slate-400 font-sans">
                      暂无符合条件的审计日志记录。
                    </td>
                  </tr>
                ) : (
                  auditLogs.map((log) => (
                    <tr key={log.id} className="hover:bg-slate-50">
                      <td className="px-3 py-2 text-slate-500 whitespace-nowrap">
                        {new Date(log.created_at).toLocaleString()}
                      </td>
                      <td className="px-3 py-2 font-sans">
                        <span className="font-semibold text-slate-800">{log.username || '-'}</span>{' '}
                        <span className="text-slate-400">({log.role || '-'})</span>
                      </td>
                      <td className="px-3 py-2 font-semibold">
                        <span
                          className={
                            log.action.startsWith('mcp.')
                              ? 'px-1.5 py-0.5 rounded bg-cyan-50 border border-cyan-200 text-cyan-800'
                              : 'text-sky-800'
                          }
                        >
                          {log.action}
                        </span>
                      </td>
                      <td className="px-3 py-2 text-slate-700">
                        {log.resource}
                        {log.resource_id ? `:${log.resource_id}` : ''}
                      </td>
                      <td className="px-3 py-2 text-slate-500">{log.remote_addr || '-'}</td>
                      <td className="px-3 py-2 max-w-xs truncate text-slate-600" title={log.detail ? JSON.stringify(log.detail) : ''}>
                        {log.detail ? JSON.stringify(log.detail) : '-'}
                      </td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}
