import { useEffect, useMemo, useState } from 'react'
import type uPlot from 'uplot'
import { Download, Eye, GitCompare, RefreshCw, Trash2 } from 'lucide-react'
import { UPlotChart } from '../components/UPlotChart'
import { api, APIError } from '../lib/api'
import type { Run, RunComparisonResponse } from '../lib/types'

interface Props {
  projectId: string
  onInspectRun: (runId: string) => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

const COMPARE_COLORS = ['#0284c7', '#e11d48', '#16a34a', '#d97706', '#7c3aed', '#0f766e']

function fmtDeltaPct(val?: number, lowerIsBetter = false) {
  if (val === undefined || val === null) return <span className="text-slate-400">-</span>
  const good = lowerIsBetter ? val <= 0 : val >= 0
  const sign = val > 0 ? '+' : ''
  return (
    <span className={`font-mono font-semibold ${val === 0 ? 'text-slate-600' : good ? 'text-emerald-700' : 'text-rose-700'}`}>
      {sign}
      {val.toFixed(2)}%
    </span>
  )
}

export function HistoryCompareView({ projectId, onInspectRun, onToast }: Props) {
  const [runs, setRuns] = useState<Run[]>([])
  const [loading, setLoading] = useState(false)
  const [selectedIds, setSelectedIds] = useState<string[]>([])
  const [baselineId, setBaselineId] = useState('')
  const [comparing, setComparing] = useState(false)
  const [cmpResult, setCmpResult] = useState<RunComparisonResponse | null>(null)
  const [confirmDelId, setConfirmDelId] = useState('')

  const loadRuns = async () => {
    setLoading(true)
    try {
      setRuns(await api.listRuns(projectId, 100, 0))
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
    void loadRuns()
  }, [projectId])

  const toggleSelect = (id: string) => {
    setSelectedIds((prev) => {
      if (prev.includes(id)) {
        const next = prev.filter((x) => x !== id)
        if (baselineId === id) setBaselineId(next[0] || '')
        return next
      }
      if (prev.length >= 6) {
        onToast('info', '最多同时选择 6 次压测进行对比')
        return prev
      }
      const next = [...prev, id]
      if (!baselineId) setBaselineId(next[0])
      return next
    })
  }

  const handleCompare = async () => {
    if (selectedIds.length < 2) {
      onToast('info', '请勾选 2 至 6 次历史压测进行对比')
      return
    }
    const base = baselineId && selectedIds.includes(baselineId) ? baselineId : selectedIds[0]
    const ordered = [base, ...selectedIds.filter((x) => x !== base)]
    setComparing(true)
    try {
      setCmpResult(await api.compareRuns(ordered, 600))
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '对比分析失败')
    } finally {
      setComparing(false)
    }
  }

  // Align snapshots by sequential elapsed second index so runs overlay cleanly.
  const cmpCharts = useMemo(() => {
    if (!cmpResult || cmpResult.runs.length === 0) return null
    let maxLen = 0
    for (const item of cmpResult.runs) {
      maxLen = Math.max(maxLen, item.snapshots?.length || 0)
    }
    const xSec = Array.from({ length: maxLen }, (_, i) => i + 1)
    const rpsSeries = cmpResult.runs.map((item, idx) => ({
      label: `${idx === 0 ? '[基线] ' : ''}${item.run.scenario_name} (${item.run.id.slice(-5)})`,
      stroke: COMPARE_COLORS[idx % COMPARE_COLORS.length],
    }))
    const rpsY = cmpResult.runs.map((item) =>
      xSec.map((_, i) => (item.snapshots?.[i] ? item.snapshots[i].rps || 0 : null))
    )
    const p95Y = cmpResult.runs.map((item) =>
      xSec.map((_, i) => (item.snapshots?.[i] ? item.snapshots[i].latency?.p95_ms || 0 : null))
    )
    return {
      series: rpsSeries,
      rpsData: [xSec, ...rpsY] as uPlot.AlignedData,
      p95Data: [xSec, ...p95Y] as uPlot.AlignedData,
    }
  }, [cmpResult])

