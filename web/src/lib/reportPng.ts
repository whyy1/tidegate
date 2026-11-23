/**
 * Zero-dependency PNG report renderer (pure <canvas>, 2x DPR).
 * Colours follow the currently rendered light / dark scheme.
 * Charts: live uPlot canvases are captured from the page (`[data-tg-chart] canvas`);
 * when none are available, line charts are redrawn from snapshots.
 */
import type { Run, RunComparisonItem, RunStatus, SnapshotView, ThresholdResult } from './types'
import { chartPalette, resolvedScheme, type ResolvedScheme } from './theme'
import { downloadBlob, fileStamp } from './download'

const W = 1200
const PAD = 32
const SCALE = 2
const SANS = '"Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", system-ui, sans-serif'
const MONO = 'ui-monospace, "Cascadia Mono", "SFMono-Regular", Consolas, "Liberation Mono", monospace'
const MAX_TABLE_ROWS = 80

export const COMPARE_COLORS = ['#0284c7', '#e11d48', '#16a34a', '#d97706', '#8b5cf6', '#0d9488']

/** Scenario iterations per second for a snapshot window (live) or the whole-run summary. */
export function iterationsPerSecond(s?: SnapshotView | null): number {
  if (!s || !s.duration_ms || s.duration_ms <= 0) return 0
  return (s.iterations || 0) / (s.duration_ms / 1000)
}

// ---------------------------------------------------------------------------
// Theme

export type Tone = 'accent' | 'danger' | 'warn' | 'ok' | 'muted'

interface Theme {
  dark: boolean
  canvas: string
  surface: string
  subtle: string
  border: string
  grid: string
  axis: string
  text: string
  muted: string
  accent: string
  danger: string
  warn: string
  ok: string
}

function makeTheme(scheme: ResolvedScheme): Theme {
  const p = chartPalette(scheme)
  const dark = scheme === 'dark'
  return {
    dark,
    canvas: p.canvas,
    surface: p.surface,
    subtle: dark ? '#162032' : '#f6f8fb',
    border: p.border,
    grid: p.grid,
    axis: p.axis,
    text: p.text,
    muted: p.muted,
    accent: dark ? '#38bdf8' : '#0369a1',
    danger: dark ? '#fb7185' : '#be123c',
    warn: dark ? '#fbbf24' : '#b45309',
    ok: dark ? '#34d399' : '#047857',
  }
}

function toneColor(t: Theme, tone: Tone): string {
  return tone === 'muted' ? t.muted : t[tone]
}

