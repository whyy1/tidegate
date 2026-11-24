import { useEffect, useRef, useState, type FormEvent } from 'react'
import {
  AlertTriangle,
  ArrowLeft,
  Boxes,
  Check,
  Copy,
  Eye,
  EyeOff,
  KeyRound,
  Lock,
  Network,
  ShieldCheck,
  ShieldHalf,
  Terminal,
  User as UserIcon,
  Workflow,
} from 'lucide-react'
import { api, APIError } from '../lib/api'
import { copyText } from '../lib/download'
import { useGlassStagePhysics } from '../lib/glassPhysics'
import type { SetupStatus, User } from '../lib/types'
import { ThemeToggle } from './ThemeToggle'

interface Props {
  status: SetupStatus
  onAuthenticated: (user: User) => void
}

/* ---------- Tidal telemetry visual (deterministic, decorative) ---------- */

const W = 560
const H = 220
const BASE = 150

function tide(x: number): number {
  const t = x / W
  const swell = Math.sin(t * Math.PI * 2.2 - 0.6) * 34
  const chop = Math.sin(t * Math.PI * 9.5 + 1.1) * 7 + Math.sin(t * Math.PI * 23 + 0.4) * 3
  const ramp = Math.min(1, t * 3.2) // ramp-up stage
  return BASE - (52 + swell + chop) * ramp
}

const tidePts: Array<[number, number]> = Array.from({ length: 113 }, (_, i) => {
  const x = (i / 112) * W
  return [x, tide(x)]
})
const tideLine = tidePts.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`).join(' ')
const tideArea = `${tideLine} L${W} ${BASE} L0 ${BASE} Z`
const p95Line = tidePts
  .map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)} ${(y - 18 - Math.sin(x / 31) * 4).toFixed(1)}`)
  .join(' ')

// latency pulses below the baseline
const pulses = Array.from({ length: 56 }, (_, i) => {
  const x = 6 + i * 10
  const h = 6 + Math.abs(Math.sin(i * 1.7) * 14) + (i % 13 === 7 ? 18 : 0)
  return { x, h, hot: i % 13 === 7 }
})

function TidalScope() {
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="w-full h-auto" role="img" aria-label="吞吐与延时示意">
      <defs>
        <linearGradient id="tg-tide-fill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#38bdf8" stopOpacity="0.38" />
          <stop offset="100%" stopColor="#38bdf8" stopOpacity="0" />
        </linearGradient>
        <linearGradient id="tg-scan" x1="0" y1="0" x2="1" y2="0">
          <stop offset="0%" stopColor="#7dd3fc" stopOpacity="0" />
          <stop offset="100%" stopColor="#7dd3fc" stopOpacity="0.5" />
        </linearGradient>
        <clipPath id="tg-scope-clip">
          <rect x="0" y="0" width={W} height={H} />
        </clipPath>
      </defs>

      {/* graduated scale */}
      <g stroke="rgb(148 163 184 / 0.22)" strokeWidth="1">
        {Array.from({ length: 29 }, (_, i) => (
          <line key={i} x1={i * 20} x2={i * 20} y1={BASE} y2={BASE + (i % 5 === 0 ? 9 : 4)} />
        ))}
        <line x1="0" x2={W} y1={BASE} y2={BASE} stroke="rgb(148 163 184 / 0.35)" />
        {[40, 80, 120].map((y) => (
          <line key={y} x1="0" x2={W} y1={y} y2={y} strokeDasharray="2 6" />
        ))}
      </g>

      <g clipPath="url(#tg-scope-clip)">
        <path d={tideArea} fill="url(#tg-tide-fill)" />
        <path d={tideLine} fill="none" stroke="#38bdf8" strokeWidth="2" strokeLinejoin="round" />
        <path d={p95Line} fill="none" stroke="#fbbf24" strokeWidth="1.25" strokeDasharray="4 4" opacity="0.85" />

        {pulses.map((p) => (
          <rect
            key={p.x}
            x={p.x}
            y={BASE + 14}
            width="4"
            height={p.h}
            rx="1"
            fill={p.hot ? '#fb7185' : '#22d3ee'}
            opacity={p.hot ? 0.9 : 0.45}
          />
        ))}

        <g className="tg-scope-scan">
          <rect x="-60" y="0" width="60" height={H} fill="url(#tg-scan)" opacity="0.35" />
          <line x1="0" x2="0" y1="0" y2={H} stroke="#7dd3fc" strokeWidth="1" opacity="0.8" />
        </g>
      </g>

      <g fontSize="10" fontFamily="ui-monospace, SFMono-Regular, Menlo, monospace" fill="#94a3b8">
        <text x="4" y="14">RPS</text>
        <text x="4" y={H - 4}>LATENCY</text>
      </g>
      <g fontSize="10" fontFamily="ui-monospace, SFMono-Regular, Menlo, monospace">
        <rect x={W - 118} y="4" width="8" height="2" fill="#38bdf8" />
        <text x={W - 106} y="9" fill="#cbd5e1">吞吐</text>
        <rect x={W - 70} y="4" width="8" height="2" fill="#fbbf24" />
        <text x={W - 58} y="9" fill="#cbd5e1">P95</text>
      </g>
    </svg>
  )
}

