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
    t += dur
    points.push({ t, v: target })
    curV = target
  }
  if (peak === 0 && curV > 0) peak = curV
  return { totalSec: t, peak, points }
}

function fmtDuration(sec: number): string {
  if (sec <= 0) return '0s'
  if (sec < 60) return `${Number(sec.toFixed(1))}s`
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  if (m < 60) return s ? `${m}m${s}s` : `${m}m`
  const h = Math.floor(m / 60)
  return `${h}h${m % 60 ? `${m % 60}m` : ''}`
}

const inputCls =
  'w-full px-2 py-1 border border-line-strong rounded bg-surface text-fg font-mono tg-tabular focus:border-sky-500 focus:outline-none'
const selectCls = 'w-full px-2 py-1 border border-line-strong rounded bg-surface text-fg focus:border-sky-500 focus:outline-none'
const fieldLabelCls = 'block text-fg-muted mb-1'

export function LoadConfigEditor({ load, onChange, http, onHTTPChange, stepOptions = [], compact = false }: Props) {
  const [showHTTP, setShowHTTP] = useState(false)
  const stages = load.stages?.length ? load.stages : [{ duration: '0s', target: 10 }, { duration: '30s', target: 10 }]
  const thresholds = load.thresholds || []
  const { totalSec, peak, points } = buildCurvePoints(stages)
  const isRate = load.mode === 'rate'
  const unit = isRate ? 'QPS / RPS' : 'VU'

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
      const u = Math.max(10, Math.round(baseTarget / 2))
      setStages([
        { duration: '10s', target: u },
        { duration: '10s', target: u * 2 },
        { duration: '10s', target: u * 3 },
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
  const xy = points.map((pt) => ({
    x: padX + (pt.t / maxT) * innerW,
    y: padY + innerH - (pt.v / maxY) * innerH,
  }))
  const svgPoints = xy.map((p) => `${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ')
  const baseY = padY + innerH
  const areaPath = xy.length
    ? `M${xy[0].x.toFixed(1)},${baseY} ${xy.map((p) => `L${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ')} L${xy[xy.length - 1].x.toFixed(1)},${baseY} Z`
    : ''

  const modeBtn = (active: boolean) =>
    `inline-flex items-baseline gap-1.5 px-3 py-1 rounded font-medium transition-colors ${
      active ? 'bg-surface shadow-xs text-tidal' : 'text-fg-muted hover:text-fg'
    }`
  const presetBtn = 'px-2 py-1 rounded border border-line bg-surface hover:bg-surface-hover text-fg-muted hover:text-fg'

  return (
    <div className="space-y-3 text-xs">
      <div className="flex flex-wrap items-center gap-3">
        <div role="group" aria-label="负载模式" className="inline-flex rounded-md border border-line p-0.5 bg-surface-subtle">
          <button
            type="button"
            aria-pressed={!isRate}
            onClick={() => onChange({ ...load, mode: 'vus', max_vus: undefined })}
            className={modeBtn(!isRate)}
          >
            并发模式 <span className="font-mono text-[11px]">VU</span>
          </button>
          <button type="button" aria-pressed={isRate} onClick={() => onChange({ ...load, mode: 'rate' })} className={modeBtn(isRate)}>
            速率模式 <span className="font-mono text-[11px]">QPS / RPS（请求/秒）</span>
          </button>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-fg-subtle">预设</span>
          <button type="button" onClick={() => applyPreset('steady')} className={presetBtn}>
            恒定
          </button>
          <button type="button" onClick={() => applyPreset('ramp')} className={presetBtn}>
            爬坡
          </button>
          <button type="button" onClick={() => applyPreset('step')} className={presetBtn}>
            阶梯
          </button>
        </div>
      </div>

      {/* Curve Preview + Stages */}
      <div className={`grid ${compact ? 'grid-cols-1' : 'grid-cols-1 lg:grid-cols-12'} gap-3`}>
        <div className={compact ? 'space-y-1.5' : 'lg:col-span-7 space-y-1.5'}>
          <div className="border border-line rounded-lg bg-surface overflow-hidden">
            <table className="w-full text-left border-collapse">
              <thead className="bg-surface-subtle border-b border-line text-fg-muted">
                <tr>
                  <th className="px-2.5 py-1.5 w-10 font-medium">#</th>
                  <th className="px-2.5 py-1.5 font-medium">持续时间</th>
                  <th className="px-2.5 py-1.5 font-medium">
                    目标 <span className="font-mono">{isRate ? 'QPS / RPS' : 'VU'}</span>
                  </th>
                  <th className="px-2 py-1.5 w-12">
                    <span className="sr-only">操作</span>
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {stages.map((st, idx) => (
                  <tr key={idx}>
                    <td className="px-2.5 py-1 text-fg-subtle font-mono tg-tabular">{idx + 1}</td>
                    <td className="px-2 py-1">
                      <input
                        type="text"
                        aria-label={`第 ${idx + 1} 阶段持续时间`}
                        value={st.duration}
                        placeholder="30s"
                        autoCorrect="off"
                        autoCapitalize="none"
                        spellCheck={false}
                        onChange={(e) =>
                          setStages(stages.map((row, i) => (i === idx ? { ...row, duration: e.target.value } : row)))
                        }
                        className={inputCls}
                      />
                    </td>
                    <td className="px-2 py-1">
                      <input
                        type="number"
                        aria-label={`第 ${idx + 1} 阶段目标 ${unit}`}
                        min={0}
                        step={isRate ? 'any' : 1}
                        value={st.target}
                        onChange={(e) =>
                          setStages(
                            stages.map((row, i) => (i === idx ? { ...row, target: Number(e.target.value) || 0 } : row))
                          )
                        }
                        className={inputCls}
                      />
                    </td>
                    <td className="px-2 py-1 text-right">
                      {stages.length > 1 && (
                        <button
                          type="button"
                          title="删除阶段"
                          aria-label={`删除第 ${idx + 1} 阶段`}
                          onClick={() => setStages(stages.filter((_, i) => i !== idx))}
                          className="p-1 text-fg-subtle hover:text-rose-600"
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
            className="inline-flex items-center gap-1 text-tidal hover:underline font-medium"
          >
            <Plus className="w-3.5 h-3.5" /> 添加阶段
          </button>
        </div>

        <div className={compact ? '' : 'lg:col-span-5'}>
          <figure className="rounded-lg border border-line bg-surface-subtle p-2.5 h-full flex flex-col gap-1.5">
            <figcaption className="flex items-center justify-between text-[11px] text-fg-muted">
              <span className="font-medium">负载曲线</span>
              <span className="font-mono tg-tabular">
                {fmtDuration(totalSec)} · 峰值 {peak} {unit}
              </span>
            </figcaption>
            <svg
              viewBox={`0 0 ${w} ${h}`}
              role="img"
              aria-label={`负载曲线：总时长 ${fmtDuration(totalSec)}，峰值 ${peak} ${unit}`}
              className="w-full h-20 bg-surface rounded border border-line text-tidal"
            >
              <line x1={padX} x2={w - padX} y1={baseY} y2={baseY} stroke="var(--tg-border-strong)" strokeWidth="1" />
              <line
                x1={padX}
                x2={w - padX}
                y1={padY}
                y2={padY}
                stroke="var(--tg-border)"
                strokeWidth="1"
                strokeDasharray="3 3"
              />
              {areaPath && <path d={areaPath} fill="currentColor" fillOpacity="0.12" />}
              <polyline fill="none" stroke="currentColor" strokeWidth="2" strokeLinejoin="round" points={svgPoints} />
              {xy.map((p, idx) => (
                <circle key={idx} cx={p.x} cy={p.y} r="2.75" fill="var(--tg-surface)" stroke="currentColor" strokeWidth="1.5" />
              ))}
            </svg>
          </figure>
        </div>
      </div>

      {/* Extra load bounds */}
      <fieldset className="bg-surface-subtle border border-line rounded-lg p-2.5 pt-1.5">
        <legend className="px-1 text-[11px] font-medium text-fg-muted">运行边界</legend>
        <div className={`grid grid-cols-1 ${isRate ? 'sm:grid-cols-3' : 'sm:grid-cols-2'} gap-3`}>
          {isRate && (
            <div>
              <label className={fieldLabelCls}>
                最大 <span className="font-mono">VU</span>
              </label>
              <input
                type="number"
                name="max_vus"
                min={0}
                value={load.max_vus || ''}
                placeholder="自动"
                onChange={(e) => onChange({ ...load, max_vus: e.target.value ? Number(e.target.value) : undefined })}
                className={inputCls}
              />
            </div>
          )}
          <div>
            <label className={fieldLabelCls}>最大请求数</label>
            <input
              type="number"
              name="max_requests"
              min={0}
              value={load.max_requests || ''}
              placeholder="不限"
              onChange={(e) => onChange({ ...load, max_requests: e.target.value ? Number(e.target.value) : undefined })}
              className={inputCls}
            />
          </div>
          <div>
            <label className={fieldLabelCls}>优雅停止</label>
            <input
              type="text"
              name="graceful_stop"
              value={load.graceful_stop || ''}
              placeholder="5s"
              autoCorrect="off"
              autoCapitalize="none"
              spellCheck={false}
              onChange={(e) => onChange({ ...load, graceful_stop: e.target.value || undefined })}
              className={inputCls}
            />
          </div>
        </div>
      </fieldset>

      {/* Thresholds */}
      <div className="space-y-1.5">
        <div className="flex items-center justify-between">
          <span className="font-semibold text-fg">
            通过阈值 / SLA
            {thresholds.length > 0 && (
              <span className="ml-1.5 px-1.5 py-0.5 rounded-full bg-sky-50 border border-sky-200 text-sky-700 text-[10px] font-mono tg-tabular">
                {thresholds.length}
              </span>
            )}
          </span>
          <button
            type="button"
            onClick={() =>
              setThresholds([
                ...thresholds,
                { metric: 'p95_ms', op: '<=', value: 200, abort_on_fail: false, grace_period: '3s' },
              ])
            }
            className="inline-flex items-center gap-1 text-tidal hover:underline font-medium"
          >
            <Plus className="w-3.5 h-3.5" /> 添加阈值
          </button>
        </div>

        {thresholds.map((th, idx) => {
          const updateTh = (patch: Partial<Threshold>) =>
            setThresholds(thresholds.map((row, i) => (i === idx ? { ...row, ...patch } : row)))
          return (
            <div key={idx} className="grid grid-cols-12 gap-1.5 items-center p-2 rounded border border-line bg-surface">
              <select
                aria-label="指标"
                value={th.metric}
                onChange={(e) => updateTh({ metric: e.target.value })}
                className={`col-span-3 ${selectCls}`}
              >
                <option value="p95_ms">P95 延迟 ms</option>
                <option value="p99_ms">P99 延迟 ms</option>
                <option value="p50_ms">P50 延迟 ms</option>
                <option value="p90_ms">P90 延迟 ms</option>
                <option value="mean_ms">平均延迟 ms</option>
                <option value="max_ms">最大延迟 ms</option>
                <option value="error_rate_pct">错误率 %</option>
                <option value="rps">吞吐 QPS / RPS</option>
              </select>
              <select
                aria-label="比较符"
                value={th.op || (th.metric === 'rps' ? '>=' : '<=')}
                onChange={(e) => updateTh({ op: e.target.value as Threshold['op'] })}
                className={`col-span-1 ${selectCls} font-mono px-1.5`}
              >
                <option value="<=">&lt;=</option>
                <option value="<">&lt;</option>
                <option value=">=">&gt;=</option>
                <option value=">">&gt;</option>
              </select>
              <input
                type="number"
                aria-label="阈值"
                step="any"
                min={0}
                value={th.value}
                onChange={(e) => updateTh({ value: Number(e.target.value) || 0 })}
                className={`col-span-2 ${inputCls}`}
              />
              {stepOptions.length > 0 ? (
                <select
                  aria-label="作用范围"
                  value={th.step || ''}
                  onChange={(e) => updateTh({ step: e.target.value || undefined })}
                  className={`col-span-2 ${selectCls} px-1.5`}
                >
                  <option value="">全局</option>
                  {stepOptions.map((so) => (
                    <option key={so.id} value={so.id}>
                      {so.name || so.id}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  type="text"
                  aria-label="作用步骤"
                  value={th.step || ''}
                  placeholder="全局"
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  onChange={(e) => updateTh({ step: e.target.value || undefined })}
                  className={`col-span-2 ${inputCls}`}
                />
              )}
              <label className="col-span-2 inline-flex items-center gap-1 text-fg-muted cursor-pointer">
                <input
                  type="checkbox"
                  checked={Boolean(th.abort_on_fail)}
                  onChange={(e) => updateTh({ abort_on_fail: e.target.checked })}
                />
                <span>违约中止</span>
              </label>
              <input
                type="text"
                aria-label="中止宽限期"
                value={th.grace_period || ''}
                disabled={!th.abort_on_fail}
                placeholder="3s"
                title="中止宽限期"
                autoCorrect="off"
                autoCapitalize="none"
                spellCheck={false}
                onChange={(e) => updateTh({ grace_period: e.target.value || undefined })}
                className={`col-span-1 ${inputCls} px-1.5 disabled:opacity-40`}
              />
              <button
                type="button"
                title="删除阈值"
                aria-label="删除阈值"
                onClick={() => setThresholds(thresholds.filter((_, i) => i !== idx))}
                className="col-span-1 justify-self-end p-1 text-fg-subtle hover:text-rose-600"
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
            aria-expanded={showHTTP}
            onClick={() => setShowHTTP((v) => !v)}
            className="inline-flex items-center gap-1 text-fg-muted hover:text-fg font-medium"
          >
            <Sliders className="w-3.5 h-3.5 text-tidal" />
            高级连接选项
          </button>
          {showHTTP && (
            <fieldset className="mt-2 p-3 rounded-lg border border-line bg-surface-subtle">
              <legend className="sr-only">高级连接选项</legend>
              <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
              <div>
                <label className={fieldLabelCls}>连接模型</label>
                <select
                  value={http?.conn_mode || 'per_vu'}
                  onChange={(e) => onHTTPChange({ ...(http || {}), conn_mode: e.target.value as 'per_vu' | 'shared' })}
                  className={selectCls}
                >
                  <option value="per_vu">每 VU 独占连接池</option>
                  <option value="shared">全局共享连接池</option>
                </select>
              </div>
              <div>
                <label className={fieldLabelCls}>建连 / 总超时</label>
                <div className="flex gap-1">
                  <input
                    type="text"
                    aria-label="建连超时"
                    placeholder="5s"
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    value={http?.timeout?.connect || ''}
                    onChange={(e) =>
                      onHTTPChange({
                        ...(http || {}),
                        timeout: { ...(http?.timeout || {}), connect: e.target.value || undefined },
                      })
                    }
                    className={`${inputCls} w-1/2 px-1.5`}
                  />
                  <input
                    type="text"
                    aria-label="总超时"
                    placeholder="60s"
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    value={http?.timeout?.total || ''}
                    onChange={(e) =>
                      onHTTPChange({
                        ...(http || {}),
                        timeout: { ...(http?.timeout || {}), total: e.target.value || undefined },
                      })
                    }
                    className={`${inputCls} w-1/2 px-1.5`}
                  />
                </div>
              </div>
              <div>
                <label className={fieldLabelCls}>连接失败退避</label>
                <input
                  type="text"
                  placeholder="200ms"
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={http?.connect_backoff || ''}
                  onChange={(e) => onHTTPChange({ ...(http || {}), connect_backoff: e.target.value || undefined })}
                  className={inputCls}
                />
              </div>
              <div className="flex flex-col justify-center gap-1.5 text-fg">
                <label className="inline-flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={http?.keep_alive ?? true}
                    onChange={(e) => onHTTPChange({ ...(http || {}), keep_alive: e.target.checked })}
                  />
                  <span>Keep-Alive</span>
                </label>
                <label className="inline-flex items-center gap-1.5 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={Boolean(http?.insecure_skip_verify)}
                    onChange={(e) => onHTTPChange({ ...(http || {}), insecure_skip_verify: e.target.checked })}
                  />
                  <span>跳过 TLS 证书校验</span>
                </label>
              </div>
              <div>
                <label className={fieldLabelCls}>HTTP 版本</label>
                <select
                  value={http?.http_version || 'auto'}
                  onChange={(e) =>
                    onHTTPChange({
                      ...(http || {}),
                      http_version: e.target.value as 'auto' | 'http1' | 'http2',
                    })
                  }
                  className={selectCls}
                >
                  <option value="auto">自动协商</option>
                  <option value="http1">HTTP/1.1</option>
                  <option value="http2">HTTP/2</option>
                </select>
              </div>
              <div className="sm:col-span-2">
                <label className={fieldLabelCls}>出站代理</label>
                <input
                  type="text"
                  placeholder="http://127.0.0.1:8888 · socks5://127.0.0.1:1080"
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={http?.proxy_url || ''}
                  onChange={(e) => onHTTPChange({ ...(http || {}), proxy_url: e.target.value || undefined })}
                  className={inputCls}
                />
              </div>
              <div>
                <label className={fieldLabelCls}>默认失败策略</label>
                <select
                  value={http?.on_error || 'continue'}
                  onChange={(e) =>
                    onHTTPChange({
                      ...(http || {}),
                      on_error: (e.target.value === 'continue' ? '' : e.target.value) as '' | 'abort_iteration' | 'abort_vu',
                    })
                  }
                  className={selectCls}
                >
                  <option value="continue">继续后续步骤</option>
                  <option value="abort_iteration">中止本轮迭代</option>
                  <option value="abort_vu">停止当前 VU</option>
                </select>
              </div>
              <div>
                <label className={fieldLabelCls}>
                  建连速率上限 <span className="font-mono">/s</span>
                </label>
                <input
                  type="number"
                  min={0}
                  placeholder="不限"
                  value={http?.max_dials_per_sec || ''}
                  onChange={(e) => {
                    const n = parseInt(e.target.value, 10)
                    onHTTPChange({ ...(http || {}), max_dials_per_sec: n > 0 ? n : undefined })
                  }}
                  className={inputCls}
                />
              </div>
              <div className="col-span-2 sm:col-span-4">
                <label className={fieldLabelCls}>
                  DNS 覆写 <span className="font-mono text-fg-subtle">域名=IP[:端口]</span>
                </label>
                <textarea
                  rows={2}
                  placeholder="api.example.com=10.0.0.12"
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={Object.entries(http?.dns_overrides || {})
                    .map(([k, v]) => `${k}=${v}`)
                    .join('\n')}
                  onChange={(e) => {
                    const map: Record<string, string> = {}
                    for (const line of e.target.value.split(/\r?\n/)) {
                      const trimmed = line.trim()
                      if (!trimmed) continue
                      const eq = trimmed.indexOf('=')
                      if (eq > 0) {
                        const host = trimmed.slice(0, eq).trim()
                        const ip = trimmed.slice(eq + 1).trim()
                        if (host && ip) map[host] = ip
                      }
                    }
                    onHTTPChange({
                      ...(http || {}),
                      dns_overrides: Object.keys(map).length > 0 ? map : undefined,
                    })
                  }}
                  className={inputCls}
                />
              </div>
              </div>
            </fieldset>
          )}
        </div>
      )}
    </div>
  )
}