function alpha(color: string, a: number): string {
  const m = /^#([0-9a-f]{6})$/i.exec(color)
  if (!m) return color
  const n = parseInt(m[1], 16)
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${a})`
}

// ---------------------------------------------------------------------------
// Primitive drawing helpers

type Ctx = CanvasRenderingContext2D

function font(size: number, weight = 400, mono = false) {
  return `${weight} ${size}px ${mono ? MONO : SANS}`
}

function rrect(ctx: Ctx, x: number, y: number, w: number, h: number, r: number) {
  const rr = Math.min(r, w / 2, h / 2)
  ctx.beginPath()
  ctx.moveTo(x + rr, y)
  ctx.lineTo(x + w - rr, y)
  ctx.arcTo(x + w, y, x + w, y + rr, rr)
  ctx.lineTo(x + w, y + h - rr)
  ctx.arcTo(x + w, y + h, x + w - rr, y + h, rr)
  ctx.lineTo(x + rr, y + h)
  ctx.arcTo(x, y + h, x, y + h - rr, rr)
  ctx.lineTo(x, y + rr)
  ctx.arcTo(x, y, x + rr, y, rr)
  ctx.closePath()
}

function panel(ctx: Ctx, t: Theme, x: number, y: number, w: number, h: number) {
  rrect(ctx, x + 0.5, y + 0.5, w - 1, h - 1, 10)
  ctx.fillStyle = t.surface
  ctx.fill()
  ctx.strokeStyle = t.border
  ctx.lineWidth = 1
  ctx.stroke()
}

function fit(ctx: Ctx, text: string, maxW: number): string {
  if (maxW <= 0) return ''
  if (ctx.measureText(text).width <= maxW) return text
  let lo = 0
  let hi = text.length
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1
    if (ctx.measureText(text.slice(0, mid) + '…').width <= maxW) lo = mid
    else hi = mid - 1
  }
  return text.slice(0, lo) + '…'
}

function text(
  ctx: Ctx,
  s: string,
  x: number,
  y: number,
  opts: { size?: number; weight?: number; mono?: boolean; color: string; align?: CanvasTextAlign; maxW?: number },
) {
  ctx.font = font(opts.size ?? 12, opts.weight ?? 400, opts.mono)
  ctx.fillStyle = opts.color
  ctx.textAlign = opts.align ?? 'left'
  ctx.textBaseline = 'middle'
  ctx.fillText(opts.maxW ? fit(ctx, s, opts.maxW) : s, x, y)
  ctx.textAlign = 'left'
}

/** Pill badge; returns its width. `alignRight` draws it ending at x. */
function badge(ctx: Ctx, t: Theme, label: string, x: number, y: number, tone: Tone, alignRight = false): number {
  ctx.font = font(11, 600)
  const w = Math.ceil(ctx.measureText(label).width) + 16
  const h = 20
  const bx = alignRight ? x - w : x
  const c = toneColor(t, tone)
  rrect(ctx, bx, y - h / 2, w, h, h / 2)
  ctx.fillStyle = alpha(c, t.dark ? 0.16 : 0.1)
  ctx.fill()
  ctx.strokeStyle = alpha(c, t.dark ? 0.45 : 0.35)
  ctx.lineWidth = 1
  ctx.stroke()
  text(ctx, label, bx + w / 2, y + 0.5, { size: 11, weight: 600, color: c, align: 'center' })
  return w
}

function sectionTitle(ctx: Ctx, t: Theme, label: string, y: number): number {
  ctx.fillStyle = t.accent
  ctx.fillRect(PAD, y + 3, 3, 14)
  text(ctx, label, PAD + 10, y + 10, { size: 14, weight: 650, color: t.text })
  return y + 30
}

// ---------------------------------------------------------------------------
// Formatting

const n1 = (v?: number) => (Number.isFinite(v) ? (v as number).toFixed(1) : '-')
const n2 = (v?: number) => (Number.isFinite(v) ? (v as number).toFixed(2) : '-')
const pct = (v?: number) => (Number.isFinite(v) ? `${((v as number) * 100).toFixed(2)}%` : '-')
const mbps = (bps?: number) => `${((bps || 0) / 1e6).toFixed(2)}`
const int = (v?: number) => (Number.isFinite(v) ? Math.round(v as number).toLocaleString('en-US') : '-')

function fmtDuration(sec: number): string {
  if (!Number.isFinite(sec) || sec <= 0) return '-'
  const s = Math.round(sec)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const r = s % 60
  if (h > 0) return `${h}h ${String(m).padStart(2, '0')}m ${String(r).padStart(2, '0')}s`
  if (m > 0) return `${m}m ${String(r).padStart(2, '0')}s`
  return `${r}s`
}

function runElapsedSec(run: Run, snapshots: SnapshotView[], live: boolean): number {
  const start = run.started_at ? Date.parse(run.started_at) : NaN
  if (Number.isFinite(start)) {
    const end = run.finished_at ? Date.parse(run.finished_at) : live ? Date.now() : NaN
    if (Number.isFinite(end) && end > start) return (end - start) / 1000
  }
  if (run.summary?.duration_ms) return run.summary.duration_ms / 1000
  return snapshots.reduce((acc, s) => acc + (s.duration_ms || 0) / 1000, 0)
}

function statusInfo(status: RunStatus): { label: string; tone: Tone } {
  switch (status) {
    case 'preparing':
      return { label: '准备中', tone: 'accent' }
    case 'running':
      return { label: '运行中', tone: 'ok' }
    case 'stopping':
      return { label: '正在停止', tone: 'warn' }
    case 'finished':
      return { label: '已完成', tone: 'ok' }
    case 'failed':
      return { label: '失败 / 中止', tone: 'danger' }
  }
}

function fileSlug(name: string): string {
  return (name || 'run').replace(/[\\/:*?"<>|\s]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 48) || 'run'
}

function fmtTime(iso?: string): string {
  if (!iso) return '-'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '-' : d.toLocaleString()
}

// ---------------------------------------------------------------------------
// Charts

interface LegendItem {
  label: string
  color: string
}

interface CapturedChart {
  kind: 'image'
  title: string
  canvas: HTMLCanvasElement
  legend: LegendItem[]
}

interface LineSeries {
  label: string
  color: string
  values: Array<number | null>
  dash?: number[]
}

interface DrawnChart {
  kind: 'lines'
  title: string
  unit?: string
  x: number[]
  series: LineSeries[]
}

type ChartBlock = CapturedChart | DrawnChart

function captureCharts(root?: ParentNode | null): CapturedChart[] {
  const scope: ParentNode = root || document
  const out: CapturedChart[] = []
  scope.querySelectorAll<HTMLElement>('[data-tg-chart]').forEach((host) => {
    const cv = host.querySelector('canvas')
    if (!cv || cv.width < 20 || cv.height < 20) return
    const legend: LegendItem[] = []
    host.querySelectorAll<HTMLElement>('.u-legend .u-series').forEach((row, idx) => {
      if (idx === 0) return // x axis row
      const label = row.querySelector('.u-label')?.textContent?.trim()
      const marker = row.querySelector<HTMLElement>('.u-marker')
      let color = ''
      if (marker) {
        color =
          marker.style.borderColor ||
          marker.style.borderTopColor ||
          getComputedStyle(marker).borderTopColor ||
          marker.style.backgroundColor
      }
      if (label) legend.push({ label, color: color || '#64748b' })
    })
    out.push({ kind: 'image', title: host.dataset.tgChart || '', canvas: cv, legend })
  })
  return out
}

function niceMax(v: number): number {
  if (!Number.isFinite(v) || v <= 0) return 1
  const exp = Math.pow(10, Math.floor(Math.log10(v)))
  const f = v / exp
  const nf = f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10
  return nf * exp
}

function fmtTick(v: number): string {
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)}M`
  if (v >= 1e4) return `${(v / 1e3).toFixed(0)}k`
  if (v >= 1000) return `${(v / 1e3).toFixed(1)}k`
  if (v >= 10 || v === 0) return v.toFixed(0)
  return v.toFixed(v >= 1 ? 1 : 2)
}