/* ---------- Copyable command card ---------- */

function CommandCard({
  icon: Icon,
  title,
  tag,
  command,
}: {
  icon: typeof Terminal
  title: string
  tag: string
  command: string
}) {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const t = window.setTimeout(() => setCopied(false), 1600)
    return () => window.clearTimeout(t)
  }, [copied])
  return (
    <section className="rounded-xl border border-line overflow-hidden">
      <header className="flex items-center gap-2 px-3.5 py-2.5 bg-surface-subtle border-b border-line">
        <Icon className="w-4 h-4 text-tidal shrink-0" />
        <h3 className="min-w-0 flex-1 truncate text-[13px] font-semibold text-fg">{title}</h3>
        <span className="shrink-0 rounded border border-line px-1.5 py-px font-mono text-[10px] text-fg-subtle">{tag}</span>
      </header>
      <div className="tg-code-surface relative">
        <pre className="m-0 overflow-x-hidden px-3.5 py-3 pr-12 font-mono text-[12px] leading-relaxed text-sky-100 whitespace-pre-wrap break-words">
          {command}
        </pre>
        <button
          type="button"
          onClick={async () => setCopied(await copyText(command))}
          aria-label={copied ? '已复制' : `复制：${title}`}
          title={copied ? '已复制' : '复制'}
          className={`absolute right-2 top-2 inline-flex h-7 w-7 items-center justify-center rounded-md border transition-colors ${
            copied
              ? 'border-emerald-400/40 bg-emerald-400/15 text-emerald-300'
              : 'border-white/10 bg-white/5 text-slate-300 hover:bg-white/15 hover:text-white'
          }`}
        >
          {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
        </button>
      </div>
    </section>
  )
}

/* ---------- Gate ---------- */

const inputCls =
  'w-full h-10 pl-9 pr-3 text-sm rounded-lg border border-line-strong bg-surface text-fg placeholder:text-fg-subtle/70 focus:border-sky-500 focus:ring-2 focus:ring-sky-500/20 focus:outline-none transition-shadow'
const labelCls = 'block text-xs font-semibold text-fg-muted mb-1.5'
const iconCls = 'w-4 h-4 text-fg-subtle absolute left-3 top-3 pointer-events-none'

const FEATURES = [
  { icon: Workflow, title: '多协议工作台', spec: 'HTTP · WebSocket · gRPC · SSE' },
  { icon: Network, title: '分布式 mTLS 集群', spec: '证书节点 · 加权调度 · 实时遥测' },
  { icon: ShieldHalf, title: '安全 MCP 网关', spec: 'Scoped 令牌 · 白名单 · 审计' },
]

