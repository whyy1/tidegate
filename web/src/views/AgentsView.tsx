import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { AlertTriangle, Check, Copy, Cpu, HardDrive, KeyRound, Network, Plus, Save, Server, Shield, Tags, Terminal, Trash2 } from 'lucide-react'
import { RollingNumber } from '../components/DropletFilter'
import { LiquidCapsule } from '../components/LiquidCapsule'
import { api, APIError } from '../lib/api'
import { copyText } from '../lib/download'
import type { AgentJoinToken, AgentRejectView, AgentView, JoinTokenCreated, SystemInfo } from '../lib/types'
import { wsClient } from '../lib/ws'

interface Props {
  agents: AgentView[]
  isAdmin?: boolean
  onAgentsRefresh: () => Promise<void>
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

type CmdPlatform = 'linux' | 'powershell' | 'docker'

function isLoopbackJoinAddr(addr: string): boolean {
  const s = addr.trim().toLowerCase()
  if (!s) return false
  return (
    s.startsWith('127.') ||
    s.startsWith('localhost') ||
    s.startsWith('[::1]') ||
    s === '::1' ||
    s.startsWith('0.0.0.0')
  )
}

function shellArg(s: string): string {
  return /^[\w.,=:/@+-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`
}

function psArg(s: string): string {
  return /^[\w.,=:/\\@+-]+$/.test(s) ? s : `'${s.replace(/'/g, "''")}'`
}