  return (
    <div className="flex-1 overflow-y-auto bg-slate-50 p-5 space-y-5 text-xs">
      <div className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="text-sm font-semibold text-slate-900">历史压测记录与基线对比</h2>
            <p className="text-[11px] text-slate-500">勾选 2～6 条压测记录可叠加对比吞吐量、延迟分位变化与各步骤回归情况</p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {selectedIds.length >= 2 && (
              <>
                <select
                  value={baselineId}
                  onChange={(e) => setBaselineId(e.target.value)}
                  className="px-2.5 py-1.5 border border-slate-300 rounded-md bg-white"
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
                <button
                  type="button"
                  disabled={comparing}
                  onClick={() => void handleCompare()}
                  className="inline-flex items-center gap-1.5 px-3.5 py-1.5 rounded-md bg-[#0B2A4A] hover:bg-[#123d69] text-white font-semibold disabled:opacity-50"
                >
                  <GitCompare className="w-3.5 h-3.5" /> {comparing ? '计算中...' : `对比选中 (${selectedIds.length})`}
                </button>
              </>
            )}
            <button
              type="button"
              onClick={() => void loadRuns()}
              className="inline-flex items-center gap-1 px-2.5 py-1.5 rounded border border-slate-300 bg-white hover:bg-slate-50 text-slate-700"
            >
              <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} /> 刷新
            </button>
          </div>
        </div>

