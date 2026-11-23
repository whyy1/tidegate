import { useState, type FormEvent } from 'react'
import { AlertTriangle, CheckCircle2, ChevronRight, FileUp, Layers, RefreshCw, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { PreviewResult, TreeNode } from '../lib/types'
import { useNativeDialog } from '../lib/useNativeDialog'

interface Props {
  open: boolean
  projectId: string
  treeNodes: TreeNode[]
  onClose: () => void
  onApplied: () => Promise<void>
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

function collectFolders(nodes: TreeNode[], prefix = ''): Array<{ id: string; label: string }> {
  const out: Array<{ id: string; label: string }> = []
  for (const n of nodes) {
    if (n.type === 'folder') {
      const label = prefix ? `${prefix} / ${n.name}` : n.name
      out.push({ id: n.id, label })
      if (n.children) {
        out.push(...collectFolders(n.children, label))
      }
    }
  }
  return out
}

const FORM_ID = 'tg-import-form'
const selectCls = 'px-2 py-1 border border-line-strong rounded bg-surface text-fg focus:border-sky-500 focus:outline-none'

export function ImportModal({ open, projectId, treeNodes, onClose, onApplied, onToast }: Props) {
  const ref = useNativeDialog(open, onClose)
  const [sourceMode, setSourceMode] = useState<'file' | 'text'>('text')
  const [format, setFormat] = useState('auto')
  const [strategy, setStrategy] = useState<'merge' | 'overwrite' | 'skip'>('merge')
  const [targetFolderId, setTargetFolderId] = useState('')
  const [textContent, setTextContent] = useState('')
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<PreviewResult | null>(null)
  const [busy, setBusy] = useState(false)

  const folders = open ? collectFolders(treeNodes) : []

  const buildPayload = (): FormData | { format: string; strategy: 'merge' | 'overwrite' | 'skip'; target_folder_id: string; content: string } => {
    if (sourceMode === 'file' && selectedFile) {
      const fd = new FormData()
      fd.set('format', format)
      fd.set('strategy', strategy)
      if (targetFolderId) fd.set('target_folder_id', targetFolderId)
      fd.set('file', selectedFile)
      return fd
    }
    return {
      format,
      strategy,
      target_folder_id: targetFolderId,
      content: textContent,
    }
  }

  const handlePreview = async (e: FormEvent) => {
    e.preventDefault()
    if (sourceMode === 'file' && !selectedFile) {
      onToast('error', '请先选择要导入的文件')
      return
    }
    if (sourceMode === 'text' && !textContent.trim()) {
      onToast('error', '请先粘贴 OpenAPI / Postman / Apifox / JMeter / HAR / cURL 文本内容')
      return
    }
    setBusy(true)
    try {
      const res = await api.importPreview(projectId, buildPayload())
      setPreview(res)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '解析导入内容失败')
    } finally {
      setBusy(false)
    }
  }

  const handleApply = async () => {
    setBusy(true)
    try {
      const res = await api.importApply(projectId, buildPayload())
      await onApplied()
      const r = res.report
      onToast(
        'success',
        `导入完成：新增 ${r.apis_created || 0}、保留 ULID 更新 ${r.apis_updated || 0}、跳过 ${r.apis_skipped || 0} 个接口`
      )
      setPreview(null)
      onClose()
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '确认导入失败')
    } finally {
      setBusy(false)
    }
  }

  const stepCls = (active: boolean) =>
    `px-2 py-0.5 rounded-full border ${active ? 'bg-sky-50 border-sky-200 text-sky-800' : 'bg-surface border-line text-fg-subtle'}`

  return (
    <dialog
      ref={ref}
      {...({ closedby: 'any' } as Record<string, string>)}
      aria-labelledby="tg-import-title"
      className="tg-dialog w-[min(48rem,calc(100vw-2rem))] max-h-[86dvh]"
    >
      {open && (
        <>
          <header className="px-5 py-3 border-b border-line flex items-center justify-between gap-3 bg-surface-subtle shrink-0">
            <h2 id="tg-import-title" className="text-sm font-semibold text-fg">
              导入
            </h2>
            <div className="flex items-center gap-3">
              <div className="hidden sm:flex items-center gap-1 text-[11px] font-medium">
                <span className={stepCls(!preview)}>1. 选择来源</span>
                <ChevronRight aria-hidden="true" className="w-3.5 h-3.5 text-fg-subtle" />
                <span className={stepCls(Boolean(preview))}>2. 差异确认</span>
              </div>
              <button
                type="button"
                onClick={onClose}
                aria-label="关闭导入"
                className="p-1 text-fg-subtle hover:text-fg rounded hover:bg-surface-hover"
              >
                <X className="w-4 h-4" />
              </button>
            </div>
          </header>

          <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 space-y-4 text-xs">
            <form id={FORM_ID} onSubmit={handlePreview} className="space-y-3">
              <div className="flex flex-wrap items-center gap-3">
                <div className="inline-flex rounded-md border border-line p-0.5 bg-surface-subtle">
                  <button
                    type="button"
                    aria-pressed={sourceMode === 'text'}
                    onClick={() => {
                      setSourceMode('text')
                      setPreview(null)
                    }}
                    className={`px-3 py-1 rounded font-medium ${
                      sourceMode === 'text' ? 'bg-surface shadow-xs text-sky-700' : 'text-fg-muted hover:text-fg'
                    }`}
                  >
                    粘贴文本 / cURL
                  </button>
                  <button
                    type="button"
                    aria-pressed={sourceMode === 'file'}
                    onClick={() => {
                      setSourceMode('file')
                      setPreview(null)
                    }}
                    className={`px-3 py-1 rounded font-medium ${
                      sourceMode === 'file' ? 'bg-surface shadow-xs text-sky-700' : 'text-fg-muted hover:text-fg'
                    }`}
                  >
                    上传文件
                  </button>
                </div>

                <label className="flex items-center gap-1.5">
                  <span className="text-fg-muted">格式</span>
                  <select
                    name="import-format"
                    value={format}
                    onChange={(e) => {
                      setFormat(e.target.value)
                      setPreview(null)
                    }}
                    className={selectCls}
                  >
                    <option value="auto">自动检测</option>
                    <option value="openapi3">OpenAPI 3.0 / 3.1</option>
                    <option value="swagger2">Swagger 2.0</option>
                    <option value="postman">Postman v2.1 / 环境</option>
                    <option value="apifox">Apifox 项目导出 (.json)</option>
                    <option value="jmeter">JMeter 测试计划 (.jmx)</option>
                    <option value="har">HAR 1.2</option>
                    <option value="curl">cURL 命令</option>
                    <option value="tidegate">.tidegate 压缩包</option>
                  </select>
                </label>

                <label className="flex items-center gap-1.5">
                  <span className="text-fg-muted">冲突策略</span>
                  <select
                    name="import-strategy"
                    value={strategy}
                    onChange={(e) => {
                      setStrategy(e.target.value as 'merge' | 'overwrite' | 'skip')
                      setPreview(null)
                    }}
                    className={selectCls}
                  >
                    <option value="merge">智能合并（保留提取器/断言与 ULID）</option>
                    <option value="overwrite">覆盖匹配接口（保留 ULID）</option>
                    <option value="skip">跳过已存在接口</option>
                  </select>
                </label>

                <label className="flex items-center gap-1.5 min-w-0">
                  <span className="text-fg-muted shrink-0">目标目录</span>
                  <select
                    name="import-target-folder"
                    value={targetFolderId}
                    onChange={(e) => {
                      setTargetFolderId(e.target.value)
                      setPreview(null)
                    }}
                    className={`${selectCls} max-w-[160px]`}
                  >
                    <option value="">项目根目录</option>
                    {folders.map((f) => (
                      <option key={f.id} value={f.id}>
                        {f.label}
                      </option>
                    ))}
                  </select>
                </label>
              </div>

              {sourceMode === 'text' ? (
                <textarea
                  name="import-content"
                  aria-label="导入内容"
                  rows={7}
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={textContent}
                  onChange={(e) => {
                    setTextContent(e.target.value)
                    setPreview(null)
                  }}
                  placeholder="粘贴 OpenAPI JSON/YAML、Swagger 2.0、Postman v2.1、Apifox JSON、JMeter XML (.jmx)、HAR JSON 或 curl 命令…"
                  className="w-full px-3 py-2 border border-line-strong rounded-lg bg-surface text-fg font-mono text-xs focus:border-sky-500 focus:outline-none"
                />
              ) : (
                <label className="flex flex-col items-center justify-center gap-2 border-2 border-dashed border-line-strong hover:border-sky-400 rounded-lg p-6 cursor-pointer bg-surface-subtle">
                  <FileUp className="w-5 h-5 text-sky-600" />
                  <span className="text-fg font-medium">
                    {selectedFile
                      ? `${selectedFile.name} (${Math.round(selectedFile.size / 1024)} KB)`
                      : '选择文件 (.yaml, .json, .jmx, .har, .tidegate)'}
                  </span>
                  <input
                    type="file"
                    accept=".yaml,.yml,.json,.jmx,.xml,.har,.tidegate,.zip,.txt"
                    onChange={(e) => {
                      setSelectedFile(e.target.files?.[0] || null)
                      setPreview(null)
                    }}
                    className="hidden"
                  />
                </label>
              )}
            </form>

            {preview && (
              <section aria-label="差异预览" className="rounded-lg border border-sky-200 bg-sky-50/40 p-4 space-y-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-2 font-semibold text-fg">
                    <Layers className="w-4 h-4 text-sky-600" />
                    <span>
                      识别格式：<code className="font-mono text-sky-700">{preview.report.format}</code>
                      {preview.report.detected_version ? ` (${preview.report.detected_version})` : ''}
                    </span>
                  </div>
                  <div className="flex flex-wrap gap-2 text-[11px] text-fg-muted tg-tabular">
                    <span>接口：{preview.report.apis_found}</span>
                    {(preview.report.folders_found || 0) > 0 && <span>目录：{preview.report.folders_found}</span>}
                    {(preview.report.envs_found || 0) > 0 && <span>环境：{preview.report.envs_found}</span>}
                    {(preview.report.scenarios_found || 0) > 0 && <span>场景：{preview.report.scenarios_found}</span>}
                    {(preview.report.files_found || 0) > 0 && <span>数据文件：{preview.report.files_found}</span>}
                  </div>
                </div>

                {(preview.report.warnings?.length || 0) > 0 && (
                  <div className="rounded border border-amber-200 bg-amber-50 p-2.5 text-amber-800 space-y-1">
                    <div className="font-semibold flex items-center gap-1">
                      <AlertTriangle className="w-3.5 h-3.5" /> 转换提醒
                    </div>
                    <ul className="list-disc pl-4 space-y-0.5">
                      {preview.report.warnings!.map((w, i) => (
                        <li key={i}>{w}</li>
                      ))}
                    </ul>
                  </div>
                )}

                {(preview.report.skipped?.length || 0) > 0 && (
                  <div className="rounded border border-line bg-surface p-2.5 text-fg-muted space-y-1">
                    <div className="font-semibold">
                      已跳过 <span className="tg-tabular">{preview.report.skipped!.length}</span> 项
                    </div>
                    <ul className="list-disc pl-4 max-h-24 overflow-y-auto tg-scroller text-[11px]">
                      {preview.report.skipped!.map((s, i) => (
                        <li key={i}>{s}</li>
                      ))}
                    </ul>
                  </div>
                )}

                {preview.diffs && preview.diffs.length > 0 && (
                  <div className="border border-line rounded bg-surface max-h-56 overflow-y-auto tg-scroller">
                    <table className="w-full text-left border-collapse">
                      <thead className="bg-surface-subtle border-b border-line text-fg-muted sticky top-0">
                        <tr>
                          <th className="px-2.5 py-1.5 w-20 font-medium">动作</th>
                          <th className="px-2.5 py-1.5 w-16 font-medium">方法</th>
                          <th className="px-2.5 py-1.5 font-medium">名称 / 目录</th>
                          <th className="px-2.5 py-1.5 font-medium">路径</th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-line">
                        {preview.diffs.map((d, i) => (
                          <tr key={i}>
                            <td className="px-2.5 py-1.5">
                              <span
                                className={`px-1.5 py-0.5 rounded text-[10px] font-semibold ${
                                  d.action === 'create'
                                    ? 'bg-emerald-100 text-emerald-800'
                                    : d.action === 'update'
                                      ? 'bg-amber-100 text-amber-800'
                                      : 'bg-slate-100 text-fg-muted'
                                }`}
                              >
                                {d.action === 'create' ? '新建' : d.action === 'update' ? '更新' : '跳过'}
                              </span>
                            </td>
                            <td className="px-2.5 py-1.5 font-mono font-semibold text-fg">{d.method}</td>
                            <td className="px-2.5 py-1.5">
                              <div className="text-fg">{d.name}</div>
                              {d.folder_path && <div className="text-[10px] text-fg-subtle">{d.folder_path}</div>}
                              {(d.preserved_fields?.length || 0) > 0 && (
                                <div className="flex flex-wrap gap-1 mt-1">
                                  {d.preserved_fields!.map((f, idx) => (
                                    <span key={`p-${idx}`} className="px-1.5 py-0.5 rounded bg-emerald-50 text-emerald-700 border border-emerald-200 text-[10px]">
                                      保留 {f}
                                    </span>
                                  ))}
                                  {d.changed_fields?.map((f, idx) => (
                                    <span key={`c-${idx}`} className="px-1.5 py-0.5 rounded bg-sky-50 text-sky-700 border border-sky-200 text-[10px]">
                                      同步 {f}
                                    </span>
                                  ))}
                                </div>
                              )}
                            </td>
                            <td className="px-2.5 py-1.5 font-mono text-fg-muted truncate max-w-[220px]" title={d.url}>
                              {d.url}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </section>
            )}
          </div>

          <footer className="px-5 py-3 border-t border-line flex items-center justify-end gap-2 shrink-0 text-xs">
            <button
              type="button"
              onClick={onClose}
              className="px-3 py-1.5 rounded-md border border-line-strong bg-surface text-fg hover:bg-surface-hover"
            >
              取消
            </button>
            <button
              type="submit"
              form={FORM_ID}
              disabled={busy}
              className={`inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md font-medium disabled:opacity-50 ${
                preview
                  ? 'border border-line-strong bg-surface text-fg hover:bg-surface-hover'
                  : 'bg-primary hover:bg-primary-hover text-white'
              }`}
            >
              <RefreshCw className={`w-3.5 h-3.5 ${busy ? 'animate-spin' : ''}`} /> {preview ? '重新解析' : '解析并预览差异'}
            </button>
            {preview && (
              <button
                type="button"
                disabled={busy}
                onClick={() => void handleApply()}
                className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-emerald-600 hover:bg-emerald-solid text-white font-medium disabled:opacity-50"
              >
                <CheckCircle2 className="w-3.5 h-3.5" /> 确认导入
              </button>
            )}
          </footer>
        </>
      )}
    </dialog>
  )
}