/** Wrap legend items across up to 2 rows; returns height consumed. */
function drawLegend(ctx: Ctx, t: Theme, items: LegendItem[], x: number, y: number, maxW: number): number {
  let cx = x
  let cy = y
  let rows = 1
  for (const it of items) {
    ctx.font = font(11, 500)
    const label = fit(ctx, it.label, 190)
    const w = ctx.measureText(label).width + 20
    if (cx > x && cx + w > x + maxW) {
      if (rows >= 2) break
      rows = 2
      cx = x
      cy += 16
    }
    ctx.fillStyle = it.color
    rrect(ctx, cx, cy - 4, 10, 8, 2)
    ctx.fill()
    text(ctx, label, cx + 14, cy, { size: 11, weight: 500, color: t.muted })
    cx += w + 8
  }
  return rows * 16
}

function drawLines(ctx: Ctx, t: Theme, spec: DrawnChart, x: number, y: number, w: number, h: number) {
  const left = x + 48
  const right = x + w - 8
  const top = y + 6
  const bottom = y + h - 20
  let maxV = 0
  for (const s of spec.series) for (const v of s.values) if (v != null && v > maxV) maxV = v
  const yMax = niceMax(maxV * 1.08)
  const xs = spec.x
  const xMin = xs[0] ?? 0
  const xMax = xs[xs.length - 1] ?? 1
  const xSpan = xMax - xMin || 1
  const px = (v: number) => left + ((v - xMin) / xSpan) * (right - left)
  const py = (v: number) => bottom - (v / yMax) * (bottom - top)

  ctx.lineWidth = 1
  for (let i = 0; i <= 4; i++) {
    const v = (yMax / 4) * i
    const gy = Math.round(py(v)) + 0.5
    ctx.strokeStyle = t.grid
    ctx.beginPath()
    ctx.moveTo(left, gy)
    ctx.lineTo(right, gy)
    ctx.stroke()
    text(ctx, `${fmtTick(v)}${i === 4 && spec.unit ? spec.unit : ''}`, left - 6, gy, {
      size: 10,
      mono: true,
      color: t.axis,
      align: 'right',
    })
  }
  if (xs.length > 0) {
    const ticks = xs.length > 2 ? [xs[0], xs[Math.floor(xs.length / 2)], xs[xs.length - 1]] : xs
    ticks.forEach((tv, i) => {
      text(ctx, `${fmtTick(tv)}s`, px(tv), bottom + 11, {
        size: 10,
        mono: true,
        color: t.axis,
        align: i === 0 ? 'left' : i === ticks.length - 1 ? 'right' : 'center',
      })
    })
  }
  if (xs.length === 0) {
    text(ctx, '暂无数据', x + w / 2, y + h / 2, { size: 12, color: t.muted, align: 'center' })
    return
  }
  ctx.save()
  ctx.beginPath()
  ctx.rect(left, top - 2, right - left, bottom - top + 4)
  ctx.clip()
  for (const s of spec.series) {
    ctx.strokeStyle = s.color
    ctx.lineWidth = 1.6
    ctx.lineJoin = 'round'
    ctx.setLineDash(s.dash || [])
    ctx.beginPath()
    let pen = false
    for (let i = 0; i < xs.length; i++) {
      const v = s.values[i]
      if (v == null || !Number.isFinite(v)) {
        pen = false
        continue
      }
      const X = px(xs[i])
      const Y = py(v)
      if (pen) ctx.lineTo(X, Y)
      else ctx.moveTo(X, Y)
      pen = true
    }
    ctx.stroke()
  }
  ctx.setLineDash([])
  ctx.restore()
}

const CHART_H = 290

function drawChartGrid(ctx: Ctx, t: Theme, charts: ChartBlock[], y: number): number {
  const gap = 16
  const cw = (W - PAD * 2 - gap) / 2
  charts.forEach((c, i) => {
    const col = i % 2
    const row = Math.floor(i / 2)
    const cx = PAD + col * (cw + gap)
    const cy = y + row * (CHART_H + gap)
    panel(ctx, t, cx, cy, cw, CHART_H)
    text(ctx, c.title, cx + 14, cy + 18, { size: 12, weight: 650, color: t.text, maxW: cw - 28 })
    const legend: LegendItem[] = c.kind === 'image' ? c.legend : c.series.map((s) => ({ label: s.label, color: s.color }))
    const legH = drawLegend(ctx, t, legend, cx + 14, cy + 36, cw - 28)
    const ix = cx + 10
    const iy = cy + 32 + legH + 4
    const iw = cw - 20
    const ih = CHART_H - (iy - cy) - 8
    if (c.kind === 'image') {
      const ratio = c.canvas.width / c.canvas.height
      let dw = iw
      let dh = iw / ratio
      if (dh > ih) {
        dh = ih
        dw = ih * ratio
      }
      try {
        ctx.drawImage(c.canvas, ix + (iw - dw) / 2, iy + (ih - dh) / 2, dw, dh)
      } catch {
        text(ctx, '图表不可用', cx + cw / 2, cy + CHART_H / 2, { size: 12, color: t.muted, align: 'center' })
      }
    } else {
      drawLines(ctx, t, c, ix, iy, iw, ih)
    }
  })
  const rows = Math.ceil(charts.length / 2)
  return y + rows * CHART_H + Math.max(0, rows - 1) * gap
}

