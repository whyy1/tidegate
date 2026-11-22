import { useEffect, useMemo, useRef, useState } from 'react'
import type uPlot from 'uplot'
import {
  Activity,
  AlertTriangle,
  BarChart3,
  CheckCircle2,
  CopyCheck,
  Download,
  Gauge,
  Layers,
  OctagonAlert,
  Play,
  RefreshCw,
  Server,
  Square,
  Wrench,
  XCircle,
} from 'lucide-react'
import { LoadConfigEditor } from '../components/LoadConfigEditor'
import { UPlotChart, type AlertWindow } from '../components/UPlotChart'
import { api, APIError, type StartRunPayload } from '../lib/api'
import type {
  API,
  AgentView,
  Alert,
  Environment,
  LoadConfig,
  RunDetail,
  RunEstimate,
  RunHistogramResponse,
  SampleRecord,
  SampleView,
  Scenario,
  ScenarioMixItem,
  SnapshotView,
  TreeNode,
} from '../lib/types'
import { wsClient } from '../lib/ws'

interface Props {
  projectId: string
  environments: Environment[]
  activeEnvId: string
  tempVars: Record<string, string>
  agents: AgentView[]
  canWrite?: boolean
  preloadedAPI: Partial<API> | null
  preloadedScenarioId: string
  inspectRunId: string
  onClearPreloads: () => void
  onReplaySample: (sample: SampleView) => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

function flattenAPIs(nodes: TreeNode[], prefix = ''): Array<{ id: string; name: string; method: string; label: string }> {
  const out: Array<{ id: string; name: string; method: string; label: string }> = []
  for (const n of nodes) {
    if (n.type === 'folder') {
      const next = prefix ? `${prefix} / ${n.name}` : n.name
      if (n.children) out.push(...flattenAPIs(n.children, next))
    } else {
      out.push({
        id: n.id,
        name: n.name,
        method: n.method || 'GET',
        label: prefix ? `${prefix} / ${n.name}` : n.name,
      })
    }
  }
  return out
}

const defaultLoad: LoadConfig = {
  mode: 'vus',
  stages: [
    { duration: '0s', target: 10 },
    { duration: '20s', target: 10 },
  ],
}

const FIXED_BAND_LABELS = [
  '<0.5ms',
  '0.5~1ms',
  '1~2ms',
  '2~5ms',
  '5~10ms',
  '10~25ms',
  '25~50ms',
  '50~100ms',
  '100~250ms',
  '250~500ms',
  '500~1000ms',
  '>=1000ms',
] as const

const REQUEST_PHASES = [
  { key: 'dns', label: 'DNS 解析', color: '#8b5cf6' },
  { key: 'connect', label: 'TCP 建连', color: '#0ea5e9' },
  { key: 'tls', label: 'TLS 握手', color: '#f59e0b' },
  { key: 'first_byte', label: '首字节 (TTFB)', color: '#10b981' },
  { key: 'download', label: '响应下载', color: '#64748b' },
] as const

function parseSelectorText(raw: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const part of raw.split(/[,\n\s]+/)) {
    const t = part.trim()
    if (!t) continue
    const eq = t.indexOf('=')
    if (eq > 0 && eq < t.length - 1) {
      out[t.slice(0, eq).trim()] = t.slice(eq + 1).trim()
    }
  }
  return Object.keys(out).length > 0 ? out : undefined
}

function statusBadge(status: RunDetail['status']) {
  switch (status) {
    case 'preparing':
      return { text: '准备中', cls: 'bg-sky-100 text-sky-800' }
    case 'running':
      return { text: '压测运行中', cls: 'bg-emerald-100 text-emerald-800 animate-pulse' }
    case 'stopping':
      return { text: '正在停止', cls: 'bg-amber-100 text-amber-800' }
    case 'finished':
      return { text: '已完成', cls: 'bg-emerald-100 text-emerald-800' }
    case 'failed':
      return { text: '失败 / 中止', cls: 'bg-rose-100 text-rose-800' }
  }
}

