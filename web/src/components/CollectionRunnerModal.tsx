import { useEffect, useMemo, useRef, useState } from 'react'
import { Check, CheckCircle2, ChevronDown, ChevronRight, Copy, Play, SkipForward, Terminal, X, XCircle } from 'lucide-react'
import { api, APIError } from '../lib/api'
import { copyText } from '../lib/download'
import type { CollectionTestResult, Environment, TreeNode } from '../lib/types'
import { useNativeDialog } from '../lib/useNativeDialog'

interface Props {
  open: boolean
  projectId: string
  treeNodes: TreeNode[]
  environments: Environment[]
  initialFolderId?: string
  initialEnvId: string
  tempVars: Record<string, string>
  canWrite: boolean
  onClose: () => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

interface FolderChoice {
  id: string
  label: string
}

function collectFolderChoices(nodes: TreeNode[], prefix = ''): FolderChoice[] {
  const out: FolderChoice[] = []
  for (const n of nodes) {
    if (n.type !== 'folder') continue
    const path = prefix ? `${prefix} / ${n.name}` : n.name
    out.push({ id: n.id, label: path })
    if (n.children && n.children.length > 0) {
      out.push(...collectFolderChoices(n.children, path))
    }
  }
  return out
}

export function CollectionRunnerModal({
  open,
  projectId,
  treeNodes,
  environments,
  initialFolderId = '',
  initialEnvId,
  tempVars,
  canWrite,
  onClose,
  onToast,
}: Props) {
  const dialogRef = useNativeDialog(open, onClose)
  const [folderId, setFolderId] = useState(initialFolderId)
  const [envId, setEnvId] = useState(initialEnvId)
  const [stopOnError, setStopOnError] = useState(false)
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<CollectionTestResult | null>(null)
  const [expandedStepId, setExpandedStepId] = useState('')
  const [copied, setCopied] = useState(false)
  const copiedTimer = useRef<number | undefined>(undefined)

  const folderChoices = useMemo(() => collectFolderChoices(treeNodes), [treeNodes])

  useEffect(() => {
    if (open) {
      setFolderId(initialFolderId || '')
      setEnvId(initialEnvId || '')
    }
  }, [open, initialFolderId, initialEnvId])

  useEffect(() => () => window.clearTimeout(copiedTimer.current), [])

  const cliCmd = useMemo(() => {
    const parts = [`tidegate test ./data --project ${projectId}`]
    if (folderId) parts.push(`--folder ${folderId}`)
    if (envId) parts.push(`--env ${envId}`)
    if (stopOnError) parts.push('--stop-on-error')
    parts.push('--junit junit.xml')
    return parts.join(' ')
  }, [projectId, folderId, envId, stopOnError])

  const handleRun = async () => {
    if (!canWrite) return
    setRunning(true)
    try {
      const res = await api.runCollectionTest(projectId, {
        folder_id: folderId || undefined,
        environment_id: envId || undefined,
        variables: Object.keys(tempVars).length > 0 ? tempVars : undefined,
        stop_on_error: stopOnError,
      })
      setResult(res)
      const firstFail = res.items.find((x) => !x.passed && !x.skipped)
      setExpandedStepId(firstFail ? firstFail.step_id : '')
      if (res.passed) {
        onToast('success', `批量测试全部通过（${res.passed_count}/${res.total}）`)
      } else {
        onToast('error', `批量测试完成：${res.passed_count} 通过 / ${res.failed_count} 失败`)
      }
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '执行批量回归测试失败')
    } finally {
      setRunning(false)
    }
  }


  const handleCopy = async () => {
    const ok = await copyText(cliCmd)
    if (!ok) {
      onToast('error', '复制失败，请手动选择命令')
      return
    }
    setCopied(true)
    window.clearTimeout(copiedTimer.current)
    copiedTimer.current = window.setTimeout(() => setCopied(false), 1500)
  }

