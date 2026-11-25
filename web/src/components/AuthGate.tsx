import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  AlertTriangle,
  Eye,
  EyeOff,
  KeyRound,
  Lock,
  ShieldCheck,
  User as UserIcon,
} from 'lucide-react'
import { api, APIError } from '../lib/api'
import { useGlassStagePhysics } from '../lib/glassPhysics'
import type { SetupStatus, User } from '../lib/types'
import { ThemeToggle } from './ThemeToggle'

interface Props {
  status: SetupStatus
  onAuthenticated: (user: User) => void
}

/* ---------- V1 Interactive Tidal Gate Visual & Pain-Point Switcher ---------- */

const GATE_W = 600
const GATE_H = 130
const GATE_BASE = 112

function computeTidePaths(rps: number) {
  const amp = 20 + (rps / 265000) * 72
  const pts: string[] = []
  for (let i = 0; i <= 60; i++) {
    const x = i * 10
    const t = i / 60
    const env = t < 0.25 ? t / 0.25 : t > 0.8 ? (1 - t) / 0.2 : 1
    const y = GATE_BASE - amp * env - Math.sin(i * 0.45) * 4 * env
    pts.push(`${i === 0 ? 'M' : 'L'}${x} ${y.toFixed(1)}`)
  }
  const line = pts.join(' ')
  const area = `${line} L${GATE_W} ${GATE_BASE} L0 ${GATE_BASE} Z`
  const gateHeight = 58 - (rps / 265000) * 40
  return { line, area, gateHeight }
}

interface PainItem {
  id: string
  tab: string
  lead: string
  detail: string
}

const PAIN_ITEMS: PainItem[] = [
  {
    id: 'unity',
    tab: '工具一体化',
    lead: '接口管理和压测不再来回切换：',
    detail:
      '同一份接口定义同时服务 HTTP / WS / gRPC / SSE 调试、目录批量回归测试与多步骤场景，改完接口参数即可一键发起压测并导出 Tidegate 原生 YAML / JSON 或 k6 脚本。',
  },
  {
    id: 'smooth',
    tab: '压测不卡界面',
    lead: '高并发压测期间控制台依然流畅：',
    detail:
      '界面渲染与 Go 发压引擎完全分离，发压机按每秒窗口上报 HDR 直方图与聚合快照，CPU 跑满也不会阻塞浏览器响应。',
  },
  {
    id: 'bottleneck',
    tab: '双端瓶颈定位',
    lead: '先分清是目标慢还是发压机满载：',
    detail:
      '实时同步每台节点的 CPU、内存、带宽占用及丢迭代信号，一旦发压机自身触发瓶颈即刻标黄提示。',
  },
  {
    id: 'cluster',
    tab: '分布式加机',
    lead: '单机带宽不够一条命令扩节点：',
    detail:
      '内置 ECDSA 集群 CA 与双向 mTLS 证书签发，支持标签调度（node_selector）与节点断线重平衡。',
  },
  {
    id: 'mcp',
    tab: 'AI 安全协作',
    lead: '让 AI 助手在边界内排查压测与调试接口：',
    detail:
      '提供 16 个工具 + 4 类只读 Resources + 4 项诊断 Prompts，配合 Scope 白名单、项目 ACL 与单令牌并发限流。',
  },
]

const METRIC_CARDS = [
  { label: '最大极限吞吐', value: '265,516 RPS', delta: '+21.4% vs k6' },
  { label: 'P99 尾延时', value: '1.395 ms', delta: '比 k6 快 21.8%' },
  { label: '发压内存峰值', value: '37.3 MB', delta: '仅 k6 的 1/4' },
]

/* ---------- Gate ---------- */

const inputCls =
  'w-full h-10 pl-9 pr-3 text-sm rounded-[11px] border border-line-strong bg-surface text-fg placeholder:text-fg-subtle/70 focus:border-sky-500 focus:ring-2 focus:ring-sky-500/20 focus:outline-none transition-shadow'
const labelCls = 'block text-xs font-semibold text-fg-muted mb-1.5'
const iconCls = 'w-4 h-4 text-fg-subtle absolute left-3 top-3 pointer-events-none'

