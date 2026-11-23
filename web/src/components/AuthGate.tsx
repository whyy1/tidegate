import { useEffect, useRef, useState, type FormEvent } from 'react'
import { AlertTriangle, Check, Copy, Eye, EyeOff, KeyRound, Lock, ShieldCheck } from 'lucide-react'
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
  const [totpStep, setTotpStep] = useState(false)
  const [totpCode, setTotpCode] = useState('')
  const [showPwd, setShowPwd] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [insecureTransport, setInsecureTransport] = useState(false)
  const [copiedReset, setCopiedReset] = useState(false)
  const totpInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (totpStep) {
      totpInputRef.current?.focus()
    }
  }, [totpStep])

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
    if (!isSetup && totpStep && !totpCode.trim()) {
      setError('请输入 6 位动态验证码或恢复码')
      return
    }
    setSubmitting(true)
    try {
      const res = isSetup
        ? await api.setup({ code: code.trim(), username: username.trim(), password })
        : await api.login({
            username: username.trim(),
            password,
            totp_code: totpStep ? totpCode.trim() : undefined,
          })
      onAuthenticated(res.user)
    } catch (err) {
      if (err instanceof APIError) {
        if (!isSetup && err.code === 'totp_required') {
          setTotpStep(true)
          setTotpCode('')
          setError('')
        } else {
          setError(err.message)
          if (err.code === 'insecure_transport') {
            setInsecureTransport(true)
          }
        }
      } else {
        setError('网络请求失败，请检查服务是否在线')
      }
    } finally {
      setSubmitting(false)
    }
  }

  const resetCmd = 'docker exec -it tidegate-controller tidegate reset-password --data /data --user admin'

  return (
    <div className="tg-hero-grid min-h-dvh flex items-center justify-center p-4">
      <main className="w-full max-w-[400px] bg-white border border-slate-200 rounded-2xl shadow-xl overflow-hidden">
        <header className="tg-telemetry-strip px-6 py-4 flex items-center justify-between gap-3">
          <div className="flex items-center gap-2.5 min-w-0">
            <img src="/favicon.svg" alt="Tidegate" className="w-8 h-8 rounded-lg ring-1 ring-white/20 shrink-0" />
            <div className="min-w-0">
              <h1 className="text-base font-bold text-white leading-tight truncate">Tidegate</h1>
              <div className="text-xs text-sky-200">{isSetup ? '初始化管理员' : '登录控制台'}</div>
            </div>
          </div>
          <span className="tg-tabular px-2 py-0.5 rounded bg-white/10 border border-white/15 font-mono text-[11px] text-sky-200 shrink-0">
            {status.tls ? 'HTTPS' : 'HTTP'} · {status.version || 'dev'}
          </span>
        </header>

        <div className="p-6 space-y-4">
          <form onSubmit={handleSubmit} method="POST" className="space-y-4">
            {error && (
              <div role="alert" className="rounded-lg border border-rose-200 bg-rose-50 px-3.5 py-2.5 text-xs text-rose-800 space-y-1">
                <div className="flex items-center gap-1.5 font-medium">
                  <AlertTriangle className="w-4 h-4 shrink-0 text-rose-600" />
                  <span>{error}</span>
                </div>
                {insecureTransport && (
                  <p className="text-rose-700 leading-relaxed pl-5 font-mono text-[11px]">
                    启用 HTTPS、可信反代（TIDEGATE_TRUSTED_PROXIES）或 --allow-insecure-remote。
                  </p>
                )}
              </div>
            )}

            {isSetup && (
              <div>
                <label htmlFor="tg-setup-code" className="block text-xs font-semibold text-slate-700 mb-1.5">
                  初始化码
                </label>
                <div className="relative">
                  <KeyRound className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                  <input
                    id="tg-setup-code"
                    name="setup_code"
                    type="text"
                    required
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    autoComplete="one-time-code"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    placeholder="XXXX-XXXX-XXXX-XXXX-XXXX-XXXX"
                    className="w-full pl-9 pr-3 py-2 text-sm font-mono tracking-wider border border-slate-300 rounded-lg focus:border-sky-600 focus:ring-2 focus:ring-sky-500/20 focus:outline-none"
                  />
                </div>
              </div>
            )}

            {(!totpStep || isSetup) ? (
              <>
                <div>
                  <label htmlFor="tg-auth-username" className="block text-xs font-semibold text-slate-700 mb-1.5">
                    用户名
                  </label>
                  <div className="relative">
                    <ShieldCheck className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                    <input
                      id="tg-auth-username"
                      name="username"
                      type="text"
                      required
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      autoComplete="username"
                      value={username}
                      onChange={(e) => setUsername(e.target.value)}
                      placeholder={isSetup ? 'admin' : '用户名'}
                      className="w-full pl-9 pr-3 py-2 text-sm border border-slate-300 rounded-lg focus:border-sky-600 focus:ring-2 focus:ring-sky-500/20 focus:outline-none"
                    />
                  </div>
                </div>

                <div>
                  <label htmlFor="tg-auth-password" className="block text-xs font-semibold text-slate-700 mb-1.5">
                    {isSetup ? '密码（至少 10 位）' : '密码'}
                  </label>
                  <div className="relative">
                    <Lock className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                    <input
                      id="tg-auth-password"
                      name="password"
                      type={showPwd ? 'text' : 'password'}
                      required
                      autoComplete={isSetup ? 'new-password' : 'current-password'}
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      placeholder="密码"
                      className="w-full pl-9 pr-10 py-2 text-sm border border-slate-300 rounded-lg focus:border-sky-600 focus:ring-2 focus:ring-sky-500/20 focus:outline-none"
                    />
                    <button
                      type="button"
                      aria-pressed={showPwd}
                      onClick={() => setShowPwd((v) => !v)}
                      aria-label={showPwd ? '隐藏密码' : '显示密码'}
                      className="absolute right-2.5 top-2 p-0.5 rounded text-slate-400 hover:text-slate-700"
                    >
                      {showPwd ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                    </button>
                  </div>
                </div>
              </>
            ) : (
              <div className="space-y-3">
                <div className="flex items-center justify-between rounded-lg bg-slate-50 border border-slate-200 px-3 py-2 text-xs">
                  <span className="font-mono text-slate-900 font-semibold">{username}</span>
                  <button
                    type="button"
                    onClick={() => {
                      setTotpStep(false)
                      setTotpCode('')
                      setError('')
                    }}
                    className="text-sky-700 hover:underline font-medium"
                  >
                    切换账号
                  </button>
                </div>

                <div>
                  <label htmlFor="tg-auth-totp" className="block text-xs font-semibold text-slate-700 mb-1.5">
                    动态验证码（6 位数字）或恢复码
                  </label>
                  <div className="relative">
                    <KeyRound className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                    <input
                      ref={totpInputRef}
                      id="tg-auth-totp"
                      name="totp_code"
                      type="text"
                      inputMode="numeric"
                      autoComplete="one-time-code"
                      pattern="[0-9A-Za-z-]*"
                      maxLength={12}
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      required
                      value={totpCode}
                      onChange={(e) => setTotpCode(e.target.value)}
                      placeholder="6 位数字或 XXXX-XXXX"
                      className="tg-tabular w-full pl-9 pr-3 py-2 text-sm font-mono tracking-widest border border-slate-300 rounded-lg focus:border-sky-600 focus:ring-2 focus:ring-sky-500/20 focus:outline-none"
                    />
                  </div>
                </div>
              </div>
            )}

            {isSetup && (
              <div>
                <label htmlFor="tg-auth-confirm" className="block text-xs font-semibold text-slate-700 mb-1.5">
                  确认密码
                </label>
                <div className="relative">
                  <Lock className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                  <input
                    id="tg-auth-confirm"
                    name="confirm_password"
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
              {submitting ? '验证中...' : isSetup ? '完成初始化' : totpStep ? '验证并登录' : '登录'}
            </button>
          </form>

          {!isSetup && (
            <details className="border-t border-slate-100 pt-3 text-xs text-slate-500">
              <summary className="cursor-pointer hover:text-slate-800 select-none font-medium">
                忘记密码 / 命令行重置
              </summary>
              <div className="mt-2 space-y-1.5">
                <div className="flex items-center gap-1.5 rounded bg-slate-900 px-2.5 py-2 text-[11px] font-mono text-sky-100">
                  <code className="flex-1 overflow-x-auto whitespace-nowrap">{resetCmd}</code>
                  <button
                    type="button"
                    aria-label="复制重置密码命令"
                    onClick={async () => {
                      try {
                        await navigator.clipboard.writeText(resetCmd)
                        setCopiedReset(true)
                        window.setTimeout(() => setCopiedReset(false), 1500)
                      } catch {
                        // ignore
                      }
                    }}
                    className="p-1 rounded bg-white/10 hover:bg-white/20 text-white shrink-0"
                  >
                    {copiedReset ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
                  </button>
                </div>
                <div className="text-[11px] font-mono text-slate-500">
                  ENV: TIDEGATE_ADMIN_PASSWORD
                </div>
              </div>
            </details>
          )}
        </div>
      </main>
    </div>
  )
}