  return (
    <dialog
      ref={dialogRef}
      {...({ closedby: 'any' } as Record<string, string>)}
      aria-labelledby="tg-collection-runner-title"
      className="tg-dialog w-[min(56rem,calc(100vw-2rem))] max-h-[86dvh]"
    >
      <header className="px-4 py-3 border-b border-line flex items-center justify-between gap-2 bg-surface-subtle shrink-0">
        <h2 id="tg-collection-runner-title" className="text-sm font-semibold text-fg">
          批量测试
        </h2>
        <button
          type="button"
          onClick={onClose}
          aria-label="关闭批量测试"
          className="p-1 text-fg-subtle hover:text-fg rounded hover:bg-surface-hover"
        >
          <X className="w-4 h-4" />
        </button>
      </header>

      <div className="flex-1 min-h-0 p-4 space-y-4 overflow-y-auto tg-scroller text-xs">
        <div className="grid grid-cols-1 md:grid-cols-12 gap-3 items-end bg-surface-subtle border border-line rounded-lg p-3">
          <div className="md:col-span-4">
            <label htmlFor="tg-runner-scope" className="block text-fg-muted mb-1 font-medium">
              执行范围
            </label>
            <select
              id="tg-runner-scope"
              name="runner-scope"
              value={folderId}
              onChange={(e) => setFolderId(e.target.value)}
              className="w-full px-2.5 py-1.5 rounded border border-line-strong bg-surface text-fg"
            >
              <option value="">整个项目</option>
              {folderChoices.map((fc) => (
                <option key={fc.id} value={fc.id}>
                  目录：{fc.label}
                </option>
              ))}
            </select>
          </div>
          <div className="md:col-span-3">
            <label htmlFor="tg-runner-env" className="block text-fg-muted mb-1 font-medium">
              目标环境
            </label>
            <select
              id="tg-runner-env"
              name="runner-env"
              value={envId}
              onChange={(e) => setEnvId(e.target.value)}
              className="w-full px-2.5 py-1.5 rounded border border-line-strong bg-surface text-fg"
            >
              <option value="">不使用环境</option>
              {environments.map((ev) => (
                <option key={ev.id} value={ev.id}>
                  {ev.name}
                </option>
              ))}
            </select>
          </div>
          <div className="md:col-span-3 flex items-center pb-1.5">
            <label className="inline-flex items-center gap-2 text-fg cursor-pointer">
              <input
                type="checkbox"
                name="runner-stop-on-error"
                checked={stopOnError}
                onChange={(e) => setStopOnError(e.target.checked)}
                className="rounded border-line-strong text-sky-600"
              />
              <span>遇错即停</span>
            </label>
          </div>
          <div className="md:col-span-2 flex justify-end">
            <button
              type="button"
              disabled={!canWrite || running}
              onClick={() => void handleRun()}
              className="w-full inline-flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-lg bg-primary hover:bg-primary-hover text-white font-semibold disabled:opacity-45"
            >
              <Play className="w-3.5 h-3.5 fill-current" />
              {running ? '执行中…' : '开始运行'}
            </button>
          </div>
        </div>

        <div className="flex items-center justify-between gap-2 px-3 py-2 rounded-lg tg-code-surface font-mono text-[11px]">
          <div className="flex items-center gap-2 min-w-0">
            <Terminal className="w-3.5 h-3.5 text-cyan-400 shrink-0" />
            <span className="truncate text-slate-100" title={cliCmd}>
              {cliCmd}
            </span>
          </div>
          <button
            type="button"
            onClick={() => void handleCopy()}
            aria-label={copied ? '已复制命令' : '复制命令'}
            className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-white/10 hover:bg-white/20 text-slate-200 shrink-0"
          >
            {copied ? <Check className="w-3 h-3 text-emerald-400" /> : <Copy className="w-3 h-3" />}
            {copied ? '已复制' : '复制命令'}
          </button>
        </div>

        {result && (
          <div className="space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2 p-3 rounded-lg border border-line bg-surface">
              <div className="flex items-center gap-3 min-w-0">
                <span
                  className={`px-2 py-0.5 rounded font-semibold shrink-0 ${
                    result.passed ? 'bg-emerald-100 text-emerald-800' : 'bg-rose-100 text-rose-800'
                  }`}
                >
                  {result.passed ? '全部通过' : '存在失败'}
                </span>
                <span className="text-fg-muted truncate">
                  范围：<strong className="text-fg">{result.scope_name}</strong>
                </span>
              </div>
              <div className="flex items-center gap-4 font-mono text-[11px] tg-tabular">
                <span className="text-fg">总计 {result.total}</span>
                <span className="text-emerald-700">通过 {result.passed_count}</span>
                <span className="text-rose-700">失败 {result.failed_count}</span>
                {result.skipped_count > 0 && <span className="text-amber-700">跳过 {result.skipped_count}</span>}
                <span className="text-fg-subtle">{result.duration_ms.toFixed(1)} ms</span>
              </div>
            </div>

            {result.total > 0 && (
              <div className="h-2 rounded-full bg-slate-100 overflow-hidden flex">
                <div
                  className="bg-emerald-500 h-full"
                  style={{ width: `${(result.passed_count / result.total) * 100}%` }}
                />
                <div
                  className="bg-rose-500 h-full"
                  style={{ width: `${(result.failed_count / result.total) * 100}%` }}
                />
                <div
                  className="bg-amber-400 h-full"
                  style={{ width: `${(result.skipped_count / result.total) * 100}%` }}
                />
              </div>
            )}

            <div className="border border-line rounded-lg bg-surface overflow-hidden divide-y divide-line">
              {result.items.map((item) => {
                const expanded = expandedStepId === item.step_id
                return (
                  <div key={item.step_id}>
                    <button
                      type="button"
                      aria-expanded={expanded}
                      onClick={() => setExpandedStepId(expanded ? '' : item.step_id)}
                      className="w-full px-3 py-2 flex items-center justify-between gap-3 text-left hover:bg-surface-hover"
                    >
                      <div className="flex items-center gap-2 min-w-0">
                        {expanded ? (
                          <ChevronDown className="w-3.5 h-3.5 text-fg-subtle shrink-0" />
                        ) : (
                          <ChevronRight className="w-3.5 h-3.5 text-fg-subtle shrink-0" />
                        )}
                        {item.skipped ? (
                          <SkipForward className="w-4 h-4 text-amber-500 shrink-0" />
                        ) : item.passed ? (
                          <CheckCircle2 className="w-4 h-4 text-emerald-600 shrink-0" />
                        ) : (
                          <XCircle className="w-4 h-4 text-rose-600 shrink-0" />
                        )}
                        <span className="px-1.5 py-px rounded bg-slate-100 text-slate-700 font-mono text-[10px] font-bold shrink-0">
                          {item.method}
                        </span>
                        <span className="font-medium text-fg truncate">
                          {item.folder_path ? `${item.folder_path} / ` : ''}
                          {item.api_name}
                        </span>
                      </div>
                      <div className="flex items-center gap-3 font-mono text-[11px] shrink-0 tg-tabular">
                        {item.assert_total > 0 && (
                          <span className="text-fg-subtle">
                            断言 {item.assert_passed}/{item.assert_total}
                          </span>
                        )}
                        <span className={(item.status ?? 0) >= 400 ? 'text-rose-700 font-semibold' : 'text-fg-muted'}>
                          {item.skipped ? 'SKIP' : `HTTP ${item.status || 0}`}
                        </span>
                        <span className="text-fg-subtle w-16 text-right">{item.duration_ms.toFixed(1)} ms</span>
                      </div>
                    </button>

                    {expanded && !item.skipped && (
                      <div className="px-4 py-2.5 bg-surface-subtle border-t border-line space-y-2 text-[11px]">
                        <div className="font-mono text-fg-muted break-all">
                          {item.step.actual_request?.method} {item.step.actual_request?.url || '-'}
                        </div>
                        {item.error && (
                          <div className="px-2.5 py-1.5 rounded bg-rose-50 border border-rose-200 text-rose-800">
                            错误：{item.error}
                          </div>
                        )}
                        {item.step.asserts && item.step.asserts.length > 0 && (
                          <div className="space-y-1">
                            <div className="font-semibold text-fg">断言明细</div>
                            {item.step.asserts.map((as, idx) => (
                              <div key={idx} className="flex items-center justify-between gap-2 font-mono">
                                <span className={as.passed ? 'text-emerald-700' : 'text-rose-700'}>
                                  {as.passed ? 'PASS' : 'FAIL'} · [{as.type}] {as.desc || as.message}
                                </span>
                                {!as.passed && <span className="text-rose-600">{as.message}</span>}
                              </div>
                            ))}
                          </div>
                        )}
                        {item.step.extracted_vars && Object.keys(item.step.extracted_vars).length > 0 && (
                          <div className="space-y-1">
                            <div className="font-semibold text-fg">写入/提取变量</div>
                            <div className="flex flex-wrap gap-1.5 font-mono">
                              {Object.entries(item.step.extracted_vars).map(([k, v]) => (
                                <span key={k} className="px-2 py-0.5 rounded bg-surface border border-line text-fg">
                                  {k}={v}
                                </span>
                              ))}
                            </div>
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          </div>
        )}
      </div>
    </dialog>
  )
}
