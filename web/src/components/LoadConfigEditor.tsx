import { Plus, Sliders, Trash2 } from 'lucide-react'
import { useState } from 'react'
import type { HTTPOptions, LoadConfig, Stage, Threshold } from '../lib/types'

interface Props {
  load: LoadConfig
  onChange: (next: LoadConfig) => void
  http?: HTTPOptions
  onHTTPChange?: (next: HTTPOptions) => void
  stepOptions?: Array<{ id: string; name?: string }>
  compact?: boolean
}

export function parseDurationSeconds(raw: string): number {
  const s = (raw || '').trim()
  if (!s) return 0
  if (/^\d+(\.\d+)?$/.test(s)) return Number(s) / 1000
  const matches = Array.from(s.matchAll(/(\d+(?:\.\d+)?)(ms|s|m|h)/gi))
  if (matches.length === 0) return 0
  let total = 0
  for (const m of matches) {
    const num = Number(m[1])
    const unit = m[2].toLowerCase()
    if (unit === 'ms') total += num / 1000
    else if (unit === 's') total += num
    else if (unit === 'm') total += num * 60
    else if (unit === 'h') total += num * 3600
  }
  return total
}

function buildCurvePoints(stages: Stage[]): { totalSec: number; peak: number; points: Array<{ t: number; v: number }> } {
  let t = 0
  let curV = 0
  let peak = 0
  const points: Array<{ t: number; v: number }> = [{ t: 0, v: 0 }]
  for (const st of stages) {
    const dur = Math.max(0, parseDurationSeconds(st.duration))
    const target = Math.max(0, Number(st.target) || 0)
    peak = Math.max(peak, target)
    if (dur === 0) {
      points.push({ t, v: target })
    } else {
      t += dur
      points.push({ t, v: target })
    }
    curV = target
  }
  if (peak === 0 && curV > 0) peak = curV
  return { totalSec: t, peak, points }
}

