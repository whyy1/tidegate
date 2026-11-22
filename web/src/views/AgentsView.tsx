import { useEffect, useState } from 'react'
import { Check, Copy, Cpu, HardDrive, Network, Save, Server, Terminal } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { AgentView, SystemInfo } from '../lib/types'

interface Props {
  agents: AgentView[]
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

export function AgentsView({ agents, onAgentsRefresh, onToast }: Props) {
  const [sysInfo, setSysInfo] = useState<SystemInfo | null>(null)
  const [bwInputs, setBwInputs] = useState<Record<string, string>>({})
  const [savingId, setSavingId] = useState('')
  const [copiedCmd, setCopiedCmd] = useState(false)

  useEffect(() => {
    void api.systemInfo().then(setSysInfo).catch(() => undefined)
  }, [])

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
  }, [agents])

  const joinCmd = sysInfo
    ? `tidegate agent --join ${sysInfo.cluster_addr || '127.0.0.1:7443'} --token <你的集群令牌>`
    : 'tidegate agent --join <控制器IP:7443> --token <你的集群令牌>'

  const handleSaveBandwidth = async (agent: AgentView) => {
    const mbps = Number(bwInputs[agent.id] || '0')
    if (Number.isNaN(mbps) || mbps < 0) {
      onToast('error', '带宽上限必须为非负数字（Mbps），0 表示使用网卡协商速率')
      return
    }
    const bps = Math.round(mbps * 1_000_000)
    setSavingId(agent.id)
    try {
      await api.patchAgent(agent.id, bps)
      await onAgentsRefresh()
      onToast(
        'success',
        bps === 0 ? `已恢复节点「${agent.name}」使用自动网卡协商速率` : `已设置节点「${agent.name}」带宽上限为 ${mbps} Mbps`
      )
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '更新节点带宽上限失败')
    } finally {
      setSavingId('')
    }
  }

  return (
    <div className="flex-1 overflow-y-auto bg-slate-50 p-5 space-y-5 text-xs">
      <div className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-2.5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2 font-semibold text-sm text-slate-900">
            <Terminal className="w-4 h-4 text-sky-600" /> 接入分布式远程发压节点
          </div>
          {sysInfo && (
            <span className="font-mono text-[11px] text-slate-500">
              控制器版本 {sysInfo.version} · 集群端口 {sysInfo.cluster_addr}
            </span>
          )}
        </div>
        <p className="text-slate-600">
          在额外发压机上执行以下命令加入当前控制器集群（需确保控制器启动时配置了 `--cluster-token` 且网络可互通）：
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
                      <div className="font-semibold text-sm text-slate-900 flex items-center gap-2">
                        <span>{ag.name}</span>
                        <span className="px-1.5 py-0.2 rounded-full bg-emerald-100 text-emerald-800 text-[10px]">
                          在线
                        </span>
                      </div>
                      <div className="text-[11px] text-slate-500 font-mono">
                        {ag.os}/{ag.arch} · {ag.cpu_cores} 核 · {formatGiB(ag.memory_bytes)} · {ag.remote_addr || '本机'}
                      </div>
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
                    手动带宽上限（云服务器限速建议填写，0=用网卡协商速率）：
                  </div>
                  <div className="flex items-center gap-1.5">
                    <input
                      type="number"
                      min={0}
                      step="1"
                      value={bwInputs[ag.id] ?? '0'}
                      onChange={(e) => setBwInputs((prev) => ({ ...prev, [ag.id]: e.target.value }))}
                      className="w-24 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                    />
                    <span className="text-slate-500">Mbps</span>
                    <button
                      type="button"
                      disabled={savingId === ag.id}
                      onClick={() => void handleSaveBandwidth(ag)}
                      className="inline-flex items-center gap-1 px-2.5 py-1 rounded bg-[#0B2A4A] hover:bg-[#123d69] text-white font-medium disabled:opacity-50"
                    >
                      <Save className="w-3 h-3" /> 应用
                    </button>
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