// ---------------------------------------------------------------------------
// Tables

interface CellPart {
  text: string
  color?: string
  weight?: number
}

type Cell = string | { parts: CellPart[]; dot?: string; badge?: { label: string; tone: Tone } }

interface Column {
  label: string
  /** relative width */
  w: number
  align?: 'left' | 'right'
  mono?: boolean
}

const ROW_H = 26

function drawTable(ctx: Ctx, t: Theme, cols: Column[], rows: Cell[][], y: number, highlightRow?: (i: number) => boolean): number {
  const x = PAD
  const w = W - PAD * 2
  const headH = 30
  const h = headH + rows.length * ROW_H + 2
  panel(ctx, t, x, y, w, h)
  // header strip
  ctx.save()
  rrect(ctx, x + 1, y + 1, w - 2, headH, 9)
  ctx.clip()
  ctx.fillStyle = t.subtle
  ctx.fillRect(x + 1, y + 1, w - 2, headH)
  ctx.restore()
  ctx.strokeStyle = t.border
  ctx.beginPath()
  ctx.moveTo(x + 1, y + headH + 0.5)
  ctx.lineTo(x + w - 1, y + headH + 0.5)
  ctx.stroke()

  const total = cols.reduce((a, c) => a + c.w, 0)
  const inner = w - 24
  const xs: number[] = []
  let acc = x + 12
  for (const c of cols) {
    xs.push(acc)
    acc += (c.w / total) * inner
  }
  const colW = (i: number) => (cols[i].w / total) * inner

  cols.forEach((c, i) => {
    const cx = c.align === 'right' ? xs[i] + colW(i) - 6 : xs[i]
    text(ctx, c.label, cx, y + headH / 2 + 1, {
      size: 11,
      weight: 600,
      color: t.muted,
      align: c.align === 'right' ? 'right' : 'left',
      maxW: colW(i) - 6,
    })
  })

  rows.forEach((row, ri) => {
    const ry = y + headH + ri * ROW_H
    if (highlightRow?.(ri)) {
      ctx.fillStyle = alpha(t.accent, t.dark ? 0.1 : 0.06)
      ctx.fillRect(x + 1, ry, w - 2, ROW_H)
    }
    if (ri > 0) {
      ctx.strokeStyle = t.grid
      ctx.beginPath()
      ctx.moveTo(x + 12, ry + 0.5)
      ctx.lineTo(x + w - 12, ry + 0.5)
      ctx.stroke()
    }
    const cy = ry + ROW_H / 2 + 0.5
    row.forEach((cell, ci) => {
      const col = cols[ci]
      if (!col) return
      const cw = colW(ci) - 6
      const right = col.align === 'right'
      if (typeof cell === 'string') {
        text(ctx, cell, right ? xs[ci] + cw : xs[ci], cy, {
          size: 12,
          mono: col.mono,
          color: t.text,
          align: right ? 'right' : 'left',
          maxW: cw,
        })
        return
      }
      // composite cell: measure total width first
      let cx = xs[ci]
      ctx.font = font(11, 600)
      const badgeW = cell.badge ? ctx.measureText(cell.badge.label).width + 22 : 0
      let budget = cw - badgeW
      const measure = () => {
        let total = cell.dot ? 14 : 0
        for (const p of cell.parts) {
          ctx.font = font(12, p.weight ?? 400, col.mono)
          total += ctx.measureText(p.text).width
        }
        return total + badgeW
      }
      if (right) cx = xs[ci] + cw - Math.min(cw, measure())
      if (cell.dot) {
        ctx.fillStyle = cell.dot
        ctx.beginPath()
        ctx.arc(cx + 4, cy, 4, 0, Math.PI * 2)
        ctx.fill()
        cx += 14
        budget -= 14
      }
      for (const p of cell.parts) {
        ctx.font = font(12, p.weight ?? 400, col.mono)
        const s = fit(ctx, p.text, budget)
        text(ctx, s, cx, cy, { size: 12, weight: p.weight ?? 400, mono: col.mono, color: p.color || t.text })
        const pw = ctx.measureText(s).width
        cx += pw
        budget -= pw
        if (budget <= 0) break
      }
      if (cell.badge) {
        badge(ctx, t, cell.badge.label, cx + (cell.parts.length > 0 ? 6 : 0), cy, cell.badge.tone)
      }
    })
  })
  return y + h
}

// ---------------------------------------------------------------------------
// KPI cards

export interface Kpi {
  label: string
  value: string
  sub: string
  tone?: Tone
  subTone?: Tone
}

