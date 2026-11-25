import { useEffect, useMemo, useRef, useState } from 'react'
import type uPlot from 'uplot'
import { Download, Eye, FileSpreadsheet, GitCompare, ImageDown, RefreshCw, Search, Trash2, X } from 'lucide-react'
import { DropletFilter, RollingNumber } from '../components/DropletFilter'
import { UPlotChart } from '../components/UPlotChart'
import { api, APIError } from '../lib/api'
import { downloadText, fileStamp, toCSV } from '../lib/download'
import { COMPARE_COLORS, exportCompareReportPng, iterationsPerSecond } from '../lib/reportPng'
import type { Run, RunComparisonResponse, RunStatus } from '../lib/types'

interface Props {
  projectId: string
  canWrite?: boolean
  onInspectRun: (runId: string) => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

const MAX_COMPARE = 6

const STATUS_META: Record<RunStatus, { label: string; cls: string }> = {
  preparing: { label: '准备中', cls: 'bg-sky-100 text-sky-800' },
  running: { label: '运行中', cls: 'bg-emerald-100 text-emerald-800' },
  stopping: { label: '停止中', cls: 'bg-amber-100 text-amber-800' },
  finished: { label: '已完成', cls: 'bg-emerald-100 text-emerald-800' },
  failed: { label: '失败', cls: 'bg-rose-100 text-rose-800' },
}

const isActiveRun = (r: Run) => r.status === 'preparing' || r.status === 'running' || r.status === 'stopping'

function fmtDeltaPct(val?: number, lowerIsBetter = false) {
  if (val === undefined || val === null) return <span className="text-fg-subtle">-</span>
  const good = lowerIsBetter ? val <= 0 : val >= 0
  const sign = val > 0 ? '+' : ''
  return (
    <span className={`font-mono font-semibold ${val === 0 ? 'text-fg-muted' : good ? 'text-emerald-700' : 'text-rose-700'}`}>
      {sign}
      {val.toFixed(2)}%
    </span>
  )
}

const num = (v: number | undefined, d: number) => (v === undefined || v === null || !Number.isFinite(v) ? '' : Number(v.toFixed(d)))

export function HistoryCompareView({ projectId, canWrite = true, onInspectRun, onToast }: Props) {
  const [runs, setRuns] = useState<Run[]>([])
  const [loading, setLoading] = useState(false)
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'finished' | 'active' | 'failed'>('all')
  const [selectedIds, setSelectedIds] = useState<string[]>([])
  const [baselineId, setBaselineId] = useState('')
  const [comparing, setComparing] = useState(false)
  const [cmpResult, setCmpResult] = useState<RunComparisonResponse | null>(null)
  const [confirmDelId, setConfirmDelId] = useState('')
  const [confirmBulk, setConfirmBulk] = useState(false)
  const [bulkDeleting, setBulkDeleting] = useState(false)
  const [exportingPng, setExportingPng] = useState(false)
  const cmpSeq = useRef(0)
  const resultRef = useRef<HTMLDivElement>(null)

  const loadRuns = async () => {
    setLoading(true)
    try {
      const list = await api.listRuns(projectId, 100, 0)
      setRuns(list)
      const alive = new Set(list.map((r) => r.id))
      setSelectedIds((prev) => prev.filter((id) => alive.has(id)))
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '读取压测历史失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    setSelectedIds([])
    setBaselineId('')
    setCmpResult(null)
    setQuery('')
    setStatusFilter('all')
    void loadRuns()
  }, [projectId])

  // two-step confirm timers
  useEffect(() => {
    if (!confirmDelId) return
    const t = window.setTimeout(() => setConfirmDelId(''), 3000)
    return () => window.clearTimeout(t)
  }, [confirmDelId])
  useEffect(() => {
    if (!confirmBulk) return
    const t = window.setTimeout(() => setConfirmBulk(false), 3000)
    return () => window.clearTimeout(t)
  }, [confirmBulk])

  const filteredRuns = useMemo(() => {
    const q = query.trim().toLowerCase()
    return runs.filter((r) => {
      if (statusFilter === 'finished' && r.status !== 'finished') return false
      if (statusFilter === 'failed' && r.status !== 'failed') return false
      if (statusFilter === 'active' && !isActiveRun(r)) return false
      if (!q) return true
      return (r.scenario_name || '').toLowerCase().includes(q) || r.id.toLowerCase().includes(q)
    })
  }, [runs, query, statusFilter])

  const effectiveBase = baselineId && selectedIds.includes(baselineId) ? baselineId : selectedIds[0] || ''
  const canCompare = selectedIds.length >= 2 && selectedIds.length <= MAX_COMPARE
  const compareKey = canCompare ? [effectiveBase, ...selectedIds.filter((x) => x !== effectiveBase)].join(',') : ''

  const runCompare = async (ids: string[]) => {
    const seq = ++cmpSeq.current
    setComparing(true)
    try {
      const res = await api.compareRuns(ids, 600)
      if (seq === cmpSeq.current) setCmpResult(res)
    } catch (err) {
      if (seq === cmpSeq.current) onToast('error', err instanceof APIError ? err.message : '对比分析失败')
    } finally {
      if (seq === cmpSeq.current) setComparing(false)
    }
  }

  // Auto (re)compare whenever the 2–6 selection or the baseline changes.
  useEffect(() => {
    if (!compareKey) {
      cmpSeq.current++
      setComparing(false)
      setCmpResult(null)
      return
    }
    const t = window.setTimeout(() => void runCompare(compareKey.split(',')), 250)
    return () => window.clearTimeout(t)
  }, [compareKey])

  const toggleSelect = (id: string) => {
    setSelectedIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]))
  }

  const visibleIds = filteredRuns.map((r) => r.id)
  const visibleSelected = visibleIds.filter((id) => selectedIds.includes(id)).length
  const allVisibleSelected = visibleIds.length > 0 && visibleSelected === visibleIds.length
  const someVisibleSelected = visibleSelected > 0 && !allVisibleSelected

  const toggleSelectAll = () => {
    if (allVisibleSelected) {
      const hide = new Set(visibleIds)
      setSelectedIds((prev) => prev.filter((id) => !hide.has(id)))
    } else {
      setSelectedIds((prev) => [...prev, ...visibleIds.filter((id) => !prev.includes(id))])
    }
  }

  const deletableSelected = selectedIds.filter((id) => {
    const r = runs.find((x) => x.id === id)
    return r && !isActiveRun(r)
  })

  const handleBulkDelete = async () => {
    if (deletableSelected.length === 0) return
    if (!confirmBulk) {
      setConfirmBulk(true)
      return
    }
    setConfirmBulk(false)
    setBulkDeleting(true)
    let ok = 0
    const deletedIds = new Set<string>()
    let lastErr = ''
    for (const id of deletableSelected) {
      try {
        await api.deleteRun(id)
        deletedIds.add(id)
        ok++
      } catch (err) {
        lastErr = err instanceof APIError ? err.message : '删除失败'
      }
    }
    setBulkDeleting(false)
    const failed = deletableSelected.length - ok
    if (deletedIds.size > 0) {
      setSelectedIds((prev) => prev.filter((id) => !deletedIds.has(id)))
    }
    await loadRuns()
    if (failed > 0) onToast('error', `已删除 ${ok} 条，${failed} 条失败：${lastErr}`)
    else onToast('success', `已删除 ${ok} 条压测记录`)
  }

  const handleExportPng = async () => {
    if (!cmpResult) return
    setExportingPng(true)
    try {
      await exportCompareReportPng({ items: cmpResult.runs, colors: COMPARE_COLORS, chartRoot: resultRef.current })
    } catch (err) {
      onToast('error', err instanceof Error ? err.message : '导出对比 PNG 失败')
    } finally {
      setExportingPng(false)
    }
  }

  const handleExportCsv = () => {
    if (!cmpResult) return
    const rows: unknown[][] = [
      [
        '记录 ID',
        '场景',
        '角色',
        '状态',
        '开始时间',
        'QPS/RPS',
        'QPS/RPS 变化%',
        'TPS',
        'P50 ms',
        'P50 变化%',
        'P95 ms',
        'P95 变化%',
        'P99 ms',
        'P99 变化%',
        'Max ms',
        '错误率%',
        '错误率差值%',
        '请求数',
        '迭代数',
        '丢弃迭代',
      ],
    ]
    cmpResult.runs.forEach((it, idx) => {
      const s = it.summary
      const d = idx > 0 ? it.delta : undefined
      rows.push([
        it.run.id,
        it.run.scenario_name,
        idx === 0 ? '基线' : '对比',
        STATUS_META[it.run.status]?.label || it.run.status,
        it.run.started_at || it.run.created_at,
        num(s?.rps, 2),
        num(d?.rps_pct, 2),
        num(iterationsPerSecond(s), 2),
        num(s?.latency?.p50_ms, 2),
        num(d?.p50_pct, 2),
        num(s?.latency?.p95_ms, 2),
        num(d?.p95_pct, 2),
        num(s?.latency?.p99_ms, 2),
        num(d?.p99_pct, 2),
        num(s?.latency?.max_ms, 2),
        num(s ? s.error_rate * 100 : undefined, 3),
        num(d ? d.error_rate_diff * 100 : undefined, 3),
        s?.requests ?? '',
        s?.iterations ?? '',
        s?.dropped_iterations ?? '',
      ])
    })
    const stepRows = cmpResult.runs.slice(1).flatMap((it) =>
      Object.entries(it.delta?.step_deltas || {}).map(([key, sd]) => [
        it.run.id,
        it.run.scenario_name,
        sd.name || sd.id || key,
        num(sd.rps_pct, 2),
        num(sd.p50_pct, 2),
        num(sd.p95_pct, 2),
        num(sd.p99_pct, 2),
        num(sd.error_rate_diff * 100, 3),
      ]),
    )
    if (stepRows.length > 0) {
      rows.push([])
      rows.push(['对比记录 ID', '场景', '步骤', 'QPS/RPS 变化%', 'P50 变化%', 'P95 变化%', 'P99 变化%', '错误率差值%'])
      rows.push(...stepRows)
    }
    downloadText(`tidegate-compare-${fileStamp()}.csv`, toCSV(rows), 'text/csv;charset=utf-8')
  }

  // Align snapshots by sequential elapsed second index so runs overlay cleanly.
  const cmpCharts = useMemo(() => {
    if (!cmpResult || cmpResult.runs.length === 0) return null
    let maxLen = 0
    for (const item of cmpResult.runs) {
      maxLen = Math.max(maxLen, item.snapshots?.length || 0)
    }
    const xSec = Array.from({ length: maxLen }, (_, i) => i + 1)
    const series = cmpResult.runs.map((item, idx) => ({
      label: `${idx === 0 ? '[基线] ' : ''}${item.run.scenario_name} (${item.run.id.slice(-5)})`,
      stroke: COMPARE_COLORS[idx % COMPARE_COLORS.length],
    }))
    const rpsY = cmpResult.runs.map((item) =>
      xSec.map((_, i) => (item.snapshots?.[i] ? item.snapshots[i].rps || 0 : null)),
    )
    const p95Y = cmpResult.runs.map((item) =>
      xSec.map((_, i) => (item.snapshots?.[i] ? item.snapshots[i].latency?.p95_ms || 0 : null)),
    )
    return {
      series,
      rpsData: [xSec, ...rpsY] as uPlot.AlignedData,
      p95Data: [xSec, ...p95Y] as uPlot.AlignedData,
    }
  }, [cmpResult])

  const btn =
    'inline-flex items-center gap-1 px-2.5 py-1.5 rounded border border-line-strong bg-surface hover:bg-surface-hover text-fg disabled:opacity-45'

  return (
    <div className="flex-1 overflow-y-auto tg-scroller bg-transparent p-2.5 pb-[72px] space-y-3 text-xs tg-tabular">
      <div className="tg-accent-card !overflow-visible px-4 py-3 flex flex-wrap items-center justify-between gap-2 relative z-20">
        <div className="flex flex-wrap items-center gap-2.5">
          <h2 className="text-sm font-semibold text-fg">压测记录</h2>
          <DropletFilter
            size="sm"
            ariaLabel="按运行状态筛选"
            value={statusFilter}
            onChange={(v) => setStatusFilter(v as typeof statusFilter)}
            options={[
              { value: 'all', label: '全部', dotClass: 'bg-sky-400' },
              { value: 'finished', label: '已完成', dotClass: 'bg-emerald-400' },
              { value: 'active', label: '运行中', dotClass: 'bg-amber-400' },
              { value: 'failed', label: '失败', dotClass: 'bg-rose-400' },
            ]}
          />
          <RollingNumber
            value={filteredRuns.length}
            suffix=" 条"
            className="text-[11px] font-mono text-fg-subtle"
          />
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {canCompare && (
            <select
              name="baseline_run"
              aria-label="选择基线压测记录"
              value={effectiveBase}
              onChange={(e) => setBaselineId(e.target.value)}
              className="tg-select px-2.5 py-1.5 border border-line-strong rounded-lg bg-surface text-fg"
            >
              {selectedIds.map((id) => {
                const r = runs.find((x) => x.id === id)
                return (
                  <option key={id} value={id}>
                    基线：{r?.scenario_name || id} ({id.slice(-5)})
                  </option>
                )
              })}
            </select>
          )}
          <button
            type="button"
            disabled={comparing || !canCompare}
            onClick={() => compareKey && void runCompare(compareKey.split(','))}
            title={selectedIds.length > MAX_COMPARE ? `对比最多 ${MAX_COMPARE} 条` : '勾选 2~6 条自动对比'}
            className="inline-flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-primary hover:bg-primary-hover text-white font-semibold disabled:opacity-45"
          >
            <GitCompare className={`w-3.5 h-3.5 ${comparing ? 'animate-pulse' : ''}`} />
            {comparing ? '对比中' : `对比 ${Math.min(selectedIds.length, 99)}/${MAX_COMPARE}`}
          </button>
          <button
            type="button"
            onClick={() => void loadRuns()}
            className="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-lg border border-line-strong bg-surface hover:bg-surface-hover text-fg"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} /> 刷新
          </button>
        </div>
      </div>

      <div className="tg-glass-card overflow-hidden">
        <div className="p-4 space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <search className="relative flex-1 min-w-[14rem] max-w-md">
              <Search className="w-3.5 h-3.5 text-fg-subtle absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
              <input
                type="search"
                name="run_search"
                aria-label="按场景名称搜索"
                autoCorrect="off"
                autoCapitalize="none"
                spellCheck={false}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="场景名称 / Run ID"
                className="w-full pl-8 pr-7 py-1.5 border border-line-strong rounded-md bg-surface text-fg placeholder:text-fg-subtle focus:outline-none focus:border-sky-500"
              />
              {query && (
                <button
                  type="button"
                  aria-label="清空搜索"
                  onClick={() => setQuery('')}
                  className="absolute right-1.5 top-1/2 -translate-y-1/2 p-0.5 rounded text-fg-subtle hover:text-fg"
                >
                  <X className="w-3.5 h-3.5" />
                </button>
              )}
            </search>
            <div className="flex flex-wrap items-center gap-2">
              {selectedIds.length > 0 && (
                <span className="text-fg-muted">
                  已选 <span className="font-mono font-semibold text-fg">{selectedIds.length}</span>
                  {selectedIds.length > MAX_COMPARE && <span className="ml-1.5 text-amber-700">对比最多 {MAX_COMPARE} 条</span>}
                </span>
              )}
              {selectedIds.length > 0 && (
                <button type="button" onClick={() => setSelectedIds([])} className={btn}>
                  <X className="w-3.5 h-3.5" /> 清空选择
                </button>
              )}
              {canWrite && (
                <button
                  type="button"
                  disabled={bulkDeleting || deletableSelected.length === 0}
                  onClick={() => void handleBulkDelete()}
                  onBlur={() => setConfirmBulk(false)}
                  title={
                    deletableSelected.length < selectedIds.length ? '运行中的记录不会被删除' : '删除所选压测记录'
                  }
                  className={
                    confirmBulk
                      ? 'inline-flex items-center gap-1 px-2.5 py-1.5 rounded bg-rose-600 hover:bg-rose-solid text-white font-semibold'
                      : 'inline-flex items-center gap-1 px-2.5 py-1.5 rounded border border-rose-200 bg-surface hover:bg-rose-50 text-rose-700 disabled:opacity-45'
                  }
                >
                  <Trash2 className="w-3.5 h-3.5" />
                  {bulkDeleting
                    ? '删除中'
                    : confirmBulk
                      ? `确认删除 ${deletableSelected.length} 条？`
                      : `批量删除${deletableSelected.length > 0 ? ` ${deletableSelected.length}` : ''}`}
                </button>
              )}
            </div>
          </div>

          {runs.length === 0 ? (
            <p className="text-fg-subtle py-8 text-center">{loading ? '加载中…' : '该项目暂无压测历史记录。'}</p>
          ) : filteredRuns.length === 0 ? (
            <p className="text-fg-subtle py-8 text-center">没有匹配「{query}」的记录</p>
          ) : (
            <div className="overflow-x-auto border border-line rounded-lg">
              <table className="w-full text-left border-collapse">
                <thead className="bg-surface-subtle border-b border-line text-fg-subtle">
                  <tr>
                    <th className="w-10 px-3 py-2 text-center">
                      <input
                        type="checkbox"
                        aria-label="全选当前列表"
                        checked={allVisibleSelected}
                        ref={(el) => {
                          if (el) el.indeterminate = someVisibleSelected
                        }}
                        onChange={toggleSelectAll}
                        className="rounded border-slate-300 text-sky-600"
                      />
                    </th>
                    <th className="px-3 py-2">场景 / 接口</th>
                    <th className="px-3 py-2 w-20">状态</th>
                    <th className="px-3 py-2 w-24 text-right">QPS / RPS</th>
                    <th className="px-3 py-2 w-20 text-right">TPS</th>
                    <th className="px-3 py-2 w-20 text-right">错误率</th>
                    <th className="px-3 py-2 w-28 text-right">P95 / P99</th>
                    <th className="px-3 py-2 w-40">开始时间</th>
                    <th className="px-3 py-2 text-right">导出与操作</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {filteredRuns.map((r) => {
                    const checked = selectedIds.includes(r.id)
                    const active = isActiveRun(r)
                    const isBase = checked && canCompare && r.id === effectiveBase
                    const st = STATUS_META[r.status] || { label: r.status, cls: 'bg-surface-subtle text-fg-muted' }
                    const tps = iterationsPerSecond(r.summary)
                    return (
                      <tr key={r.id} className={checked ? 'bg-sky-500/10' : 'hover:bg-surface-hover'}>
                        <td className="px-3 py-2 text-center">
                          <input
                            type="checkbox"
                            aria-label={`选择 ${r.scenario_name}`}
                            checked={checked}
                            onChange={() => toggleSelect(r.id)}
                            className="rounded border-slate-300 text-sky-600"
                          />
                        </td>
                        <td className="px-3 py-2 min-w-0">
                          <div className="font-semibold text-fg flex items-center gap-1.5">
                            <span className="truncate">{r.scenario_name}</span>
                            {isBase && (
                              <span className="shrink-0 px-1.5 py-px rounded bg-sky-100 text-sky-800 text-[10px] font-medium">基线</span>
                            )}
                          </div>
                          <div className="text-[10px] font-mono text-fg-subtle">{r.id}</div>
                        </td>
                        <td className="px-3 py-2">
                          <span className={`px-2 py-0.5 rounded-full text-[10px] font-semibold whitespace-nowrap ${st.cls}`}>{st.label}</span>
                        </td>
                        <td className="px-3 py-2 font-mono text-right">{r.summary ? r.summary.rps.toFixed(1) : '-'}</td>
                        <td className="px-3 py-2 font-mono text-right">
                          {r.summary ? tps.toFixed(2) : '-'}
                          {(r.summary?.dropped_iterations || 0) > 0 && (
                            <span
                              className="ml-1 px-1 rounded bg-amber-100 text-amber-800 text-[10px]"
                              title={`丢弃迭代 ${r.summary!.dropped_iterations}`}
                            >
                              丢 {r.summary!.dropped_iterations}
                            </span>
                          )}
                        </td>
                        <td
                          className={`px-3 py-2 font-mono text-right ${
                            r.summary && r.summary.error_rate > 0.01 ? 'text-rose-700 font-semibold' : ''
                          }`}
                        >
                          {r.summary ? `${(r.summary.error_rate * 100).toFixed(2)}%` : '-'}
                        </td>
                        <td className="px-3 py-2 font-mono text-right whitespace-nowrap">
                          {r.summary ? `${r.summary.latency.p95_ms.toFixed(1)} / ${r.summary.latency.p99_ms.toFixed(1)}` : '-'}
                        </td>
                        <td className="px-3 py-2 text-fg-muted whitespace-nowrap">{new Date(r.created_at).toLocaleString()}</td>
                        <td className="px-3 py-2 text-right">
                          <div className="inline-flex items-center gap-1.5">
                            <button
                              type="button"
                              onClick={() => onInspectRun(r.id)}
                              className="inline-flex items-center gap-1 px-2 py-1 rounded border border-line-strong bg-surface hover:bg-surface-hover text-fg-muted"
                            >
                              <Eye className="w-3 h-3 text-sky-600" /> 详情
                            </button>
                            <a
                              href={api.exportRunURL(r.id, 'html')}
                              download
                              className="inline-flex items-center gap-1 px-2 py-1 rounded border border-line-strong bg-surface hover:bg-surface-hover text-fg-muted"
                              title="下载独立 HTML 报告"
                            >
                              <Download className="w-3 h-3 text-sky-600" /> HTML
                            </a>
                            <a
                              href={api.exportRunURL(r.id, 'har')}
                              download
                              className="px-1.5 py-1 rounded border border-line-strong bg-surface hover:bg-surface-hover text-fg-muted"
                              title="导出错误采样 HAR"
                            >
                              HAR
                            </a>
                            <a
                              href={api.exportRunURL(r.id, 'jtl')}
                              download
                              className="px-1.5 py-1 rounded border border-line-strong bg-surface hover:bg-surface-hover text-fg-muted"
                              title="导出 JMeter 兼容 JTL"
                            >
                              JTL
                            </a>
                            {!active && canWrite && (
                              <button
                                type="button"
                                aria-label="删除压测记录"
                                title={confirmDelId === r.id ? '再次点击确认删除' : '删除压测记录'}
                                onBlur={() => setConfirmDelId((cur) => (cur === r.id ? '' : cur))}
                                onClick={async () => {
                                  if (confirmDelId !== r.id) {
                                    setConfirmDelId(r.id)
                                    return
                                  }
                                  try {
                                    await api.deleteRun(r.id)
                                    setConfirmDelId('')
                                    setSelectedIds((prev) => prev.filter((x) => x !== r.id))
                                    await loadRuns()
                                    onToast('success', '已删除压测记录')
                                  } catch (err) {
                                    onToast('error', err instanceof APIError ? err.message : '删除失败')
                                  }
                                }}
                                className={`inline-flex items-center gap-1 p-1 rounded ${
                                  confirmDelId === r.id
                                    ? 'bg-rose-600 text-white px-2 font-semibold'
                                    : 'text-fg-subtle hover:text-rose-600 hover:bg-rose-50'
                                }`}
                              >
                                <Trash2 className="w-3.5 h-3.5" />
                                {confirmDelId === r.id && <span>确认删除？</span>}
                              </button>
                            )}
                          </div>
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>

      {/* Comparison Results */}
      {cmpResult && (
        <div ref={resultRef} className={`space-y-3 transition-opacity ${comparing ? 'opacity-60' : ''}`}>
          <div className="tg-glass-card p-4 space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="text-sm font-semibold text-fg">关键指标对比</h3>
              <div className="flex flex-wrap items-center gap-1.5">
                <button type="button" disabled={exportingPng} onClick={() => void handleExportPng()} className={btn}>
                  <ImageDown className="w-3.5 h-3.5 text-sky-600" /> {exportingPng ? '生成中' : '导出对比 PNG'}
                </button>
                <button type="button" onClick={handleExportCsv} className={btn}>
                  <FileSpreadsheet className="w-3.5 h-3.5 text-emerald-600" /> 明细 CSV
                </button>
              </div>
            </div>
            <div className="overflow-x-auto border border-line rounded-lg">
              <table className="w-full text-left border-collapse">
                <thead className="bg-surface-subtle border-b border-line text-fg-subtle">
                  <tr>
                    <th className="px-3 py-2">压测记录</th>
                    <th className="px-3 py-2 text-right">QPS / RPS</th>
                    <th className="px-3 py-2 text-right">TPS</th>
                    <th className="px-3 py-2 text-right">P50 ms</th>
                    <th className="px-3 py-2 text-right">P95 ms</th>
                    <th className="px-3 py-2 text-right">P99 ms</th>
                    <th className="px-3 py-2 text-right">错误率</th>
                    <th className="px-3 py-2 w-24" />
                  </tr>
                </thead>
                <tbody className="divide-y divide-line">
                  {cmpResult.runs.map((item, idx) => (
                    <tr key={item.run.id} className={idx === 0 ? 'bg-sky-500/8' : 'hover:bg-surface-hover'}>
                      <td className="px-3 py-2">
                        <div className="font-semibold text-fg flex items-center gap-1.5">
                          <span
                            className="w-2.5 h-2.5 rounded-full inline-block shrink-0"
                            style={{ backgroundColor: COMPARE_COLORS[idx % COMPARE_COLORS.length] }}
                          />
                          <span className="truncate">{item.run.scenario_name}</span>
                          {idx === 0 && (
                            <span className="shrink-0 px-1.5 py-px rounded bg-sky-100 text-sky-800 text-[10px]">基线</span>
                          )}
                        </div>
                        <div className="text-[10px] font-mono text-fg-subtle">{item.run.id}</div>
                      </td>
                      <td className="px-3 py-2 font-mono text-right whitespace-nowrap">
                        {item.summary ? item.summary.rps.toFixed(1) : '-'}
                        {idx > 0 && <span className="ml-1.5">{fmtDeltaPct(item.delta?.rps_pct)}</span>}
                      </td>
                      <td className="px-3 py-2 font-mono text-right">
                        {item.summary ? iterationsPerSecond(item.summary).toFixed(2) : '-'}
                      </td>
                      <td className="px-3 py-2 font-mono text-right whitespace-nowrap">
                        {item.summary ? item.summary.latency.p50_ms.toFixed(2) : '-'}
                        {idx > 0 && <span className="ml-1.5">{fmtDeltaPct(item.delta?.p50_pct, true)}</span>}
                      </td>
                      <td className="px-3 py-2 font-mono text-right whitespace-nowrap">
                        {item.summary ? item.summary.latency.p95_ms.toFixed(2) : '-'}
                        {idx > 0 && <span className="ml-1.5">{fmtDeltaPct(item.delta?.p95_pct, true)}</span>}
                      </td>
                      <td className="px-3 py-2 font-mono text-right whitespace-nowrap">
                        {item.summary ? item.summary.latency.p99_ms.toFixed(2) : '-'}
                        {idx > 0 && <span className="ml-1.5">{fmtDeltaPct(item.delta?.p99_pct, true)}</span>}
                      </td>
                      <td className="px-3 py-2 font-mono text-right whitespace-nowrap">
                        {item.summary ? `${(item.summary.error_rate * 100).toFixed(2)}%` : '-'}
                        {idx > 0 && item.delta && (
                          <span
                            className={`ml-1.5 font-semibold ${item.delta.error_rate_diff <= 0 ? 'text-emerald-700' : 'text-rose-700'}`}
                          >
                            {item.delta.error_rate_diff > 0 ? '+' : ''}
                            {(item.delta.error_rate_diff * 100).toFixed(2)}%
                          </span>
                        )}
                      </td>
                      <td className="px-3 py-2 text-right">
                        {idx > 0 && (
                          <button
                            type="button"
                            disabled={comparing}
                            onClick={() => setBaselineId(item.run.id)}
                            className="px-2 py-0.5 rounded border border-line-strong bg-surface hover:bg-surface-hover text-fg-muted whitespace-nowrap disabled:opacity-45"
                          >
                            设为基线
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>

          {cmpResult.runs.some(
            (item, idx) => idx > 0 && item.delta?.step_deltas && Object.keys(item.delta.step_deltas).length > 0,
          ) && (
            <div className="tg-glass-card p-4 space-y-3">
              <h3 className="text-sm font-semibold text-fg">分步骤回归明细</h3>
              <div className="overflow-x-auto border border-line rounded-lg">
                <table className="w-full text-left border-collapse">
                  <thead className="bg-surface-subtle border-b border-line text-fg-subtle">
                    <tr>
                      <th className="px-3 py-2">对比记录</th>
                      <th className="px-3 py-2">步骤</th>
                      <th className="px-3 py-2 text-right">QPS / RPS</th>
                      <th className="px-3 py-2 text-right">P50</th>
                      <th className="px-3 py-2 text-right">P95</th>
                      <th className="px-3 py-2 text-right">P99</th>
                      <th className="px-3 py-2 text-right">错误率差值</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-line">
                    {cmpResult.runs.slice(1).flatMap((item, runIdx) =>
                      Object.entries(item.delta?.step_deltas || {}).map(([stepKey, sd]) => (
                        <tr key={`${item.run.id}-${stepKey}`}>
                          <td className="px-3 py-2">
                            <span className="inline-flex items-center gap-1.5 font-medium text-fg">
                              <span
                                className="w-2 h-2 rounded-full inline-block"
                                style={{ backgroundColor: COMPARE_COLORS[(runIdx + 1) % COMPARE_COLORS.length] }}
                              />
                              {item.run.scenario_name} ({item.run.id.slice(-5)})
                            </span>
                          </td>
                          <td className="px-3 py-2 font-mono text-fg">{sd.name || sd.id || stepKey}</td>
                          <td className="px-3 py-2 font-mono text-right">{fmtDeltaPct(sd.rps_pct)}</td>
                          <td className="px-3 py-2 font-mono text-right">{fmtDeltaPct(sd.p50_pct, true)}</td>
                          <td className="px-3 py-2 font-mono text-right">{fmtDeltaPct(sd.p95_pct, true)}</td>
                          <td className="px-3 py-2 font-mono text-right">{fmtDeltaPct(sd.p99_pct, true)}</td>
                          <td className="px-3 py-2 font-mono text-right">
                            <span className={sd.error_rate_diff <= 0 ? 'text-emerald-700' : 'text-rose-700'}>
                              {sd.error_rate_diff > 0 ? '+' : ''}
                              {(sd.error_rate_diff * 100).toFixed(2)}%
                            </span>
                          </td>
                        </tr>
                      )),
                    )}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {cmpCharts && (
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              <UPlotChart title="QPS / RPS 叠加对比" data={cmpCharts.rpsData} series={cmpCharts.series} />
              <UPlotChart title="P95 响应延迟 (ms) 叠加对比" yUnit="ms" data={cmpCharts.p95Data} series={cmpCharts.series} />
            </div>
          )}
        </div>
      )}
    </div>
  )
}
