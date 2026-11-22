import { useState, type FormEvent } from 'react'
import { AlertTriangle, CheckCircle2, FileUp, Layers, RefreshCw, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { PreviewResult, TreeNode } from '../lib/types'

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

export function ImportModal({ open, projectId, treeNodes, onClose, onApplied, onToast }: Props) {
  const [sourceMode, setSourceMode] = useState<'file' | 'text'>('text')
  const [format, setFormat] = useState('auto')
  const [strategy, setStrategy] = useState<'merge' | 'overwrite' | 'skip'>('merge')
  const [targetFolderId, setTargetFolderId] = useState('')
  const [textContent, setTextContent] = useState('')
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<PreviewResult | null>(null)
  const [busy, setBusy] = useState(false)

  if (!open) return null

  const folders = collectFolders(treeNodes)

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

  return (
    <div className="fixed inset-0 z-40 bg-slate-900/40 flex items-center justify-center p-4">
      <div className="w-full max-w-3xl bg-white rounded-xl border border-slate-200 shadow-xl overflow-hidden flex flex-col max-h-[86vh]">
        <div className="px-5 py-3.5 border-b border-slate-200 flex items-center justify-between bg-slate-50">
          <div>
            <h2 className="text-sm font-semibold text-slate-900">两步安全导入接口 / 集合 / 测试计划</h2>
            <p className="text-[11px] text-slate-500">
              自动识别 OpenAPI 3.x、Swagger 2.0、Postman v2.1、Apifox、JMeter (.jmx)、HAR 1.2、cURL 与 .tidegate；先预览差异再写入，保留既有接口 ID
            </p>
          </div>
          <button type="button" onClick={onClose} className="p-1 text-slate-400 hover:text-slate-600 rounded">
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="p-5 overflow-y-auto space-y-4 text-xs">
          <form onSubmit={handlePreview} className="space-y-3">
            <div className="flex flex-wrap items-center gap-3">
              <div className="inline-flex rounded-md border border-slate-200 p-0.5 bg-slate-50">
                <button
                  type="button"
                  onClick={() => {
                    setSourceMode('text')
                    setPreview(null)
                  }}
                  className={`px-3 py-1 rounded font-medium ${
                    sourceMode === 'text' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
                  }`}
                >
                  粘贴文本 / cURL
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setSourceMode('file')
                    setPreview(null)
                  }}
                  className={`px-3 py-1 rounded font-medium ${
                    sourceMode === 'file' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
                  }`}
                >
                  上传文件
                </button>
              </div>

              <label className="flex items-center gap-1.5">
                <span className="text-slate-600">格式：</span>
                <select
                  value={format}
                  onChange={(e) => {
                    setFormat(e.target.value)
                    setPreview(null)
                  }}
                  className="px-2 py-1 border border-slate-300 rounded bg-white"
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
                <span className="text-slate-600">冲突策略：</span>
                <select
                  value={strategy}
                  onChange={(e) => {
                    setStrategy(e.target.value as 'merge' | 'overwrite' | 'skip')
                    setPreview(null)
                  }}
                  className="px-2 py-1 border border-slate-300 rounded bg-white"
                >
                  <option value="merge">智能合并（保留提取器/断言与 ULID）</option>
                  <option value="overwrite">覆盖匹配接口（保留 ULID）</option>
                  <option value="skip">跳过已存在接口</option>
                </select>
              </label>

              <label className="flex items-center gap-1.5">
                <span className="text-slate-600">目标目录：</span>
                <select
                  value={targetFolderId}
                  onChange={(e) => {
                    setTargetFolderId(e.target.value)
                    setPreview(null)
                  }}
                  className="px-2 py-1 border border-slate-300 rounded bg-white max-w-[160px]"
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
                rows={7}
                value={textContent}
                onChange={(e) => {
                  setTextContent(e.target.value)
                  setPreview(null)
                }}
                placeholder="粘贴 OpenAPI JSON/YAML、Swagger 2.0、Postman v2.1、Apifox JSON、JMeter XML (.jmx)、HAR JSON 或 curl 命令..."
                className="w-full px-3 py-2 border border-slate-300 rounded-lg font-mono text-xs focus:border-sky-500 focus:outline-none"
              />
            ) : (
              <label className="flex flex-col items-center justify-center gap-2 border-2 border-dashed border-slate-300 hover:border-sky-400 rounded-lg p-6 cursor-pointer bg-slate-50">
                <FileUp className="w-5 h-5 text-sky-600" />
                <span className="text-slate-700 font-medium">
                  {selectedFile
                    ? `${selectedFile.name} (${Math.round(selectedFile.size / 1024)} KB)`
                    : '点击选择导入文件 (.yaml, .json, .jmx, .har, .tidegate)'}
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

            <div className="flex justify-end">
              <button
                type="submit"
                disabled={busy}
                className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-[#0B2A4A] hover:bg-[#123d69] text-white font-medium disabled:opacity-50"
              >
                <RefreshCw className={`w-3.5 h-3.5 ${busy ? 'animate-spin' : ''}`} /> 第一步：解析并预览差异
              </button>
            </div>
          </form>

          {preview && (
            <div className="rounded-lg border border-sky-200 bg-sky-50/40 p-4 space-y-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2 font-semibold text-slate-800">
                  <Layers className="w-4 h-4 text-sky-600" />
                  <span>
                    识别格式：<code className="font-mono text-sky-700">{preview.report.format}</code>
                    {preview.report.detected_version ? ` (${preview.report.detected_version})` : ''}
                  </span>
                </div>
                <div className="flex flex-wrap gap-2 text-[11px] text-slate-600">
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
                <div className="rounded border border-slate-200 bg-white p-2.5 text-slate-600 space-y-1">
                  <div className="font-semibold">已跳过项（{preview.report.skipped!.length}）：</div>
                  <ul className="list-disc pl-4 max-h-24 overflow-y-auto text-[11px]">
                    {preview.report.skipped!.map((s, i) => (
                      <li key={i}>{s}</li>
                    ))}
                  </ul>
                </div>
              )}

              {preview.diffs && preview.diffs.length > 0 && (
                <div className="border border-slate-200 rounded bg-white max-h-56 overflow-y-auto">
                  <table className="w-full text-left border-collapse">
                    <thead className="bg-slate-50 border-b border-slate-200 text-slate-500 sticky top-0">
                      <tr>
                        <th className="px-2.5 py-1.5 w-20">动作</th>
                        <th className="px-2.5 py-1.5 w-16">方法</th>
                        <th className="px-2.5 py-1.5">名称 / 目录</th>
                        <th className="px-2.5 py-1.5">路径</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-slate-100">
                      {preview.diffs.map((d, i) => (
                        <tr key={i}>
                          <td className="px-2.5 py-1.5">
                            <span
                              className={`px-1.5 py-0.5 rounded text-[10px] font-semibold ${
                                d.action === 'create'
                                  ? 'bg-emerald-100 text-emerald-800'
                                  : d.action === 'update'
                                    ? 'bg-amber-100 text-amber-800'
                                    : 'bg-slate-100 text-slate-600'
                              }`}
                            >
                              {d.action === 'create' ? '新建' : d.action === 'update' ? '更新' : '跳过'}
                            </span>
                          </td>
                          <td className="px-2.5 py-1.5 font-mono font-semibold text-slate-700">{d.method}</td>
                          <td className="px-2.5 py-1.5">
                            <div className="text-slate-900">{d.name}</div>
                            {d.folder_path && <div className="text-[10px] text-slate-400">{d.folder_path}</div>}
                          </td>
                          <td className="px-2.5 py-1.5 font-mono text-slate-600 truncate max-w-[220px]" title={d.url}>
                            {d.url}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}

              <div className="flex justify-end pt-1">
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => void handleApply()}
                  className="inline-flex items-center gap-1.5 px-4 py-2 rounded-md bg-emerald-600 hover:bg-emerald-700 text-white font-medium disabled:opacity-50"
                >
                  <CheckCircle2 className="w-3.5 h-3.5" /> 第二步：确认写入当前项目
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