const KPI_H = 86

function drawKpis(ctx: Ctx, t: Theme, kpis: Kpi[], y: number): number {
  const gap = 12
  const cw = (W - PAD * 2 - gap * (kpis.length - 1)) / kpis.length
  kpis.forEach((k, i) => {
    const x = PAD + i * (cw + gap)
    panel(ctx, t, x, y, cw, KPI_H)
    if (k.tone && k.tone !== 'muted') {
      ctx.fillStyle = toneColor(t, k.tone)
      ctx.fillRect(x + 1, y + 12, 2, KPI_H - 24)
    }
    text(ctx, k.label, x + 14, y + 20, { size: 11, weight: 600, color: t.muted, maxW: cw - 24 })
    text(ctx, k.value, x + 14, y + 46, {
      size: 19,
      weight: 700,
      mono: true,
      color: k.tone === 'danger' ? t.danger : t.text,
      maxW: cw - 24,
    })
    text(ctx, k.sub, x + 14, y + 70, {
      size: 11,
      mono: true,
      color: k.subTone ? toneColor(t, k.subTone) : t.muted,
      maxW: cw - 24,
    })
  })
  return y + KPI_H
}

// ---------------------------------------------------------------------------
// Page frame / output

interface HeaderSpec {
  kicker: string
  title: string
  meta: string
  badges: Array<{ label: string; tone: Tone }>
}

function drawHeader(ctx: Ctx, t: Theme, h: HeaderSpec): number {
  const y = PAD
  // brand mark: tidal gauge glyph
  ctx.strokeStyle = t.accent
  ctx.lineWidth = 2
  ctx.beginPath()
  for (let i = 0; i <= 24; i++) {
    const X = PAD + i
    const Y = y + 9 + Math.sin((i / 24) * Math.PI * 2) * 4
    if (i === 0) ctx.moveTo(X, Y)
    else ctx.lineTo(X, Y)
  }
  ctx.stroke()
  text(ctx, h.kicker, PAD + 32, y + 9, { size: 12, weight: 600, color: t.accent })

  let bx = W - PAD
  for (let i = h.badges.length - 1; i >= 0; i--) {
    const b = h.badges[i]
    bx -= badge(ctx, t, b.label, bx, y + 9, b.tone, true) + 6
  }
  text(ctx, h.title, PAD, y + 42, { size: 22, weight: 700, color: t.text, maxW: W - PAD * 2 })
  text(ctx, h.meta, PAD, y + 70, { size: 12, mono: true, color: t.muted, maxW: W - PAD * 2 })
  ctx.strokeStyle = t.border
  ctx.lineWidth = 1
  ctx.beginPath()
  ctx.moveTo(PAD, y + 92.5)
  ctx.lineTo(W - PAD, y + 92.5)
  ctx.stroke()
  return y + 110
}

function drawFooter(ctx: Ctx, t: Theme, y: number): number {
  text(ctx, `Tidegate · ${new Date().toLocaleString()}`, W - PAD, y + 10, {
    size: 11,
    mono: true,
    color: t.muted,
    align: 'right',
  })
  return y + 20 + PAD
}

/** Two-pass render: measure height on a scratch context, then paint at 2x. */
async function renderPng(paint: (ctx: Ctx, t: Theme) => number): Promise<Blob> {
  const t = makeTheme(resolvedScheme())
  const scratch = document.createElement('canvas')
  scratch.width = 8
  scratch.height = 8
  const sctx = scratch.getContext('2d')
  if (!sctx) throw new Error('当前浏览器不支持 Canvas')
  const height = Math.ceil(paint(sctx, t))

  const cv = document.createElement('canvas')
  const maxPx = 32000
  const scale = Math.min(SCALE, maxPx / height)
  cv.width = Math.round(W * scale)
  cv.height = Math.round(height * scale)
  const ctx = cv.getContext('2d')
  if (!ctx) throw new Error('当前浏览器不支持 Canvas')
  ctx.scale(scale, scale)
  ctx.fillStyle = t.canvas
  ctx.fillRect(0, 0, W, height)
  paint(ctx, t)
  return new Promise<Blob>((resolve, reject) => {
    cv.toBlob((b) => (b ? resolve(b) : reject(new Error('PNG 编码失败'))), 'image/png')
  })
}

// ---------------------------------------------------------------------------
// Run report

export interface RunReportOptions {
  run: Run
  /** latest live snapshot or the final summary */
  summary: SnapshotView | null
  snapshots: SnapshotView[]
  /** true while the run is still in progress (snapshot export) */
  live?: boolean
  thresholds?: ThresholdResult[]
  alertCount?: number
  /** element containing the uPlot charts; defaults to document */
  chartRoot?: ParentNode | null
}