export function AuthGate({ status, onAuthenticated }: Props) {
  const isSetup = status.setup_required
  const [gateRPS, setGateRPS] = useState(32768)
  const [activePain, setActivePain] = useState<string>('unity')
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
  const authStageRef = useRef<HTMLDivElement>(null)
  useGlassStagePhysics(authStageRef)

  // Read /#setup=CODE fragment on first-run launch without keeping it in browser URL bar.
  useEffect(() => {
    if (!isSetup || typeof window === 'undefined') return
    const hash = window.location.hash || ''
    const m = hash.match(/setup=([A-Za-z0-9-]+)/)
    if (m && m[1]) {
      setCode(m[1].toUpperCase())
      try {
        window.history.replaceState(null, '', window.location.pathname + window.location.search)
      } catch {
        // ignore history error
      }
    }
  }, [isSetup])

  useEffect(() => {
    if (totpStep) totpInputRef.current?.focus()
  }, [totpStep])

  const tideGeometry = useMemo(() => computeTidePaths(gateRPS), [gateRPS])
  const currentPain = PAIN_ITEMS.find((p) => p.id === activePain) || PAIN_ITEMS[0]

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

  const transport = status.tls ? 'HTTPS' : 'HTTP'

  return (
    <div ref={authStageRef} className="min-h-dvh p-3 lg:p-5 grid place-items-center tg-mesh-canvas">
      <div className="w-[min(1240px,100%)] grid lg:grid-cols-[1.25fr_0.88fr] gap-5 items-stretch">
        {/* ---------------- V1 Narrative & Interactive Tidal Gate Column ---------------- */}
        <section className="tg-hero-frost hidden lg:flex flex-col justify-between rounded-[26px] p-7 xl:p-8 gap-5">
          <div>
            <header className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex items-center gap-3">
                <img src="/favicon.svg" alt="" className="w-10 h-10 rounded-[13px] border border-line bg-surface shadow-xs" />
                <div>
                  <h1 className="text-xl font-extrabold tracking-tight text-fg">Tidegate 潮汐闸门</h1>
                  <p className="text-xs text-fg-muted mt-0.5">本地优先的 API 调试、场景编排与分布式 Go 压测控制台</p>
                </div>
              </div>
              <div className="flex items-center gap-2">
                <span className="px-2.5 py-1 rounded-full border border-line bg-surface text-[11px] font-mono text-fg-muted tg-tabular">
                  v{status.version || 'dev'}
                </span>
                <span
                  className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 font-mono text-[11px] ${
                    status.tls
                      ? 'border-emerald-300/70 bg-emerald-500/10 text-emerald-700'
                      : 'border-amber-300/70 bg-amber-500/10 text-amber-700'
                  }`}
                >
                  <Lock className="h-3 w-3" />
                  {transport}
                </span>
              </div>
            </header>

            <p className="mt-4 text-[13px] leading-[1.7] text-fg-muted">
              压测的流量像潮水，有涨有落：爬坡是涨潮，保持是满潮，下降是退潮。闸门决定放多少水过去、什么时候放。Tidegate 把接口管理、分屏调试与发压引擎放进同一个单二进制控制台——数据全在自己机器上，不依赖云账号。
            </p>
          </div>

          {/* Interactive Tidal Gate SVG Card */}
          <div className="tg-accent-card p-4.5 sm:p-5 rounded-[20px]">
            <div className="flex flex-wrap items-center justify-between gap-2 text-xs font-bold">
              <span className="text-fg">交互式潮汐闸门流量示意图</span>
              <span className="font-mono text-tidal tg-tabular">
                开闸目标 {gateRPS.toLocaleString()} RPS · {gateRPS > 180000 ? '洪峰冲击' : '稳态满潮'}
              </span>
            </div>

            <svg viewBox={`0 0 ${GATE_W} ${GATE_H}`} className="w-full h-auto mt-2.5 block" role="img" aria-label="潮汐闸门流量曲线示意">
              <defs>
                <linearGradient id="tg-v1-tide-fill" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="#0ea5e9" stopOpacity="0.42" />
                  <stop offset="100%" stopColor="#0ea5e9" stopOpacity="0.02" />
                </linearGradient>
              </defs>
              <path d={tideGeometry.area} fill="url(#tg-v1-tide-fill)" />
              <path d={tideGeometry.line} fill="none" stroke="#0ea5e9" strokeWidth="2.5" strokeLinejoin="round" />
              <rect x="155" y="18" width="10" height={tideGeometry.gateHeight} rx="4" fill="#ea580c" opacity="0.88" />
              <line x1="0" y1={GATE_BASE} x2={GATE_W} y2={GATE_BASE} stroke="currentColor" className="text-line-strong" strokeWidth="1" />
            </svg>

            <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
              <label className="flex items-center gap-2.5 flex-1 min-w-[220px] text-xs text-fg-muted">
                <span className="shrink-0 font-medium">调节开闸吞吐</span>
                <input
                  type="range"
                  name="gate_rps_preview"
                  min={5000}
                  max={265000}
                  step={1000}
                  value={gateRPS}
                  onChange={(e) => setGateRPS(Number(e.target.value))}
                  className="flex-1 cursor-pointer"
                />
              </label>
              <span className="px-2.5 py-1 rounded-full border border-line bg-surface font-mono text-[11px] text-fg-muted tg-tabular">
                37.3 MB 内存占用
              </span>
            </div>
          </div>

          {/* 5 Pain Points Switcher */}
          <div>
            <div className="text-xs font-bold text-fg-muted mb-2">解决痛点</div>
            <div role="tablist" aria-label="核心场景对照" className="flex flex-wrap gap-2">
              {PAIN_ITEMS.map((item) => {
                const active = activePain === item.id
                return (
                  <button
                    key={item.id}
                    type="button"
                    role="tab"
                    aria-selected={active}
                    onClick={() => setActivePain(item.id)}
                    className={`px-3 py-1.5 rounded-full border text-xs font-semibold transition-all ${
                      active
                        ? 'bg-primary border-primary text-white shadow-[0_6px_16px_-6px_rgb(2_132_199/0.55)]'
                        : 'border-line bg-surface text-fg-muted hover:text-fg hover:bg-surface-hover'
                    }`}
                  >
                    {item.tab}
                  </button>
                )
              })}
            </div>
            <div className="mt-2.5 px-4 py-3 rounded-2xl border border-line bg-surface text-[13px] leading-[1.65] text-fg">
              <strong>{currentPain.lead}</strong>
              {currentPain.detail}
            </div>
          </div>

          {/* 3-Column Verified Benchmark Strip */}
          <div className="grid grid-cols-3 gap-2.5 font-mono tg-tabular">
            {METRIC_CARDS.map((m) => (
              <div key={m.label} className="rounded-2xl border border-line bg-surface px-3.5 py-3">
                <div className="text-[11px] text-fg-subtle font-sans">{m.label}</div>
                <div className="text-lg font-bold text-tidal mt-0.5">{m.value}</div>
                <div className="text-[11px] text-emerald-600 mt-0.5">{m.delta}</div>
              </div>
            ))}
          </div>
        </section>

        {/* ---------------- Right Auth Panel (Aligned to V1 Layout) ---------------- */}
        <main className="tg-glass-card tg-tilt-card rounded-[26px] p-7 flex flex-col justify-between gap-5">
          <div>
            <div className="flex items-center justify-between gap-2 mb-5">
              <div className="flex items-center gap-2.5 min-w-0">
                <img src="/favicon.svg" alt="" className="h-8 w-8 rounded-lg lg:hidden" />
                <h2 className="text-lg font-extrabold tracking-tight text-fg truncate">
                  {isSetup ? '初始化管理员' : totpStep ? '双因素验证' : '账号登录'}
                </h2>
              </div>
              <div className="flex items-center gap-2 shrink-0">
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
            </div>

            <div className="relative z-10 tg-swap-in">
                {totpStep && !isSetup && (
                  <div className="mb-4 flex items-center justify-between rounded-xl border border-line bg-surface-subtle px-3 py-2 text-xs">
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

                <form onSubmit={handleSubmit} method="POST" className="space-y-4">
                  {error && (
                    <div role="alert" className="space-y-1 rounded-xl border border-rose-200 bg-rose-50 px-3.5 py-2.5 text-xs text-rose-800">
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
                          {isSetup && <span className="tg-tabular font-mono text-[11px] text-fg-subtle">≥ 10</span>}
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
                    className="mt-2 h-11 w-full rounded-xl bg-primary text-sm font-bold text-white shadow-xs transition-colors hover:bg-primary-hover disabled:opacity-50"
                  >
                    {submitting ? '验证中…' : isSetup ? '完成初始化' : totpStep ? '验证并登录' : '进入工作台'}
                  </button>
                </form>
            </div>
          </div>

          <footer className="border-t border-line pt-3.5 text-xs text-fg-muted space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <span>内置能力</span>
              <span className="font-mono text-[11px] text-fg">MCP 2.0 · 2FA · RBAC · mTLS</span>
            </div>
            <div className="flex items-center justify-between gap-2">
              <span>数据主权</span>
              <span>SQLite 本地持久化，零遥测外传</span>
            </div>
          </footer>
        </main>
      </div>
    </div>
  )
}
