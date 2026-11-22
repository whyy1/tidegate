import { useEffect, useState, type FormEvent } from 'react'
import { Check, Copy, Cpu, Download, HardDrive, KeyRound, Network, Plus, Save, Server, Shield, Terminal, Trash2 } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { AgentJoinToken, AgentView, JoinTokenCreated, SystemInfo } from '../lib/types'

interface Props {
  agents: AgentView[]
  isAdmin?: boolean
  onAgentsRefresh: () => Promise<void>
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

function formatGiB(bytes: number): string {
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`
}

function formatMbpsFromBps(bps: number): string {
  if (bps <= 0) return '0 Mbps'
  if (bps >= 1e9) return `${(bps / 1e9).toFixed(2)} Gbps`
  return `${(bps / 1e6).toFixed(1)} Mbps`
}

export function AgentsView({ agents, isAdmin = false, onAgentsRefresh, onToast }: Props) {
  const [sysInfo, setSysInfo] = useState<SystemInfo | null>(null)
  const [joinTokens, setJoinTokens] = useState<AgentJoinToken[]>([])
  const [tokenName, setTokenName] = useState('')
  const [tokenTTL, setTokenTTL] = useState(24)
  const [tokenSingleUse, setTokenSingleUse] = useState(true)
  const [creatingTok, setCreatingTok] = useState(false)
  const [createdTok, setCreatedTok] = useState<JoinTokenCreated | null>(null)
  const [bwInputs, setBwInputs] = useState<Record<string, string>>({})
  const [weightInputs, setWeightInputs] = useState<Record<string, string>>({})
  const [savingId, setSavingId] = useState('')
  const [confirmRevokeId, setConfirmRevokeId] = useState('')
  const [copiedCmd, setCopiedCmd] = useState(false)

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
    setBwInputs((prev) => {
      const next = { ...prev }
      for (const ag of agents) {
        if (next[ag.id] === undefined) {
          next[ag.id] = ag.bandwidth_bps > 0 ? String(Math.round(ag.bandwidth_bps / 1e6)) : '0'
        }
      }
      return next
    })
    setWeightInputs((prev) => {
      const next = { ...prev }
      for (const ag of agents) {
        if (next[ag.id] === undefined) {
          next[ag.id] = String(ag.weight > 0 ? ag.weight : 1)
        }
      }
      return next
    })
  }, [agents])

  const fpFlag = sysInfo?.cluster_ca_fingerprint ? ` --ca-fingerprint ${sysInfo.cluster_ca_fingerprint}` : ''
  const joinCmd = sysInfo
    ? `tidegate agent --join ${sysInfo.cluster_addr || '127.0.0.1:7443'} --token ${createdTok?.token || '<一次性加入令牌>'} --cert-dir ./agent-certs${fpFlag} --labels zone=default`
    : 'tidegate agent --join <控制器IP:7443> --token <加入令牌> --cert-dir ./agent-certs --ca-fingerprint <CA指纹>'

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
      onToast('success', `已更新节点「${agent.name}」权重(${weight})与带宽设置`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '更新节点设置失败')
    } finally {
      setSavingId('')
    }
  }

  const handleCreateJoinToken = async (e: FormEvent) => {
    e.preventDefault()
    if (!tokenName.trim()) {
      onToast('error', '请输入加入令牌备注名称（如 节点-华东-01）')
      return
    }
    setCreatingTok(true)
    try {
      const res = await api.createJoinToken({
        name: tokenName.trim(),
        ttl_hours: Math.max(1, Number(tokenTTL) || 24),
        single_use: tokenSingleUse,
      })
      setCreatedTok(res)
      setTokenName('')
      await loadJoinTokens()
      onToast('success', '已签发节点加入令牌，可直接复制下方命令接入节点')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '签发加入令牌失败')
    } finally {
      setCreatingTok(false)
    }
  }

  return (
    <div className="flex-1 overflow-y-auto bg-slate-50 p-5 space-y-5 text-xs">
      <div className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2 font-semibold text-sm text-slate-900">
            <Terminal className="w-4 h-4 text-sky-600" /> mTLS 分布式节点注册与零信任接入
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {sysInfo && (
              <span className="font-mono text-[11px] text-slate-500">
                控制器 {sysInfo.version} · 集群端口 {sysInfo.cluster_addr}
              </span>
            )}
            <a
              href={api.exportAgentCAPEMUrl()}
              download="tidegate-cluster-ca.pem"
              className="inline-flex items-center gap-1 px-2.5 py-1 rounded border border-slate-300 bg-white hover:bg-slate-50 text-slate-700 font-medium"
            >
              <Download className="w-3.5 h-3.5 text-sky-600" /> 下载 CA 证书 (.pem)
            </a>
          </div>
        </div>
        {sysInfo?.cluster_ca_fingerprint && (
          <div className="flex flex-wrap items-center gap-2 px-3 py-2 rounded-lg bg-sky-50/70 border border-sky-200 text-sky-950 font-mono text-[11px]">
            <Shield className="w-3.5 h-3.5 text-sky-700 shrink-0" />
            <span className="font-sans font-medium">集群 CA SHA-256 指纹：</span>
            <span className="break-all select-all">{sysInfo.cluster_ca_fingerprint}</span>
          </div>
        )}
        <p className="text-slate-600">
          远端发压机首次使用下方一次性加入令牌完成 CSR 双向 mTLS 证书签发后，后续重启无需再次提供令牌（已持久化签名证书）：
        </p>
        <div className="flex items-center gap-2 bg-slate-900 text-sky-100 px-3 py-2 rounded-lg font-mono text-xs">
          <code className="flex-1 overflow-x-auto">{joinCmd}</code>
          <button
            type="button"
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(joinCmd)
                setCopiedCmd(true)
                onToast('success', '已复制节点接入命令')
                window.setTimeout(() => setCopiedCmd(false), 1500)
              } catch {
                onToast('info', '请手动选择复制命令')
              }
            }}
            className="inline-flex items-center gap-1 px-2.5 py-1 rounded bg-white/15 hover:bg-white/25 text-white shrink-0"
          >
            {copiedCmd ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
            复制
          </button>
        </div>

        {isAdmin && (
          <div className="pt-3 border-t border-slate-100 space-y-2.5">
            <div className="flex items-center justify-between">
              <span className="font-semibold text-slate-800 flex items-center gap-1.5">
                <KeyRound className="w-3.5 h-3.5 text-sky-600" /> 节点加入令牌管理 (Join Tokens)
              </span>
            </div>
            <form onSubmit={handleCreateJoinToken} className="flex flex-wrap items-center gap-2">
              <input
                type="text"
                value={tokenName}
                onChange={(e) => setTokenName(e.target.value)}
                placeholder="节点标识备注（例如 perf-node-02）"
                className="flex-1 min-w-[180px] px-2.5 py-1.5 border border-slate-300 rounded bg-white"
              />
              <label className="inline-flex items-center gap-1 text-slate-600">
                <span>有效小时</span>
                <input
                  type="number"
                  min={1}
                  max={8760}
                  value={tokenTTL}
                  onChange={(e) => setTokenTTL(Math.max(1, Number(e.target.value) || 24))}
                  className="w-16 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                />
              </label>
              <label className="inline-flex items-center gap-1 text-slate-700 cursor-pointer">
                <input
                  type="checkbox"
                  checked={tokenSingleUse}
                  onChange={(e) => setTokenSingleUse(e.target.checked)}
                />
                <span>单次一次性使用</span>
              </label>
              <button
                type="submit"
                disabled={creatingTok}
                className="inline-flex items-center gap-1 px-3 py-1.5 rounded bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
              >
                <Plus className="w-3.5 h-3.5" /> 签发令牌
              </button>
            </form>

            {createdTok && (
              <div className="p-2.5 rounded-lg border border-emerald-300 bg-emerald-50 text-emerald-950 font-mono text-[11px] space-y-1">
                <div className="font-sans font-semibold">
                  新签发令牌明文（已自动填充至上方命令，仅本次展示）：
                </div>
                <div className="break-all select-all">{createdTok.token}</div>
              </div>
            )}

            {joinTokens.length > 0 && (
              <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                {joinTokens.map((jt) => (
                  <div
                    key={jt.id}
                    className="flex items-center justify-between gap-2 px-2.5 py-1.5 rounded border border-slate-200 bg-slate-50"
                  >
                    <div className="min-w-0">
                      <div className="font-medium text-slate-800 truncate">
                        {jt.name || jt.id}{' '}
                        <span className="text-[10px] font-mono text-slate-500">
                          · {jt.single_use ? '一次性' : '多次'} · 已用 {jt.used_count} 次
                        </span>
                      </div>
                      <div className="text-[10px] text-slate-400">
                        过期：{new Date(jt.expires_at).toLocaleString()}
                      </div>
                    </div>
                    <button
                      type="button"
                      onClick={async () => {
                        try {
                          await api.deleteJoinToken(jt.id)
                          await loadJoinTokens()
                          onToast('success', '已删除加入令牌')
                        } catch (err) {
                          onToast('error', err instanceof APIError ? err.message : '删除加入令牌失败')
                        }
                      }}
                      className="p-1 text-slate-400 hover:text-rose-600 shrink-0"
                      title="删除令牌"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>

      {agents.length === 0 ? (
        <div className="bg-white rounded-xl border border-slate-200 p-10 text-center text-slate-400">
          当前暂无已连接发压节点（若以 `--no-local-agent` 启动，请手动接入外部节点）。
        </div>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
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

            return (
              <div key={ag.id} className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-3">
                <div className="flex flex-wrap items-start justify-between gap-2 border-b border-slate-100 pb-2.5">
                  <div className="flex items-center gap-2">
                    <div className="p-2 rounded-lg bg-sky-50 text-sky-700">
                      <Server className="w-4 h-4" />
                    </div>
                    <div>
                      <div className="font-semibold text-sm text-slate-900 flex flex-wrap items-center gap-2">
                        <span>{ag.name}</span>
                        <span className="px-1.5 py-0.2 rounded-full bg-emerald-100 text-emerald-800 text-[10px]">
                          在线
                        </span>
                        {(ag.load_share ?? 0) > 0 && (
                          <span className="px-1.5 py-0.2 rounded-full bg-sky-100 text-sky-800 text-[10px] font-mono">
                            负载份额 {((ag.load_share || 0) * 100).toFixed(1)}%
                          </span>
                        )}
                      </div>
                      <div className="text-[11px] text-slate-500 font-mono">
                        {ag.os}/{ag.arch} · {ag.cpu_cores} 核 · {formatGiB(ag.memory_bytes)} · {ag.remote_addr || '本机'}
                      </div>
                      {(Object.keys(ag.labels || {}).length > 0 || ag.cert_serial) && (
                        <div className="flex flex-wrap items-center gap-1 mt-1">
                          {Object.entries(ag.labels || {}).map(([k, v]) => (
                            <span
                              key={k}
                              className="px-1.5 py-0.5 rounded bg-slate-100 border border-slate-200 font-mono text-[10px] text-slate-700"
                            >
                              {k}={v}
                            </span>
                          ))}
                          {ag.cert_serial && (
                            <span className="px-1.5 py-0.5 rounded bg-emerald-50 border border-emerald-200 font-mono text-[10px] text-emerald-800">
                              mTLS #{ag.cert_serial.slice(0, 8)}
                            </span>
                          )}
                        </div>
                      )}
                    </div>
                  </div>
                  <div className="text-right font-mono text-[11px] text-slate-500">
                    <div>RTT: {ag.rtt_ms.toFixed(2)} ms</div>
                    <div>时钟差: {ag.clock_offset_ms.toFixed(2)} ms</div>
                  </div>
                </div>

                {ag.stats ? (
                  <div className="grid grid-cols-2 gap-3">
                    <div className="space-y-1">
                      <div className="flex items-center justify-between text-slate-600">
                        <span className="inline-flex items-center gap-1">
                          <Cpu className="w-3.5 h-3.5 text-sky-600" /> 整机 CPU
                        </span>
                        <span className="font-mono font-semibold">{cpuPct.toFixed(1)}%</span>
                      </div>
                      <div className="h-2 rounded-full bg-slate-100 overflow-hidden">
                        <div
                          className={`h-full ${cpuPct > 85 ? 'bg-rose-500' : 'bg-sky-500'}`}
                          style={{ width: `${cpuPct}%` }}
                        />
                      </div>
                      <div className="text-[10px] text-slate-400 font-mono">发压进程 CPU: {procCpuPct.toFixed(1)}%</div>
                    </div>

                    <div className="space-y-1">
                      <div className="flex items-center justify-between text-slate-600">
                        <span className="inline-flex items-center gap-1">
                          <HardDrive className="w-3.5 h-3.5 text-emerald-600" /> 内存占用
                        </span>
                        <span className="font-mono font-semibold">{memPct.toFixed(1)}%</span>
                      </div>
                      <div className="h-2 rounded-full bg-slate-100 overflow-hidden">
                        <div
                          className={`h-full ${memPct > 85 ? 'bg-rose-500' : 'bg-emerald-500'}`}
                          style={{ width: `${memPct}%` }}
                        />
                      </div>
                      <div className="text-[10px] text-slate-400 font-mono">
                        {formatGiB(memUsed)} / {formatGiB(memTotal)}
                      </div>
                    </div>

                    <div className="p-2 rounded bg-slate-50 border border-slate-100 font-mono">
                      <div className="text-[10px] text-slate-500 flex items-center gap-1 mb-0.5">
                        <Network className="w-3 h-3 text-sky-600" /> 实时网卡流量 (非回环)
                      </div>
                      <div className="text-slate-800">
                        上行 {formatMbpsFromBps(totalTxBps)} · 下行 {formatMbpsFromBps(totalRxBps)}
                      </div>
                      <div className="text-[10px] text-slate-400">
                        网卡协商上限：{maxLinkBps > 0 ? formatMbpsFromBps(maxLinkBps) : '未检测到'}
                      </div>
                    </div>

                    <div className="p-2 rounded bg-slate-50 border border-slate-100 font-mono">
                      <div className="text-[10px] text-slate-500 mb-0.5">TCP 与临时端口池</div>
                      <div className="text-slate-800">
                        活跃 TCP：{ag.stats.ports.tcp_conns} / 端口段 {ag.stats.ports.ephemeral_count}
                      </div>
                      <div className="text-[10px] text-slate-400">
                        TCP 重传/秒：{ag.stats.tcp.retrans_per_sec.toFixed(2)} · Goroutines：{ag.stats.runtime.goroutines}
                      </div>
                    </div>
                  </div>
                ) : (
                  <p className="text-slate-400">等待节点上报系统监控心跳...</p>
                )}

                <div className="pt-2 border-t border-slate-100 flex flex-wrap items-center justify-between gap-2">
                  <div className="text-slate-600">
                    节点权重（有效 {ag.effective_weight.toFixed(1)}）与手动带宽上限 (Mbps, 0=自动)：
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="text-slate-500">权重</span>
                    <input
                      type="number"
                      min={0.1}
                      step="0.5"
                      disabled={!isAdmin}
                      value={weightInputs[ag.id] ?? '1'}
                      onChange={(e) => setWeightInputs((prev) => ({ ...prev, [ag.id]: e.target.value }))}
                      className="w-16 px-2 py-1 border border-slate-300 rounded font-mono bg-white disabled:opacity-50"
                    />
                    <span className="text-slate-500">带宽</span>
                    <input
                      type="number"
                      min={0}
                      step="1"
                      disabled={!isAdmin}
                      value={bwInputs[ag.id] ?? '0'}
                      onChange={(e) => setBwInputs((prev) => ({ ...prev, [ag.id]: e.target.value }))}
                      className="w-20 px-2 py-1 border border-slate-300 rounded font-mono bg-white disabled:opacity-50"
                    />
                    <span className="text-slate-500">Mbps</span>
                    {isAdmin && (
                      <>
                        <button
                          type="button"
                          disabled={savingId === ag.id}
                          onClick={() => void handleSaveSettings(ag)}
                          className="inline-flex items-center gap-1 px-2.5 py-1 rounded bg-[#0B2A4A] hover:bg-[#123d69] text-white font-medium disabled:opacity-50"
                        >
                          <Save className="w-3 h-3" /> 应用
                        </button>
                        {ag.id !== 'local' && (
                          <button
                            type="button"
                            onClick={async () => {
                              if (confirmRevokeId !== ag.id) {
                                setConfirmRevokeId(ag.id)
                                return
                              }
                              try {
                                await api.revokeAgent(ag.id)
                                setConfirmRevokeId('')
                                await onAgentsRefresh()
                                onToast('success', `已吊销节点「${ag.name}」证书并断开连接`)
                              } catch (err) {
                                onToast('error', err instanceof APIError ? err.message : '吊销节点失败')
                              }
                            }}
                            className={`inline-flex items-center gap-1 px-2 py-1 rounded border font-medium ${
                              confirmRevokeId === ag.id
                                ? 'bg-rose-600 border-rose-600 text-white'
                                : 'border-rose-200 text-rose-700 hover:bg-rose-50'
                            }`}
                          >
                            <Trash2 className="w-3 h-3" /> {confirmRevokeId === ag.id ? '确认吊销' : '吊销'}
                          </button>
                        )}
                      </>
                    )}
                  </div>
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