function runFallbackCharts(snapshots: SnapshotView[]): DrawnChart[] {
  const x: number[] = []
  let acc = 0
  for (const s of snapshots) {
    acc += (s.duration_ms || 1000) / 1000
    x.push(Number(acc.toFixed(1)))
  }
  const col = (f: (s: SnapshotView) => number) => snapshots.map(f)
  return [
    {
      kind: 'lines',
      title: '吞吐量 QPS / RPS 与 TPS',
      x,
      series: [
        { label: 'QPS / RPS', color: '#0284c7', values: col((s) => s.rps || 0) },
        { label: 'TPS', color: '#8b5cf6', values: col((s) => iterationsPerSecond(s)) },
      ],
    },
    {
      kind: 'lines',
      title: '响应延迟百分位 (ms)',
      unit: 'ms',
      x,
      series: [
        { label: 'P50', color: '#16a34a', values: col((s) => s.latency?.p50_ms || 0) },
        { label: 'P95', color: '#d97706', values: col((s) => s.latency?.p95_ms || 0) },
        { label: 'P99', color: '#dc2626', values: col((s) => s.latency?.p99_ms || 0) },
      ],
    },
    {
      kind: 'lines',
      title: '错误率 (%)',
      unit: '%',
      x,
      series: [{ label: '错误率 (%)', color: '#e11d48', values: col((s) => (s.error_rate || 0) * 100) }],
    },
    {
      kind: 'lines',
      title: '活跃 VU 与带宽 (Mbps)',
      x,
      series: [
        { label: '活跃 VU', color: '#0f766e', values: col((s) => s.active_vus || 0) },
        { label: '上行 Mbps', color: '#0ea5e9', values: col((s) => (s.sent_bps || 0) / 1e6) },
        { label: '下行 Mbps', color: '#8b5cf6', values: col((s) => (s.received_bps || 0) / 1e6), dash: [5, 4] },
      ],
    },
  ]
}

export function runKpis(s: SnapshotView | null): Kpi[] {
  const tps = iterationsPerSecond(s)
  const dropped = s?.dropped_iterations || 0
  const er = s?.error_rate || 0
  return [
    { label: 'QPS / RPS', value: `${n1(s?.rps)} /s`, sub: `请求 ${int(s?.requests)}`, tone: 'accent' },
    {
      label: 'TPS',
      value: `${n2(tps)} /s`,
      sub: dropped > 0 ? `迭代 ${int(s?.iterations)} · 丢弃 ${int(dropped)}` : `迭代 ${int(s?.iterations)}`,
      subTone: dropped > 0 ? 'warn' : undefined,
    },
    {
      label: '错误率',
      value: pct(er),
      sub: `失败 ${int(s?.errors)}`,
      tone: er > 0.01 ? 'danger' : undefined,
    },
    {
      label: 'P95 / P99',
      value: `${n1(s?.latency?.p95_ms)} / ${n1(s?.latency?.p99_ms)}`,
      sub: `ms · P50 ${n1(s?.latency?.p50_ms)} · Max ${n1(s?.latency?.max_ms)}`,
    },
    { label: '并发 VU', value: int(s?.active_vus), sub: `连接 ${int(s?.active_conns)} · 目标 ${n1(s?.target)}` },
    {
      label: '上行 / 下行',
      value: `${mbps(s?.sent_bps)} / ${mbps(s?.received_bps)}`,
      sub: 'Mbps',
    },
  ]
}

