import { useEffect, useRef, useState } from 'react'
import {
  ArrowRight,
  BookmarkPlus,
  Check,
  CheckCircle2,
  Clock,
  Copy,
  Download,
  RefreshCw,
  Send,
  Terminal,
  Trash2,
  XCircle,
  Zap,
} from 'lucide-react'
import { copyText, downloadText, fileStamp } from '../lib/download'
import type { APIResponseExample, DebugAPIResponse, DebugRecord } from '../lib/types'
import { RollingNumber } from './DropletFilter'
import { JSONTreeViewer } from './JSONTreeViewer'
import { LiquidCapsule } from './LiquidCapsule'
import { ModuleZoomButton } from './ModuleStage'

export type RespSubTab = 'body' | 'extract_assert' | 'actual' | 'headers' | 'history'

interface Props {
  projectId: string
  activeEnvId?: string
  debugResp: DebugAPIResponse | null
  historyList: DebugRecord[]
  loadingDebug: boolean
  autoSyncTempVars: boolean
  /** Base name for downloaded response files (usually the API name). */
  fileBaseName?: string
  /** Highlights the history row currently replayed in the panel. */
  activeHistoryId?: string
  onToggleAutoSync: (next: boolean) => void
  onPickExtractJSONPath: (expr: string, suggestedName: string) => void
  onPickAssertJSONPath: (expr: string, sampleVal: string) => void
  onPushTempVars: (vars: Record<string, string>) => void
  onSaveEnvVars?: (vars: Record<string, string>) => Promise<void>
  /** Append the current response to the API's examples. Omit to hide the action (e.g. read-only). */
  onSaveExample?: (example: APIResponseExample) => void
  zoomed?: boolean
  onToggleZoom?: () => void
  onRefreshHistory: () => Promise<void>
  onLoadHistoryRecord: (id: string) => Promise<void>
  onClearHistory: () => Promise<void>
}

function formatBytes(n: number): string {
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(2)} MB`
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${n} B`
}

function responseFileType(contentType: string | undefined, body: string): { ext: string; mime: string } {
  const ct = (contentType || '').toLowerCase()
  const charset = ';charset=utf-8'
  if (ct.includes('json')) return { ext: 'json', mime: `application/json${charset}` }
  if (ct.includes('html')) return { ext: 'html', mime: `text/html${charset}` }
  if (ct.includes('xml')) return { ext: 'xml', mime: `application/xml${charset}` }
  if (!ct) {
    const t = body.trim()
    if (t.startsWith('{') || t.startsWith('[')) {
      try {
        JSON.parse(t)
        return { ext: 'json', mime: `application/json${charset}` }
      } catch {
        // fall through
      }
    }
  }
  return { ext: 'txt', mime: `text/plain${charset}` }
}