export function LiveRunView({
  projectId,
  environments,
  activeEnvId,
  tempVars,
  agents,
  canWrite = true,
  preloadedAPI,
  preloadedScenarioId,
  inspectRunId,
  onClearPreloads,
  onReplaySample,
  onToast,
}: Props) {
  const [scenarios, setScenarios] = useState<Scenario[]>([])
  const [apis, setApis] = useState<Array<{ id: string; name: string; method: string; label: string }>>([])

  const [sourceType, setSourceType] = useState<'scenario' | 'mix' | 'api' | 'draft'>('scenario')
  const [selectedScenarioId, setSelectedScenarioId] = useState('')
  const [mixItems, setMixItems] = useState<ScenarioMixItem[]>([])
  const [selectedApiId, setSelectedApiId] = useState('')
  const [envOverrideId, setEnvOverrideId] = useState(activeEnvId)
  const [loadCfg, setLoadCfg] = useState<LoadConfig>(defaultLoad)
  const [overrideScenarioLoad, setOverrideScenarioLoad] = useState(false)
  const [nodeSelectorText, setNodeSelectorText] = useState('')
  const [nodeFailurePolicy, setNodeFailurePolicy] = useState<'redistribute' | 'abort'>('redistribute')
  const [showLauncher, setShowLauncher] = useState(true)

  const [estimate, setEstimate] = useState<RunEstimate | null>(null)
  const [estimating, setEstimating] = useState(false)
  const [starting, setStarting] = useState(false)

  const [run, setRun] = useState<RunDetail | null>(null)
  const [seriesState, setSeriesState] = useState<{ snapshots: SnapshotView[]; nicTxMbps: number[] }>({
    snapshots: [],
    nicTxMbps: [],
  })
  const snapshots = seriesState.snapshots
  const nicTxMbpsSeries = seriesState.nicTxMbps
  const targetSyncedRef = useRef(false)
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [samples, setSamples] = useState<SampleRecord[]>([])
  const [histogram, setHistogram] = useState<RunHistogramResponse | null>(null)
  const [histStepId, setHistStepId] = useState('')
  const [sampleFilter, setSampleFilter] = useState<'all' | 'error' | 'slow'>('all')
  const [detailTab, setDetailTab] = useState<'steps' | 'distribution' | 'phases' | 'nodes' | 'thresholds' | 'samples'>('steps')
  const [targetInput, setTargetInput] = useState('10')
  const [updatingTarget, setUpdatingTarget] = useState(false)

  const agentsRef = useRef(agents)
  agentsRef.current = agents

  const currentNicTxMbps = () => {
    let txBps = 0
    for (const ag of agentsRef.current) {
      for (const nic of ag.stats?.nics || []) {
        if (!nic.loopback && nic.up) {
          txBps += nic.tx_bytes_per_sec * 8
        }
      }
    }
    return Number((txBps / 1e6).toFixed(2))
  }

  const loadHistogram = async (id: string) => {
    try {
      setHistogram(await api.runHistogram(id))
    } catch {
      setHistogram(null)
    }
  }

  const hydrateRun = async (id: string) => {
    try {
      const [detail, snaps, smps] = await Promise.all([
        api.getRun(id),
        api.runSnapshots(id),
        api.runSamples(id, { limit: 200 }),
      ])
      setRun(detail)
      setSeriesState({
        snapshots: snaps,
        nicTxMbps: snaps.map(() => 0),
      })
      setAlerts(detail.alerts || [])
      setSamples(smps)
      setHistStepId('')
      void loadHistogram(id)
      if (snaps.length > 0 && snaps[snaps.length - 1].target > 0) {
        targetSyncedRef.current = true
        setTargetInput(String(snaps[snaps.length - 1].target))
      } else {
        targetSyncedRef.current = false
      }
      if (detail.status === 'preparing' || detail.status === 'running' || detail.status === 'stopping') {
        wsClient.subscribe(detail.id)
      }
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '读取压测运行详情失败')
    }
  }

  useEffect(() => {
    setEnvOverrideId(activeEnvId)
  }, [activeEnvId])

  useEffect(() => {
    const loadMeta = async () => {
      try {
        const [scList, tree, recentRuns] = await Promise.all([
          api.listScenarios(projectId),
          api.getTree(projectId),
          api.listRuns(projectId, 5, 0),
        ])
        setScenarios(scList)
        const flat = flattenAPIs(tree.children || [])
        setApis(flat)
        if (!selectedScenarioId && scList.length > 0) setSelectedScenarioId(scList[0].id)
        if (!selectedApiId && flat.length > 0) setSelectedApiId(flat[0].id)

        if (inspectRunId) {
          setShowLauncher(false)
          await hydrateRun(inspectRunId)
        } else if (preloadedAPI) {
          setSourceType('draft')
          setShowLauncher(true)
        } else if (preloadedScenarioId) {
          setSourceType('scenario')
          setSelectedScenarioId(preloadedScenarioId)
          setShowLauncher(true)
        } else if (recentRuns.length > 0) {
          const activeOne = recentRuns.find((r) => r.status === 'running' || r.status === 'preparing' || r.status === 'stopping')
          if (activeOne) {
            setShowLauncher(false)
            await hydrateRun(activeOne.id)
          } else {
            await hydrateRun(recentRuns[0].id)
          }
        }
      } catch {
        // ignore initial load hiccup
      }
    }
    void loadMeta()
    return () => {
      wsClient.unsubscribe()
    }
  }, [projectId])

  useEffect(() => {
    if (inspectRunId) {
      setShowLauncher(false)
      void hydrateRun(inspectRunId)
    }
  }, [inspectRunId])

  useEffect(() => {
    if (preloadedAPI) {
      setSourceType('draft')
      setShowLauncher(true)
    }
  }, [preloadedAPI])

  useEffect(() => {
    if (preloadedScenarioId) {
      setSourceType('scenario')
      setSelectedScenarioId(preloadedScenarioId)
      setShowLauncher(true)
    }
  }, [preloadedScenarioId])

  useEffect(() => {
    const unsub = wsClient.onEvent((ev) => {
      if (!run || ev.run_id !== run.id) return
      if (ev.type === 'snapshot' && ev.snapshot) {
        const snap = ev.snapshot
        const tx = currentNicTxMbps()
        if (!targetSyncedRef.current && snap.target > 0) {
          targetSyncedRef.current = true
          setTargetInput(String(snap.target))
        }
        setSeriesState((prev) => {
          if (prev.snapshots.some((s) => s.seq === snap.seq)) return prev
          let nextSnaps = [...prev.snapshots, snap]
          let nextNic = [...prev.nicTxMbps, tx]
          if (nextSnaps.length > 2400) {
            nextSnaps = nextSnaps.filter((_, idx) => idx % 2 === 0 || idx === nextSnaps.length - 1)
            nextNic = nextNic.filter((_, idx) => idx % 2 === 0 || idx === nextNic.length - 1)
          }
          return { snapshots: nextSnaps, nicTxMbps: nextNic }
        })
        if (snap.samples && snap.samples.length > 0) {
          const incoming: SampleRecord[] = snap.samples.map((sm, idx) => ({
            id: -(snap.seq * 1000 + idx + 1),
            seq: snap.seq,
            kind: sm.kind,
            step: sm.step,
            time: sm.time,
            data: sm,
          }))
          setSamples((prev) => [...prev, ...incoming].slice(-200))
        }
      } else if (ev.type === 'alert' && ev.alert) {
        const al = ev.alert
        setAlerts((prev) => {
          const idx = prev.findIndex((a) => a.id === al.id)
          if (idx >= 0) return prev.map((a, i) => (i === idx ? al : a))
          return [...prev, al]
        })
      } else if (ev.type === 'run_status' && ev.status) {
        setRun((prev) => (prev ? { ...prev, status: ev.status!, error: ev.error || prev.error } : prev))
        if (ev.status === 'finished' || ev.status === 'failed') {
          void api
            .getRun(run.id)
            .then((d) => {
              setRun(d)
              setAlerts(d.alerts || [])
            })
            .catch(() => undefined)
          void api.runSamples(run.id, { limit: 200 }).then(setSamples).catch(() => undefined)
          void loadHistogram(run.id)
        }
      }
    })
    return unsub
  }, [run?.id])

  const buildStartPayload = (): StartRunPayload | null => {
    const vars = Object.keys(tempVars).length > 0 ? tempVars : undefined
    const node_selector = parseSelectorText(nodeSelectorText)
    const dist = {
      ...(node_selector ? { node_selector } : {}),
      node_failure_policy: nodeFailurePolicy,
    }
    if (sourceType === 'draft' && preloadedAPI) {
      return {
        api_id: preloadedAPI.id || undefined,
        api: preloadedAPI,
        environment_id: envOverrideId,
        variables: vars,
        load: loadCfg,
        ...dist,
      }
    }
    if (sourceType === 'mix') {
      const valid = mixItems.filter((m) => m.scenario_id && m.weight > 0)
      if (valid.length === 0) return null
      return {
        mix: valid,
        environment_id: envOverrideId,
        variables: vars,
        load: overrideScenarioLoad ? loadCfg : undefined,
        ...dist,
      }
    }
    if (sourceType === 'api') {
      if (!selectedApiId) return null
      return {
        api_id: selectedApiId,
        environment_id: envOverrideId,
        variables: vars,
        load: loadCfg,
        ...dist,
      }
    }
    if (!selectedScenarioId) return null
    return {
      scenario_id: selectedScenarioId,
      environment_id: envOverrideId,
      variables: vars,
      load: overrideScenarioLoad ? loadCfg : undefined,
      ...dist,
    }
  }

  const handleEstimate = async () => {
    const payload = buildStartPayload()
    if (!payload) {
      onToast('error', '请先选择要压测的场景或接口')
      return
    }
    setEstimating(true)
    try {
      setEstimate(await api.estimateRun(projectId, payload))
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '容量预估失败')
    } finally {
      setEstimating(false)
    }
  }

  const handleStart = async () => {
    const payload = buildStartPayload()
    if (!payload) {
      onToast('error', '请先选择要压测的场景或接口')
      return
    }
    setStarting(true)
    try {
      const created = await api.startRun(projectId, payload)
      onClearPreloads()
      setShowLauncher(false)
      setEstimate(null)
      setSeriesState({ snapshots: [], nicTxMbps: [] })
      targetSyncedRef.current = false
      setAlerts([])
      setSamples([])
      setRun({ ...created, alerts: [] })
      wsClient.subscribe(created.id)
      onToast('success', `压测任务已启动：${created.scenario_name}`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '启动压测失败')
    } finally {
      setStarting(false)
    }
  }

  const handleSetTarget = async (nextTarget: number) => {
    if (!run || nextTarget <= 0) return
    setUpdatingTarget(true)
    try {
      await api.setRunTarget(run.id, nextTarget)
      setTargetInput(String(nextTarget))
      onToast('success', `已将运行目标调整为 ${nextTarget}`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '动态调整目标失败')
    } finally {
      setUpdatingTarget(false)
    }
  }

  const isActive = run && (run.status === 'preparing' || run.status === 'running' || run.status === 'stopping')
  const latest = run?.summary || (snapshots.length > 0 ? snapshots[snapshots.length - 1] : null)

  // Compute time-aligned chart arrays and alert interval windows in elapsed seconds.
  const chartModel = useMemo(() => {
    const xSec: number[] = []
    const rps: number[] = []
    const errRatePct: number[] = []
    const p50: number[] = []
    const p95: number[] = []
    const p99: number[] = []
    const appUpMbps: number[] = []
    const appDownMbps: number[] = []
    const nicTxMbps: (number | null)[] = []
    const vus: number[] = []
    const conns: number[] = []

    const t0 = snapshots[0]?.time ? new Date(snapshots[0].time).getTime() : 0
    const baseOffsetSec = (snapshots[0]?.duration_ms || 1000) / 1000
    let elapsed = 0
    for (let i = 0; i < snapshots.length; i++) {
      const s = snapshots[i]
      if (t0 > 0 && s.time) {
        const ts = new Date(s.time).getTime()
        elapsed = Number.isFinite(ts)
          ? Math.max(baseOffsetSec, (ts - t0) / 1000 + baseOffsetSec)
          : elapsed + (s.duration_ms || 1000) / 1000
      } else {
        elapsed += (s.duration_ms || 1000) / 1000
      }
      xSec.push(Number(elapsed.toFixed(1)))
      rps.push(s.rps || 0)
      errRatePct.push(Number(((s.error_rate || 0) * 100).toFixed(2)))
      p50.push(s.latency?.p50_ms || 0)
      p95.push(s.latency?.p95_ms || 0)
      p99.push(s.latency?.p99_ms || 0)
      appUpMbps.push(Number(((s.sent_bps || 0) / 1e6).toFixed(3)))
      appDownMbps.push(Number(((s.received_bps || 0) / 1e6).toFixed(3)))
      const ntx = nicTxMbpsSeries[i]
      nicTxMbps.push(ntx && ntx > 0 ? ntx : null)
      vus.push(s.active_vus || 0)
      conns.push(s.active_conns || 0)
    }

    const maxSec = xSec[xSec.length - 1] || 1
    const alertWindows: AlertWindow[] =
      t0 > 0
        ? alerts.map((a) => {
            const s = Math.max(0, (new Date(a.started_at).getTime() - t0) / 1000)
            const e = a.ended_at ? Math.min(maxSec, (new Date(a.ended_at).getTime() - t0) / 1000) : maxSec
            return { startSec: s, endSec: Math.max(s + 0.5, e), level: a.level }
          })
        : []

    return {
      rpsData: [xSec, rps, errRatePct] as uPlot.AlignedData,
      latData: [xSec, p50, p95, p99] as uPlot.AlignedData,
      bwData: [xSec, appUpMbps, appDownMbps, nicTxMbps] as uPlot.AlignedData,
      vuData: [xSec, vus, conns] as uPlot.AlignedData,
      alertWindows,
    }
  }, [snapshots, nicTxMbpsSeries, alerts])

  const filteredSamples = samples.filter((s) => sampleFilter === 'all' || s.kind === sampleFilter)

  return (
    <div className="flex-1 overflow-y-auto bg-slate-50 p-4 space-y-4 text-xs">
      {/* Top Launcher Card */}
      <div className="bg-white rounded-xl border border-slate-200 shadow-xs overflow-hidden">
        <div
          onClick={() => setShowLauncher((v) => !v)}
          className="px-4 py-2.5 bg-slate-50 border-b border-slate-200 flex items-center justify-between cursor-pointer"
        >
          <span className="font-semibold text-sm text-slate-900 flex items-center gap-2">
            <Play className="w-4 h-4 text-sky-600" /> 发起新压测与开压容量预估
          </span>
          <span className="text-sky-700 font-medium">{showLauncher ? '收起配置' : '展开发起新压测'}</span>
        </div>

        {showLauncher && (
          <div className="p-4 space-y-4">
            <div className="flex flex-wrap items-center gap-3">
              <div className="inline-flex rounded-md border border-slate-200 p-0.5 bg-slate-50">
                <button
                  type="button"
                  onClick={() => setSourceType('scenario')}
                  className={`px-3 py-1 rounded font-medium ${
                    sourceType === 'scenario' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
                  }`}
                >
                  压测场景
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setSourceType('mix')
                    if (mixItems.length === 0 && scenarios.length > 0) {
                      setMixItems(
                        scenarios.slice(0, 2).map((sc, i) => ({
                          scenario_id: sc.id,
                          weight: i === 0 ? 70 : 30,
                        }))
                      )
                    }
                  }}
                  className={`px-3 py-1 rounded font-medium ${
                    sourceType === 'mix' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
                  }`}
                >
                  多场景混合 (Mix)
                </button>
                <button
                  type="button"
                  onClick={() => setSourceType('api')}
                  className={`px-3 py-1 rounded font-medium ${
                    sourceType === 'api' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
                  }`}
                >
                  单接口快速压测
                </button>
                {preloadedAPI && (
                  <button
                    type="button"
                    onClick={() => setSourceType('draft')}
                    className={`px-3 py-1 rounded font-medium ${
                      sourceType === 'draft' ? 'bg-amber-100 shadow-xs text-amber-900' : 'text-slate-600'
                    }`}
                  >
                    工作台草稿：{preloadedAPI.name || '当前请求'}
                  </button>
                )}
              </div>

              {sourceType === 'scenario' && (
                <select
                  value={selectedScenarioId}
                  onChange={(e) => setSelectedScenarioId(e.target.value)}
                  className="px-2.5 py-1.5 border border-slate-300 rounded-md bg-white min-w-[200px]"
                >
                  <option value="">选择场景...</option>
                  {scenarios.map((sc) => (
                    <option key={sc.id} value={sc.id}>
                      {sc.name} ({sc.steps?.length || 0} 步)
                    </option>
                  ))}
                </select>
              )}

              {sourceType === 'api' && (
                <select
                  value={selectedApiId}
                  onChange={(e) => setSelectedApiId(e.target.value)}
                  className="px-2.5 py-1.5 border border-slate-300 rounded-md bg-white min-w-[220px]"
                >
                  <option value="">选择接口...</option>
                  {apis.map((a) => (
                    <option key={a.id} value={a.id}>
                      [{a.method}] {a.label}
                    </option>
                  ))}
                </select>
              )}

              <select
                value={envOverrideId}
                onChange={(e) => setEnvOverrideId(e.target.value)}
                className="px-2.5 py-1.5 border border-slate-300 rounded-md bg-white"
              >
                <option value="">环境：使用默认 / 无环境</option>
                {environments.map((env) => (
                  <option key={env.id} value={env.id}>
                    环境：{env.name}
                  </option>
                ))}
              </select>

              {(sourceType === 'scenario' || sourceType === 'mix') && (
                <label className="inline-flex items-center gap-1.5 text-slate-700 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={overrideScenarioLoad}
                    onChange={(e) => setOverrideScenarioLoad(e.target.checked)}
                  />
                  <span>临时覆盖负载配置</span>
                </label>
              )}
            </div>

            {sourceType === 'mix' && (
              <div className="rounded-lg border border-slate-200 bg-slate-50/70 p-3 space-y-2">
                <div className="flex items-center justify-between">
                  <span className="font-semibold text-slate-800">多场景按权重加权并发编排</span>
                  <button
                    type="button"
                    onClick={() =>
                      setMixItems((prev) => [
                        ...prev,
                        { scenario_id: scenarios[0]?.id || '', weight: 20 },
                      ])
                    }
                    className="text-sky-700 hover:underline font-medium"
                  >
                    + 添加混合场景
                  </button>
                </div>
                {mixItems.length === 0 ? (
                  <p className="text-slate-400">当前项目暂无场景，请先在「场景与数据」创建压测场景。</p>
                ) : (
                  (() => {
                    const totalW = mixItems.reduce((acc, x) => acc + Math.max(0, x.weight || 0), 0) || 1
                    return (
                      <div className="space-y-1.5">
                        {mixItems.map((it, idx) => (
                          <div key={idx} className="grid grid-cols-12 gap-2 items-center">
                            <select
                              value={it.scenario_id}
                              onChange={(e) =>
                                setMixItems((prev) =>
                                  prev.map((row, j) => (j === idx ? { ...row, scenario_id: e.target.value } : row))
                                )
                              }
                              className="col-span-6 px-2 py-1 border border-slate-300 rounded bg-white"
                            >
                              <option value="">选择场景...</option>
                              {scenarios.map((sc) => (
                                <option key={sc.id} value={sc.id}>
                                  {sc.name}
                                </option>
                              ))}
                            </select>
                            <div className="col-span-3 flex items-center gap-1">
                              <span className="text-slate-500">权重</span>
                              <input
                                type="number"
                                min={0.1}
                                step="any"
                                value={it.weight}
                                onChange={(e) =>
                                  setMixItems((prev) =>
                                    prev.map((row, j) =>
                                      j === idx ? { ...row, weight: Math.max(0.1, Number(e.target.value) || 0) } : row
                                    )
                                  )
                                }
                                className="w-full px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                              />
                            </div>
                            <span className="col-span-2 font-mono text-slate-600">
                              占比 {(((it.weight || 0) / totalW) * 100).toFixed(1)}%
                            </span>
                            <button
                              type="button"
                              onClick={() => setMixItems((prev) => prev.filter((_, j) => j !== idx))}
                              className="col-span-1 justify-self-end text-slate-400 hover:text-rose-600"
                            >
                              ✕
                            </button>
                          </div>
                        ))}
                      </div>
                    )
                  })()
                )}
              </div>
            )}

            {/* Distributed Scheduling & Failover */}
            <div className="rounded-lg border border-slate-200 bg-slate-50/70 p-3 grid grid-cols-1 md:grid-cols-2 gap-3">
              <div>
                <label className="block font-medium text-slate-700 mb-1">
                  目标节点标签筛选 (node_selector，逗号分隔 key=value)
                </label>
                <input
                  type="text"
                  value={nodeSelectorText}
                  onChange={(e) => setNodeSelectorText(e.target.value)}
                  placeholder="留空默认使用全部在线节点；例如 zone=cn-east,tier=perf"
                  className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                />
                {(() => {
                  const pairs = new Set<string>()
                  for (const ag of agents) {
                    for (const [k, v] of Object.entries(ag.labels || {})) {
                      pairs.add(`${k}=${v}`)
                    }
                  }
                  const list = Array.from(pairs)
                  if (list.length === 0) return null
                  return (
                    <div className="flex flex-wrap gap-1 mt-1.5">
                      <span className="text-[10px] text-slate-500">当前节点可用标签：</span>
                      {list.map((kv) => (
                        <button
                          key={kv}
                          type="button"
                          onClick={() =>
                            setNodeSelectorText((cur) => {
                              const parts = cur
                                .split(/[,\s]+/)
                                .map((s) => s.trim())
                                .filter(Boolean)
                              if (parts.includes(kv)) return parts.filter((s) => s !== kv).join(', ')
                              return [...parts, kv].join(', ')
                            })
                          }
                          className="px-1.5 py-0.5 rounded bg-white border border-slate-300 hover:border-sky-400 font-mono text-[10px] text-slate-700"
                        >
                          +{kv}
                        </button>
                      ))}
                    </div>
                  )
                })()}
              </div>
              <div>
                <label className="block font-medium text-slate-700 mb-1">节点掉线容错策略 (node_failure_policy)</label>
                <select
                  value={nodeFailurePolicy}
                  onChange={(e) => setNodeFailurePolicy(e.target.value as 'redistribute' | 'abort')}
                  className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white"
                >
                  <option value="redistribute">掉线后自动重平衡剩余节点承担负载 (redistribute)</option>
                  <option value="abort">任意参与发压节点掉线立即中止运行 (abort)</option>
                </select>
              </div>
            </div>

            {((sourceType !== 'scenario' && sourceType !== 'mix') || overrideScenarioLoad) && (
              <LoadConfigEditor load={loadCfg} onChange={setLoadCfg} compact />
            )}

            {estimate && (
              <div className="rounded-lg border border-sky-200 bg-sky-50/60 p-3 space-y-2">
                <div className="grid grid-cols-2 sm:grid-cols-5 gap-2 font-mono">
                  <div>
                    <div className="text-[11px] text-slate-500">预估峰值 RPS</div>
                    <div className="font-bold text-slate-900">{estimate.estimated_peak_rps} req/s</div>
                  </div>
                  <div>
                    <div className="text-[11px] text-slate-500">单请求估算体积</div>
                    <div className="font-bold text-slate-900">{estimate.avg_request_bytes} B</div>
                  </div>
                  <div>
                    <div className="text-[11px] text-slate-500">预估上行带宽</div>
                    <div className="font-bold text-sky-800">{(estimate.estimated_uplink_bps / 1e6).toFixed(2)} Mbps</div>
                  </div>
                  <div>
                    <div className="text-[11px] text-slate-500">集群可用上限</div>
                    <div className="font-bold text-slate-900">
                      {estimate.available_uplink_bps > 0
                        ? `${(estimate.available_uplink_bps / 1e6).toFixed(1)} Mbps`
                        : '未检测/未设置'}
                    </div>
                  </div>
                  <div>
                    <div className="text-[11px] text-slate-500">在线节点 / 总核数</div>
                    <div className="font-bold text-slate-900">
                      {estimate.online_agents} 节点 / {estimate.total_cpu_cores} 核
                    </div>
                  </div>
                </div>
                {(estimate.warnings?.length || 0) > 0 && (
                  <div className="rounded border border-amber-300 bg-amber-50 p-2 text-amber-900 space-y-0.5">
                    {estimate.warnings!.map((w, i) => (
                      <div key={i} className="flex items-center gap-1.5">
                        <AlertTriangle className="w-3.5 h-3.5 text-amber-600 shrink-0" /> {w}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            <div className="flex flex-wrap items-center justify-end gap-2">
              <button
                type="button"
                disabled={estimating}
                onClick={() => void handleEstimate()}
                className="inline-flex items-center gap-1.5 px-3.5 py-2 rounded-lg border border-slate-300 bg-white hover:bg-slate-50 text-slate-700 font-medium disabled:opacity-50"
              >
                <Gauge className="w-3.5 h-3.5 text-sky-600" />
                {estimating ? '计算中...' : '开压前带宽预估'}
              </button>
              <button
                type="button"
                disabled={!canWrite || starting || Boolean(isActive)}
                onClick={() => void handleStart()}
                className="inline-flex items-center gap-1.5 px-4 py-2 rounded-lg bg-[#0B2A4A] hover:bg-[#123d69] text-white font-semibold disabled:opacity-45"
              >
                <Play className="w-3.5 h-3.5 fill-current" />
                {starting ? '正在调度节点...' : isActive ? '已有任务运行中' : '立即启动压测'}
              </button>
            </div>
          </div>
        )}
      </div>

      {/* Active / Inspected Run Dashboard */}
      {run && (
        <div className="space-y-4">
          <div className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs flex flex-wrap items-center justify-between gap-3">
            <div className="space-y-0.5">
              <div className="flex items-center gap-2">
                <h2 className="text-base font-bold text-slate-900">{run.scenario_name}</h2>
                <span className={`px-2 py-0.5 rounded-full text-[11px] font-semibold ${statusBadge(run.status).cls}`}>
                  {statusBadge(run.status).text}
                </span>
              </div>
              <div className="text-[11px] font-mono text-slate-500">
                Run ID: {run.id} · 创建于 {new Date(run.created_at).toLocaleString()}
                {run.error && <span className="text-rose-700 ml-2">· {run.error}</span>}
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              {isActive ? (
                <>
                  <div className="flex items-center gap-1 bg-slate-100 p-1 rounded-lg border border-slate-200">
                    <span className="text-slate-600 px-1.5">在线调压：</span>
                    <button
                      type="button"
                      disabled={!canWrite || updatingTarget}
                      onClick={() => void handleSetTarget(Math.max(1, Math.round(Number(targetInput || 10) * 0.8)))}
                      className="px-2 py-1 rounded bg-white border border-slate-300 hover:bg-slate-50 font-mono disabled:opacity-45"
                    >
                      -20%
                    </button>
                    <input
                      type="number"
                      min={1}
                      disabled={!canWrite}
                      value={targetInput}
                      onChange={(e) => setTargetInput(e.target.value)}
                      className="w-20 px-2 py-1 border border-slate-300 rounded font-mono bg-white disabled:opacity-50"
                    />
                    <button
                      type="button"
                      disabled={!canWrite || updatingTarget}
                      onClick={() => void handleSetTarget(Number(targetInput))}
                      className="px-2.5 py-1 rounded bg-sky-600 text-white font-medium hover:bg-sky-700 disabled:opacity-45"
                    >
                      应用
                    </button>
                    <button
                      type="button"
                      disabled={!canWrite || updatingTarget}
                      onClick={() => void handleSetTarget(Math.max(1, Math.round(Number(targetInput || 10) * 1.2)))}
                      className="px-2 py-1 rounded bg-white border border-slate-300 hover:bg-slate-50 font-mono disabled:opacity-45"
                    >
                      +20%
                    </button>
                  </div>

                  <button
                    type="button"
                    disabled={!canWrite}
                    onClick={() => void api.stopRun(run.id, 'graceful').catch(() => undefined)}
                    className="inline-flex items-center gap-1 px-3 py-1.5 rounded-lg bg-amber-500 hover:bg-amber-600 text-slate-950 font-semibold disabled:opacity-45"
                  >
                    <Square className="w-3.5 h-3.5" /> 优雅停止
                  </button>
                  <button
                    type="button"
                    disabled={!canWrite}
                    onClick={() => void api.stopRun(run.id, 'abort').catch(() => undefined)}
                    className="inline-flex items-center gap-1 px-3 py-1.5 rounded-lg bg-rose-600 hover:bg-rose-700 text-white font-semibold disabled:opacity-45"
                  >
                    <OctagonAlert className="w-3.5 h-3.5" /> 立即中止
                  </button>
                </>
              ) : (
                <div className="flex flex-wrap items-center gap-1.5">
                  <button
                    type="button"
                    onClick={() => void hydrateRun(run.id)}
                    className="inline-flex items-center gap-1 px-2.5 py-1.5 rounded border border-slate-300 bg-white hover:bg-slate-50 text-slate-700"
                  >
                    <RefreshCw className="w-3.5 h-3.5" /> 刷新
                  </button>
                  {(
                    [
                      { fmt: 'html' as const, label: '导出 HTML 报告' },
                      { fmt: 'csv' as const, label: 'CSV' },
                      { fmt: 'json' as const, label: 'JSON' },
                      { fmt: 'har' as const, label: '错误采样 HAR' },
                      { fmt: 'jtl' as const, label: 'JTL' },
                    ] as const
                  ).map((ex) => (
                    <a
                      key={ex.fmt}
                      href={api.exportRunURL(run.id, ex.fmt)}
                      download
                      className="inline-flex items-center gap-1 px-2.5 py-1.5 rounded border border-slate-300 bg-white hover:bg-slate-50 text-slate-700"
                    >
                      <Download className="w-3.5 h-3.5 text-sky-600" /> {ex.label}
                    </a>
                  ))}
                </div>
              )}
            </div>
          </div>

          {alerts.length > 0 && (
            <div className="rounded-xl border border-amber-400 bg-amber-50 px-4 py-3 text-amber-950 space-y-1.5">
              <div className="font-semibold flex items-center gap-2 text-sm">
                <AlertTriangle className="w-4 h-4 text-amber-600 shrink-0" />
                <span>发压机或阈值在本次压测期间触发告警，当前结果可能低估被测服务真实能力</span>
              </div>
              <div className="space-y-0.5 pl-6 text-xs">
                {alerts.map((al) => (
                  <div key={al.id} className="font-mono">
                    <span className={al.level === 'critical' ? 'text-rose-700 font-bold' : 'text-amber-800 font-semibold'}>
                      [{al.level === 'critical' ? '严重' : '警告'}
                      {al.agent ? ` · ${al.agent}` : ''}]
                    </span>{' '}
                    {al.message}（{new Date(al.started_at).toLocaleTimeString()}
                    {al.ended_at ? ` ~ ${new Date(al.ended_at).toLocaleTimeString()}` : ' ~ 持续中'}）
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* 6 KPI Cards */}
          {latest && (
            <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
              <KPICard label="吞吐量 RPS" value={`${(latest.rps || 0).toFixed(1)} /s`} sub={`总请求 ${latest.requests}`} />
              <KPICard
                label="错误率"
                value={`${((latest.error_rate || 0) * 100).toFixed(2)}%`}
                sub={`失败 ${latest.errors} 次`}
                danger={(latest.error_rate || 0) > 0.01}
              />
              <KPICard label="P50 / P95" value={`${(latest.latency?.p50_ms || 0).toFixed(1)} / ${(latest.latency?.p95_ms || 0).toFixed(1)} ms`} sub="响应延迟" />
              <KPICard label="P99 / Max" value={`${(latest.latency?.p99_ms || 0).toFixed(1)} / ${(latest.latency?.max_ms || 0).toFixed(1)} ms`} sub="长尾延迟" />
              <KPICard label="并发 / 连接" value={`${latest.active_vus} VU`} sub={`活跃连接 ${latest.active_conns}`} />
              <KPICard
                label="上行 / 下行流量"
                value={`${((latest.sent_bps || 0) / 1e6).toFixed(2)} Mbps`}
                sub={`下行 ${((latest.received_bps || 0) / 1e6).toFixed(2)} Mbps`}
              />
            </div>
          )}

          {/* 4 uPlot Charts */}
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            <UPlotChart
              title="吞吐量与错误率走势"
              data={chartModel.rpsData}
              series={[
                { label: 'RPS', stroke: '#0284c7', fill: 'rgba(2, 132, 199, 0.08)' },
                { label: '错误率 (%)', stroke: '#e11d48' },
              ]}
              alertWindows={chartModel.alertWindows}
            />
            <UPlotChart
              title="响应延迟百分位 (ms)"
              yUnit="ms"
              data={chartModel.latData}
              series={[
                { label: 'P50', stroke: '#16a34a' },
                { label: 'P95', stroke: '#d97706' },
                { label: 'P99', stroke: '#dc2626' },
              ]}
              alertWindows={chartModel.alertWindows}
            />
            <UPlotChart
              title="双层网络带宽 (应用层 vs 网卡实时速率 Mbps)"
              yUnit="M"
              data={chartModel.bwData}
              series={[
                { label: '应用上行 Mbps', stroke: '#0ea5e9' },
                { label: '应用下行 Mbps', stroke: '#8b5cf6' },
                { label: '网卡TX Mbps', stroke: '#f97316', dash: [5, 4] },
              ]}
              alertWindows={chartModel.alertWindows}
            />
            <UPlotChart
              title="活跃虚拟用户数与连接数"
              data={chartModel.vuData}
              series={[
                { label: '活跃 VU', stroke: '#0f766e' },
                { label: '活跃连接', stroke: '#64748b' },
              ]}
              alertWindows={chartModel.alertWindows}
            />
          </div>

          {/* Bottom Diagnostic Panel */}
          <div className="bg-white rounded-xl border border-slate-200 shadow-xs overflow-hidden">
            <div className="px-4 py-2 border-b border-slate-200 bg-slate-50 flex flex-wrap items-center justify-between gap-2">
              <div className="flex flex-wrap items-center gap-1 font-medium">
                <button
                  type="button"
                  onClick={() => setDetailTab('steps')}
                  className={`px-3 py-1 rounded ${
                    detailTab === 'steps' ? 'bg-white shadow-xs text-sky-700 font-semibold' : 'text-slate-600'
                  }`}
                >
                  <Activity className="w-3.5 h-3.5 inline mr-1" /> 按步骤统计 ({latest?.steps?.length || 0})
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setDetailTab('distribution')
                    if (run) void loadHistogram(run.id)
                  }}
                  className={`px-3 py-1 rounded ${
                    detailTab === 'distribution' ? 'bg-white shadow-xs text-sky-700 font-semibold' : 'text-slate-600'
                  }`}
                >
                  <BarChart3 className="w-3.5 h-3.5 inline mr-1" /> 延迟分布与热力图
                </button>
                <button
                  type="button"
                  onClick={() => setDetailTab('phases')}
                  className={`px-3 py-1 rounded ${
                    detailTab === 'phases' ? 'bg-white shadow-xs text-sky-700 font-semibold' : 'text-slate-600'
                  }`}
                >
                  <Layers className="w-3.5 h-3.5 inline mr-1" /> 五段耗时分解
                </button>
                <button
                  type="button"
                  onClick={() => setDetailTab('nodes')}
                  className={`px-3 py-1 rounded ${
                    detailTab === 'nodes' ? 'bg-white shadow-xs text-sky-700 font-semibold' : 'text-slate-600'
                  }`}
                >
                  <Server className="w-3.5 h-3.5 inline mr-1" /> 分节点指标 ({latest?.nodes?.length || 0})
                </button>
                <button
                  type="button"
                  onClick={() => setDetailTab('thresholds')}
                  className={`px-3 py-1 rounded ${
                    detailTab === 'thresholds' ? 'bg-white shadow-xs text-sky-700 font-semibold' : 'text-slate-600'
                  }`}
                >
                  <CopyCheck className="w-3.5 h-3.5 inline mr-1" /> 阈值校验 ({run.thresholds?.length || 0})
                </button>
                <button
                  type="button"
                  onClick={() => setDetailTab('samples')}
                  className={`px-3 py-1 rounded ${
                    detailTab === 'samples' ? 'bg-white shadow-xs text-sky-700 font-semibold' : 'text-slate-600'
                  }`}
                >
                  <Wrench className="w-3.5 h-3.5 inline mr-1" /> 错误与慢请求采样 ({samples.length})
                </button>
              </div>
            </div>

            <div className="p-4">
              {detailTab === 'steps' && (
                <div className="overflow-x-auto">
                  {!latest?.steps?.length ? (
                    <p className="text-slate-400">暂无步骤统计数据。</p>
                  ) : (
                    <table className="w-full text-left border-collapse">
                      <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
                        <tr>
                          <th className="px-3 py-1.5">步骤</th>
                          <th className="px-3 py-1.5">请求数</th>
                          <th className="px-3 py-1.5">RPS</th>
                          <th className="px-3 py-1.5">错误率</th>
                          <th className="px-3 py-1.5">P50 (ms)</th>
                          <th className="px-3 py-1.5">P95 (ms)</th>
                          <th className="px-3 py-1.5">P99 (ms)</th>
                          <th className="px-3 py-1.5">状态码</th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-slate-100 font-mono">
                        {latest.steps.map((st, idx) => (
                          <tr key={idx}>
                            <td className="px-3 py-1.5 font-sans font-semibold text-slate-800">
                              <span>{st.name || st.id}</span>
                              {st.is_group && (
                                <span className="ml-1.5 px-1.5 py-0.2 rounded bg-sky-100 text-sky-800 text-[10px] font-medium">
                                  事务组
                                </span>
                              )}
                            </td>
                            <td className="px-3 py-1.5">{st.requests}</td>
                            <td className="px-3 py-1.5">{st.rps.toFixed(1)}</td>
                            <td className={`px-3 py-1.5 ${st.error_rate > 0 ? 'text-rose-700 font-semibold' : ''}`}>
                              {(st.error_rate * 100).toFixed(2)}%
                            </td>
                            <td className="px-3 py-1.5">{st.latency.p50_ms.toFixed(2)}</td>
                            <td className="px-3 py-1.5">{st.latency.p95_ms.toFixed(2)}</td>
                            <td className="px-3 py-1.5">{st.latency.p99_ms.toFixed(2)}</td>
                            <td className="px-3 py-1.5 text-[11px] text-slate-600">
                              {st.status
                                ? Object.entries(st.status)
                                    .map(([code, cnt]) => `${code}×${cnt}`)
                                    .join(' ')
                                : '-'}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                </div>
              )}

              {detailTab === 'distribution' && (
                <LatencyDistributionPanel
                  histogram={histogram}
                  histStepId={histStepId}
                  onChangeStepId={setHistStepId}
                  onRefresh={() => void loadHistogram(run.id)}
                  snapshots={snapshots}
                />
              )}

              {detailTab === 'phases' && <PhasesBreakdownPanel latest={latest} />}

              {detailTab === 'nodes' && (
                <NodesMetricsPanel latest={latest} agents={agents} alerts={alerts} />
              )}

              {detailTab === 'thresholds' && (
                <div>
                  {!run.thresholds?.length ? (
                    <p className="text-slate-400">本次压测未配置阈值规则，或仍在运行尚未生成汇总评估。</p>
                  ) : (
                    <div className="space-y-2">
                      {run.thresholds.map((tr, i) => (
                        <div
                          key={i}
                          className={`flex items-center justify-between p-2.5 rounded-lg border ${
                            tr.passed ? 'bg-emerald-50 border-emerald-200 text-emerald-900' : 'bg-rose-50 border-rose-200 text-rose-900'
                          }`}
                        >
                          <div className="flex items-center gap-2 font-medium">
                            {tr.passed ? (
                              <CheckCircle2 className="w-4 h-4 text-emerald-600" />
                            ) : (
                              <XCircle className="w-4 h-4 text-rose-600" />
                            )}
                            <span>{tr.message}</span>
                          </div>
                          <span className="font-mono text-[11px]">
                            目标 {tr.metric} {tr.op} {tr.value}
                          </span>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              )}

              {detailTab === 'samples' && (
                <div className="space-y-3">
                  <div className="flex items-center gap-2">
                    {(['all', 'error', 'slow'] as const).map((k) => (
                      <button
                        key={k}
                        type="button"
                        onClick={() => setSampleFilter(k)}
                        className={`px-2.5 py-1 rounded border font-medium ${
                          sampleFilter === k ? 'bg-sky-50 border-sky-300 text-sky-800' : 'border-slate-200 text-slate-600'
                        }`}
                      >
                        {k === 'all' ? '全部' : k === 'error' ? '失败请求' : '慢请求'}
                      </button>
                    ))}
                  </div>
                  {filteredSamples.length === 0 ? (
                    <p className="text-slate-400">当前筛选条件下暂无异常采样。</p>
                  ) : (
                    <div className="space-y-2 max-h-96 overflow-y-auto">
                      {filteredSamples.map((rec) => {
                        const sm = rec.data
                        return (
                          <details key={rec.id} className="border border-slate-200 rounded-lg bg-slate-50/50">
                            <summary className="px-3 py-2 cursor-pointer flex flex-wrap items-center justify-between gap-2 hover:bg-slate-100">
                              <div className="font-mono flex items-center gap-2">
                                <span
                                  className={`px-1.5 py-0.5 rounded text-[10px] font-bold ${
                                    sm.kind === 'error' ? 'bg-rose-100 text-rose-800' : 'bg-amber-100 text-amber-800'
                                  }`}
                                >
                                  {sm.kind}
                                </span>
                                <span className="font-semibold">{sm.method}</span>
                                <span className="text-slate-700 truncate max-w-md">{sm.url}</span>
                              </div>
                              <div className="flex items-center gap-2 font-mono">
                                <span>HTTP {sm.status || 'ERR'}</span>
                                <span>{sm.duration_ms}ms</span>
                                {sm.error && <span className="text-rose-700 max-w-xs truncate">{sm.error}</span>}
                                <button
                                  type="button"
                                  onClick={(e) => {
                                    e.preventDefault()
                                    e.stopPropagation()
                                    onReplaySample(sm)
                                  }}
                                  className="ml-2 px-2 py-0.5 rounded bg-sky-600 hover:bg-sky-700 text-white font-sans font-medium"
                                >
                                  在工作台复现
                                </button>
                              </div>
                            </summary>
                            <div className="p-3 border-t border-slate-200 bg-white space-y-2">
                              {sm.request_body && (
                                <div>
                                  <div className="font-semibold text-slate-600 mb-1">采样请求体（已捕获用于复现）</div>
                                  <pre className="p-2 rounded bg-slate-900 text-sky-100 font-mono overflow-auto max-h-36 whitespace-pre-wrap">
                                    {sm.request_body}
                                  </pre>
                                </div>
                              )}
                              <div>
                                <div className="font-semibold text-slate-600 mb-1">响应体片段</div>
                                <pre className="p-2 rounded bg-slate-900 text-slate-100 font-mono overflow-auto max-h-40 whitespace-pre-wrap">
                                  {sm.body || '(空)'}
                                </pre>
                              </div>
                            </div>
                          </details>
                        )
                      })}
                    </div>
                  )}
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function KPICard({ label, value, sub, danger }: { label: string; value: string; sub: string; danger?: boolean }) {
  return (
    <div className="bg-white rounded-xl border border-slate-200 p-3 shadow-xs">
      <div className="text-[11px] text-slate-500">{label}</div>
      <div className={`text-base font-bold font-mono mt-0.5 ${danger ? 'text-rose-600' : 'text-slate-900'}`}>
        {value}
      </div>
      <div className="text-[11px] text-slate-400 font-mono mt-0.5">{sub}</div>
    </div>
  )
}

function LatencyDistributionPanel({
  histogram,
  histStepId,
  onChangeStepId,
  onRefresh,
  snapshots,
}: {
  histogram: RunHistogramResponse | null
  histStepId: string
  onChangeStepId: (id: string) => void
  onRefresh: () => void
  snapshots: SnapshotView[]
}) {
  const stepView = histStepId ? histogram?.steps?.find((s) => s.id === histStepId) : undefined
  const activeBuckets = stepView ? stepView.buckets : histogram?.buckets || []
  const activeBands = stepView ? stepView.bands : histogram?.bands || []
  const activeLatency = stepView ? stepView.latency : histogram?.latency

  const maxBucket = Math.max(1, ...activeBuckets.map((b) => b.count))
  const totalBucketCount = activeBuckets.reduce((acc, b) => acc + b.count, 0) || 1

  // Sample time columns cleanly (max 48 slices) for time × 12-band heatmap
  const { heatSnaps, maxHeatCell } = useMemo(() => {
    const valid = snapshots.filter((s) => (s.latency_buckets?.length || 0) >= 12)
    let sampled = valid
    if (valid.length > 48) {
      const stride = valid.length / 48
      const out: SnapshotView[] = []
      for (let i = 0; i < 48; i++) {
        out.push(valid[Math.min(valid.length - 1, Math.floor(i * stride))])
      }
      sampled = out
    }
    let peak = 1
    for (const s of sampled) {
      for (let i = 0; i < 12; i++) {
        const c = s.latency_buckets?.[i] || 0
        if (c > peak) peak = c
      }
    }
    return { heatSnaps: sampled, maxHeatCell: peak }
  }, [snapshots])

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-3">
          <select
            value={histStepId}
            onChange={(e) => onChangeStepId(e.target.value)}
            className="px-2.5 py-1 border border-slate-300 rounded bg-white text-xs"
          >
            <option value="">全局聚合直方图</option>
            {(histogram?.steps || []).map((st) => (
              <option key={st.id} value={st.id}>
                步骤：{st.name || st.id}
              </option>
            ))}
          </select>
          {activeLatency && activeLatency.count > 0 && (
            <span className="font-mono text-slate-600 text-[11px]">
              样本数 {activeLatency.count} · Mean {activeLatency.mean_ms.toFixed(2)}ms · P50 {activeLatency.p50_ms.toFixed(2)}ms · P95{' '}
              {activeLatency.p95_ms.toFixed(2)}ms · P99 {activeLatency.p99_ms.toFixed(2)}ms · Max {activeLatency.max_ms.toFixed(2)}ms
            </span>
          )}
        </div>
        <button
          type="button"
          onClick={onRefresh}
          className="inline-flex items-center gap-1 px-2.5 py-1 rounded border border-slate-300 bg-white hover:bg-slate-50 text-slate-700"
        >
          <RefreshCw className="w-3 h-3" /> 刷新分布
        </button>
      </div>

      {/* HDR Histogram Buckets */}
      <div className="space-y-2">
        <div className="font-semibold text-slate-800">精确响应延迟直方图 (HDR Histogram 分桶)</div>
        {activeBuckets.length > 0 ? (
          <div className="space-y-1.5 max-h-72 overflow-y-auto pr-1">
            {activeBuckets.map((b, idx) => {
              const pctWidth = (b.count / maxBucket) * 100
              const share = (b.count / totalBucketCount) * 100
              return (
                <div key={idx} className="grid grid-cols-12 gap-2 items-center font-mono text-[11px]">
                  <span className="col-span-3 text-slate-600 truncate">
                    {b.low_ms.toFixed(2)} ~ {b.high_ms.toFixed(2)} ms
                  </span>
                  <div className="col-span-7 h-4 bg-slate-100 rounded overflow-hidden">
                    <div
                      className="h-full bg-sky-600 rounded-r transition-all"
                      style={{ width: `${Math.max(1, pctWidth)}%` }}
                    />
                  </div>
                  <span className="col-span-2 text-right text-slate-700">
                    {b.count} ({share.toFixed(1)}%)
                  </span>
                </div>
              )
            })}
          </div>
        ) : activeBands.some((n) => n > 0) ? (
          <div className="space-y-1.5">
            {FIXED_BAND_LABELS.map((label, i) => {
              const cnt = activeBands[i] || 0
              const maxB = Math.max(1, ...activeBands)
              return (
                <div key={label} className="grid grid-cols-12 gap-2 items-center font-mono text-[11px]">
                  <span className="col-span-2 text-slate-600">{label}</span>
                  <div className="col-span-8 h-4 bg-slate-100 rounded overflow-hidden">
                    <div className="h-full bg-sky-500" style={{ width: `${cnt > 0 ? Math.max(1, (cnt / maxB) * 100) : 0}%` }} />
                  </div>
                  <span className="col-span-2 text-right text-slate-700">{cnt}</span>
                </div>
              )
            })}
          </div>
        ) : (
          <p className="text-slate-400">暂无直方图采样数据（运行中请等待首个统计周期写入）。</p>
        )}
      </div>

      {/* Time x Latency Heatmap */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <span className="font-semibold text-slate-800">时间 × 延迟分桶热力图（纵轴低延迟→高延迟，颜色越深请求密度越高）</span>
          <span className="text-[11px] text-slate-400 font-mono">{heatSnaps.length} 个时间片</span>
        </div>
        {heatSnaps.length === 0 ? (
          <p className="text-slate-400">暂无带分桶数据的实时快照。</p>
        ) : (
          <div className="overflow-x-auto border border-slate-200 rounded-lg p-3 bg-slate-50/50">
            <div className="min-w-[560px] space-y-1">
              {[...FIXED_BAND_LABELS].reverse().map((label, revIdx) => {
                const bandIdx = FIXED_BAND_LABELS.length - 1 - revIdx
                return (
                  <div key={label} className="flex items-center gap-2">
                    <span className="w-20 shrink-0 text-right font-mono text-[10px] text-slate-500">{label}</span>
                    <div className="flex-1 grid gap-0.5" style={{ gridTemplateColumns: `repeat(${heatSnaps.length}, minmax(6px, 1fr))` }}>
                      {heatSnaps.map((snap, sIdx) => {
                        const cnt = snap.latency_buckets?.[bandIdx] || 0
                        const intensity = cnt > 0 ? Math.min(1, Math.log1p(cnt) / Math.log1p(maxHeatCell)) : 0
                        const bg =
                          cnt === 0
                            ? '#f1f5f9'
                            : bandIdx >= 9
                              ? `rgba(225, 29, 72, ${0.22 + intensity * 0.78})`
                              : bandIdx >= 6
                                ? `rgba(217, 119, 6, ${0.2 + intensity * 0.8})`
                                : `rgba(2, 132, 199, ${0.18 + intensity * 0.82})`
                        return (
                          <div
                            key={sIdx}
                            className="h-3.5 rounded-[2px]"
                            style={{ backgroundColor: bg }}
                            title={`Seq #${snap.seq} · ${label}: ${cnt} 请求`}
                          />
                        )
                      })}
                    </div>
                  </div>
                )
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function PhasesBreakdownPanel({ latest }: { latest?: SnapshotView | null }) {
  const globalPhases = latest?.phases
  const steps = latest?.steps || []
  const totalMs = REQUEST_PHASES.reduce((acc, p) => acc + (globalPhases?.[p.key]?.avg_ms || 0), 0)

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="font-semibold text-slate-800">五段网络耗时分解（最近采样窗口均值 ms）</span>
          {totalMs > 0 && <span className="font-mono text-slate-600">五段均值合计：{totalMs.toFixed(3)} ms</span>}
        </div>
        {!globalPhases || totalMs <= 0 ? (
          <p className="text-slate-400">暂无阶段耗时分解数据（长连接复用后 DNS/Connect/TLS 通常为 0ms）。</p>
        ) : (
          <div className="space-y-2">
            <div className="h-6 rounded-lg overflow-hidden flex border border-slate-200 bg-slate-100">
              {REQUEST_PHASES.map((p) => {
                const ms = globalPhases[p.key]?.avg_ms || 0
                if (ms <= 0) return null
                const pct = (ms / totalMs) * 100
                return (
                  <div
                    key={p.key}
                    style={{ width: `${Math.max(1.5, pct)}%`, backgroundColor: p.color }}
                    title={`${p.label}: ${ms.toFixed(3)} ms (${pct.toFixed(1)}%)`}
                    className="h-full flex items-center justify-center text-[10px] font-mono text-white truncate px-1"
                  >
                    {pct >= 9 ? `${p.label} ${ms.toFixed(1)}` : ''}
                  </div>
                )
              })}
            </div>
            <div className="flex flex-wrap gap-3 text-[11px]">
              {REQUEST_PHASES.map((p) => {
                const pv = globalPhases[p.key]
                return (
                  <div key={p.key} className="inline-flex items-center gap-1.5 font-mono">
                    <span className="w-2.5 h-2.5 rounded-sm inline-block" style={{ backgroundColor: p.color }} />
                    <span className="text-slate-700 font-sans">{p.label}:</span>
                    <span className="font-semibold text-slate-900">{pv ? `${pv.avg_ms.toFixed(3)} ms` : '0.000 ms'}</span>
                    <span className="text-slate-400">({pv?.count || 0})</span>
                  </div>
                )
              })}
            </div>
          </div>
        )}
      </div>

      {steps.length > 0 && (
        <div className="overflow-x-auto border border-slate-200 rounded-lg">
          <table className="w-full text-left border-collapse">
            <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
              <tr>
                <th className="px-3 py-1.5">步骤</th>
                {REQUEST_PHASES.map((p) => (
                  <th key={p.key} className="px-3 py-1.5">
                    {p.label} (ms)
                  </th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 font-mono">
              {steps.map((st, idx) => (
                <tr key={idx}>
                  <td className="px-3 py-1.5 font-sans font-semibold text-slate-800">{st.name || st.id}</td>
                  {REQUEST_PHASES.map((p) => {
                    const pv = st.phases?.[p.key]
                    return (
                      <td key={p.key} className="px-3 py-1.5 text-slate-700">
                        {pv ? `${pv.avg_ms.toFixed(2)} (${pv.count})` : '-'}
                      </td>
                    )
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function NodesMetricsPanel({
  latest,
  agents,
  alerts,
}: {
  latest?: SnapshotView | null
  agents: AgentView[]
  alerts: Alert[]
}) {
  const nodes = latest?.nodes || []
  const failoverAlerts = alerts.filter((a) => a.kind === 'node_offline')

  return (
    <div className="space-y-3">
      {failoverAlerts.length > 0 && (
        <div className="rounded-lg border border-amber-300 bg-amber-50 p-3 text-amber-950 space-y-1">
          <div className="font-semibold">节点掉线 / 容错切换事件</div>
          {failoverAlerts.map((al) => (
            <div key={al.id} className="font-mono text-[11px]">
              [{al.agent || 'cluster'}] {al.message}
            </div>
          ))}
        </div>
      )}
      {nodes.length === 0 ? (
        <p className="text-slate-400">当前运行快照暂未包含分节点拆解明细（等待下一次快照推送）。</p>
      ) : (
        <div className="overflow-x-auto border border-slate-200 rounded-lg">
          <table className="w-full text-left border-collapse">
            <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
              <tr>
                <th className="px-3 py-1.5">节点名称</th>
                <th className="px-3 py-1.5">标签 / 权重</th>
                <th className="px-3 py-1.5">目标配额</th>
                <th className="px-3 py-1.5">活跃 VU / 连接</th>
                <th className="px-3 py-1.5">RPS</th>
                <th className="px-3 py-1.5">错误率</th>
                <th className="px-3 py-1.5">P50 / P95 / P99 (ms)</th>
                <th className="px-3 py-1.5">上行 / 下行</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 font-mono">
              {nodes.map((nd) => {
                const live = agents.find((a) => a.id === nd.agent_id)
                const labels = live?.labels ? Object.entries(live.labels).map(([k, v]) => `${k}=${v}`).join(', ') : ''
                return (
                  <tr key={nd.agent_id}>
                    <td className="px-3 py-1.5 font-sans font-semibold text-slate-800">
                      <div>{nd.agent_name || nd.agent_id}</div>
                      <div className="text-[10px] font-mono text-slate-400">{nd.agent_id}</div>
                    </td>
                    <td className="px-3 py-1.5 text-[11px]">
                      <div>{labels || '无标签'}</div>
                      {live && (
                        <div className="text-slate-500">
                          权重 {live.weight} · 占比 {((live.load_share || 0) * 100).toFixed(1)}%
                        </div>
                      )}
                    </td>
                    <td className="px-3 py-1.5">{nd.target.toFixed(1)}</td>
                    <td className="px-3 py-1.5">
                      {nd.active_vus} / {nd.active_conns}
                    </td>
                    <td className="px-3 py-1.5 font-semibold text-sky-800">{nd.rps.toFixed(1)}</td>
                    <td className={`px-3 py-1.5 ${nd.error_rate > 0 ? 'text-rose-700 font-semibold' : ''}`}>
                      {(nd.error_rate * 100).toFixed(2)}%
                    </td>
                    <td className="px-3 py-1.5">
                      {nd.p50_ms.toFixed(1)} / {nd.p95_ms.toFixed(1)} / {nd.p99_ms.toFixed(1)}
                    </td>
                    <td className="px-3 py-1.5">
                      {(nd.sent_bps / 1e6).toFixed(2)} / {(nd.received_bps / 1e6).toFixed(2)} Mbps
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