export async function exportRunReportPng(opts: RunReportOptions): Promise<void> {
  const { run, summary, snapshots, live = false } = opts
  const captured = captureCharts(opts.chartRoot)
  const charts: ChartBlock[] = captured.length > 0 ? captured : snapshots.length > 0 ? runFallbackCharts(snapshots) : []
  const steps = summary?.steps || []
  const elapsed = runElapsedSec(run, snapshots, live)
  const st = statusInfo(run.status)
  const thresholds = opts.thresholds || []

  const blob = await renderPng((ctx, t) => {
    const badges: HeaderSpec['badges'] = [st]
    if (live) badges.unshift({ label: '实时快照', tone: 'accent' })
    if ((opts.alertCount || 0) > 0) badges.push({ label: `告警 ${opts.alertCount}`, tone: 'warn' })
    if ((summary?.dropped_iterations || 0) > 0) {
      badges.push({ label: `丢弃迭代 ${int(summary?.dropped_iterations)}`, tone: 'warn' })
    }
    if (thresholds.length > 0) {
      const failed = thresholds.filter((r) => !r.passed).length
      badges.push(failed > 0 ? { label: `阈值未通过 ${failed}`, tone: 'danger' } : { label: '阈值全部通过', tone: 'ok' })
    }
    const mode = run.scenario?.load?.mode === 'rate' ? 'QPS 模式' : run.scenario?.load?.mode === 'vus' ? 'VU 模式' : ''
    let y = drawHeader(ctx, t, {
      kicker: 'Tidegate 压测报告',
      title: run.scenario_name || run.id,
      meta: [
        `Run ${run.id}`,
        `开始 ${fmtTime(run.started_at || run.created_at)}`,
        `时长 ${fmtDuration(elapsed)}`,
        mode,
      ]
        .filter(Boolean)
        .join('  ·  '),
      badges,
    })

    y = drawKpis(ctx, t, runKpis(summary), y) + 24

    if (charts.length > 0) {
      y = sectionTitle(ctx, t, '运行曲线', y)
      y = drawChartGrid(ctx, t, charts, y) + 24
    }

    if (steps.length > 0) {
      y = sectionTitle(ctx, t, '分步指标明细', y)
      const shown = steps.slice(0, MAX_TABLE_ROWS)
      const rows: Cell[][] = shown.map((s) => [
        {
          parts: [{ text: s.name || s.id || '-', weight: 600 }],
          badge: s.is_group ? { label: '事务 TPS', tone: 'accent' } : undefined,
        },
        int(s.requests),
        s.is_group ? { parts: [{ text: n1(s.rps), color: t.accent, weight: 600 }] } : n1(s.rps),
        { parts: [{ text: pct(s.error_rate), color: s.error_rate > 0 ? t.danger : undefined }] },
        n2(s.latency?.p50_ms),
        n2(s.latency?.p95_ms),
        n2(s.latency?.p99_ms),
        n2(s.latency?.max_ms),
        s.status
          ? Object.entries(s.status)
              .map(([c, n]) => `${c}×${n}`)
              .join(' ')
          : '-',
      ])
      y = drawTable(
        ctx,
        t,
        [
          { label: '步骤', w: 3.2 },
          { label: '请求数', w: 1.1, align: 'right', mono: true },
          { label: 'QPS / RPS · TPS', w: 1.4, align: 'right', mono: true },
          { label: '错误率', w: 1, align: 'right', mono: true },
          { label: 'P50 ms', w: 1, align: 'right', mono: true },
          { label: 'P95 ms', w: 1, align: 'right', mono: true },
          { label: 'P99 ms', w: 1, align: 'right', mono: true },
          { label: 'Max ms', w: 1, align: 'right', mono: true },
          { label: '状态码', w: 2, mono: true },
        ],
        rows,
        y,
      )
      if (steps.length > shown.length) {
        text(ctx, `+${steps.length - shown.length}`, W - PAD, y + 12, { size: 11, mono: true, color: t.muted, align: 'right' })
        y += 12
      }
      y += 24
    }

    if (thresholds.length > 0) {
      y = sectionTitle(ctx, t, '阈值校验', y)
      y =
        drawTable(
          ctx,
          t,
          [
            { label: '结果', w: 1 },
            { label: '规则', w: 2.2, mono: true },
            { label: '说明', w: 5 },
          ],
          thresholds.map((r) => [
            { parts: [], badge: r.passed ? { label: '通过', tone: 'ok' } : { label: '未通过', tone: 'danger' } },
            `${r.metric} ${r.op} ${r.value}`,
            r.message || '',
          ]),
          y,
        ) + 24
    }

    return drawFooter(ctx, t, y)
  })

  downloadBlob(`tidegate-${fileSlug(run.scenario_name)}-${live ? 'snapshot-' : ''}${fileStamp()}.png`, blob, 'image/png')
}

// ---------------------------------------------------------------------------
// Compare report

export interface CompareReportOptions {
  /** baseline first */
  items: RunComparisonItem[]
  colors?: string[]
  chartRoot?: ParentNode | null
}

function deltaPart(t: Theme, v: number | undefined, lowerIsBetter: boolean): CellPart[] {
  if (v === undefined || v === null || !Number.isFinite(v)) return []
  const good = lowerIsBetter ? v <= 0 : v >= 0
  return [
    {
      text: `  ${v > 0 ? '+' : ''}${v.toFixed(2)}%`,
      color: v === 0 ? t.muted : good ? t.ok : t.danger,
      weight: 600,
    },
  ]
}

function compareFallbackCharts(items: RunComparisonItem[], colors: string[]): DrawnChart[] {
  const maxLen = Math.max(0, ...items.map((it) => it.snapshots?.length || 0))
  const x = Array.from({ length: maxLen }, (_, i) => i + 1)
  const label = (it: RunComparisonItem, i: number) => `${i === 0 ? '[基线] ' : ''}${it.run.scenario_name} (${it.run.id.slice(-5)})`
  const mk = (f: (s: SnapshotView) => number): LineSeries[] =>
    items.map((it, i) => ({
      label: label(it, i),
      color: colors[i % colors.length],
      values: x.map((_, k) => (it.snapshots?.[k] ? f(it.snapshots[k]) : null)),
    }))
  return [
    { kind: 'lines', title: 'QPS / RPS 叠加对比', x, series: mk((s) => s.rps || 0) },
    { kind: 'lines', title: 'P95 响应延迟 (ms) 叠加对比', unit: 'ms', x, series: mk((s) => s.latency?.p95_ms || 0) },
  ]
}