function safeFileName(s: string): string {
  return s.replace(/[\\/:*?"<>|\s]+/g, '_').replace(/^_+|_+$/g, '').slice(0, 60) || 'response'
}

function statusTone(status: number, error?: string): string {
  if (error || status <= 0) return 'bg-rose-50 text-rose-800 border-rose-200'
  if (status >= 200 && status < 300) return 'bg-emerald-50 text-emerald-800 border-emerald-200'
  if (status >= 400) return 'bg-rose-50 text-rose-800 border-rose-200'
  return 'bg-amber-50 text-amber-800 border-amber-200'
}

const headerBtn =
  'inline-flex items-center gap-1 px-2 py-1 rounded-md border border-line bg-surface hover:bg-surface-hover text-fg-muted hover:text-fg text-[11px] font-medium transition-colors'

export function ResponsePanel({
  activeEnvId,
  debugResp,
  historyList,
  loadingDebug,
  autoSyncTempVars,
  fileBaseName,
  activeHistoryId,
  onToggleAutoSync,
  onPickExtractJSONPath,
  onPickAssertJSONPath,
  onPushTempVars,
  onSaveEnvVars,
  onSaveExample,
  zoomed,
  onToggleZoom,
  onRefreshHistory,
  onLoadHistoryRecord,
  onClearHistory,
}: Props) {
  const [subTab, setSubTab] = useState<RespSubTab>('body')
  const [rawMode, setRawMode] = useState(false)
  const [copiedBody, setCopiedBody] = useState(false)
  const [savedExample, setSavedExample] = useState(false)
  const [confirmClear, setConfirmClear] = useState(false)
  const [loadingRecordId, setLoadingRecordId] = useState('')
  const confirmTimer = useRef<number | undefined>(undefined)
  const respTabsRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => () => window.clearTimeout(confirmTimer.current), [])
  useEffect(() => setSavedExample(false), [debugResp])

  const step = debugResp?.result
  const timing = step?.timing
  const hasTextBody = Boolean(step && !step.is_binary && step.body)

  const phases = timing
    ? [
        { key: 'DNS', ms: Math.max(0, timing.dns_ms), color: 'bg-cyan-500' },
        { key: '建连', ms: Math.max(0, timing.connect_ms), color: 'bg-amber-500' },
        { key: 'TLS', ms: Math.max(0, timing.tls_ms), color: 'bg-purple-500' },
        { key: '首字节', ms: Math.max(0, timing.first_byte_ms), color: 'bg-emerald-500' },
        { key: '下载', ms: Math.max(0, timing.download_ms), color: 'bg-sky-500' },
      ]
    : []
  const phaseSum = Math.max(
    0.001,
    phases.reduce((acc, p) => acc + p.ms, 0)
  )

  const extractedVars = step?.extracted_vars || {}
  const extractNames = new Set((step?.extracts || []).map((e) => e.name))
  const scriptVars = Object.entries(extractedVars).filter(([k]) => !extractNames.has(k))
  const extractedCount = Object.keys(extractedVars).length

  const handleDownload = () => {
    if (!step || !step.body) return
    const { ext, mime } = responseFileType(step.content_type, step.body)
    downloadText(`${safeFileName(fileBaseName || 'response')}-${step.status || 'err'}-${fileStamp()}.${ext}`, step.body, mime)
  }

  const handleSaveExample = () => {
    if (!step || !onSaveExample) return
    onSaveExample({
      name: `响应 ${step.status || 200}`,
      status: step.status || 200,
      content_type: step.content_type || 'application/json',
      body: step.body || '',
    })
    setSavedExample(true)
  }

  const handleClear = () => {
    if (!confirmClear) {
      setConfirmClear(true)
      window.clearTimeout(confirmTimer.current)
      confirmTimer.current = window.setTimeout(() => setConfirmClear(false), 3000)
      return
    }
    window.clearTimeout(confirmTimer.current)
    setConfirmClear(false)
    void onClearHistory()
  }

  const loadRecord = async (id: string) => {
    setLoadingRecordId(id)
    try {
      await onLoadHistoryRecord(id)
      setSubTab('body')
    } finally {
      setLoadingRecordId('')
    }
  }

  return (
    <div className="tg-accent-card flex-1 min-h-0 flex flex-col">
      {/* Response Header Tabs & Telemetry */}
      <div className="relative z-10 px-3 py-1 border-b border-line bg-surface-subtle/60 flex flex-wrap items-center justify-between gap-x-2.5 gap-y-1 shrink-0">
        <div
          ref={respTabsRef}
          role="tablist"
          aria-label="响应检查器页签"
          className="relative tg-tablist gap-1 text-xs font-medium min-w-0 py-0.5"
        >
          <LiquidCapsule
            containerRef={respTabsRef}
            activeId={subTab}
            orientation="horizontal"
            className="rounded-lg"
          />
          {(
            [
              { id: 'body' as const, label: '响应体', count: 0 },
              {
                id: 'extract_assert' as const,
                label: '提取与断言',
                count:
                  (step?.extracts?.length || 0) +
                  (step?.asserts?.length || 0) +
                  (step?.extracts?.length ? 0 : extractedCount),
              },
              { id: 'actual' as const, label: '实际请求', count: 0 },
              { id: 'headers' as const, label: '响应头', count: step?.response_headers?.length || 0 },
              { id: 'history' as const, label: '调试历史', count: historyList.length },
            ] as const
          ).map((t) => {
            const selected = subTab === t.id
            return (
              <button
                key={t.id}
                data-liq-id={t.id}
                type="button"
                role="tab"
                aria-selected={selected}
                onClick={() => {
                  setSubTab(t.id)
                  if (t.id === 'history') void onRefreshHistory()
                }}
                className={`relative z-10 inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg transition-colors whitespace-nowrap ${
                  selected ? 'text-tidal font-semibold' : 'text-fg-muted hover:text-fg'
                }`}
              >
                <span>{t.label}</span>
                {t.count > 0 && (
                  <span className="tg-tabular px-1.5 rounded-full bg-sky-500/12 text-tidal font-mono text-[10px]">
                    {t.count}
                  </span>
                )}
              </button>
            )
          })}
        </div>

        <div className="tg-tabular flex flex-wrap items-center gap-2 text-xs py-0.5 ml-auto">
          {step && (
            <>
              <span className={`px-2 py-0.5 rounded-full border font-mono font-semibold ${statusTone(step.status, step.error)}`}>
                {step.status > 0 ? `HTTP ${step.status}` : '网络错误'}
              </span>
              <RollingNumber
                value={step.timing.total_ms}
                decimals={2}
                suffix=" ms"
                className="font-mono font-semibold text-fg"
              />
              <span className="text-fg-subtle text-[11px] font-mono" title="接收 / 发送">
                {formatBytes(step.bytes_received)} / {formatBytes(step.bytes_sent)}
              </span>
              <span
                className={`inline-flex items-center gap-1 font-semibold ${
                  step.passed ? 'text-emerald-700' : 'text-rose-700'
                }`}
              >
                {step.passed ? <CheckCircle2 className="w-3.5 h-3.5" /> : <XCircle className="w-3.5 h-3.5" />}
                {step.passed ? '通过' : '未通过'}
              </span>
              {hasTextBody && (
                <span className="inline-flex items-center gap-1 pl-2 border-l border-line">
                  <button type="button" onClick={handleDownload} className={headerBtn} title="下载响应体">
                    <Download className="w-3 h-3" /> 下载
                  </button>
                  {onSaveExample && (
                    <button
                      type="button"
                      onClick={handleSaveExample}
                      disabled={savedExample}
                      className={`${headerBtn} disabled:opacity-70`}
                      title="追加到接口文档的响应示例"
                    >
                      {savedExample ? (
                        <Check className="w-3 h-3 text-emerald-600" />
                      ) : (
                        <BookmarkPlus className="w-3 h-3" />
                      )}
                      {savedExample ? '已存为示例' : '存为示例'}
                    </button>
                  )}
                </span>
              )}
            </>
          )}
          {onToggleZoom && (
            <ModuleZoomButton expanded={Boolean(zoomed)} onToggle={onToggleZoom} />
          )}
        </div>
      </div>

      {/* Inline 5-Phase Waterfall */}
      {timing && subTab !== 'history' && (
        <div className="relative z-10 px-3 py-2 bg-surface-subtle/70 border-b border-line flex flex-wrap items-center gap-3 text-[11px] shrink-0">
          <div className="flex items-center gap-1 text-fg-muted font-medium">
            <Clock className="w-3 h-3 text-tidal" /> 耗时分布
          </div>
          <div className="flex-1 min-w-[140px] h-2 rounded-full overflow-hidden bg-line flex">
            {phases.map((p) =>
              p.ms > 0 ? (
                <div
                  key={p.key}
                  style={{ width: `${Math.max(2, (p.ms / phaseSum) * 100)}%` }}
                  className={p.color}
                  title={`${p.key}: ${p.ms.toFixed(2)} ms`}
                />
              ) : null
            )}
          </div>
          <div className="tg-tabular flex flex-wrap items-center gap-2 font-mono text-fg-muted">
            {phases.map((p) => (
              <span key={p.key} className="inline-flex items-center gap-1">
                <span className={`w-2 h-2 rounded-full ${p.color}`} />
                {p.key} {p.ms.toFixed(1)}ms
              </span>
            ))}
            {timing.reused && (
              <span className="px-1.5 rounded bg-sky-50 border border-sky-200 text-sky-800 text-[10px]">复用连接</span>
            )}
          </div>
        </div>
      )}

      {/* Scrollable Inspector Content */}
      <div className="relative z-10 flex-1 min-h-0 overflow-y-auto tg-scroller p-3 text-xs flex flex-col">
        {loadingDebug ? (
          <div className="py-12 text-center">
            <div className="inline-flex items-center gap-2 px-3 py-1.5 rounded-full bg-sky-50 border border-sky-200 text-sky-800 font-medium">
              <span className="w-2 h-2 rounded-full bg-sky-500 animate-pulse" />
              <span>发送中…</span>
            </div>
          </div>
        ) : subTab === 'history' ? (
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <span className="font-medium text-fg-muted">最近 {historyList.length} 条</span>
              <div className="flex items-center gap-1.5">
                <button type="button" onClick={() => void onRefreshHistory()} className={headerBtn} title="刷新">
                  <RefreshCw className="w-3 h-3" /> 刷新
                </button>
                {historyList.length > 0 && (
                  <button
                    type="button"
                    onClick={handleClear}
                    onBlur={() => setConfirmClear(false)}
                    className={
                      confirmClear
                        ? 'inline-flex items-center gap-1 px-2 py-1 rounded-md bg-rose-600 hover:bg-rose-solid text-white text-[11px] font-semibold'
                        : `${headerBtn} hover:text-rose-700`
                    }
                  >
                    <Trash2 className="w-3 h-3" /> {confirmClear ? '确认清空？' : '清空'}
                  </button>
                )}
              </div>
            </div>
            {historyList.length === 0 ? (
              <p className="text-fg-subtle py-8 text-center">暂无调试记录</p>
            ) : (
              <div className="border border-line rounded-lg bg-surface overflow-auto tg-scroller max-h-[60dvh]">
                <table className="w-full text-left border-collapse">
                  <thead className="bg-surface-subtle border-b border-line text-fg-subtle sticky top-0">
                    <tr>
                      <th className="px-3 py-1.5 w-20 font-medium">方法</th>
                      <th className="px-3 py-1.5 font-medium">URL</th>
                      <th className="px-3 py-1.5 w-16 font-medium">状态</th>
                      <th className="px-3 py-1.5 w-24 font-medium text-right">耗时</th>
                      <th className="px-3 py-1.5 w-24 font-medium text-right">时间</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-[var(--tg-border)] tg-tabular">
                    {historyList.map((rec) => {
                      const current = rec.id === activeHistoryId
                      return (
                        <tr
                          key={rec.id}
                          tabIndex={0}
                          aria-current={current || undefined}
                          aria-busy={loadingRecordId === rec.id || undefined}
                          onClick={() => void loadRecord(rec.id)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') {
                              e.preventDefault()
                              void loadRecord(rec.id)
                            }
                          }}
                          title="回填请求并查看响应"
                          className={`cursor-pointer focus:outline-none focus-visible:bg-surface-hover hover:bg-surface-hover ${
                            current ? 'bg-sky-500/10 shadow-[inset_2px_0_0_var(--tg-tidal-600)]' : ''
                          } ${loadingRecordId === rec.id ? 'opacity-60' : ''}`}
                        >
                          <td className="px-3 py-1.5 font-mono font-semibold text-fg">{rec.method}</td>
                          <td className="px-3 py-1.5 font-mono text-fg-muted truncate max-w-[300px]" title={rec.url}>
                            {rec.url}
                          </td>
                          <td className="px-3 py-1.5 font-mono">
                            <span
                              className={
                                rec.status >= 200 && rec.status < 300
                                  ? 'text-emerald-700 font-semibold'
                                  : rec.status >= 300 && rec.status < 400
                                    ? 'text-amber-700 font-semibold'
                                    : 'text-rose-700 font-semibold'
                              }
                            >
                              {rec.status || 'ERR'}
                            </span>
                          </td>
                          <td className="px-3 py-1.5 font-mono text-fg-muted text-right">{rec.duration_ms.toFixed(1)} ms</td>
                          <td className="px-3 py-1.5 text-fg-subtle text-right" title={new Date(rec.created_at).toLocaleString()}>
                            {new Date(rec.created_at).toLocaleTimeString()}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        ) : !step ? (
          <div className="h-full min-h-[200px] flex flex-col items-center justify-center text-center gap-2.5">
            <div className="w-10 h-10 rounded-xl bg-surface-subtle border border-line flex items-center justify-center text-fg-subtle">
              <Terminal className="w-5 h-5" />
            </div>
            <div className="text-xs font-medium text-fg-muted">等待发送</div>
            <kbd className="tg-kbd font-mono">Ctrl+Enter</kbd>
          </div>
        ) : (
          <>
            {step.error && (
              <div className="mb-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-rose-800">
                <span className="font-semibold">[{step.error_kind || 'error'}]</span> {step.error}
              </div>
            )}

            {subTab === 'body' && (
              <div className="flex-1 min-h-0 flex flex-col gap-2">
                <div className="flex flex-wrap items-center justify-between gap-2 shrink-0">
                  <div className="flex items-center gap-2 text-fg-subtle min-w-0">
                    <span className="font-mono text-[11px] truncate">{step.content_type || 'unknown'}</span>
                    {step.body_truncated && (
                      <span className="px-1.5 py-0.5 rounded-full border border-amber-200 bg-amber-100 text-amber-800 text-[11px] shrink-0">
                        已截断
                      </span>
                    )}
                  </div>
                  {!step.is_binary && (
                    <div className="flex items-center gap-1.5">
                      {step.body && (
                        <button
                          type="button"
                          onClick={async () => {
                            if (await copyText(step.body || '')) {
                              setCopiedBody(true)
                              window.setTimeout(() => setCopiedBody(false), 1500)
                            }
                          }}
                          className={headerBtn}
                        >
                          {copiedBody ? <Check className="w-3 h-3 text-emerald-600" /> : <Copy className="w-3 h-3" />}
                          <span>{copiedBody ? '已复制' : '复制'}</span>
                        </button>
                      )}
                      <div role="radiogroup" aria-label="响应体视图" className="inline-flex rounded-md border border-line p-0.5 bg-surface-subtle text-[11px]">
                        {(
                          [
                            { raw: false, label: '树形' },
                            { raw: true, label: '原始' },
                          ] as const
                        ).map((o) => (
                          <button
                            key={o.label}
                            type="button"
                            role="radio"
                            aria-checked={rawMode === o.raw}
                            onClick={() => setRawMode(o.raw)}
                            className={`px-2 py-0.5 rounded font-medium ${
                              rawMode === o.raw ? 'bg-surface shadow-xs text-tidal' : 'text-fg-muted hover:text-fg'
                            }`}
                          >
                            {o.label}
                          </button>
                        ))}
                      </div>
                    </div>
                  )}
                </div>

                {step.is_binary ? (
                  <div className="p-4 rounded-lg border border-line bg-surface space-y-2">
                    <div className="font-medium text-fg tg-tabular">二进制响应体 · {formatBytes(step.bytes_received)}</div>
                    {step.body_base64 && (
                      <a
                        download={`response-${step.step_id || 'body'}`}
                        href={`data:${step.content_type || 'application/octet-stream'};base64,${step.body_base64}`}
                        className="inline-flex items-center gap-1 px-3 py-1.5 rounded-md bg-sky-600 hover:bg-sky-solid text-white font-medium"
                      >
                        <Download className="w-3.5 h-3.5" /> 下载
                      </a>
                    )}
                  </div>
                ) : rawMode ? (
                  <pre className="flex-1 min-h-[180px] p-3.5 rounded-lg border tg-code-surface tg-scroller-dark font-mono text-xs overflow-auto whitespace-pre-wrap break-all">
                    {step.body || '(空响应体)'}
                  </pre>
                ) : (
                  <JSONTreeViewer
                    rawText={step.body || ''}
                    onPickExtractJSONPath={onPickExtractJSONPath}
                    onPickAssertJSONPath={onPickAssertJSONPath}
                  />
                )}
              </div>
            )}

            {subTab === 'extract_assert' && (
              <div className="space-y-4">
                <div className="flex flex-wrap items-center justify-between gap-2 bg-surface border border-line rounded-lg px-3 py-2">
                  <label className="inline-flex items-center gap-2 text-fg-muted cursor-pointer font-medium">
                    <input
                      type="checkbox"
                      checked={autoSyncTempVars}
                      onChange={(e) => onToggleAutoSync(e.target.checked)}
                      className="rounded border-line-strong"
                    />
                    <Zap className="w-3.5 h-3.5 text-amber-500 shrink-0" />
                    <span>自动写入临时变量</span>
                  </label>
                  {extractedCount > 0 && (
                    <div className="flex flex-wrap items-center gap-1.5">
                      {!autoSyncTempVars && (
                        <button
                          type="button"
                          onClick={() => onPushTempVars(extractedVars)}
                          className="inline-flex items-center gap-1 px-2.5 py-1 rounded-md bg-amber-50 border border-amber-300 text-amber-800 hover:bg-amber-100"
                        >
                          <Zap className="w-3.5 h-3.5" /> 写入临时变量 <span className="tg-tabular">{extractedCount}</span>
                        </button>
                      )}
                      {activeEnvId && onSaveEnvVars && (
                        <button
                          type="button"
                          onClick={() => void onSaveEnvVars(extractedVars)}
                          className="inline-flex items-center gap-1 px-2.5 py-1 rounded-md bg-sky-50 border border-sky-300 text-sky-800 hover:bg-sky-100"
                        >
                          <Zap className="w-3.5 h-3.5" /> 写入当前环境 <span className="tg-tabular">{extractedCount}</span>
                        </button>
                      )}
                    </div>
                  )}
                </div>

                <section>
                  <h4 className="font-semibold text-fg mb-1.5">变量提取</h4>
                  {(step.extracts?.length || 0) === 0 ? (
                    <p className="text-fg-subtle">未配置提取规则</p>
                  ) : (
                    <div className="border border-line rounded-lg bg-surface overflow-hidden">
                      <table className="w-full text-left border-collapse">
                        <thead className="bg-surface-subtle border-b border-line text-fg-subtle">
                          <tr>
                            <th className="px-3 py-1.5 font-medium">变量名</th>
                            <th className="px-3 py-1.5 font-medium">来源 / 表达式</th>
                            <th className="px-3 py-1.5 font-medium">状态</th>
                            <th className="px-3 py-1.5 font-medium">提取值</th>
                          </tr>
                        </thead>
                        <tbody className="divide-y divide-[var(--tg-border)]">
                          {step.extracts!.map((ex, i) => (
                            <tr key={i}>
                              <td className="px-3 py-1.5 font-mono font-semibold text-sky-800">{ex.name}</td>
                              <td className="px-3 py-1.5 font-mono text-fg-muted">
                                <span className="px-1 rounded bg-surface-subtle border border-line text-fg-muted text-[10px] mr-1">{ex.from}</span>
                                {ex.expr}
                              </td>
                              <td className="px-3 py-1.5">
                                {ex.matched ? (
                                  <span className="inline-flex items-center gap-1 text-emerald-700 font-semibold">
                                    <CheckCircle2 className="w-3.5 h-3.5" /> 命中
                                  </span>
                                ) : (
                                  <span className="inline-flex items-center gap-1 text-rose-700 font-semibold">
                                    <XCircle className="w-3.5 h-3.5" /> 未命中 {ex.error}
                                  </span>
                                )}
                              </td>
                              <td className="px-3 py-1.5 font-mono text-fg break-all">{ex.value || '-'}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </section>

                {scriptVars.length > 0 && (
                  <section>
                    <h4 className="font-semibold text-fg mb-1.5">脚本变量</h4>
                    <div className="flex flex-wrap gap-1.5 font-mono text-[11px]">
                      {scriptVars.map(([k, val]) => (
                        <span key={k} className="px-2 py-0.5 rounded border border-line bg-surface-subtle text-fg">
                          <span className="text-sky-700 font-semibold">{k}</span>={val}
                        </span>
                      ))}
                    </div>
                  </section>
                )}

                <section>
                  <h4 className="font-semibold text-fg mb-1.5">响应断言</h4>
                  {(step.asserts?.length || 0) === 0 ? (
                    <p className="text-fg-subtle">未配置断言 · 默认 2xx/3xx 通过</p>
                  ) : (
                    <div className="border border-line rounded-lg bg-surface overflow-hidden">
                      <table className="w-full text-left border-collapse">
                        <thead className="bg-surface-subtle border-b border-line text-fg-subtle">
                          <tr>
                            <th className="px-3 py-1.5 w-20 font-medium">结果</th>
                            <th className="px-3 py-1.5 w-28 font-medium">类型</th>
                            <th className="px-3 py-1.5 font-medium">预期</th>
                            <th className="px-3 py-1.5 font-medium">失败明细</th>
                          </tr>
                        </thead>
                        <tbody className="divide-y divide-[var(--tg-border)]">
                          {step.asserts!.map((as, i) => (
                            <tr key={i}>
                              <td className="px-3 py-1.5 font-semibold">
                                {as.passed ? (
                                  <span className="inline-flex items-center gap-1 text-emerald-700">
                                    <CheckCircle2 className="w-3.5 h-3.5" /> 通过
                                  </span>
                                ) : (
                                  <span className="inline-flex items-center gap-1 text-rose-700">
                                    <XCircle className="w-3.5 h-3.5" /> 失败
                                  </span>
                                )}
                              </td>
                              <td className="px-3 py-1.5 font-mono text-fg-muted">{as.type}</td>
                              <td className="px-3 py-1.5 text-fg">{as.description || as.desc}</td>
                              <td className="px-3 py-1.5 text-rose-700">{as.message || '-'}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </section>
              </div>
            )}

            {subTab === 'actual' && (
              <div className="space-y-3">
                <div className="p-3 rounded-lg border border-line bg-surface space-y-2">
                  <div className="flex items-center gap-2 font-mono font-semibold text-fg">
                    <Send className="w-3.5 h-3.5 text-tidal shrink-0" />
                    <span>{step.actual_request.method}</span>
                    <span className="break-all text-sky-800">{step.actual_request.url}</span>
                  </div>
                  <div className="border-t border-line pt-2">
                    <div className="text-fg-subtle font-medium mb-1">请求头</div>
                    <div className="space-y-0.5 font-mono">
                      {(step.actual_request.headers || []).map((h, i) => (
                        <div key={i} className="flex gap-2">
                          <span className="text-fg-subtle shrink-0">{h.name}:</span>
                          <span className="text-fg break-all">{h.value}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                  {step.actual_request.body && (
                    <div className="border-t border-line pt-2">
                      <div className="text-fg-subtle font-medium mb-1">请求体</div>
                      <pre className="p-2.5 rounded-md border tg-code-surface tg-scroller-dark font-mono overflow-auto max-h-48 whitespace-pre-wrap break-all">
                        {step.actual_request.body}
                      </pre>
                    </div>
                  )}
                </div>

                {(step.redirects?.length || 0) > 0 && (
                  <div className="p-3 rounded-lg border border-line bg-surface">
                    <div className="font-semibold text-fg mb-1.5 tg-tabular">重定向 · {step.redirects!.length} 跳</div>
                    <div className="space-y-1 font-mono text-[11px] tg-tabular">
                      {step.redirects!.map((hop, i) => (
                        <div key={i} className="flex items-center gap-2 text-fg-muted min-w-0">
                          <span className="px-1.5 py-0.5 rounded bg-surface-subtle border border-line text-fg-muted">{hop.status}</span>
                          <span>{hop.method}</span>
                          <span className="truncate">{hop.url}</span>
                          {hop.location && (
                            <span className="inline-flex items-center gap-1 text-sky-700 min-w-0">
                              <ArrowRight className="w-3 h-3 shrink-0" />
                              <span className="truncate">{hop.location}</span>
                            </span>
                          )}
                          <span className="ml-auto shrink-0">{hop.duration_ms.toFixed(1)}ms</span>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            )}

            {subTab === 'headers' && (
              <div className="border border-line rounded-lg bg-surface overflow-auto tg-scroller max-h-[64dvh]">
                <table className="w-full text-left border-collapse font-mono">
                  <thead className="bg-surface-subtle border-b border-line text-fg-subtle sticky top-0">
                    <tr>
                      <th className="px-3 py-1.5 w-1/3 font-medium">名称</th>
                      <th className="px-3 py-1.5 font-medium">值</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-[var(--tg-border)]">
                    {(step.response_headers || []).map((h, i) => (
                      <tr key={i}>
                        <td className="px-3 py-1.5 text-fg-muted font-semibold">{h.name}</td>
                        <td className="px-3 py-1.5 text-fg break-all">{h.value}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}