export function LoadConfigEditor({ load, onChange, http, onHTTPChange, stepOptions = [], compact = false }: Props) {
  const [showHTTP, setShowHTTP] = useState(false)
  const stages = load.stages?.length ? load.stages : [{ duration: '0s', target: 10 }, { duration: '30s', target: 10 }]
  const thresholds = load.thresholds || []
  const { totalSec, peak, points } = buildCurvePoints(stages)

  const setStages = (next: Stage[]) => onChange({ ...load, stages: next })
  const setThresholds = (next: Threshold[]) => onChange({ ...load, thresholds: next })

  const applyPreset = (kind: 'steady' | 'ramp' | 'step') => {
    const baseTarget = Math.max(10, Math.round(peak || 20))
    if (kind === 'steady') {
      setStages([
        { duration: '0s', target: baseTarget },
        { duration: '30s', target: baseTarget },
      ])
    } else if (kind === 'ramp') {
      setStages([
        { duration: '5s', target: baseTarget },
        { duration: '25s', target: baseTarget },
        { duration: '5s', target: 0 },
      ])
    } else {
      const unit = Math.max(10, Math.round(baseTarget / 2))
      setStages([
        { duration: '10s', target: unit },
        { duration: '10s', target: unit * 2 },
        { duration: '10s', target: unit * 3 },
      ])
    }
  }

  const w = 360
  const h = 84
  const padX = 8
  const padY = 8
  const innerW = w - padX * 2
  const innerH = h - padY * 2
  const maxT = Math.max(1, totalSec)
  const maxY = Math.max(1, peak)
  const svgPoints = points
    .map((pt) => {
      const x = padX + (pt.t / maxT) * innerW
      const y = padY + innerH - (pt.v / maxY) * innerH
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')

  return (
    <div className="space-y-3 text-xs">
      <div className="flex flex-wrap items-center gap-3">
        <div className="inline-flex rounded-md border border-slate-200 p-0.5 bg-slate-50">
          <button
            type="button"
            onClick={() => onChange({ ...load, mode: 'vus', max_vus: undefined })}
            className={`px-3 py-1 rounded font-medium ${
              load.mode === 'vus' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
            }`}
          >
            并发模式 (VUs · 闭环)
          </button>
          <button
            type="button"
            onClick={() => onChange({ ...load, mode: 'rate' })}
            className={`px-3 py-1 rounded font-medium ${
              load.mode === 'rate' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
            }`}
          >
            速率模式 (RPS · 开环)
          </button>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-slate-500">快捷预设：</span>
          <button
            type="button"
            onClick={() => applyPreset('steady')}
            className="px-2 py-1 rounded border border-slate-200 bg-white hover:bg-slate-50 text-slate-700"
          >
            恒定施压 (0s-&gt;N)
          </button>
          <button
            type="button"
            onClick={() => applyPreset('ramp')}
            className="px-2 py-1 rounded border border-slate-200 bg-white hover:bg-slate-50 text-slate-700"
          >
            平滑爬坡
          </button>
          <button
            type="button"
            onClick={() => applyPreset('step')}
            className="px-2 py-1 rounded border border-slate-200 bg-white hover:bg-slate-50 text-slate-700"
          >
            阶梯探顶
          </button>
        </div>
      </div>

      {/* Curve Preview + Stages */}
      <div className={`grid ${compact ? 'grid-cols-1' : 'grid-cols-1 lg:grid-cols-12'} gap-3`}>
        <div className={compact ? '' : 'lg:col-span-7 space-y-1.5'}>
          <div className="border border-slate-200 rounded-lg bg-white overflow-hidden">
            <table className="w-full text-left border-collapse">
              <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
                <tr>
                  <th className="px-2.5 py-1.5 w-10">#</th>
                  <th className="px-2.5 py-1.5">持续时间 (如 0s / 10s / 1m)</th>
                  <th className="px-2.5 py-1.5">目标 {load.mode === 'rate' ? 'RPS' : '并发 VU'}</th>
                  <th className="px-2 py-1.5 w-12 text-right">删</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {stages.map((st, idx) => (
                  <tr key={idx}>
                    <td className="px-2.5 py-1 text-slate-400 font-mono">{idx + 1}</td>
                    <td className="px-2 py-1">
                      <input
                        type="text"
                        value={st.duration}
                        onChange={(e) =>
                          setStages(stages.map((row, i) => (i === idx ? { ...row, duration: e.target.value } : row)))
                        }
                        className="w-full px-2 py-1 border border-slate-200 rounded font-mono focus:border-sky-500 focus:outline-none"
                      />
                    </td>
                    <td className="px-2 py-1">
                      <input
                        type="number"
                        min={0}
                        step={load.mode === 'rate' ? 'any' : 1}
                        value={st.target}
                        onChange={(e) =>
                          setStages(
                            stages.map((row, i) => (i === idx ? { ...row, target: Number(e.target.value) || 0 } : row))
                          )
                        }
                        className="w-full px-2 py-1 border border-slate-200 rounded font-mono focus:border-sky-500 focus:outline-none"
                      />
                    </td>
                    <td className="px-2 py-1 text-right">
                      {stages.length > 1 && (
                        <button
                          type="button"
                          onClick={() => setStages(stages.filter((_, i) => i !== idx))}
                          className="p-1 text-slate-400 hover:text-rose-600"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <button
            type="button"
            onClick={() => setStages([...stages, { duration: '10s', target: peak || 10 }])}
            className="inline-flex items-center gap-1 text-sky-600 hover:underline font-medium"
          >
            <Plus className="w-3.5 h-3.5" /> 添加阶段
          </button>
        </div>

        <div className={compact ? '' : 'lg:col-span-5'}>
          <div className="rounded-lg border border-slate-200 bg-slate-50/70 p-2.5 h-full flex flex-col justify-between">
            <div className="flex items-center justify-between text-[11px] text-slate-600 mb-1">
              <span className="font-medium">负载曲线预览（线性爬坡）</span>
              <span className="font-mono">
                总时长 {totalSec.toFixed(1)}s · 峰值 {peak} {load.mode === 'rate' ? 'RPS' : 'VU'}
              </span>
            </div>
            <svg viewBox={`0 0 ${w} ${h}`} className="w-full h-20 bg-white rounded border border-slate-200">
              <polyline
                fill="none"
                stroke="#0284c7"
                strokeWidth="2.5"
                strokeLinejoin="round"
                points={svgPoints}
              />
              {points.map((pt, idx) => {
                const x = padX + (pt.t / maxT) * innerW
                const y = padY + innerH - (pt.v / maxY) * innerH
                return <circle key={idx} cx={x} cy={y} r="3" fill="#0B2A4A" />
              })}
            </svg>
            <div className="text-[10px] text-slate-400 mt-1">
              提示：若需“一开始即以 {peak || 10} 并发运行”，请将第 1 阶段设为 `0s -&gt; {peak || 10}`。
            </div>
          </div>
        </div>
      </div>

      {/* Extra load bounds */}
      <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 bg-slate-50 border border-slate-200 rounded-lg p-2.5">
        {load.mode === 'rate' && (
          <div>
            <label className="block text-slate-600 mb-1">最大虚拟用户上限 (max_vus)</label>
            <input
              type="number"
              min={0}
              value={load.max_vus || ''}
              placeholder="0 表示按峰值速率自动估算"
              onChange={(e) => onChange({ ...load, max_vus: e.target.value ? Number(e.target.value) : undefined })}
              className="w-full px-2 py-1 border border-slate-300 rounded bg-white font-mono"
            />
          </div>
        )}
        <div>
          <label className="block text-slate-600 mb-1">最大请求数限制 (可选)</label>
          <input
            type="number"
            min={0}
            value={load.max_requests || ''}
            placeholder="不限制"
            onChange={(e) => onChange({ ...load, max_requests: e.target.value ? Number(e.target.value) : undefined })}
            className="w-full px-2 py-1 border border-slate-300 rounded bg-white font-mono"
          />
        </div>
        <div>
          <label className="block text-slate-600 mb-1">优雅停止等待 (graceful_stop)</label>
          <input
            type="text"
            value={load.graceful_stop || ''}
            placeholder="默认 5s"
            onChange={(e) => onChange({ ...load, graceful_stop: e.target.value || undefined })}
            className="w-full px-2 py-1 border border-slate-300 rounded bg-white font-mono"
          />
        </div>
      </div>

      {/* Thresholds */}
      <div className="space-y-1.5">
        <div className="flex items-center justify-between">
          <span className="font-semibold text-slate-700">通过阈值 / SLA（不达标将在报告标红，开启“触发即中止”时运行期自动停压）</span>
          <button
            type="button"
            onClick={() =>
              setThresholds([
                ...thresholds,
                { metric: 'p95_ms', op: '<=', value: 200, abort_on_fail: false, grace_period: '3s' },
              ])
            }
            className="inline-flex items-center gap-1 text-sky-600 hover:underline font-medium"
          >
            <Plus className="w-3.5 h-3.5" /> 添加阈值
          </button>
        </div>

        {thresholds.map((th, idx) => {
          const updateTh = (patch: Partial<Threshold>) =>
            setThresholds(thresholds.map((row, i) => (i === idx ? { ...row, ...patch } : row)))
          return (
            <div
              key={idx}
              className="grid grid-cols-12 gap-1.5 items-center p-2 rounded border border-slate-200 bg-white"
            >
              <select
                value={th.metric}
                onChange={(e) => updateTh({ metric: e.target.value })}
                className="col-span-3 px-2 py-1 border border-slate-300 rounded bg-white"
              >
                <option value="p95_ms">P95 延迟 (ms)</option>
                <option value="p99_ms">P99 延迟 (ms)</option>
                <option value="p50_ms">P50 延迟 (ms)</option>
                <option value="p90_ms">P90 延迟 (ms)</option>
                <option value="mean_ms">平均延迟 (ms)</option>
                <option value="max_ms">最大延迟 (ms)</option>
                <option value="error_rate_pct">错误率 (%)</option>
                <option value="rps">吞吐量 (RPS)</option>
              </select>
              <select
                value={th.op || (th.metric === 'rps' ? '>=' : '<=')}
                onChange={(e) => updateTh({ op: e.target.value as Threshold['op'] })}
                className="col-span-1 px-1.5 py-1 border border-slate-300 rounded font-mono bg-white"
              >
                <option value="<=">&lt;=</option>
                <option value="<">&lt;</option>
                <option value=">=">&gt;=</option>
                <option value=">">&gt;</option>
              </select>
              <input
                type="number"
                step="any"
                min={0}
                value={th.value}
                onChange={(e) => updateTh({ value: Number(e.target.value) || 0 })}
                className="col-span-2 px-2 py-1 border border-slate-300 rounded font-mono"
              />
              {stepOptions.length > 0 ? (
                <select
                  value={th.step || ''}
                  onChange={(e) => updateTh({ step: e.target.value || undefined })}
                  className="col-span-2 px-1.5 py-1 border border-slate-300 rounded bg-white"
                >
                  <option value="">全局总览</option>
                  {stepOptions.map((so) => (
                    <option key={so.id} value={so.id}>
                      步骤: {so.name || so.id}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  type="text"
                  value={th.step || ''}
                  placeholder="步骤名(空=全局)"
                  onChange={(e) => updateTh({ step: e.target.value || undefined })}
                  className="col-span-2 px-2 py-1 border border-slate-300 rounded"
                />
              )}
              <label className="col-span-2 inline-flex items-center gap-1 text-slate-700 cursor-pointer">
                <input
                  type="checkbox"
                  checked={Boolean(th.abort_on_fail)}
                  onChange={(e) => updateTh({ abort_on_fail: e.target.checked })}
                  className="rounded border-slate-300 text-rose-600"
                />
                <span>违约中止</span>
              </label>
              <input
                type="text"
                value={th.grace_period || ''}
                disabled={!th.abort_on_fail}
                placeholder="宽限 3s"
                onChange={(e) => updateTh({ grace_period: e.target.value || undefined })}
                className="col-span-1 px-1.5 py-1 border border-slate-300 rounded font-mono disabled:opacity-40"
                title="中止检查宽限期（如 3s）"
              />
              <button
                type="button"
                onClick={() => setThresholds(thresholds.filter((_, i) => i !== idx))}
                className="col-span-1 justify-self-end p-1 text-slate-400 hover:text-rose-600"
              >
                <Trash2 className="w-3.5 h-3.5" />
              </button>
            </div>
          )
        })}
      </div>

      {/* Optional HTTP Connection Options */}
      {onHTTPChange && (
        <div>
          <button
            type="button"
            onClick={() => setShowHTTP((v) => !v)}
            className="inline-flex items-center gap-1 text-slate-600 hover:text-slate-900 font-medium"
          >
            <Sliders className="w-3.5 h-3.5 text-sky-600" />
            {showHTTP ? '收起高级 HTTP 连接选项' : '展开高级 HTTP 连接选项（连接复用、超时、TLS 校验、连接退避）'}
          </button>
          {showHTTP && (
            <div className="mt-2 p-3 rounded-lg border border-slate-200 bg-slate-50 grid grid-cols-2 sm:grid-cols-4 gap-3">
              <div>
                <label className="block text-slate-600 mb-1">连接模型</label>
                <select
                  value={http?.conn_mode || 'per_vu'}
                  onChange={(e) => onHTTPChange({ ...(http || {}), conn_mode: e.target.value as 'per_vu' | 'shared' })}
                  className="w-full px-2 py-1 border border-slate-300 rounded bg-white"
                >
                  <option value="per_vu">每 VU 独占连接池</option>
                  <option value="shared">全局共享连接池</option>
                </select>
              </div>
              <div>
                <label className="block text-slate-600 mb-1">建连/总超时</label>
                <div className="flex gap-1">
                  <input
                    type="text"
                    placeholder="建连 5s"
                    value={http?.timeout?.connect || ''}
                    onChange={(e) =>
                      onHTTPChange({
                        ...(http || {}),
                        timeout: { ...(http?.timeout || {}), connect: e.target.value || undefined },
                      })
                    }
                    className="w-1/2 px-1.5 py-1 border border-slate-300 rounded font-mono bg-white"
                  />
                  <input
                    type="text"
                    placeholder="总计 60s"
                    value={http?.timeout?.total || ''}
                    onChange={(e) =>
                      onHTTPChange({
                        ...(http || {}),
                        timeout: { ...(http?.timeout || {}), total: e.target.value || undefined },
                      })
                    }
                    className="w-1/2 px-1.5 py-1 border border-slate-300 rounded font-mono bg-white"
                  />
                </div>
              </div>
              <div>
                <label className="block text-slate-600 mb-1">连接失败退避</label>
                <input
                  type="text"
                  placeholder="默认 200ms"
                  value={http?.connect_backoff || ''}
                  onChange={(e) => onHTTPChange({ ...(http || {}), connect_backoff: e.target.value || undefined })}
                  className="w-full px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                />
              </div>
              <div className="flex flex-col justify-center gap-1.5">
                <label className="inline-flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={http?.keep_alive ?? true}
                    onChange={(e) => onHTTPChange({ ...(http || {}), keep_alive: e.target.checked })}
                  />
                  <span>Keep-Alive 长连接</span>
                </label>
                <label className="inline-flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={Boolean(http?.insecure_skip_verify)}
                    onChange={(e) => onHTTPChange({ ...(http || {}), insecure_skip_verify: e.target.checked })}
                  />
                  <span>忽略自签名 HTTPS 证书</span>
                </label>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
