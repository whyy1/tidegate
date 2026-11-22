import { useState } from 'react'
import { Check, CheckCircle2, Clock, Copy, History, Send, Terminal, Trash2, XCircle, Zap } from 'lucide-react'
import type { DebugAPIResponse, DebugRecord } from '../lib/types'
import { JSONTreeViewer } from './JSONTreeViewer'

export type RespSubTab = 'body' | 'extract_assert' | 'actual' | 'headers' | 'history'

interface Props {
  projectId: string
  debugResp: DebugAPIResponse | null
  historyList: DebugRecord[]
  loadingDebug: boolean
  autoSyncTempVars: boolean
  onToggleAutoSync: (next: boolean) => void
  onPickExtractJSONPath: (expr: string, suggestedName: string) => void
  onPickAssertJSONPath: (expr: string, sampleVal: string) => void
  onPushTempVars: (vars: Record<string, string>) => void
  onRefreshHistory: () => Promise<void>
  onLoadHistoryRecord: (id: string) => Promise<void>
  onClearHistory: () => Promise<void>
}

function formatBytes(n: number): string {
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(2)} MB`
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${n} B`
}

export function ResponsePanel({
  debugResp,
  historyList,
  loadingDebug,
  autoSyncTempVars,
  onToggleAutoSync,
  onPickExtractJSONPath,
  onPickAssertJSONPath,
  onPushTempVars,
  onRefreshHistory,
  onLoadHistoryRecord,
  onClearHistory,
}: Props) {
  const [subTab, setSubTab] = useState<RespSubTab>('body')
  const [rawMode, setRawMode] = useState(false)
  const [copiedBody, setCopiedBody] = useState(false)

  const step = debugResp?.result
  const timing = step?.timing

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

  return (
    <div className="flex-1 min-h-0 bg-slate-50/70 flex flex-col">
      {/* Response Header Tabs & Telemetry */}
      <div className="px-4 border-b border-slate-200 bg-white flex flex-wrap items-center justify-between gap-2 shrink-0">
        <div role="tablist" aria-label="响应检查器页签" className="tg-tablist gap-1 text-xs font-medium min-w-0">
          {(
            [
              { id: 'body' as const, label: '响应体', count: 0 },
              {
                id: 'extract_assert' as const,
                label: '提取与断言',
                count: (step?.extracts?.length || 0) + (step?.asserts?.length || 0),
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
                type="button"
                role="tab"
                aria-selected={selected}
                onClick={() => {
                  setSubTab(t.id)
                  if (t.id === 'history') void onRefreshHistory()
                }}
                className={`tg-tab inline-flex items-center gap-1.5 px-2.5 py-2 transition-colors ${
                  selected ? 'text-sky-700' : 'text-slate-600 hover:text-slate-900'
                }`}
              >
                <span>{t.label}</span>
                {t.count > 0 && (
                  <span className="tg-tabular px-1.5 py-0.2 rounded-full bg-slate-100 text-slate-700 font-mono text-[10px]">
                    {t.count}
                  </span>
                )}
              </button>
            )
          })}
        </div>

        {step && (
          <div className="tg-tabular flex flex-wrap items-center gap-2 text-xs py-1">
            <span
              className={`px-2 py-0.5 rounded-full font-mono font-semibold ${
                step.error
                  ? 'bg-rose-100 text-rose-800'
                  : step.status >= 200 && step.status < 300
                    ? 'bg-emerald-100 text-emerald-800'
                    : step.status >= 400
                      ? 'bg-rose-100 text-rose-800'
                      : 'bg-amber-100 text-amber-800'
              }`}
            >
              {step.status > 0 ? `HTTP ${step.status}` : '网络错误'}
            </span>
            <span className="font-mono font-semibold text-slate-800">{step.timing.total_ms.toFixed(2)} ms</span>
            <span className="text-slate-500 text-[11px]">
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
          </div>
        )}
      </div>

      {/* Inline 5-Phase Waterfall */}
      {timing && subTab !== 'history' && (
        <div className="px-4 py-1.5 bg-white border-b border-slate-200 flex flex-wrap items-center gap-3 text-[11px] shrink-0">
          <div className="flex items-center gap-1 text-slate-500 font-medium">
            <Clock className="w-3 h-3" /> 耗时分布
          </div>
          <div className="flex-1 min-w-[140px] h-2 rounded-full overflow-hidden bg-slate-100 flex">
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
          <div className="tg-tabular flex flex-wrap items-center gap-2 font-mono text-slate-600">
            {phases.map((p) => (
              <span key={p.key} className="inline-flex items-center gap-1">
                <span className={`w-2 h-2 rounded-full ${p.color}`} />
                {p.key} {p.ms.toFixed(1)}ms
              </span>
            ))}
            {timing.reused && (
              <span className="px-1.5 py-0.2 rounded bg-sky-100 text-sky-800 text-[10px]">复用连接</span>
            )}
          </div>
        </div>
      )}

      {/* Scrollable Inspector Content */}
      <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-4 text-xs">
        {loadingDebug ? (
          <div className="py-12 text-center text-slate-500 space-y-2">
            <div className="inline-flex items-center gap-2 px-3 py-1.5 rounded-full bg-sky-50 border border-sky-200 text-sky-800 font-medium">
              <span className="w-2 h-2 rounded-full bg-sky-500 animate-pulse" />
              <span>正在由 Tidegate 引擎发送真实请求...</span>
            </div>
          </div>
        ) : subTab === 'history' ? (
          <div className="space-y-2.5">
            <div className="flex items-center justify-between">
              <span className="font-medium text-slate-700 flex items-center gap-1.5">
                <History className="w-3.5 h-3.5 text-sky-600" /> 项目近期调试记录（点击任意行恢复现场）
              </span>
              {historyList.length > 0 && (
                <button
                  type="button"
                  onClick={() => void onClearHistory()}
                  className="inline-flex items-center gap-1 text-rose-600 hover:text-rose-700"
                >
                  <Trash2 className="w-3.5 h-3.5" /> 清空历史
                </button>
              )}
            </div>
            {historyList.length === 0 ? (
              <p className="text-slate-400 py-8 text-center">暂无调试历史记录。</p>
            ) : (
              <div className="border border-slate-200 rounded-lg bg-white overflow-auto tg-scroller max-h-80">
                <table className="w-full text-left border-collapse">
                  <thead className="bg-slate-50 border-b border-slate-200 text-slate-500 sticky top-0">
                    <tr>
                      <th className="px-3 py-1.5 w-20">方法</th>
                      <th className="px-3 py-1.5">实际 URL</th>
                      <th className="px-3 py-1.5 w-20">状态</th>
                      <th className="px-3 py-1.5 w-24">耗时</th>
                      <th className="px-3 py-1.5 w-32">时间</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100 tg-tabular">
                    {historyList.map((rec) => (
                      <tr
                        key={rec.id}
                        onClick={() => void onLoadHistoryRecord(rec.id)}
                        className="hover:bg-sky-50 cursor-pointer"
                      >
                        <td className="px-3 py-1.5 font-mono font-semibold text-slate-800">{rec.method}</td>
                        <td className="px-3 py-1.5 font-mono text-slate-600 truncate max-w-[300px]" title={rec.url}>
                          {rec.url}
                        </td>
                        <td className="px-3 py-1.5 font-mono">
                          <span
                            className={
                              rec.status >= 200 && rec.status < 300
                                ? 'text-emerald-700 font-semibold'
                                : 'text-rose-700 font-semibold'
                            }
                          >
                            {rec.status || 'ERR'}
                          </span>
                        </td>
                        <td className="px-3 py-1.5 font-mono text-slate-600">{rec.duration_ms.toFixed(1)} ms</td>
                        <td className="px-3 py-1.5 text-slate-400">
                          {new Date(rec.created_at).toLocaleTimeString()}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        ) : !step ? (
          <div className="h-full min-h-[200px] flex flex-col items-center justify-center text-center text-slate-400 space-y-2.5">
            <div className="w-10 h-10 rounded-xl bg-slate-100 border border-slate-200 flex items-center justify-center text-slate-500">
              <Terminal className="w-5 h-5" />
            </div>
            <div className="space-y-1">
              <div className="text-xs font-medium text-slate-600">等待发送调试请求</div>
              <p className="text-[11px] text-slate-400">
                点击 <strong className="text-slate-600">发送调试</strong> 或按{' '}
                <kbd className="px-1.5 py-0.5 border border-slate-300 rounded bg-white font-mono text-[10px]">
                  Ctrl+Enter
                </kbd>{' '}
                实时捕获响应、五段耗时瀑布与提取断言结果
              </p>
            </div>
          </div>
        ) : (
          <>
            {step.error && (
              <div className="mb-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-rose-800">
                <span className="font-semibold">[{step.error_kind || 'error'}]</span> {step.error}
              </div>
            )}

            {subTab === 'body' && (
              <div className="space-y-2.5">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-2 text-slate-500">
                    <span className="font-mono text-[11px]">{step.content_type || 'unknown'}</span>
                    {step.body_truncated && (
                      <span className="px-1.5 py-0.5 rounded bg-amber-100 text-amber-800">响应体超出调试限制已截断</span>
                    )}
                  </div>
                  <div className="flex items-center gap-1.5">
                    {step.body && !step.is_binary && (
                      <button
                        type="button"
                        onClick={async () => {
                          try {
                            await navigator.clipboard.writeText(step.body || '')
                            setCopiedBody(true)
                            window.setTimeout(() => setCopiedBody(false), 1800)
                          } catch {
                            // ignore
                          }
                        }}
                        className="inline-flex items-center gap-1 px-2 py-1 rounded border border-slate-300 bg-white hover:bg-slate-100 text-slate-700"
                      >
                        {copiedBody ? <Check className="w-3 h-3 text-emerald-600" /> : <Copy className="w-3 h-3" />}
                        <span>{copiedBody ? '已复制' : '复制'}</span>
                      </button>
                    )}
                    <button
                      type="button"
                      onClick={() => setRawMode((r) => !r)}
                      className="px-2.5 py-1 rounded border border-slate-300 bg-white hover:bg-slate-100 text-slate-700 font-medium"
                    >
                      {rawMode ? 'JSON 结构化视图' : '原始源码'}
                    </button>
                  </div>
                </div>

                {step.is_binary ? (
                  <div className="p-4 rounded-lg border border-slate-200 bg-white space-y-2">
                    <div className="font-medium text-slate-800">
                      二进制响应体（{formatBytes(step.bytes_received)}）
                    </div>
                    {step.body_base64 && (
                      <a
                        download={`response-${step.step_id || 'body'}`}
                        href={`data:${step.content_type || 'application/octet-stream'};base64,${step.body_base64}`}
                        className="inline-flex items-center gap-1 px-3 py-1.5 rounded bg-sky-600 text-white font-medium"
                      >
                        下载二进制响应内容
                      </a>
                    )}
                  </div>
                ) : rawMode ? (
                  <pre className="p-3.5 rounded-lg bg-[#07192E] text-slate-100 font-mono text-xs overflow-auto tg-scroller-dark max-h-[420px] whitespace-pre-wrap break-all border border-slate-800">
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
                <div className="flex flex-wrap items-center justify-between gap-2 bg-white border border-slate-200 rounded-lg p-3">
                  <label className="inline-flex items-center gap-2 text-slate-700 cursor-pointer font-medium">
                    <input
                      type="checkbox"
                      checked={autoSyncTempVars}
                      onChange={(e) => onToggleAutoSync(e.target.checked)}
                      className="rounded border-slate-300 text-sky-600"
                    />
                    <Zap className="w-3.5 h-3.5 text-amber-500 shrink-0" />
                    <span>调试提取成功时自动同步写入顶部临时变量池</span>
                  </label>
                  {step.extracted_vars && Object.keys(step.extracted_vars).length > 0 && (
                    <button
                      type="button"
                      onClick={() => onPushTempVars(step.extracted_vars!)}
                      className="inline-flex items-center gap-1 px-2.5 py-1 rounded bg-amber-50 border border-amber-300 text-amber-800 hover:bg-amber-100"
                    >
                      <Zap className="w-3.5 h-3.5" /> 立即同步本次提取变量（{Object.keys(step.extracted_vars).length}）
                    </button>
                  )}
                </div>

                <div>
                  <h4 className="font-semibold text-slate-700 mb-1.5">变量提取结果（全量非短路执行）</h4>
                  {(step.extracts?.length || 0) === 0 ? (
                    <p className="text-slate-400">当前请求未配置提取规则。</p>
                  ) : (
                    <div className="border border-slate-200 rounded-lg bg-white overflow-hidden">
                      <table className="w-full text-left border-collapse">
                        <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
                          <tr>
                            <th className="px-3 py-1.5">变量名</th>
                            <th className="px-3 py-1.5">来源 / 表达式</th>
                            <th className="px-3 py-1.5">状态</th>
                            <th className="px-3 py-1.5">提取值</th>
                          </tr>
                        </thead>
                        <tbody className="divide-y divide-slate-100">
                          {step.extracts!.map((ex, i) => (
                            <tr key={i}>
                              <td className="px-3 py-1.5 font-mono font-semibold text-sky-800">{ex.name}</td>
                              <td className="px-3 py-1.5 font-mono text-slate-600">
                                [{ex.from}] {ex.expr}
                              </td>
                              <td className="px-3 py-1.5">
                                {ex.matched ? (
                                  <span className="text-emerald-700 font-semibold">命中</span>
                                ) : (
                                  <span className="text-rose-700 font-semibold">未命中 {ex.error}</span>
                                )}
                              </td>
                              <td className="px-3 py-1.5 font-mono text-slate-800 break-all">{ex.value || '-'}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </div>

                <div>
                  <h4 className="font-semibold text-slate-700 mb-1.5">响应断言结果（全量非短路执行）</h4>
                  {(step.asserts?.length || 0) === 0 ? (
                    <p className="text-slate-400">当前请求未配置断言（默认以 HTTP 2xx/3xx 且无网络错误为通过）。</p>
                  ) : (
                    <div className="border border-slate-200 rounded-lg bg-white overflow-hidden">
                      <table className="w-full text-left border-collapse">
                        <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
                          <tr>
                            <th className="px-3 py-1.5 w-20">结果</th>
                            <th className="px-3 py-1.5 w-28">类型</th>
                            <th className="px-3 py-1.5">预期描述</th>
                            <th className="px-3 py-1.5">失败明细</th>
                          </tr>
                        </thead>
                        <tbody className="divide-y divide-slate-100">
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
                              <td className="px-3 py-1.5 font-mono text-slate-600">{as.type}</td>
                              <td className="px-3 py-1.5 text-slate-800">{as.description || as.desc}</td>
                              <td className="px-3 py-1.5 text-rose-700">{as.message || '-'}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                </div>
              </div>
            )}

            {subTab === 'actual' && (
              <div className="space-y-3">
                <div className="p-3 rounded-lg border border-slate-200 bg-white space-y-2">
                  <div className="flex items-center gap-2 font-mono font-semibold text-slate-900">
                    <Send className="w-3.5 h-3.5 text-sky-600 shrink-0" />
                    <span>{step.actual_request.method}</span>
                    <span className="break-all text-sky-800">{step.actual_request.url}</span>
                  </div>
                  <div className="border-t border-slate-100 pt-2">
                    <div className="text-slate-500 font-medium mb-1">实际请求头（变量与继承头均已展开）</div>
                    <div className="space-y-0.5 font-mono">
                      {(step.actual_request.headers || []).map((h, i) => (
                        <div key={i} className="flex gap-2">
                          <span className="text-slate-500 shrink-0">{h.name}:</span>
                          <span className="text-slate-800 break-all">{h.value}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                  {step.actual_request.body && (
                    <div className="border-t border-slate-100 pt-2">
                      <div className="text-slate-500 font-medium mb-1">实际请求体</div>
                      <pre className="p-2.5 rounded bg-[#07192E] text-slate-100 font-mono overflow-auto tg-scroller-dark max-h-48 whitespace-pre-wrap break-all">
                        {step.actual_request.body}
                      </pre>
                    </div>
                  )}
                </div>

                {(step.redirects?.length || 0) > 0 && (
                  <div className="p-3 rounded-lg border border-slate-200 bg-white">
                    <div className="font-semibold text-slate-700 mb-1.5">重定向路径（{step.redirects!.length} 跳）</div>
                    <div className="space-y-1 font-mono text-[11px] tg-tabular">
                      {step.redirects!.map((hop, i) => (
                        <div key={i} className="flex items-center gap-2 text-slate-600">
                          <span className="px-1.5 py-0.5 rounded bg-slate-100">{hop.status}</span>
                          <span>{hop.method}</span>
                          <span className="truncate">{hop.url}</span>
                          {hop.location && <span className="text-sky-700">-&gt; {hop.location}</span>}
                          <span className="ml-auto shrink-0">{hop.duration_ms.toFixed(1)}ms</span>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            )}

            {subTab === 'headers' && (
              <div className="border border-slate-200 rounded-lg bg-white overflow-auto tg-scroller max-h-80">
                <table className="w-full text-left border-collapse font-mono">
                  <thead className="bg-slate-50 border-b border-slate-200 text-slate-500 sticky top-0">
                    <tr>
                      <th className="px-3 py-1.5 w-1/3">Header 名称</th>
                      <th className="px-3 py-1.5">值</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100">
                    {(step.response_headers || []).map((h, i) => (
                      <tr key={i}>
                        <td className="px-3 py-1.5 text-slate-600 font-semibold">{h.name}</td>
                        <td className="px-3 py-1.5 text-slate-900 break-all">{h.value}</td>
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