export function AuthGate({ status, onAuthenticated }: Props) {
  const isSetup = status.setup_required
  const [view, setView] = useState<'auth' | 'reset'>('auth')
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
  const totpInputRef = useRef<HTMLInputElement>(null)
  const firstFieldRef = useRef<HTMLInputElement>(null)
  const backRef = useRef<HTMLButtonElement>(null)
  const authStageRef = useRef<HTMLDivElement>(null)
  useGlassStagePhysics(authStageRef)

  useEffect(() => {
    if (totpStep) totpInputRef.current?.focus()
  }, [totpStep])

  useEffect(() => {
    if (view === 'reset') backRef.current?.focus()
  }, [view])

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
          if (err.code === 'insecure_transport') setInsecureTransport(true)
        }
      } else {
        setError('网络请求失败，请检查服务是否在线')
      }
    } finally {
      setSubmitting(false)
    }
  }

  const dockerCmd = [
    "TIDEGATE_ADMIN_PASSWORD='<新密码>' \\",
    'TIDEGATE_ADMIN_RESET_TOTP=true \\',
    'docker compose up -d \\',
    '  --force-recreate tidegate',
  ].join('\n')
  const cliCmd = [
    'docker exec -it tidegate-controller \\',
    '  tidegate reset-password --data /data \\',
    '  --user admin \\',
    "  --password '<新密码>' --reset-totp",
    '',
    '# 非容器部署',
    'tidegate reset-password --data ./data \\',
    '  --user admin \\',
    "  --password '<新密码>' --reset-totp",
  ].join('\n')

  const transport = status.tls ? 'HTTPS' : 'HTTP'

  return (
    <div ref={authStageRef} className="min-h-dvh grid lg:grid-cols-[1.08fr_1fr] tg-mesh-canvas">
      {/* ---------------- Deep-sea visual column ---------------- */}
      <aside className="tg-hero-grid relative hidden lg:flex flex-col justify-between overflow-hidden px-12 py-10 xl:px-16">
        <div className="flex items-center gap-3">
          <img src="/favicon.svg" alt="" className="w-9 h-9 rounded-lg ring-1 ring-white/20" />
          <div className="leading-tight">
            <div className="text-lg font-semibold tracking-tight text-white">Tidegate</div>
            <div className="text-xs text-slate-400">接口调试与分布式压测</div>
          </div>
        </div>

        <div className="my-10 max-w-[600px] w-full">
          <div className="rounded-2xl border border-white/10 bg-[#061426]/70 p-5 shadow-[0_30px_80px_-30px_rgb(2_132_199/0.45)] backdrop-blur-sm">
            <div className="mb-3 flex items-center gap-2 text-[11px] font-mono text-slate-400">
              <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 shadow-[0_0_8px_2px_rgb(52_211_153/0.6)]" />
              <span className="text-slate-300">ramp-up</span>
              <span className="text-slate-600">/</span>
              <span className="text-slate-300">steady</span>
            </div>
            <TidalScope />
          </div>

          <ul className="mt-6 grid grid-cols-3 gap-3">
            {FEATURES.map(({ icon: Icon, title, spec }) => (
              <li key={title} className="rounded-xl border border-white/10 bg-white/[0.035] p-3.5 hover:border-sky-400/30 transition-colors">
                <Icon className="mb-2.5 h-4 w-4 text-sky-300" />
                <div className="text-[13px] font-semibold text-white">{title}</div>
                <div className="mt-1 text-[11px] leading-snug text-slate-400">{spec}</div>
              </li>
            ))}
          </ul>
        </div>

        <footer className="flex items-center justify-between gap-3 border-t border-white/10 pt-4 text-[11px] font-mono text-slate-400">
          <span className="tg-tabular">v{status.version || 'dev'}</span>
          <span
            className={`inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 ${
              status.tls
                ? 'border-emerald-400/30 bg-emerald-400/10 text-emerald-300'
                : 'border-amber-400/30 bg-amber-400/10 text-amber-300'
            }`}
          >
            <Lock className="h-3 w-3" />
            {transport}
          </span>
        </footer>
      </aside>

      {/* ---------------- Auth column ---------------- */}
      <main className="relative flex min-h-dvh flex-col items-center justify-center px-5 py-16">
        <div className="absolute right-4 top-4 flex items-center gap-2">
          <span
            className={`lg:hidden inline-flex items-center gap-1 rounded-full border px-2 py-0.5 font-mono text-[11px] ${
              status.tls ? 'border-emerald-200 bg-emerald-50 text-emerald-700' : 'border-amber-200 bg-amber-50 text-amber-700'
            }`}
          >
            <Lock className="h-3 w-3" />
            {transport}
          </span>
          <ThemeToggle />
        </div>

        <div className="w-full max-w-[400px]">
          <div className="mb-6 flex items-center gap-2.5 lg:hidden">
            <img src="/favicon.svg" alt="" className="h-8 w-8 rounded-lg" />
            <span className="text-base font-semibold text-fg">Tidegate</span>
          </div>

          <div className="tg-glass-card tg-tilt-card p-7">
            {view === 'auth' ? (
              <div key="auth" className="relative z-10 tg-swap-in">
                <header className="mb-6">
                  <h1 className="text-xl font-semibold tracking-tight text-fg">
                    {isSetup ? '初始化管理员' : totpStep ? '双因素验证' : '登录'}
                  </h1>
                  {totpStep && !isSetup && (
                    <div className="mt-3 flex items-center justify-between rounded-lg border border-line bg-surface-subtle px-3 py-2 text-xs">
                      <span className="flex min-w-0 items-center gap-1.5">
                        <UserIcon className="h-3.5 w-3.5 shrink-0 text-fg-subtle" />
                        <span className="truncate font-mono font-semibold text-fg">{username}</span>
                      </span>
                      <button
                        type="button"
                        onClick={() => {
                          setTotpStep(false)
                          setTotpCode('')
                          setError('')
                        }}
                        className="shrink-0 font-medium text-tidal hover:underline"
                      >
                        切换账号
                      </button>
                    </div>
                  )}
                </header>

                <form onSubmit={handleSubmit} method="POST" className="space-y-4">
                  {error && (
                    <div role="alert" className="space-y-1 rounded-lg border border-rose-200 bg-rose-50 px-3.5 py-2.5 text-xs text-rose-800">
                      <div className="flex items-center gap-1.5 font-medium">
                        <AlertTriangle className="h-4 w-4 shrink-0 text-rose-600" />
                        <span>{error}</span>
                      </div>
                      {insecureTransport && (
                        <p className="pl-5 font-mono text-[11px] leading-relaxed text-rose-700">
                          HTTPS / TIDEGATE_TRUSTED_PROXIES / --allow-insecure-remote
                        </p>
                      )}
                    </div>
                  )}

                  {isSetup && (
                    <div>
                      <label htmlFor="tg-setup-code" className={labelCls}>
                        初始化码
                      </label>
                      <div className="relative">
                        <KeyRound className={iconCls} />
                        <input
                          ref={firstFieldRef}
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
                          className={`${inputCls} font-mono tracking-wider`}
                        />
                      </div>
                    </div>
                  )}

                  {!totpStep || isSetup ? (
                    <>
                      <div>
                        <label htmlFor="tg-auth-username" className={labelCls}>
                          用户名
                        </label>
                        <div className="relative">
                          <ShieldCheck className={iconCls} />
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
                            placeholder={isSetup ? 'admin' : ''}
                            className={inputCls}
                          />
                        </div>
                      </div>

                      <div>
                        <div className="mb-1.5 flex items-center justify-between gap-2">
                          <label htmlFor="tg-auth-password" className="text-xs font-semibold text-fg-muted">
                            密码
                          </label>
                          {isSetup ? (
                            <span className="tg-tabular font-mono text-[11px] text-fg-subtle">≥ 10</span>
                          ) : (
                            <button
                              type="button"
                              onClick={() => {
                                setError('')
                                setView('reset')
                              }}
                              className="rounded text-xs font-medium text-tidal hover:underline focus-visible:outline-2 focus-visible:outline-sky-500"
                            >
                              忘记密码？
                            </button>
                          )}
                        </div>
                        <div className="relative">
                          <Lock className={iconCls} />
                          <input
                            id="tg-auth-password"
                            name="password"
                            type={showPwd ? 'text' : 'password'}
                            required
                            autoComplete={isSetup ? 'new-password' : 'current-password'}
                            value={password}
                            onChange={(e) => setPassword(e.target.value)}
                            className={`${inputCls} pr-10`}
                          />
                          <button
                            type="button"
                            aria-pressed={showPwd}
                            onClick={() => setShowPwd((v) => !v)}
                            aria-label={showPwd ? '隐藏密码' : '显示密码'}
                            className="absolute right-2 top-2 rounded-md p-1 text-fg-subtle hover:bg-surface-hover hover:text-fg"
                          >
                            {showPwd ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                          </button>
                        </div>
                      </div>
                    </>
                  ) : (
                    <div>
                      <label htmlFor="tg-auth-totp" className={labelCls}>
                        动态验证码或恢复码
                      </label>
                      <div className="relative">
                        <KeyRound className={iconCls} />
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
                          placeholder="000000"
                          className={`${inputCls} tg-tabular font-mono text-base tracking-[0.35em]`}
                        />
                      </div>
                    </div>
                  )}

                  {isSetup && (
                    <div>
                      <label htmlFor="tg-auth-confirm" className={labelCls}>
                        确认密码
                      </label>
                      <div className="relative">
                        <Lock className={iconCls} />
                        <input
                          id="tg-auth-confirm"
                          name="confirm_password"
                          type={showPwd ? 'text' : 'password'}
                          required
                          autoComplete="new-password"
                          value={confirmPwd}
                          onChange={(e) => setConfirmPwd(e.target.value)}
                          className={inputCls}
                        />
                      </div>
                    </div>
                  )}

                  <button
                    type="submit"
                    disabled={submitting}
                    className="mt-2 h-10 w-full rounded-lg bg-primary text-sm font-semibold text-white shadow-xs transition-colors hover:bg-primary-hover disabled:opacity-50"
                  >
                    {submitting ? '验证中…' : isSetup ? '完成初始化' : totpStep ? '验证并登录' : '登录'}
                  </button>
                </form>
              </div>
            ) : (
              <div key="reset" className="relative z-10 tg-swap-in">
                <button
                  ref={backRef}
                  type="button"
                  onClick={() => setView('auth')}
                  className="mb-4 -ml-1 inline-flex items-center gap-1.5 rounded-md px-1 py-0.5 text-xs font-medium text-fg-muted hover:text-fg focus-visible:outline-2 focus-visible:outline-sky-500"
                >
                  <ArrowLeft className="h-3.5 w-3.5" />
                  返回登录
                </button>
                <h1 className="mb-5 text-xl font-semibold tracking-tight text-fg">管理员凭据重置</h1>
                <div className="space-y-3.5">
                  <CommandCard icon={Boxes} title="容器重启同步" tag="TIDEGATE_ADMIN_PASSWORD" command={dockerCmd} />
                  <CommandCard icon={Terminal} title="命令行离线重置" tag="reset-password" command={cliCmd} />
                </div>
              </div>
            )}
          </div>
        </div>
      </main>
    </div>
  )
}