function formatGiB(bytes: number): string {
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`
}

function formatMbpsFromBps(bps: number): string {
  if (bps <= 0) return '0 Mbps'
  if (bps >= 1e9) return `${(bps / 1e9).toFixed(2)} Gbps`
  return `${(bps / 1e6).toFixed(1)} Mbps`
}

/** Parse `k=v,k2=v2` the same way the agent CLI does; returns normalized pairs and whether any pair is malformed. */
function parseLabels(raw: string): { normalized: string; invalid: boolean } {
  const pairs: string[] = []
  let invalid = false
  for (const part of raw.split(/[,，]/)) {
    const p = part.trim()
    if (!p) continue
    const idx = p.indexOf('=')
    const k = idx >= 0 ? p.slice(0, idx).trim() : ''
    const v = idx >= 0 ? p.slice(idx + 1).trim() : ''
    if (!k || !v) {
      invalid = true
      continue
    }
    pairs.push(`${k}=${v}`)
  }
  return { normalized: pairs.join(','), invalid }
}

const bwInputFor = (ag: AgentView) => (ag.bandwidth_bps > 0 ? String(Math.round(ag.bandwidth_bps / 1e6)) : '0')
const weightInputFor = (ag: AgentView) => String(ag.weight > 0 ? ag.weight : 1)

/** Inline two-step confirm: first call arms `id`, a second call within 3s returns true. */
function useArmedConfirm() {
  const [armed, setArmed] = useState('')
  const timer = useRef(0)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const arm = (id: string) => {
    window.clearTimeout(timer.current)
    if (armed === id) {
      setArmed('')
      return true
    }
    setArmed(id)
    timer.current = window.setTimeout(() => setArmed(''), 3000)
    return false
  }
  const reset = () => {
    window.clearTimeout(timer.current)
    setArmed('')
  }
  return { armed, arm, reset }
}

export function AgentsView({ agents, isAdmin = false, onAgentsRefresh, onToast }: Props) {
  const [sysInfo, setSysInfo] = useState<SystemInfo | null>(null)
  const [joinAddrOverride, setJoinAddrOverride] = useState('')
  const [cmdPlatform, setCmdPlatform] = useState<CmdPlatform>('linux')
  const cmdTabsRef = useRef<HTMLDivElement | null>(null)
  const [joinTokens, setJoinTokens] = useState<AgentJoinToken[]>([])
  const [tokenName, setTokenName] = useState('')
  const [nodeName, setNodeName] = useState('')
  const [tokenTTL, setTokenTTL] = useState(24)
  const [tokenSingleUse, setTokenSingleUse] = useState(true)
  const [tokenLabels, setTokenLabels] = useState('')
  const [creatingTok, setCreatingTok] = useState(false)
  const [createdTok, setCreatedTok] = useState<JoinTokenCreated | null>(null)
  const [rejectAlerts, setRejectAlerts] = useState<AgentRejectView[]>([])
  const [bwInputs, setBwInputs] = useState<Record<string, string>>({})
  const [weightInputs, setWeightInputs] = useState<Record<string, string>>({})
  const [savingId, setSavingId] = useState('')
  const revoke = useArmedConfirm()
  const delToken = useArmedConfirm()
  const [copiedCmd, setCopiedCmd] = useState(false)
  const [copiedTok, setCopiedTok] = useState(false)

  const loadJoinTokens = async () => {
    if (!isAdmin) return
    try {
      setJoinTokens(await api.listJoinTokens())
    } catch {
      // ignore non-admin or transient errors
    }
  }

  useEffect(() => {
    void api.systemInfo().then(setSysInfo).catch(() => undefined)
    void loadJoinTokens()
  }, [isAdmin])

  useEffect(() => {
    const unsub = wsClient.onEvent((ev) => {
      if (ev.type === 'agents') {
        void loadJoinTokens()
      } else if (ev.type === 'agent_rejected' && ev.agent_rejected) {
        const item = ev.agent_rejected
        setRejectAlerts((prev) => [item, ...prev].slice(0, 5))
        void loadJoinTokens()
        onToast('error', `节点接入被拒：${item.message}`)
      }
    })
    return unsub
  }, [isAdmin, onToast])

  useEffect(() => {
    setBwInputs((prev) => {
      const next = { ...prev }
      for (const ag of agents) {
        if (next[ag.id] === undefined) next[ag.id] = bwInputFor(ag)
      }
      return next
    })
    setWeightInputs((prev) => {
      const next = { ...prev }
      for (const ag of agents) {
        if (next[ag.id] === undefined) next[ag.id] = weightInputFor(ag)
      }
      return next
    })
  }, [agents])

  const effectiveJoinAddr = (joinAddrOverride.trim() || sysInfo?.cluster_addr || '<控制器IP:7443>')
  const showLoopbackWarning =
    Boolean(sysInfo?.cluster_listen_loopback) || isLoopbackJoinAddr(effectiveJoinAddr)
  const labels = parseLabels(tokenLabels)

  const buildCommand = (tokValue: string): string => {
    const fp = sysInfo?.cluster_ca_fingerprint
    const cleanName = nodeName.trim()
    if (cmdPlatform === 'powershell') {
      const parts = [
        '.\\tidegate.exe agent',
        `--join ${psArg(effectiveJoinAddr)}`,
        `--token ${psArg(tokValue)}`,
        '--cert-dir "$env:LOCALAPPDATA\\Tidegate\\agent-certs"',
      ]
      if (cleanName) parts.push(`--name ${psArg(cleanName)}`)
      if (fp) parts.push(`--ca-fingerprint ${fp}`)
      if (labels.normalized) parts.push(`--labels ${psArg(labels.normalized)}`)
      return parts.join(' ')
    }
    if (cmdPlatform === 'docker') {
      const envArgs = [
        `-e TIDEGATE_JOIN=${shellArg(effectiveJoinAddr)}`,
        `-e TIDEGATE_TOKEN=${shellArg(tokValue)}`,
        '-e TIDEGATE_CERT_DIR=/certs',
      ]
      if (cleanName) envArgs.push(`-e TIDEGATE_NAME=${shellArg(cleanName)}`)
      if (fp) envArgs.push(`-e TIDEGATE_CA_FINGERPRINT=${fp}`)
      if (labels.normalized) envArgs.push(`-e TIDEGATE_LABELS=${shellArg(labels.normalized)}`)
      return `docker run -d --name tidegate-agent --restart unless-stopped -v tidegate-agent-certs:/certs ${envArgs.join(' ')} tidegate:latest agent`
    }
    const parts = [
      './tidegate agent',
      `--join ${shellArg(effectiveJoinAddr)}`,
      `--token ${shellArg(tokValue)}`,
      '--cert-dir ~/.tidegate/agent-certs',
    ]
    if (cleanName) parts.push(`--name ${shellArg(cleanName)}`)
    if (fp) parts.push(`--ca-fingerprint ${fp}`)
    if (labels.normalized) parts.push(`--labels ${shellArg(labels.normalized)}`)
    return parts.join(' ')
  }

  const joinCmd = buildCommand(createdTok?.token || '<加入令牌>')
  const canCopyCmd = Boolean(createdTok)

  const handleSaveSettings = async (agent: AgentView) => {
    const mbps = Number(bwInputs[agent.id] || '0')
    if (Number.isNaN(mbps) || mbps < 0) {
      onToast('error', '带宽上限必须为非负数字（Mbps），0 表示使用网卡协商速率')
      return
    }
    const weight = Number(weightInputs[agent.id] || '1')
    if (Number.isNaN(weight) || weight <= 0) {
      onToast('error', '节点发压权重必须大于 0')
      return
    }
    const bps = Math.round(mbps * 1_000_000)
    setSavingId(agent.id)
    try {
      await api.patchAgent(agent.id, { bandwidth_bps: bps, weight })
      await onAgentsRefresh()
      onToast('success', `已更新节点「${agent.name}」：权重 ${weight}，带宽 ${mbps > 0 ? `${mbps} Mbps` : '自动'}`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '更新节点设置失败')
    } finally {
      setSavingId('')
    }
  }

  const issueJoinToken = async (copyCommandAfter = false) => {
    if (labels.invalid) {
      onToast('error', '预设标签格式应为 key=value，多个用逗号分隔')
      return
    }
    setCreatingTok(true)
    try {
      const res = await api.createJoinToken({
        name: tokenName.trim() || nodeName.trim() || 'agent-join',
        ttl_hours: Math.max(1, Number(tokenTTL) || 24),
        single_use: tokenSingleUse,
      })
      setCreatedTok(res)
      setTokenName('')
      await loadJoinTokens()
      if (copyCommandAfter) {
        const cmd = buildCommand(res.token)
        if (await copyText(cmd)) {
          setCopiedCmd(true)
          window.setTimeout(() => setCopiedCmd(false), 1500)
          onToast('success', '已签发令牌并复制完整接入命令')
          return
        }
      }
      onToast('success', '已签发加入令牌，复制命令到节点执行即可接入')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '签发加入令牌失败')
    } finally {
      setCreatingTok(false)
    }
  }

  const handleCreateJoinToken = async (e: FormEvent) => {
    e.preventDefault()
    await issueJoinToken(false)
  }

  const copyWithFeedback = async (text: string, done: (v: boolean) => void, msg: string) => {
    if (await copyText(text)) {
      done(true)
      onToast('success', msg)
      window.setTimeout(() => done(false), 1500)
    } else {
      onToast('info', '复制失败，请手动选择文本')
    }
  }

  const onSettingsKey = (e: KeyboardEvent<HTMLInputElement>, ag: AgentView) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      void handleSaveSettings(ag)
    } else if (e.key === 'Escape') {
      e.preventDefault()
      setWeightInputs((prev) => ({ ...prev, [ag.id]: weightInputFor(ag) }))
      setBwInputs((prev) => ({ ...prev, [ag.id]: bwInputFor(ag) }))
    }
  }

  return (
    <div className="flex-1 overflow-y-auto tg-scroller bg-transparent p-2.5 pb-[72px] space-y-3 text-xs">
      <section aria-label="节点接入" className="tg-accent-card p-4 space-y-3.5">
        <div className="pb-3 border-b border-line flex flex-wrap items-center justify-between gap-2">
          <h2 className="flex items-center gap-2 font-semibold text-sm text-fg">
            <Terminal className="w-4 h-4 text-tidal" /> 节点接入
          </h2>
          <div className="flex flex-wrap items-center gap-2">
            {sysInfo && (
              <span className="tg-tabular font-mono text-[11px] text-fg-muted">
                控制端版本 {sysInfo.version} · {effectiveJoinAddr}
              </span>
            )}
            <div
              ref={cmdTabsRef}
              role="tablist"
              aria-label="运行环境命令"
              className="relative tg-tablist inline-flex items-center gap-0.5 rounded-[10px] border border-line bg-surface-subtle p-0.5"
            >
              <LiquidCapsule containerRef={cmdTabsRef} activeId={cmdPlatform} className="rounded-lg" />
              {(
                [
                  { id: 'linux' as const, label: 'Linux / macOS' },
                  { id: 'powershell' as const, label: 'Windows PowerShell' },
                  { id: 'docker' as const, label: 'Docker' },
                ] as const
              ).map((tab) => {
                const selected = cmdPlatform === tab.id
                return (
                  <button
                    key={tab.id}
                    type="button"
                    role="tab"
                    data-liq-id={tab.id}
                    aria-selected={selected}
                    onClick={() => setCmdPlatform(tab.id)}
                    className={`relative z-10 px-2.5 py-1 rounded-lg font-semibold whitespace-nowrap transition-colors ${
                      selected ? 'text-tidal' : 'text-fg-muted hover:text-fg'
                    }`}
                  >
                    {tab.label}
                  </button>
                )
              })}
            </div>
          </div>
        </div>

        {showLoopbackWarning && (
          <div className="px-3 py-2 rounded-[10px] border border-amber-400/55 bg-amber-500/12 text-amber-800 flex flex-wrap items-center gap-2">
            <AlertTriangle className="w-4 h-4 text-amber-500 shrink-0" />
            <span className="flex-1 min-w-0 leading-relaxed">
              {sysInfo?.cluster_listen_loopback
                ? '控制器当前集群端口仅监听回环地址（127.0.0.1），跨机器节点无法接入；请改用 --cluster-listen 0.0.0.0:7443 启动，并在右侧填入控制端对外 IP:7443。'
                : '当前接入地址为本机回环（localhost / 127.0.0.1），远程机器执行时请在右侧填入控制端局域网或公网 IP:7443。'}
            </span>
          </div>
        )}

        {rejectAlerts.length > 0 && (
          <div className="px-3 py-2 rounded-[10px] border border-rose-400/55 bg-rose-500/12 text-rose-700 space-y-1">
            <div className="flex items-center justify-between gap-2 font-semibold">
              <span className="flex items-center gap-1.5">
                <AlertTriangle className="w-3.5 h-3.5 text-rose-500 shrink-0" /> 最近节点握手拒接诊断
              </span>
              <button
                type="button"
                onClick={() => setRejectAlerts([])}
                className="text-[11px] underline hover:no-underline"
              >
                清除提醒
              </button>
            </div>
            {rejectAlerts.map((al, i) => (
              <div key={`${al.time}-${i}`} className="font-mono text-[11px] flex flex-wrap items-center gap-x-2">
                <span>[{new Date(al.time).toLocaleTimeString()}]</span>
                <span>{al.node_name || al.remote_addr || '未知节点'}</span>
                {al.node_version && <span>({al.node_version})</span>}
                <span>· {al.message}</span>
              </div>
            ))}
          </div>
        )}

        <div className="space-y-3">
          <div className="grid grid-cols-1 xl:grid-cols-12 gap-3 items-start">
            {isAdmin && (
              <form
                onSubmit={handleCreateJoinToken}
                className="xl:col-span-5 grid grid-cols-2 gap-2 p-3 rounded-xl border border-line bg-surface-subtle"
              >
                <div className="col-span-2 flex flex-wrap items-center gap-2">
                  <input
                    name="token_name"
                    aria-label="令牌备注名"
                    type="text"
                    autoComplete="off"
                    value={tokenName}
                    onChange={(e) => setTokenName(e.target.value)}
                    placeholder="备注（可选，默认 agent-join）"
                    className="flex-1 min-w-[140px] px-2.5 py-1.5 border border-line-strong rounded-[10px] bg-surface text-fg"
                  />
                  <label className="inline-flex items-center gap-1 text-fg-muted">
                    <input
                      name="ttl_hours"
                      aria-label="有效小时数"
                      type="number"
                      min={1}
                      max={8760}
                      value={tokenTTL}
                      onChange={(e) => setTokenTTL(Math.max(1, Number(e.target.value) || 24))}
                      className="tg-tabular w-16 px-2 py-1.5 border border-line-strong rounded-[10px] font-mono bg-surface text-fg"
                    />
                    <span>小时</span>
                  </label>
                  <label className="inline-flex items-center gap-1 text-fg-muted cursor-pointer">
                    <input type="checkbox" checked={tokenSingleUse} onChange={(e) => setTokenSingleUse(e.target.checked)} />
                    <span>单次</span>
                  </label>
                </div>
                <input
                  name="node_name"
                  aria-label="节点名称"
                  type="text"
                  autoComplete="off"
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={nodeName}
                  onChange={(e) => setNodeName(e.target.value)}
                  placeholder="节点名 --name（可选）"
                  className="w-full px-2.5 py-1.5 border border-line-strong rounded-[10px] bg-surface text-fg font-mono"
                />
                <label className="relative min-w-0">
                  <Tags className="w-3.5 h-3.5 text-fg-subtle absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
                  <input
                    name="labels"
                    aria-label="预设标签"
                    type="text"
                    autoComplete="off"
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    value={tokenLabels}
                    onChange={(e) => setTokenLabels(e.target.value)}
                    placeholder="标签 zone=sh,tier=edge"
                    aria-invalid={labels.invalid || undefined}
                    className={`w-full pl-8 pr-2.5 py-1.5 border rounded-[10px] bg-surface text-fg font-mono ${
                      labels.invalid ? 'border-rose-400 focus:outline-rose-500' : 'border-line-strong'
                    }`}
                  />
                </label>
                <div className="col-span-2 flex flex-wrap items-center justify-end gap-2">
                  <button
                    type="submit"
                    disabled={creatingTok}
                    className="tg-btn-secondary"
                  >
                    <Plus className="w-3.5 h-3.5" /> 仅签发令牌
                  </button>
                  <button
                    type="button"
                    disabled={creatingTok}
                    onClick={() => void issueJoinToken(true)}
                    className="tg-btn-primary"
                  >
                    <KeyRound className="w-3.5 h-3.5" /> {creatingTok ? '生成中…' : '一键签发并复制命令'}
                  </button>
                </div>
              </form>
            )}

            <div className={`${isAdmin ? 'xl:col-span-7' : 'xl:col-span-12'} space-y-2 min-w-0`}>
              <label className="flex flex-wrap items-center gap-2">
                <span className="text-fg-muted font-medium shrink-0">控制器接入地址</span>
                <input
                  name="cluster_join_addr"
                  type="text"
                  autoComplete="off"
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={joinAddrOverride}
                  onChange={(e) => setJoinAddrOverride(e.target.value)}
                  placeholder={sysInfo?.cluster_addr || '192.168.1.10:7443'}
                  className="flex-1 min-w-[200px] px-2.5 py-1.5 border border-line-strong rounded-[10px] bg-surface text-fg font-mono"
                />
              </label>

              <div className="flex items-center gap-2 tg-code-surface px-3 py-2.5 rounded-[10px] font-mono text-xs">
                <code className="flex-1 min-w-0 overflow-x-auto tg-scroller-dark whitespace-nowrap text-sky-100">{joinCmd}</code>
                <button
                  type="button"
                  disabled={!canCopyCmd}
                  title={canCopyCmd ? '复制接入命令' : '签发令牌后可复制'}
                  onClick={() => void copyWithFeedback(joinCmd, setCopiedCmd, '已复制节点接入命令')}
                  className="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg bg-white/15 hover:bg-white/25 text-white shrink-0 disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:bg-white/15"
                >
                  {copiedCmd ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
                  复制
                </button>
              </div>
              {sysInfo?.cluster_ca_fingerprint && (
                <div className="flex items-center gap-2 px-3 py-1.5 rounded-[10px] border border-sky-400/35 bg-sky-500/10 text-fg font-mono text-[11px] min-w-0">
                  <Shield className="w-3.5 h-3.5 text-tidal shrink-0" />
                  <span className="font-sans font-medium shrink-0">CA 指纹</span>
                  <span className="truncate select-all text-fg-muted">{sysInfo.cluster_ca_fingerprint}</span>
                </div>
              )}
            </div>
          </div>

          {isAdmin && (createdTok || joinTokens.length > 0) && (
            <div className="pt-3 border-t border-line space-y-2">
              {createdTok && (
                <div className="px-3 py-2 rounded-lg border border-emerald-300 bg-emerald-50 text-emerald-900 font-mono text-[11px] flex items-center gap-2">
                  <KeyRound className="w-3.5 h-3.5 text-emerald-600 shrink-0" />
                  <span className="flex-1 min-w-0 break-all select-all">{createdTok.token}</span>
                  <button
                    type="button"
                    onClick={() => void copyWithFeedback(createdTok.token, setCopiedTok, '已复制加入令牌')}
                    aria-label="复制令牌"
                    className="p-1 rounded text-emerald-700 hover:bg-emerald-100 shrink-0"
                  >
                    {copiedTok ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
                  </button>
                </div>
              )}

              {joinTokens.length > 0 && (
                <ul className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-2">
                  {joinTokens.map((jt) => {
                    const armed = delToken.armed === jt.id
                    return (
                      <li
                        key={jt.id}
                        className="flex items-center justify-between gap-2 px-2.5 py-1.5 rounded border border-line bg-surface-subtle"
                      >
                        <div className="min-w-0">
                          <div className="font-medium text-fg truncate">
                            {jt.name || jt.id}{' '}
                            <span className="tg-tabular text-[11px] font-mono font-normal text-fg-subtle">
                              · {jt.single_use ? '单次' : '多次'} · {jt.used_count} 次
                            </span>
                          </div>
                          <div className="tg-tabular text-[11px] text-fg-subtle">过期 {new Date(jt.expires_at).toLocaleString()}</div>
                        </div>
                        <button
                          type="button"
                          onClick={async () => {
                            if (!delToken.arm(jt.id)) return
                            try {
                              await api.deleteJoinToken(jt.id)
                              await loadJoinTokens()
                              onToast('success', '已删除加入令牌')
                            } catch (err) {
                              onToast('error', err instanceof APIError ? err.message : '删除加入令牌失败')
                            }
                          }}
                          onBlur={delToken.reset}
                          className={`inline-flex items-center gap-1 rounded shrink-0 ${
                            armed ? 'px-2 py-1 bg-rose-600 hover:bg-rose-solid text-white font-medium' : 'p-1 text-fg-subtle hover:text-rose-600'
                          }`}
                          aria-label="删除令牌"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                          {armed && '确认删除？'}
                        </button>
                      </li>
                    )
                  })}
                </ul>
              )}
            </div>
          )}
        </div>
      </section>

      {agents.length === 0 ? (
        <div className="tg-glass-card p-10 text-center text-fg-subtle">暂无在线发压节点</div>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
          {agents.map((ag) => {
            const cpuPct = ag.stats ? Math.min(100, ag.stats.cpu.machine * 100) : 0
            const procCpuPct = ag.stats ? Math.min(100, ag.stats.cpu.process * 100) : 0
            const memTotal = ag.stats?.memory.total_bytes || ag.memory_bytes || 1
            const memUsed = ag.stats?.memory.used_bytes || 0
            const memPct = Math.min(100, (memUsed / memTotal) * 100)
            const activeNICs = (ag.stats?.nics || []).filter((n) => !n.loopback && n.up)
            const totalTxBps = activeNICs.reduce((acc, n) => acc + n.tx_bytes_per_sec * 8, 0)
            const totalRxBps = activeNICs.reduce((acc, n) => acc + n.rx_bytes_per_sec * 8, 0)
            const maxLinkBps = Math.max(0, ...(ag.interfaces || []).map((i) => i.link_speed_bps))
            const weightVal = weightInputs[ag.id] ?? weightInputFor(ag)
            const bwVal = bwInputs[ag.id] ?? bwInputFor(ag)
            const weightDirty = Number(weightVal) !== Number(weightInputFor(ag))
            const bwDirty = Number(bwVal) !== Number(bwInputFor(ag))
            const dirty = weightDirty || bwDirty
            const revokeArmed = revoke.armed === ag.id
            const dirtyCls = 'border-amber-400 bg-amber-50 text-amber-900'
            const cleanCls = 'border-line-strong bg-surface text-fg'

            return (
              <article key={ag.id} className="tg-glass-card tg-tilt-card p-4 space-y-3">
                <header className="flex flex-wrap items-start justify-between gap-2 border-b border-line pb-2.5">
                  <div className="flex items-center gap-2 min-w-0">
                    <div className="p-2 rounded-lg bg-sky-50 text-sky-700 shrink-0">
                      <Server className="w-4 h-4" />
                    </div>
                    <div className="min-w-0">
                      <div className="font-semibold text-sm text-fg flex flex-wrap items-center gap-2">
                        <span className="truncate">{ag.name}</span>
                        <span className="px-1.5 py-px rounded-full bg-emerald-50 border border-emerald-200 text-emerald-800 text-[11px] font-medium">
                          在线
                        </span>
                        {(ag.load_share ?? 0) > 0 && (
                          <span className="tg-tabular px-1.5 py-px rounded-full bg-sky-50 border border-sky-200 text-sky-800 text-[11px] font-mono">
                            负载份额 {((ag.load_share || 0) * 100).toFixed(1)}%
                          </span>
                        )}
                      </div>
                      <div className="tg-tabular text-[11px] text-fg-subtle font-mono">
                        {ag.os}/{ag.arch} · {ag.cpu_cores} 核 · {formatGiB(ag.memory_bytes)} · {ag.remote_addr || '本机'}
                      </div>
                      {(Object.keys(ag.labels || {}).length > 0 || ag.cert_serial) && (
                        <div className="flex flex-wrap items-center gap-1 mt-1">
                          {Object.entries(ag.labels || {}).map(([k, v]) => (
                            <span
                              key={k}
                              className="px-1.5 py-0.5 rounded bg-surface-subtle border border-line font-mono text-[11px] text-fg-muted"
                            >
                              {k}={v}
                            </span>
                          ))}
                          {ag.cert_serial && (
                            <span className="px-1.5 py-0.5 rounded bg-emerald-50 border border-emerald-200 font-mono text-[11px] text-emerald-800">
                              mTLS #{ag.cert_serial.slice(0, 8)}
                            </span>
                          )}
                        </div>
                      )}
                    </div>
                  </div>
                  <dl className="tg-tabular text-right font-mono text-[11px] text-fg-subtle grid grid-cols-[auto_auto] gap-x-2">
                    <dt>RTT</dt>
                    <dd className="text-fg-muted">{ag.rtt_ms.toFixed(2)} ms</dd>
                    <dt>时钟差</dt>
                    <dd className="text-fg-muted">{ag.clock_offset_ms.toFixed(2)} ms</dd>
                  </dl>
                </header>

                {ag.stats ? (
                  <div className="grid grid-cols-2 gap-3">
                    <div className="space-y-1">
                      <div className="flex items-center justify-between text-fg-muted">
                        <span className="inline-flex items-center gap-1">
                          <Cpu className="w-3.5 h-3.5 text-sky-600" /> 整机 CPU
                        </span>
                        <RollingNumber value={cpuPct} decimals={1} suffix="%" className="font-mono font-semibold text-fg" />
                      </div>
                      <div className="h-2 rounded-full bg-surface-subtle border border-line overflow-hidden">
                        <div className={`h-full ${cpuPct > 85 ? 'bg-rose-500' : 'bg-sky-500'}`} style={{ width: `${cpuPct}%` }} />
                      </div>
                      <div className="tg-tabular text-[11px] text-fg-subtle font-mono">发压进程 {procCpuPct.toFixed(1)}%</div>
                    </div>

                    <div className="space-y-1">
                      <div className="flex items-center justify-between text-fg-muted">
                        <span className="inline-flex items-center gap-1">
                          <HardDrive className="w-3.5 h-3.5 text-emerald-600" /> 内存
                        </span>
                        <RollingNumber value={memPct} decimals={1} suffix="%" className="font-mono font-semibold text-fg" />
                      </div>
                      <div className="h-2 rounded-full bg-surface-subtle border border-line overflow-hidden">
                        <div className={`h-full ${memPct > 85 ? 'bg-rose-500' : 'bg-emerald-500'}`} style={{ width: `${memPct}%` }} />
                      </div>
                      <div className="tg-tabular text-[11px] text-fg-subtle font-mono">
                        {formatGiB(memUsed)} / {formatGiB(memTotal)}
                      </div>
                    </div>

                    <div className="p-2 rounded bg-surface-subtle border border-line font-mono tg-tabular">
                      <div className="text-[11px] text-fg-subtle flex items-center gap-1 mb-0.5 font-sans">
                        <Network className="w-3 h-3 text-sky-600" /> 网卡流量
                      </div>
                      <div className="text-fg">
                        上行 {formatMbpsFromBps(totalTxBps)} · 下行 {formatMbpsFromBps(totalRxBps)}
                      </div>
                      <div className="text-[11px] text-fg-subtle">
                        协商上限 {maxLinkBps > 0 ? formatMbpsFromBps(maxLinkBps) : '未检测到'}
                      </div>
                    </div>

                    <div className="p-2 rounded bg-surface-subtle border border-line font-mono tg-tabular">
                      <div className="text-[11px] text-fg-subtle mb-0.5 font-sans">TCP 与临时端口</div>
                      <div className="text-fg">
                        活跃 {ag.stats.ports.tcp_conns} / 端口段 {ag.stats.ports.ephemeral_count}
                      </div>
                      <div className="text-[11px] text-fg-subtle">
                        重传 {ag.stats.tcp.retrans_per_sec.toFixed(2)}/s · Goroutines {ag.stats.runtime.goroutines}
                      </div>
                    </div>
                  </div>
                ) : (
                  <p className="text-fg-subtle">等待节点心跳…</p>
                )}

                <footer className="pt-2 border-t border-line flex flex-wrap items-center justify-between gap-2">
                  <span className="tg-tabular text-fg-subtle">
                    有效权重 <span className="font-mono text-fg-muted">{ag.effective_weight.toFixed(1)}</span>
                  </span>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <label className="inline-flex items-center gap-1 text-fg-muted">
                      权重
                      <input
                        type="number"
                        name={`weight-${ag.id}`}
                        min={0.1}
                        step="0.5"
                        disabled={!isAdmin}
                        value={weightVal}
                        onChange={(e) => setWeightInputs((prev) => ({ ...prev, [ag.id]: e.target.value }))}
                        onKeyDown={(e) => onSettingsKey(e, ag)}
                        className={`tg-tabular w-16 px-2 py-1 border rounded font-mono disabled:opacity-50 ${weightDirty ? dirtyCls : cleanCls}`}
                      />
                    </label>
                    <label className="inline-flex items-center gap-1 text-fg-muted">
                      带宽
                      <input
                        type="number"
                        name={`bandwidth-${ag.id}`}
                        min={0}
                        step="1"
                        disabled={!isAdmin}
                        value={bwVal}
                        placeholder="0"
                        title="0 = 按网卡协商速率"
                        onChange={(e) => setBwInputs((prev) => ({ ...prev, [ag.id]: e.target.value }))}
                        onKeyDown={(e) => onSettingsKey(e, ag)}
                        className={`tg-tabular w-20 px-2 py-1 border rounded font-mono disabled:opacity-50 ${bwDirty ? dirtyCls : cleanCls}`}
                      />
                      Mbps
                    </label>
                    {isAdmin && (
                      <>
                        <button
                          type="button"
                          disabled={savingId === ag.id || !dirty}
                          onClick={() => void handleSaveSettings(ag)}
                          className={`inline-flex items-center gap-1 px-2.5 py-1 rounded font-medium disabled:opacity-50 ${
                            dirty
                              ? 'bg-primary hover:bg-primary-hover text-white'
                              : 'border border-line-strong text-fg-subtle bg-surface'
                          }`}
                        >
                          <Save className="w-3 h-3" /> 应用
                        </button>
                        {ag.id !== 'local' && (
                          <button
                            type="button"
                            onClick={async () => {
                              if (!revoke.arm(ag.id)) return
                              try {
                                await api.revokeAgent(ag.id)
                                await onAgentsRefresh()
                                onToast('success', `已吊销节点「${ag.name}」证书并断开连接`)
                              } catch (err) {
                                onToast('error', err instanceof APIError ? err.message : '吊销节点失败')
                              }
                            }}
                            onBlur={revoke.reset}
                            className={`inline-flex items-center gap-1 px-2 py-1 rounded border font-medium ${
                              revokeArmed
                                ? 'bg-rose-600 border-rose-600 hover:bg-rose-solid text-white'
                                : 'border-rose-200 text-rose-700 hover:bg-rose-50'
                            }`}
                          >
                            <Trash2 className="w-3 h-3" /> {revokeArmed ? '确认吊销？' : '吊销'}
                          </button>
                        )}
                      </>
                    )}
                  </div>
                </footer>
              </article>
            )
          })}
        </div>
      )}
    </div>
  )
}