        {runs.length === 0 ? (
          <p className="text-slate-400 py-8 text-center">该项目暂无压测历史记录。</p>
        ) : (
          <div className="overflow-x-auto border border-slate-200 rounded-lg">
            <table className="w-full text-left border-collapse">
              <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
                <tr>
                  <th className="w-10 px-3 py-2 text-center">对比</th>
                  <th className="px-3 py-2">场景 / 接口</th>
                  <th className="px-3 py-2 w-24">状态</th>
                  <th className="px-3 py-2 w-24">RPS</th>
                  <th className="px-3 py-2 w-24">错误率</th>
                  <th className="px-3 py-2 w-28">P95 / P99</th>
                  <th className="px-3 py-2 w-36">开始时间</th>
                  <th className="px-3 py-2 text-right">导出与操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {runs.map((r) => {
                  const checked = selectedIds.includes(r.id)
                  const active = r.status === 'preparing' || r.status === 'running' || r.status === 'stopping'
                  return (
                    <tr key={r.id} className={checked ? 'bg-sky-50/70' : 'hover:bg-slate-50'}>
                      <td className="px-3 py-2 text-center">
                        <input
                          type="checkbox"
                          checked={checked}
                          onChange={() => toggleSelect(r.id)}
                          className="rounded border-slate-300 text-sky-600"
                        />
                      </td>
                      <td className="px-3 py-2">
                        <div className="font-semibold text-slate-900">{r.scenario_name}</div>
                        <div className="text-[10px] font-mono text-slate-400">{r.id}</div>
                      </td>
                      <td className="px-3 py-2">
                        <span
                          className={`px-2 py-0.5 rounded-full text-[10px] font-semibold ${
                            r.status === 'finished'
                              ? 'bg-emerald-100 text-emerald-800'
                              : r.status === 'failed'
                                ? 'bg-rose-100 text-rose-800'
                                : 'bg-sky-100 text-sky-800'
                          }`}
                        >
                          {r.status}
                        </span>
                      </td>
                      <td className="px-3 py-2 font-mono">{r.summary ? `${r.summary.rps.toFixed(1)}` : '-'}</td>
                      <td className="px-3 py-2 font-mono">
                        {r.summary ? `${(r.summary.error_rate * 100).toFixed(2)}%` : '-'}
                      </td>
                      <td className="px-3 py-2 font-mono">
                        {r.summary ? `${r.summary.latency.p95_ms.toFixed(1)} / ${r.summary.latency.p99_ms.toFixed(1)}` : '-'}
                      </td>
                      <td className="px-3 py-2 text-slate-500">{new Date(r.created_at).toLocaleString()}</td>
                      <td className="px-3 py-2 text-right">
                        <div className="inline-flex items-center gap-1.5">
                          <button
                            type="button"
                            onClick={() => onInspectRun(r.id)}
                            className="inline-flex items-center gap-1 px-2 py-1 rounded border border-slate-300 bg-white hover:bg-slate-100 text-slate-700"
                          >
                            <Eye className="w-3 h-3 text-sky-600" /> 详情
                          </button>
                          <a
                            href={api.exportRunURL(r.id, 'html')}
                            download
                            className="inline-flex items-center gap-1 px-2 py-1 rounded border border-slate-300 bg-white hover:bg-slate-100 text-slate-700"
                            title="下载独立 HTML 报告"
                          >
                            <Download className="w-3 h-3 text-sky-600" /> HTML
                          </a>
                          <a
                            href={api.exportRunURL(r.id, 'har')}
                            download
                            className="px-1.5 py-1 rounded border border-slate-300 bg-white hover:bg-slate-100 text-slate-600"
                            title="导出错误采样 HAR"
                          >
                            HAR
                          </a>
                          {!active && (
                            <button
                              type="button"
                              title={confirmDelId === r.id ? '再次点击确认删除' : '删除压测记录'}
                              onClick={async () => {
                                if (confirmDelId !== r.id) {
                                  setConfirmDelId(r.id)
                                  return
                                }
                                try {
                                  await api.deleteRun(r.id)
                                  setConfirmDelId('')
                                  await loadRuns()
                                  onToast('success', '已删除压测记录')
                                } catch (err) {
                                  onToast('error', err instanceof APIError ? err.message : '删除失败')
                                }
                              }}
                              className={`p-1 rounded ${
                                confirmDelId === r.id ? 'bg-rose-600 text-white' : 'text-slate-400 hover:text-rose-600'
                              }`}
                            >
                              <Trash2 className="w-3.5 h-3.5" />
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

      {/* Comparison Results */}
      {cmpResult && (
        <div className="space-y-4">
          <div className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-3">
            <h3 className="text-sm font-semibold text-slate-900">多压测关键指标对比（相对基线变化率）</h3>
            <div className="overflow-x-auto border border-slate-200 rounded-lg">
              <table className="w-full text-left border-collapse">
                <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
                  <tr>
                    <th className="px-3 py-2">压测记录</th>
                    <th className="px-3 py-2">RPS (变化)</th>
                    <th className="px-3 py-2">P50 ms (变化)</th>
                    <th className="px-3 py-2">P95 ms (变化)</th>
                    <th className="px-3 py-2">P99 ms (变化)</th>
                    <th className="px-3 py-2">错误率 (差值)</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100">
                  {cmpResult.runs.map((item, idx) => (
                    <tr key={item.run.id} className={idx === 0 ? 'bg-sky-50/50' : ''}>
                      <td className="px-3 py-2">
                        <div className="font-semibold text-slate-900 flex items-center gap-1.5">
                          <span
                            className="w-2.5 h-2.5 rounded-full inline-block"
                            style={{ backgroundColor: COMPARE_COLORS[idx % COMPARE_COLORS.length] }}
                          />
                          {item.run.scenario_name}
                          {idx === 0 && (
                            <span className="px-1.5 py-0.2 rounded bg-sky-100 text-sky-800 text-[10px]">基线</span>
                          )}
                        </div>
                        <div className="text-[10px] font-mono text-slate-400">{item.run.id}</div>
                      </td>
                      <td className="px-3 py-2 font-mono">
                        {item.summary ? item.summary.rps.toFixed(1) : '-'} {idx > 0 && <>({fmtDeltaPct(item.delta?.rps_pct)})</>}
                      </td>
                      <td className="px-3 py-2 font-mono">
                        {item.summary ? item.summary.latency.p50_ms.toFixed(2) : '-'}{' '}
                        {idx > 0 && <>({fmtDeltaPct(item.delta?.p50_pct, true)})</>}
                      </td>
                      <td className="px-3 py-2 font-mono">
                        {item.summary ? item.summary.latency.p95_ms.toFixed(2) : '-'}{' '}
                        {idx > 0 && <>({fmtDeltaPct(item.delta?.p95_pct, true)})</>}
                      </td>
                      <td className="px-3 py-2 font-mono">
                        {item.summary ? item.summary.latency.p99_ms.toFixed(2) : '-'}{' '}
                        {idx > 0 && <>({fmtDeltaPct(item.delta?.p99_pct, true)})</>}
                      </td>
                      <td className="px-3 py-2 font-mono">
                        {item.summary ? `${(item.summary.error_rate * 100).toFixed(2)}%` : '-'}{' '}
                        {idx > 0 && item.delta && (
                          <span className={item.delta.error_rate_diff <= 0 ? 'text-emerald-700' : 'text-rose-700'}>
                            ({item.delta.error_rate_diff > 0 ? '+' : ''}
                            {(item.delta.error_rate_diff * 100).toFixed(2)}%)
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>

          {cmpResult.runs.some(
            (item, idx) => idx > 0 && item.delta?.step_deltas && Object.keys(item.delta.step_deltas).length > 0
          ) && (
            <div className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs space-y-3">
              <h3 className="text-sm font-semibold text-slate-900">分步骤回归明细（相对基线）</h3>
              <div className="overflow-x-auto border border-slate-200 rounded-lg">
                <table className="w-full text-left border-collapse">
                  <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
                    <tr>
                      <th className="px-3 py-2">对比记录</th>
                      <th className="px-3 py-2">步骤</th>
                      <th className="px-3 py-2">RPS 变化</th>
                      <th className="px-3 py-2">P50 变化</th>
                      <th className="px-3 py-2">P95 变化</th>
                      <th className="px-3 py-2">P99 变化</th>
                      <th className="px-3 py-2">错误率差值</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100">
                    {cmpResult.runs.slice(1).flatMap((item, runIdx) =>
                      Object.entries(item.delta?.step_deltas || {}).map(([stepKey, sd]) => (
                        <tr key={`${item.run.id}-${stepKey}`}>
                          <td className="px-3 py-2">
                            <span className="inline-flex items-center gap-1.5 font-medium text-slate-800">
                              <span
                                className="w-2 h-2 rounded-full inline-block"
                                style={{ backgroundColor: COMPARE_COLORS[(runIdx + 1) % COMPARE_COLORS.length] }}
                              />
                              {item.run.scenario_name} ({item.run.id.slice(-5)})
                            </span>
                          </td>
                          <td className="px-3 py-2 font-mono text-slate-800">{sd.name || sd.id || stepKey}</td>
                          <td className="px-3 py-2 font-mono">{fmtDeltaPct(sd.rps_pct)}</td>
                          <td className="px-3 py-2 font-mono">{fmtDeltaPct(sd.p50_pct, true)}</td>
                          <td className="px-3 py-2 font-mono">{fmtDeltaPct(sd.p95_pct, true)}</td>
                          <td className="px-3 py-2 font-mono">{fmtDeltaPct(sd.p99_pct, true)}</td>
                          <td className="px-3 py-2 font-mono">
                            <span className={sd.error_rate_diff <= 0 ? 'text-emerald-700' : 'text-rose-700'}>
                              {sd.error_rate_diff > 0 ? '+' : ''}
                              {(sd.error_rate_diff * 100).toFixed(2)}%
                            </span>
                          </td>
                        </tr>
                      ))
                    )}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {cmpCharts && (
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              <UPlotChart title="吞吐量 (RPS) 叠加对比曲线" data={cmpCharts.rpsData} series={cmpCharts.series} />
              <UPlotChart
                title="P95 响应延迟 (ms) 叠加对比曲线"
                yUnit="ms"
                data={cmpCharts.p95Data}
                series={cmpCharts.series}
              />
            </div>
          )}
        </div>
      )}
    </div>
  )
}
