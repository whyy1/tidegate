import { useEffect, useState, type FormEvent } from 'react'
import { ArchiveRestore, Download, FolderPlus, Save, Trash2, Upload, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { KeyValue, Project } from '../lib/types'
import { KeyValueEditor } from './KeyValueEditor'

interface Props {
  open: boolean
  projects: Project[]
  activeProject: Project | null
  onClose: () => void
  onSelectProject: (id: string) => void
  onProjectsChanged: (selectId?: string) => Promise<void>
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

export function ProjectModal({
  open,
  projects,
  activeProject,
  onClose,
  onSelectProject,
  onProjectsChanged,
  onToast,
}: Props) {
  const [tab, setTab] = useState<'edit' | 'create' | 'import'>('edit')
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [vars, setVars] = useState<KeyValue[]>([])
  const [newName, setNewName] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [archiveFile, setArchiveFile] = useState<File | null>(null)
  const [archiveName, setArchiveName] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  useEffect(() => {
    if (open && activeProject) {
      setName(activeProject.name)
      setDesc(activeProject.description || '')
      setVars(activeProject.variables || [])
      setConfirmDelete(false)
    }
  }, [open, activeProject])

  if (!open) return null

  const handleSaveCurrent = async (e: FormEvent) => {
    e.preventDefault()
    if (!activeProject) return
    setBusy(true)
    try {
      await api.patchProject(activeProject.id, {
        name: name.trim(),
        description: desc.trim(),
        variables: vars.filter((v) => v.name.trim() !== '' || v.value !== ''),
        version: activeProject.version,
      })
      await onProjectsChanged(activeProject.id)
      onToast('success', '项目配置已保存')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '保存项目失败')
    } finally {
      setBusy(false)
    }
  }

  const handleCreate = async (e: FormEvent) => {
    e.preventDefault()
    if (!newName.trim()) return
    setBusy(true)
    try {
      const created = await api.createProject({ name: newName.trim(), description: newDesc.trim() })
      setNewName('')
      setNewDesc('')
      await onProjectsChanged(created.id)
      onSelectProject(created.id)
      setTab('edit')
      onToast('success', `已创建项目「${created.name}」`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '创建项目失败')
    } finally {
      setBusy(false)
    }
  }

  const handleDelete = async () => {
    if (!activeProject) return
    if (!confirmDelete) {
      setConfirmDelete(true)
      return
    }
    setBusy(true)
    try {
      await api.deleteProject(activeProject.id, activeProject.version)
      await onProjectsChanged()
      onToast('success', `已删除项目「${activeProject.name}」`)
      onClose()
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '删除项目失败')
    } finally {
      setBusy(false)
      setConfirmDelete(false)
    }
  }

  const handleImportArchive = async (e: FormEvent) => {
    e.preventDefault()
    if (!archiveFile) {
      onToast('error', '请选择 `.tidegate` 项目压缩包')
      return
    }
    setBusy(true)
    try {
      const fd = new FormData()
      fd.set('file', archiveFile)
      if (archiveName.trim()) fd.set('name', archiveName.trim())
      const res = await api.importProjectArchive(fd)
      setArchiveFile(null)
      setArchiveName('')
      await onProjectsChanged(res.project.id)
      onSelectProject(res.project.id)
      onToast(
        'success',
        `已导入项目「${res.project.name}」（接口 ${res.report.apis_created || 0} 个，场景 ${res.report.scenarios_created || 0} 个）`
      )
      onClose()
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '导入项目压缩包失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 z-40 bg-slate-900/40 flex items-center justify-center p-4">
      <div className="w-full max-w-2xl bg-white rounded-xl border border-slate-200 shadow-xl overflow-hidden">
        <div className="px-5 py-3.5 border-b border-slate-200 flex items-center justify-between bg-slate-50">
          <h2 className="text-sm font-semibold text-slate-900">项目管理与导出备份</h2>
          <button type="button" onClick={onClose} className="p-1 text-slate-400 hover:text-slate-600 rounded">
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="px-5 pt-3 border-b border-slate-200 flex gap-4 text-xs font-medium">
          <button
            type="button"
            onClick={() => setTab('edit')}
            className={`pb-2.5 border-b-2 ${
              tab === 'edit' ? 'border-sky-500 text-sky-700' : 'border-transparent text-slate-500 hover:text-slate-800'
            }`}
          >
            当前项目配置
          </button>
          <button
            type="button"
            onClick={() => setTab('create')}
            className={`pb-2.5 border-b-2 ${
              tab === 'create' ? 'border-sky-500 text-sky-700' : 'border-transparent text-slate-500 hover:text-slate-800'
            }`}
          >
            <FolderPlus className="w-3.5 h-3.5 inline mr-1" /> 新建项目
          </button>
          <button
            type="button"
            onClick={() => setTab('import')}
            className={`pb-2.5 border-b-2 ${
              tab === 'import' ? 'border-sky-500 text-sky-700' : 'border-transparent text-slate-500 hover:text-slate-800'
            }`}
          >
            <ArchiveRestore className="w-3.5 h-3.5 inline mr-1" /> 导入 `.tidegate` 归档
          </button>
        </div>

        <div className="p-5 max-h-[70vh] overflow-y-auto text-xs">
          {tab === 'edit' && activeProject && (
            <form onSubmit={handleSaveCurrent} className="space-y-4">
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block font-medium text-slate-700 mb-1">项目名称</label>
                  <input
                    type="text"
                    required
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                  />
                </div>
                <div>
                  <label className="block font-medium text-slate-700 mb-1">说明</label>
                  <input
                    type="text"
                    value={desc}
                    onChange={(e) => setDesc(e.target.value)}
                    className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                  />
                </div>
              </div>

              <div>
                <div className="flex items-center justify-between mb-1">
                  <span className="font-medium text-slate-700">项目级变量（低于环境变量与临时变量优先级）</span>
                </div>
                <p className="text-[11px] text-slate-500 mb-1.5">
                  勾选「密」的敏感变量将自动存入 AES-256-GCM 加密保险箱，导出与界面回显时会自动脱敏。
                </p>
                <KeyValueEditor
                  items={vars}
                  onChange={setVars}
                  keyPlaceholder="变量名"
                  valuePlaceholder="默认值"
                />
              </div>

              <div className="rounded-lg border border-slate-200 bg-slate-50 p-3 space-y-2">
                <div className="font-medium text-slate-700">导出当前项目（已自动脱敏「密」标记变量及明文凭据）</div>
                <div className="flex flex-wrap gap-2">
                  {(
                    [
                      { fmt: 'tidegate' as const, label: '导出 .tidegate 完整归档（含场景/CSV）' },
                      { fmt: 'openapi-yaml' as const, label: '导出 OpenAPI 3.0 YAML' },
                      { fmt: 'openapi-json' as const, label: '导出 OpenAPI 3.0 JSON' },
                      { fmt: 'postman' as const, label: '导出 Postman v2.1 集合' },
                    ] as const
                  ).map((item) => (
                    <a
                      key={item.fmt}
                      href={api.exportProjectURL(activeProject.id, item.fmt)}
                      download
                      className="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded border border-slate-300 bg-white hover:bg-slate-100 text-slate-700"
                    >
                      <Download className="w-3.5 h-3.5 text-sky-600" /> {item.label}
                    </a>
                  ))}
                </div>
              </div>

              <div className="flex items-center justify-between pt-2 border-t border-slate-200">
                <div>
                  {projects.length > 1 && (
                    <button
                      type="button"
                      onClick={() => void handleDelete()}
                      disabled={busy}
                      className={`inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md border text-xs font-medium ${
                        confirmDelete
                          ? 'bg-rose-600 border-rose-600 text-white'
                          : 'border-rose-200 text-rose-700 hover:bg-rose-50'
                      }`}
                    >
                      <Trash2 className="w-3.5 h-3.5" /> {confirmDelete ? '再次点击确认删除项目' : '删除项目'}
                    </button>
                  )}
                </div>
                <button
                  type="submit"
                  disabled={busy}
                  className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
                >
                  <Save className="w-3.5 h-3.5" /> 保存项目
                </button>
              </div>
            </form>
          )}

          {tab === 'create' && (
            <form onSubmit={handleCreate} className="space-y-3 max-w-md">
              <div>
                <label className="block font-medium text-slate-700 mb-1">新项目名称</label>
                <input
                  type="text"
                  required
                  value={newName}
                  onChange={(e) => setNewName(e.target.value)}
                  placeholder="例如 订单核心服务"
                  className="w-full px-3 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                />
              </div>
              <div>
                <label className="block font-medium text-slate-700 mb-1">项目描述（可选）</label>
                <input
                  type="text"
                  value={newDesc}
                  onChange={(e) => setNewDesc(e.target.value)}
                  className="w-full px-3 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                />
              </div>
              <button
                type="submit"
                disabled={busy}
                className="inline-flex items-center gap-1.5 px-4 py-2 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium"
              >
                <FolderPlus className="w-3.5 h-3.5" /> 创建并切换
              </button>
            </form>
          )}

          {tab === 'import' && (
            <form onSubmit={handleImportArchive} className="space-y-3 max-w-md">
              <div>
                <label className="block font-medium text-slate-700 mb-1">选择 `.tidegate` 归档文件</label>
                <label className="flex items-center justify-center gap-2 border-2 border-dashed border-slate-300 hover:border-sky-400 rounded-lg p-5 cursor-pointer bg-slate-50">
                  <Upload className="w-4 h-4 text-sky-600" />
                  <span className="text-slate-600">
                    {archiveFile ? archiveFile.name : '点击选择 .tidegate / .zip 文件'}
                  </span>
                  <input
                    type="file"
                    accept=".tidegate,.zip"
                    onChange={(e) => setArchiveFile(e.target.files?.[0] || null)}
                    className="hidden"
                  />
                </label>
              </div>
              <div>
                <label className="block font-medium text-slate-700 mb-1">新项目名称覆盖（可选）</label>
                <input
                  type="text"
                  value={archiveName}
                  onChange={(e) => setArchiveName(e.target.value)}
                  placeholder="留空则沿用归档中的项目名"
                  className="w-full px-3 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                />
              </div>
              <button
                type="submit"
                disabled={busy}
                className="inline-flex items-center gap-1.5 px-4 py-2 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
              >
                <ArchiveRestore className="w-3.5 h-3.5" /> 导入为独立项目
              </button>
            </form>
          )}
        </div>
      </div>
    </div>
  )
}
