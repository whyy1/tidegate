import { useEffect, useState, type FormEvent } from 'react'
import { ArchiveRestore, Download, FolderPlus, Save, Shield, Trash2, Upload, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { KeyValue, Project, User } from '../lib/types'
import { KeyValueEditor } from './KeyValueEditor'

interface Props {
  open: boolean
  projects: Project[]
  activeProject: Project | null
  isAdmin?: boolean
  onClose: () => void
  onSelectProject: (id: string) => void
  onProjectsChanged: (selectId?: string) => Promise<void>
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

export function ProjectModal({
  open,
  projects,
  activeProject,
  isAdmin = false,
  onClose,
  onSelectProject,
  onProjectsChanged,
  onToast,
}: Props) {
  const [tab, setTab] = useState<'edit' | 'acl' | 'create' | 'import'>('edit')
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [vars, setVars] = useState<KeyValue[]>([])
  const [newName, setNewName] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [archiveFile, setArchiveFile] = useState<File | null>(null)
  const [archiveName, setArchiveName] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [aclLoading, setAclLoading] = useState(false)
  const [aclSaving, setAclSaving] = useState(false)
  const [aclRestricted, setAclRestricted] = useState(false)
  const [aclUsers, setAclUsers] = useState<User[]>([])
  const [aclRoles, setAclRoles] = useState<Record<number, '' | 'viewer' | 'member'>>({})

  useEffect(() => {
    if (open && activeProject) {
      setName(activeProject.name)
      setDesc(activeProject.description || '')
      setVars(activeProject.variables || [])
      setConfirmDelete(false)
    }
  }, [open, activeProject])

  useEffect(() => {
    if (!open || tab !== 'acl' || !isAdmin || !activeProject) return
    let active = true
    setAclLoading(true)
    Promise.all([api.getProjectACL(activeProject.id), api.listUsers()])
      .then(([acl, users]) => {
        if (!active) return
        setAclRestricted(Boolean(acl.restricted))
        setAclUsers(users)
        const roleMap: Record<number, '' | 'viewer' | 'member'> = {}
        for (const m of acl.members || []) {
          roleMap[m.user_id] = m.role
        }
        setAclRoles(roleMap)
      })
      .catch((err) => {
        if (!active) return
        onToast('error', err instanceof APIError ? err.message : '加载项目访问控制失败')
      })
      .finally(() => {
        if (active) setAclLoading(false)
      })
    return () => {
      active = false
    }
  }, [open, tab, isAdmin, activeProject?.id])

  useEffect(() => {
    if ((!isAdmin || !activeProject) && tab === 'acl') {
      setTab('edit')
    }
  }, [isAdmin, activeProject, tab])

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

  const handleSaveACL = async (e: FormEvent) => {
    e.preventDefault()
    if (!activeProject) return
    setAclSaving(true)
    try {
      const members = aclUsers
        .filter((u) => u.role !== 'admin')
        .map((u) => ({ user_id: u.id, role: aclRoles[u.id] }))
        .filter((m): m is { user_id: number; role: 'member' | 'viewer' } => m.role === 'member' || m.role === 'viewer')
      const saved = await api.putProjectACL(activeProject.id, {
        restricted: aclRestricted,
        members,
      })
      const roleMap: Record<number, '' | 'viewer' | 'member'> = {}
      for (const m of saved.members || []) {
        roleMap[m.user_id] = m.role
      }
      setAclRestricted(saved.restricted)
      setAclRoles(roleMap)
      onToast('success', '项目级成员访问控制 (ACL) 已更新')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '保存项目 ACL 失败')
    } finally {
      setAclSaving(false)
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

        <div role="tablist" aria-label="项目管理页签" className="tg-tablist px-5 pt-2 border-b border-slate-200 flex gap-5 text-xs font-medium">
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'edit'}
            onClick={() => setTab('edit')}
            className={`tg-tab pb-2.5 transition-colors ${
              tab === 'edit' ? 'text-sky-700' : 'text-slate-500 hover:text-slate-800'
            }`}
          >
            当前项目配置
          </button>
          {isAdmin && activeProject && (
            <button
              type="button"
              role="tab"
              aria-selected={tab === 'acl'}
              onClick={() => setTab('acl')}
              className={`tg-tab pb-2.5 transition-colors ${
                tab === 'acl' ? 'text-sky-700' : 'text-slate-500 hover:text-slate-800'
              }`}
            >
              <Shield className="w-3.5 h-3.5 inline mr-1" /> 成员访问控制
            </button>
          )}
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'create'}
            onClick={() => setTab('create')}
            className={`tg-tab pb-2.5 transition-colors ${
              tab === 'create' ? 'text-sky-700' : 'text-slate-500 hover:text-slate-800'
            }`}
          >
            <FolderPlus className="w-3.5 h-3.5 inline mr-1" /> 新建项目
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'import'}
            onClick={() => setTab('import')}
            className={`tg-tab pb-2.5 transition-colors ${
              tab === 'import' ? 'text-sky-700' : 'text-slate-500 hover:text-slate-800'
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

          {tab === 'acl' && isAdmin && activeProject && (
            <form onSubmit={handleSaveACL} className="space-y-4">
              {aclLoading ? (
                <p className="text-slate-500">正在加载项目成员访问控制...</p>
              ) : (
                <>
                  <div className="p-3 rounded-lg border border-slate-200 bg-slate-50 space-y-1.5">
                    <label className="flex items-center gap-2 font-semibold text-slate-800 cursor-pointer">
                      <input
                        type="checkbox"
                        checked={aclRestricted}
                        onChange={(e) => setAclRestricted(e.target.checked)}
                      />
                      <span>开启私有隔离：仅显式授权成员可访问此项目（管理员始终可见）</span>
                    </label>
                    <p className="text-[11px] text-slate-500 pl-6">
                      未开启时，团队所有已登录用户均按全局角色访问该项目（下方成员配置仍可对单个用户授予额外权限，如将 viewer 在本项目提升为 member）。
                    </p>
                  </div>

                  <div className="border border-slate-200 rounded-lg overflow-hidden">
                    <table className="w-full text-left border-collapse">
                      <thead className="bg-slate-50 border-b border-slate-200 text-slate-500">
                        <tr>
                          <th className="px-3 py-2">用户名</th>
                          <th className="px-3 py-2">全局角色</th>
                          <th className="px-3 py-2">本项目权限分配</th>
                        </tr>
                      </thead>
                      <tbody className="divide-y divide-slate-100">
                        {aclUsers.map((u) => (
                          <tr key={u.id}>
                            <td className="px-3 py-2 font-medium text-slate-800">{u.username}</td>
                            <td className="px-3 py-2 font-mono text-slate-500">{u.role}</td>
                            <td className="px-3 py-2">
                              {u.role === 'admin' ? (
                                <span className="px-2 py-0.5 rounded bg-sky-50 text-sky-800 font-mono text-[11px]">
                                  admin（始终可管理）
                                </span>
                              ) : (
                                <select
                                  value={aclRoles[u.id] || ''}
                                  onChange={(e) =>
                                    setAclRoles((prev) => ({
                                      ...prev,
                                      [u.id]: e.target.value as '' | 'viewer' | 'member',
                                    }))
                                  }
                                  className="px-2 py-1 border border-slate-300 rounded bg-white text-xs"
                                >
                                  <option value="">
                                    {aclRestricted ? '无权限（禁止访问）' : '默认（沿用全局角色）'}
                                  </option>
                                  <option value="viewer">viewer（只读访问）</option>
                                  <option value="member">member（读写与执行）</option>
                                </select>
                              )}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>

                  <div className="flex justify-end pt-2 border-t border-slate-200">
                    <button
                      type="submit"
                      disabled={aclSaving}
                      className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
                    >
                      <Save className="w-3.5 h-3.5" /> {aclSaving ? '保存中...' : '保存项目 ACL'}
                    </button>
                  </div>
                </>
              )}
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
