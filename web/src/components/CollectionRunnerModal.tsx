import { useEffect, useMemo, useRef, useState } from 'react'
import { CheckCircle2, ChevronDown, ChevronRight, Copy, Play, SkipForward, Terminal, X, XCircle } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { CollectionTestResult, Environment, TreeNode } from '../lib/types'

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
  const dialogRef = useRef<HTMLDialogElement>(null)
  const [folderId, setFolderId] = useState(initialFolderId)
  const [envId, setEnvId] = useState(initialEnvId)
  const [stopOnError, setStopOnError] = useState(false)
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<CollectionTestResult | null>(null)
  const [expandedStepId, setExpandedStepId] = useState('')

  const folderChoices = useMemo(() => collectFolderChoices(treeNodes), [treeNodes])

  useEffect(() => {
    if (open) {
      setFolderId(initialFolderId || '')
      setEnvId(initialEnvId || '')
    }
  }, [open, initialFolderId, initialEnvId])

  useEffect(() => {
    const dlg = dialogRef.current
    if (!dlg) return
    if (open) {
      if (!dlg.open) dlg.showModal()
    } else if (dlg.open) {
      dlg.close()
    }
    const onCloseEv = () => {
      if (open) onClose()
    }
    dlg.addEventListener('close', onCloseEv)

    // Light-dismiss fallback for browsers without HTMLDialogElement closedby="any" (e.g. Safari).
    let pressedOutside = false
    const outside = (e: PointerEvent) => {
      const r = dlg.getBoundingClientRect()
      return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom
    }
    const onPointerDown = (e: PointerEvent) => {
      pressedOutside = outside(e)
    }
    const onPointerUp = (e: PointerEvent) => {
      if (!('closedBy' in HTMLDialogElement.prototype) && pressedOutside && outside(e)) {
        dlg.close()
      }
      pressedOutside = false
    }
    dlg.addEventListener('pointerdown', onPointerDown)
    dlg.addEventListener('pointerup', onPointerUp)
    return () => {
      dlg.removeEventListener('close', onCloseEv)
      dlg.removeEventListener('pointerdown', onPointerDown)
      dlg.removeEventListener('pointerup', onPointerUp)
    }
  }, [open, onClose])

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

  return (
    <dialog
      ref={dialogRef}
      {...({ closedby: 'any' } as Record<string, string>)}
      aria-labelledby="tg-collection-runner-title"
      className="tg-dialog w-full max-w-4xl overflow-hidden"
    >
      <div className="px-4 py-3 border-b border-slate-200 flex items-center justify-between gap-2 bg-slate-50">
        <div>
          <h3 id="tg-collection-runner-title" className="text-sm font-semibold text-slate-900">
            集合 / 目录批量功能回归测试
          </h3>
          <p className="text-[11px] text-slate-500">
            按目录树深度优先顺序逐个调试执行接口，自动传递提取变量与前后置脚本状态，支持 CI JUnit 集成
          </p>
        </div>
        <button
          type="button"
          onClick={onClose}
          aria-label="关闭批量测试窗口"
          className="p-1 text-slate-400 hover:text-slate-700 rounded"
        >
          <X className="w-4 h-4" />
        </button>
      </div>

      <div className="p-4 space-y-4 max-h-[78vh] overflow-y-auto tg-scroller text-xs">
        <div className="grid grid-cols-1 md:grid-cols-12 gap-3 items-end bg-slate-50 border border-slate-200 rounded-lg p-3">
          <div className="md:col-span-4">
            <label className="block text-slate-600 mb-1 font-medium">执行范围</label>
            <select
              value={folderId}
              onChange={(e) => setFolderId(e.target.value)}
              className="w-full px-2.5 py-1.5 rounded border border-slate-300 bg-white"
            >
              <option value="">整个项目（全部目录与接口）</option>
              {folderChoices.map((fc) => (
                <option key={fc.id} value={fc.id}>
                  目录：{fc.label}
                </option>
              ))}
            </select>
          </div>
          <div className="md:col-span-3">
            <label className="block text-slate-600 mb-1 font-medium">目标环境</label>
            <select
              value={envId}
              onChange={(e) => setEnvId(e.target.value)}
              className="w-full px-2.5 py-1.5 rounded border border-slate-300 bg-white"
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
            <label className="inline-flex items-center gap-2 text-slate-700 cursor-pointer">
              <input
                type="checkbox"
                checked={stopOnError}
                onChange={(e) => setStopOnError(e.target.checked)}
                className="rounded border-slate-300 text-sky-600"
              />
              <span>遇到首个失败即终止</span>
            </label>
          </div>
          <div className="md:col-span-2 flex justify-end">
            <button
              type="button"
              disabled={!canWrite || running}
              onClick={() => void handleRun()}
              className="w-full inline-flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-lg bg-[#0B2A4A] hover:bg-[#123d69] text-white font-semibold disabled:opacity-45"
            >
              <Play className="w-3.5 h-3.5 fill-current" />
              {running ? '执行中...' : '开始运行'}
            </button>
          </div>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-2 px-3 py-2 rounded-lg bg-slate-900 text-slate-100 font-mono text-[11px]">
          <div className="flex items-center gap-2 min-w-0 truncate">
            <Terminal className="w-3.5 h-3.5 text-cyan-400 shrink-0" />
            <span className="truncate">{cliCmd}</span>
          </div>
          <button
            type="button"
            onClick={() => {
              void navigator.clipboard.writeText(cliCmd)
              onToast('info', '已复制 CLI JUnit 执行命令')
            }}
            className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-slate-800 hover:bg-slate-700 text-slate-200 shrink-0"
          >
            <Copy className="w-3 h-3" /> 复制命令
          </button>
        </div>

        {result && (
          <div className="space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2 p-3 rounded-lg border border-slate-200 bg-white">
              <div className="flex items-center gap-3">
                <span
                  className={`px-2 py-0.5 rounded font-semibold ${
                    result.passed ? 'bg-emerald-100 text-emerald-800' : 'bg-rose-100 text-rose-800'
                  }`}
                >
                  {result.passed ? '全部通过' : '存在失败用例'}
                </span>
                <span className="text-slate-600">
                  范围：<strong>{result.scope_name}</strong>
                </span>
              </div>
              <div className="flex items-center gap-4 font-mono text-[11px] tg-tabular">
                <span>总计 {result.total}</span>
                <span className="text-emerald-700">通过 {result.passed_count}</span>
                <span className="text-rose-700">失败 {result.failed_count}</span>
                {result.skipped_count > 0 && <span className="text-amber-700">跳过 {result.skipped_count}</span>}
                <span className="text-slate-500">{result.duration_ms.toFixed(1)} ms</span>
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

            <div className="border border-slate-200 rounded-lg bg-white overflow-hidden divide-y divide-slate-100">
              {result.items.map((item) => {
                const expanded = expandedStepId === item.step_id
                return (
                  <div key={item.step_id}>
                    <button
                      type="button"
                      onClick={() => setExpandedStepId(expanded ? '' : item.step_id)}
                      className="w-full px-3 py-2 flex items-center justify-between gap-3 text-left hover:bg-slate-50"
                    >
                      <div className="flex items-center gap-2 min-w-0">
                        {expanded ? (
                          <ChevronDown className="w-3.5 h-3.5 text-slate-400 shrink-0" />
                        ) : (
                          <ChevronRight className="w-3.5 h-3.5 text-slate-400 shrink-0" />
                        )}
                        {item.skipped ? (
                          <SkipForward className="w-4 h-4 text-amber-500 shrink-0" />
                        ) : item.passed ? (
                          <CheckCircle2 className="w-4 h-4 text-emerald-600 shrink-0" />
                        ) : (
                          <XCircle className="w-4 h-4 text-rose-600 shrink-0" />
                        )}
                        <span className="px-1.5 py-0.2 rounded bg-slate-100 text-slate-700 font-mono text-[10px] font-bold shrink-0">
                          {item.method}
                        </span>
                        <span className="font-medium text-slate-900 truncate">
                          {item.folder_path ? `${item.folder_path} / ` : ''}
                          {item.api_name}
                        </span>
                      </div>
                      <div className="flex items-center gap-3 font-mono text-[11px] shrink-0 tg-tabular">
                        {item.assert_total > 0 && (
                          <span className="text-slate-500">
                            断言 {item.assert_passed}/{item.assert_total}
                          </span>
                        )}
                        <span className={(item.status ?? 0) >= 400 ? 'text-rose-700 font-semibold' : 'text-slate-700'}>
                          {item.skipped ? 'SKIP' : `HTTP ${item.status || 0}`}
                        </span>
                        <span className="text-slate-500 w-16 text-right">{item.duration_ms.toFixed(1)} ms</span>
                      </div>
                    </button>

                    {expanded && !item.skipped && (
                      <div className="px-4 py-2.5 bg-slate-50 border-t border-slate-100 space-y-2 text-[11px]">
                        <div className="font-mono text-slate-600 break-all">
                          {item.step.actual_request?.method} {item.step.actual_request?.url || '-'}
                        </div>
                        {item.error && (
                          <div className="px-2.5 py-1.5 rounded bg-rose-50 border border-rose-200 text-rose-800">
                            错误：{item.error}
                          </div>
                        )}
                        {item.step.asserts && item.step.asserts.length > 0 && (
                          <div className="space-y-1">
                            <div className="font-semibold text-slate-700">断言明细</div>
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
                            <div className="font-semibold text-slate-700">写入/提取变量</div>
                            <div className="flex flex-wrap gap-1.5 font-mono">
                              {Object.entries(item.step.extracted_vars).map(([k, v]) => (
                                <span key={k} className="px-2 py-0.5 rounded bg-white border border-slate-200 text-slate-700">
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