export async function exportCompareReportPng(opts: CompareReportOptions): Promise<void> {
  const items = opts.items
  if (items.length === 0) return
  const colors = opts.colors && opts.colors.length > 0 ? opts.colors : COMPARE_COLORS
  const captured = captureCharts(opts.chartRoot)
  const charts: ChartBlock[] = captured.length > 0 ? captured : compareFallbackCharts(items, colors)
  const base = items[0]

  const stepRows = items.slice(1).flatMap((it, ri) =>
    Object.entries(it.delta?.step_deltas || {}).map(([key, sd]) => ({ it, idx: ri + 1, key, sd })),
  )

  const blob = await renderPng((ctx, t) => {
    let y = drawHeader(ctx, t, {
      kicker: 'Tidegate 压测对比报告',
      title: `${items.length} 次压测对比`,
      meta: `基线 ${base.run.scenario_name} (${base.run.id.slice(-8)})  ·  ${fmtTime(base.run.started_at || base.run.created_at)}`,
      badges: [{ label: `基线 + ${items.length - 1}`, tone: 'accent' }],
    })

    y = sectionTitle(ctx, t, '核心指标对照（相对基线）', y)
    const rows: Cell[][] = items.map((it, i) => {
      const s = it.summary
      const d = it.delta
      const er = s?.error_rate
      return [
        {
          dot: colors[i % colors.length],
          parts: [
            { text: it.run.scenario_name, weight: 600 },
            { text: ` ${it.run.id.slice(-5)}`, color: t.muted },
          ],
          badge: i === 0 ? { label: '基线', tone: 'accent' } : undefined,
        },
        { parts: [{ text: n1(s?.rps) }, ...(i > 0 ? deltaPart(t, d?.rps_pct, false) : [])] },
        n2(iterationsPerSecond(s)),
        { parts: [{ text: n2(s?.latency?.p50_ms) }, ...(i > 0 ? deltaPart(t, d?.p50_pct, true) : [])] },
        { parts: [{ text: n2(s?.latency?.p95_ms) }, ...(i > 0 ? deltaPart(t, d?.p95_pct, true) : [])] },
        { parts: [{ text: n2(s?.latency?.p99_ms) }, ...(i > 0 ? deltaPart(t, d?.p99_pct, true) : [])] },
        {
          parts: [
            { text: pct(er), color: (er || 0) > 0.01 ? t.danger : undefined },
            ...(i > 0 && d
              ? [
                  {
                    text: `  ${d.error_rate_diff > 0 ? '+' : ''}${(d.error_rate_diff * 100).toFixed(2)}%`,
                    color: d.error_rate_diff <= 0 ? t.ok : t.danger,
                    weight: 600,
                  },
                ]
              : []),
          ],
        },
        fmtTime(it.run.started_at || it.run.created_at),
      ]
    })
    y =
      drawTable(
        ctx,
        t,
        [
          { label: '压测记录', w: 2.6 },
          { label: 'QPS / RPS', w: 1.5, align: 'right', mono: true },
          { label: 'TPS', w: 0.9, align: 'right', mono: true },
          { label: 'P50 ms', w: 1.5, align: 'right', mono: true },
          { label: 'P95 ms', w: 1.5, align: 'right', mono: true },
          { label: 'P99 ms', w: 1.5, align: 'right', mono: true },
          { label: '错误率', w: 1.5, align: 'right', mono: true },
          { label: '开始时间', w: 1.6, mono: true },
        ],
        rows,
        y,
        (i) => i === 0,
      ) + 24

    if (charts.length > 0) {
      y = sectionTitle(ctx, t, '叠加曲线', y)
      y = drawChartGrid(ctx, t, charts, y) + 24
    }

    if (stepRows.length > 0) {
      y = sectionTitle(ctx, t, '分步回归明细', y)
      const shown = stepRows.slice(0, MAX_TABLE_ROWS)
      const dp = (v: number | undefined, lower: boolean): Cell => {
        const parts = deltaPart(t, v, lower).map((p) => ({ ...p, text: p.text.trim() }))
        return parts.length > 0 ? { parts } : '-'
      }
      y = drawTable(
        ctx,
        t,
        [
          { label: '对比记录', w: 2.4 },
          { label: '步骤', w: 2.4 },
          { label: 'QPS / RPS', w: 1, align: 'right', mono: true },
          { label: 'P50', w: 1, align: 'right', mono: true },
          { label: 'P95', w: 1, align: 'right', mono: true },
          { label: 'P99', w: 1, align: 'right', mono: true },
          { label: '错误率差值', w: 1.2, align: 'right', mono: true },
        ],
        shown.map(({ it, idx, key, sd }) => [
          {
            dot: colors[idx % colors.length],
            parts: [{ text: `${it.run.scenario_name} (${it.run.id.slice(-5)})` }],
          },
          sd.name || sd.id || key,
          dp(sd.rps_pct, false),
          dp(sd.p50_pct, true),
          dp(sd.p95_pct, true),
          dp(sd.p99_pct, true),
          {
            parts: [
              {
                text: `${sd.error_rate_diff > 0 ? '+' : ''}${(sd.error_rate_diff * 100).toFixed(2)}%`,
                color: sd.error_rate_diff <= 0 ? t.ok : t.danger,
                weight: 600,
              },
            ],
          },
        ]),
        y,
      )
      if (stepRows.length > shown.length) {
        text(ctx, `+${stepRows.length - shown.length}`, W - PAD, y + 12, { size: 11, mono: true, color: t.muted, align: 'right' })
        y += 12
      }
      y += 24
    }

    return drawFooter(ctx, t, y)
  })

  downloadBlob(`tidegate-compare-${fileStamp()}.png`, blob, 'image/png')
}
