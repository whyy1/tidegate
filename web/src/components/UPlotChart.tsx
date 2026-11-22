import { useEffect, useRef } from 'react'
import uPlot from 'uplot'

export interface ChartSeriesSpec {
  label: string
  stroke: string
  fill?: string
  dash?: number[]
}

export interface AlertWindow {
  startSec: number
  endSec: number
  level?: string
}

interface Props {
  title: string
  series: ChartSeriesSpec[]
  data: uPlot.AlignedData
  height?: number
  yUnit?: string
  alertWindows?: AlertWindow[]
}

export function UPlotChart({ title, series, data, height = 220, yUnit = '', alertWindows = [] }: Props) {
  const hostRef = useRef<HTMLDivElement | null>(null)
  const plotRef = useRef<uPlot | null>(null)
  const alertsRef = useRef<AlertWindow[]>(alertWindows)
  const dataRef = useRef<uPlot.AlignedData>(data)
  const prevDataRef = useRef<uPlot.AlignedData | null>(null)
  const prevAlertsRef = useRef<AlertWindow[] | null>(null)

  alertsRef.current = alertWindows
  dataRef.current = data

  const seriesKey = series.map((s) => `${s.label}:${s.stroke}`).join('|')

  useEffect(() => {
    const el = hostRef.current
    if (!el) return

    const width = Math.max(320, el.clientWidth || 520)
    const opts: uPlot.Options = {
      width,
      height,
      scales: {
        x: { time: false },
        y: {
          range: (_u, minVal, maxVal) => {
            const hi = maxVal <= 0 ? 1 : maxVal * 1.1
            const lo = minVal < 0 ? minVal : 0
            return [lo, hi]
          },
        },
      },
      axes: [
        {
          stroke: '#64748b',
          grid: { stroke: '#e2e8f0', width: 1 },
          values: (_u, vals) => vals.map((v) => `${Math.round(v)}s`),
          font: '11px system-ui,sans-serif',
        },
        {
          stroke: '#64748b',
          grid: { stroke: '#e2e8f0', width: 1 },
          size: 54,
          values: (_u, vals) =>
            vals.map((v) => {
              const s = v >= 1000 ? `${(v / 1000).toFixed(1)}k` : v >= 10 ? v.toFixed(0) : v.toFixed(2)
              return yUnit ? `${s}${yUnit}` : s
            }),
          font: '11px system-ui,sans-serif',
        },
      ],
      series: [
        {
          label: '耗时',
          value: (_u, v) => (v == null ? '-' : `${v.toFixed(1)}s`),
        },
        ...series.map((s): uPlot.Series => ({
          label: s.label,
          stroke: s.stroke,
          fill: s.fill,
          width: 2,
          dash: s.dash,
          spanGaps: true,
          value: (_u, v) => (v == null ? '-' : `${v.toFixed(2)}${yUnit ? ' ' + yUnit : ''}`),
        })),
      ],
      cursor: {
        drag: { x: false, y: false },
      },
      hooks: {
        drawClear: [
          (u) => {
            const wins = alertsRef.current
            if (!wins || wins.length === 0) return
            const { left, top, width: bw, height: bh } = u.bbox
            if (bw <= 0 || bh <= 0) return
            const ctx = u.ctx
            ctx.save()
            for (const w of wins) {
              const x0 = Math.max(left, u.valToPos(w.startSec, 'x', true))
              const x1 = Math.min(left + bw, u.valToPos(w.endSec, 'x', true))
              if (x1 > x0) {
                ctx.fillStyle =
                  w.level === 'critical' ? 'rgba(225, 29, 72, 0.14)' : 'rgba(245, 158, 11, 0.16)'
                ctx.fillRect(x0, top, Math.max(2, x1 - x0), bh)
              }
            }
            ctx.restore()
          },
        ],
      },
    }

    const plot = new uPlot(opts, dataRef.current, el)
    plotRef.current = plot
    prevDataRef.current = dataRef.current
    prevAlertsRef.current = alertsRef.current

    const ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const w = Math.floor(entry.contentRect.width)
        if (w > 120 && plotRef.current) {
          plotRef.current.setSize({ width: w, height })
        }
      }
    })
    ro.observe(el)

    return () => {
      ro.disconnect()
      plot.destroy()
      plotRef.current = null
      prevDataRef.current = null
      prevAlertsRef.current = null
    }
  }, [seriesKey, height, yUnit])

  useEffect(() => {
    if (!plotRef.current) return
    const dataChanged = prevDataRef.current !== data
    const alertsChanged = prevAlertsRef.current !== alertWindows
    prevDataRef.current = data
    prevAlertsRef.current = alertWindows
    if (dataChanged) {
      plotRef.current.setData(data)
    } else if (alertsChanged) {
      plotRef.current.redraw(false, false)
    }
  }, [data, alertWindows])

  const pointCount = data[0]?.length || 0

  return (
    <div className="bg-white rounded-xl border border-slate-200 p-3.5 shadow-xs">
      <div className="flex items-center justify-between mb-2">
        <h3 className="text-xs font-semibold text-slate-800">{title}</h3>
        <span className="text-[11px] font-mono text-slate-400">{pointCount} 点</span>
      </div>
      <div ref={hostRef} className="w-full min-h-[180px]" />
    </div>
  )
}
