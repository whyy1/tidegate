import { useState, type FormEvent } from 'react'
import { AlertTriangle, Eye, EyeOff, KeyRound, Lock, Radio, Server, ShieldCheck } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { SetupStatus, User } from '../lib/types'

interface Props {
  status: SetupStatus
  onAuthenticated: (user: User) => void
}

export function AuthGate({ status, onAuthenticated }: Props) {
  const isSetup = status.setup_required
  const [code, setCode] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPwd, setConfirmPwd] = useState('')
  const [showPwd, setShowPwd] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [insecureTransport, setInsecureTransport] = useState(false)

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setInsecureTransport(false)
    if (isSetup) {
      if (password.length < 10) {
        setError('管理员密码长度至少为 10 位')
        return
      }
      if (password !== confirmPwd) {
        setError('两次输入的密码不一致')
        return
      }
    }
    setSubmitting(true)
    try {
      const res = isSetup
        ? await api.setup({ code: code.trim(), username: username.trim(), password })
        : await api.login({ username: username.trim(), password })
      onAuthenticated(res.user)
    } catch (err) {
      if (err instanceof APIError) {
        setError(err.message)
        if (err.code === 'insecure_transport') {
          setInsecureTransport(true)
        }
      } else {
        setError('网络请求失败，请检查服务是否在线')
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="min-h-dvh grid lg:grid-cols-[1.08fr_1fr] bg-[#F3F6FA]">
      {/* Left Architectural Tidal Control Column */}
      <section className="tg-hero-grid text-white p-7 lg:p-12 flex flex-col justify-between relative overflow-hidden">
        <div className="flex items-center gap-3 relative z-10">
          <img src="/favicon.svg" alt="Tidegate" className="w-10 h-10 rounded-xl ring-1 ring-white/20 shadow-lg" />
          <div>
            <div className="text-lg font-bold tracking-tight">Tidegate</div>
            <div className="text-xs font-mono text-sky-300/90">PRECISION API &amp; LOAD CONTROL ROOM</div>
          </div>
        </div>

        <div className="my-8 lg:my-0 space-y-6 relative z-10 max-w-lg">
          <div className="inline-flex items-center gap-2 px-3 py-1 rounded-full bg-white/8 border border-white/15 text-xs font-mono text-sky-200">
            <Radio className="w-3.5 h-3.5 text-cyan-400" />
            <span>单二进制零依赖 · 离线原生部署</span>
          </div>
          <h1 className="text-2xl lg:text-3xl font-bold tracking-tight leading-snug text-white">
            接口调试、场景编排与
            <br />
            零信任分布式压测一体化控制台
          </h1>
          <p className="text-sm text-slate-300 leading-relaxed">
            由 Go 确定性高性能引擎驱动，内置 mTLS 分布式节点 CA、AES-256-GCM 凭证金库、HDR 延迟直方图以及安全受控的 MCP 智能体协议接入。
          </p>

          <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 pt-2">
            <div className="p-3 rounded-lg bg-white/6 border border-white/10">
              <div className="text-xs font-semibold text-sky-300">多协议工作台</div>
              <div className="text-[11px] text-slate-300 mt-1">HTTP/2 · WS · gRPC · SSE 五段耗时透视</div>
            </div>
            <div className="p-3 rounded-lg bg-white/6 border border-white/10">
              <div className="text-xs font-semibold text-emerald-300">分布式 mTLS 集群</div>
              <div className="text-[11px] text-slate-300 mt-1">自动重分配与实时硬件遥测</div>
            </div>
            <div className="p-3 rounded-lg bg-white/6 border border-white/10">
              <div className="text-xs font-semibold text-cyan-300">安全 MCP 网关</div>
              <div className="text-[11px] text-slate-300 mt-1">RBAC · 密文脱敏 · 规模硬护栏</div>
            </div>
          </div>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-slate-400 border-t border-white/10 pt-4 relative z-10 font-mono">
          <span>版本：{status.version || 'dev'}</span>
          <span className="inline-flex items-center gap-1.5 text-slate-300">
            <Server className="w-3.5 h-3.5 text-sky-400" />
            {status.tls ? 'HTTPS TLS 传输已加密' : 'HTTP 本机安全回环通道'}
          </span>
        </div>
      </section>

      {/* Right Authentication Form */}
      <main className="flex items-center justify-center p-6 lg:p-10">
        <div className="w-full max-w-md bg-white border border-slate-200 rounded-2xl shadow-sm p-6 sm:p-8 space-y-5">
          <div>
            <div className="text-xs font-mono uppercase tracking-wider text-sky-700 font-semibold">
              {isSetup ? 'Initial Setup' : 'Operator Sign In'}
            </div>
            <h2 className="text-xl font-bold text-slate-900 mt-1">
              {isSetup ? '初始化系统管理员账号' : '登录 Tidegate 控制台'}
            </h2>
            <p className="text-xs text-slate-500 mt-1">
              {isSetup
                ? '请输入服务启动终端打印的一次性初始化口令并设置强密码。'
                : '使用管理员或团队成员账号登录以管理接口、场景与压测任务。'}
            </p>
          </div>

          <form onSubmit={handleSubmit} className="space-y-4">
            {error && (
              <div className="rounded-lg border border-rose-200 bg-rose-50 px-3.5 py-2.5 text-xs text-rose-800 space-y-1">
                <div className="flex items-center gap-1.5 font-medium">
                  <AlertTriangle className="w-4 h-4 shrink-0 text-rose-600" />
                  <span>{error}</span>
                </div>
                {insecureTransport && (
                  <p className="text-rose-700 leading-relaxed pl-5">
                    非本机访问拒绝明文发送凭据：请启用 `--tls-self-signed` / `--tls-cert`，或仅在受信任内网使用 `--allow-insecure-remote`。
                  </p>
                )}
              </div>
            )}

            {isSetup && (
              <div>
                <label htmlFor="tg-setup-code" className="block text-xs font-semibold text-slate-700 mb-1.5">
                  终端输出的一次性初始化码
                </label>
                <div className="relative">
                  <KeyRound className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                  <input
                    id="tg-setup-code"
                    type="text"
                    required
                    autoComplete="one-time-code"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    placeholder="例如 XXXX-XXXX-XXXX-XXXX-XXXX-XXXX"
                    className="w-full pl-9 pr-3 py-2 text-sm font-mono tracking-wider border border-slate-300 rounded-lg focus:border-sky-600 focus:ring-2 focus:ring-sky-500/20 focus:outline-none"
                  />
                </div>
              </div>
            )}

            <div>
              <label htmlFor="tg-auth-username" className="block text-xs font-semibold text-slate-700 mb-1.5">
                用户名
              </label>
              <div className="relative">
                <ShieldCheck className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                <input
                  id="tg-auth-username"
                  type="text"
                  required
                  autoComplete="username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder={isSetup ? '例如 admin' : '输入用户名'}
                  className="w-full pl-9 pr-3 py-2 text-sm border border-slate-300 rounded-lg focus:border-sky-600 focus:ring-2 focus:ring-sky-500/20 focus:outline-none"
                />
              </div>
            </div>

            <div>
              <label htmlFor="tg-auth-password" className="block text-xs font-semibold text-slate-700 mb-1.5">
                密码{isSetup ? '（至少 10 个字符）' : ''}
              </label>
              <div className="relative">
                <Lock className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                <input
                  id="tg-auth-password"
                  type={showPwd ? 'text' : 'password'}
                  required
                  autoComplete={isSetup ? 'new-password' : 'current-password'}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="输入密码"
                  className="w-full pl-9 pr-10 py-2 text-sm border border-slate-300 rounded-lg focus:border-sky-600 focus:ring-2 focus:ring-sky-500/20 focus:outline-none"
                />
                <button
                  type="button"
                  onClick={() => setShowPwd((v) => !v)}
                  aria-label={showPwd ? '隐藏密码' : '显示密码'}
                  className="absolute right-2.5 top-2 p-0.5 rounded text-slate-400 hover:text-slate-700"
                >
                  {showPwd ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                </button>
              </div>
            </div>

            {isSetup && (
              <div>
                <label htmlFor="tg-auth-confirm" className="block text-xs font-semibold text-slate-700 mb-1.5">
                  确认密码
                </label>
                <div className="relative">
                  <Lock className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                  <input
                    id="tg-auth-confirm"
                    type={showPwd ? 'text' : 'password'}
                    required
                    autoComplete="new-password"
                    value={confirmPwd}
                    onChange={(e) => setConfirmPwd(e.target.value)}
                    placeholder="再次输入密码"
                    className="w-full pl-9 pr-3 py-2 text-sm border border-slate-300 rounded-lg focus:border-sky-600 focus:ring-2 focus:ring-sky-500/20 focus:outline-none"
                  />
                </div>
              </div>
            )}

            <button
              type="submit"
              disabled={submitting}
              className="w-full py-2.5 rounded-lg bg-[#0B2A4A] hover:bg-[#123862] text-white text-sm font-semibold shadow-xs transition-colors disabled:opacity-50"
            >
              {submitting ? '正在验证并进入系统...' : isSetup ? '完成初始化并进入控制台' : '登录控制台'}
            </button>
          </form>
        </div>
      </main>
    </div>
  )
}
