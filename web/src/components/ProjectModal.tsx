import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ArchiveRestore, Download, FolderPlus, Save, Shield, Trash2, Upload, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { KeyValue, Project, User } from '../lib/types'
import { useNativeDialog } from '../lib/useNativeDialog'
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

type Tab = 'edit' | 'acl' | 'create' | 'import'

const inputCls =
  'w-full px-2.5 py-1.5 border border-line-strong rounded-md bg-surface text-fg focus:border-sky-500 focus:outline-none'

const EXPORTS = [
  { fmt: 'tidegate' as const, label: '.tidegate 归档' },
  { fmt: 'openapi-yaml' as const, label: 'OpenAPI YAML' },
  { fmt: 'openapi-json' as const, label: 'OpenAPI JSON' },
  { fmt: 'postman' as const, label: 'Postman v2.1' },
]

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
  const dialogRef = useNativeDialog(open, onClose)
  const [tab, setTab] = useState<Tab>('edit')
  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [vars, setVars] = useState<KeyValue[]>([])
  const [newName, setNewName] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [archiveFile, setArchiveFile] = useState<File | null>(null)
  const [archiveName, setArchiveName] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const confirmTimer = useRef<number>(0)
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

  useEffect(() => () => window.clearTimeout(confirmTimer.current), [])

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
      window.clearTimeout(confirmTimer.current)
      confirmTimer.current = window.setTimeout(() => setConfirmDelete(false), 3000)
      return
    }
    window.clearTimeout(confirmTimer.current)
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
      onToast('success', '项目访问控制已更新')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '保存项目 ACL 失败')
    } finally {
      setAclSaving(false)
    }
  }

  const handleImportArchive = async (e: FormEvent) => {
    e.preventDefault()
    if (!archiveFile) {
      onToast('error', '请选择 .tidegate 项目归档')
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
      onToast('error', err instanceof APIError ? err.message : '导入项目归档失败')
    } finally {
      setBusy(false)
    }
  }

  const tabs: { id: Tab; label: string; icon?: typeof Shield; show: boolean }[] = [
    { id: 'edit', label: '当前项目', show: true },
    { id: 'acl', label: '成员权限', icon: Shield, show: isAdmin && Boolean(activeProject) },
    { id: 'create', label: '新建', icon: FolderPlus, show: true },
    { id: 'import', label: '导入归档', icon: ArchiveRestore, show: true },
  ]

  return (
    <dialog
      ref={dialogRef}
      closedby="any"
      aria-labelledby="project-modal-title"
      className="tg-dialog w-[min(44rem,calc(100vw-2rem))] max-h-[86dvh]"
    >
      {open && (
        <>
          <header className="px-5 py-3 border-b border-line flex items-center justify-between gap-3 bg-surface-subtle shrink-0">
            <h2 id="project-modal-title" className="text-sm font-semibold text-fg truncate min-w-0">
              项目{activeProject ? <span className="ml-2 font-normal text-fg-subtle">{activeProject.name}</span> : null}
            </h2>
            <button
              type="button"
              onClick={onClose}
              aria-label="关闭"
              className="p-1 text-fg-subtle hover:text-fg rounded hover:bg-surface-hover"
            >
              <X className="w-4 h-4" />
            </button>
          </header>

          <div role="tablist" aria-label="项目管理页签" className="tg-tablist px-5 pt-2 border-b border-line gap-5 text-xs font-medium shrink-0">
            {tabs
              .filter((t) => t.show)
              .map((t) => {
                const Icon = t.icon
                const selected = tab === t.id
                return (
                  <button
                    key={t.id}
                    type="button"
                    role="tab"
                    aria-selected={selected}
                    onClick={() => setTab(t.id)}
                    className={`tg-tab inline-flex items-center gap-1 pb-2.5 transition-colors ${
                      selected ? 'text-sky-700' : 'text-fg-muted hover:text-fg'
                    }`}
                  >
                    {Icon && <Icon className="w-3.5 h-3.5" />} {t.label}
                  </button>
                )
              })}
          </div>

          <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 text-xs">
            {tab === 'edit' && !activeProject && <p className="text-fg-subtle py-6 text-center">暂无项目</p>}

            {tab === 'edit' && activeProject && (
              <form onSubmit={handleSaveCurrent} className="space-y-4">
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <label className="block">
                    <span className="block font-medium text-fg-muted mb-1">名称</span>
                    <input
                      type="text"
                      name="project_name"
                      required
                      autoComplete="off"
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      className={inputCls}
                    />
                  </label>
                  <label className="block">
                    <span className="block font-medium text-fg-muted mb-1">说明</span>
                    <input
                      type="text"
                      name="project_description"
                      autoComplete="off"
                      value={desc}
                      onChange={(e) => setDesc(e.target.value)}
                      className={inputCls}
                    />
                  </label>
                </div>

                <fieldset className="min-w-0">
                  <legend className="font-medium text-fg-muted mb-1.5">项目变量</legend>
                  <KeyValueEditor items={vars} onChange={setVars} keyPlaceholder="变量名" valuePlaceholder="默认值" />
                </fieldset>

                <fieldset className="rounded-lg border border-line p-3">
                  <legend className="px-1 font-medium text-fg-muted">导出 · 密文自动脱敏</legend>
                  <div className="flex flex-wrap gap-2">
                    {EXPORTS.map((item) => (
                      <a
                        key={item.fmt}
                        href={api.exportProjectURL(activeProject.id, item.fmt)}
                        download
                        className="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded border border-line-strong bg-surface hover:bg-surface-hover text-fg-muted"
                      >
                        <Download className="w-3.5 h-3.5 text-sky-600" /> {item.label}
                      </a>
                    ))}
                  </div>
                </fieldset>

                <div className="flex items-center justify-between pt-3 border-t border-line">
                  <div>
                    {projects.length > 1 && (
                      <button
                        type="button"
                        onClick={() => void handleDelete()}
                        onBlur={() => setConfirmDelete(false)}
                        disabled={busy}
                        className={`inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md border text-xs font-medium ${
                          confirmDelete
                            ? 'bg-rose-600 border-rose-600 text-white hover:bg-rose-solid'
                            : 'border-rose-200 text-rose-700 hover:bg-rose-50'
                        }`}
                      >
                        <Trash2 className="w-3.5 h-3.5" /> {confirmDelete ? '确认删除？' : '删除项目'}
                      </button>
                    )}
                  </div>
                  <button
                    type="submit"
                    disabled={busy}
                    className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
                  >
                    <Save className="w-3.5 h-3.5" /> 保存
                  </button>
                </div>
              </form>
            )}

            {tab === 'acl' && isAdmin && activeProject && (
              <form onSubmit={handleSaveACL} className="space-y-4">
                {aclLoading ? (
                  <p className="text-fg-subtle py-6 text-center">加载中…</p>
                ) : (
                  <>
                    <label className="flex items-center gap-2 p-3 rounded-lg border border-line bg-surface-subtle font-semibold text-fg cursor-pointer">
                      <input type="checkbox" checked={aclRestricted} onChange={(e) => setAclRestricted(e.target.checked)} />
                      <span>私有项目</span>
                      <span className="font-normal text-fg-subtle">仅授权成员可见</span>
                    </label>

                    <div className="border border-line rounded-lg overflow-hidden">
                      <table className="w-full text-left border-collapse">
                        <thead className="bg-surface-subtle border-b border-line text-fg-subtle">
                          <tr>
                            <th className="px-3 py-2 font-medium">用户</th>
                            <th className="px-3 py-2 font-medium">全局角色</th>
                            <th className="px-3 py-2 font-medium">本项目</th>
                          </tr>
                        </thead>
                        <tbody className="divide-y divide-line">
                          {aclUsers.map((u) => (
                            <tr key={u.id}>
                              <td className="px-3 py-2 font-medium text-fg">{u.username}</td>
                              <td className="px-3 py-2 font-mono text-fg-subtle">{u.role}</td>
                              <td className="px-3 py-2">
                                {u.role === 'admin' ? (
                                  <span className="px-2 py-0.5 rounded bg-sky-50 border border-sky-200 text-sky-800 text-[11px]">
                                    始终可管理
                                  </span>
                                ) : (
                                  <select
                                    value={aclRoles[u.id] || ''}
                                    aria-label={`${u.username} 的项目权限`}
                                    onChange={(e) =>
                                      setAclRoles((prev) => ({
                                        ...prev,
                                        [u.id]: e.target.value as '' | 'viewer' | 'member',
                                      }))
                                    }
                                    className="px-2 py-1 border border-line-strong rounded bg-surface text-fg text-xs"
                                  >
                                    <option value="">{aclRestricted ? '无权限' : '沿用全局角色'}</option>
                                    <option value="viewer">viewer · 只读</option>
                                    <option value="member">member · 读写执行</option>
                                  </select>
                                )}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>

                    <div className="flex justify-end pt-3 border-t border-line">
                      <button
                        type="submit"
                        disabled={aclSaving}
                        className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
                      >
                        <Save className="w-3.5 h-3.5" /> {aclSaving ? '保存中…' : '保存权限'}
                      </button>
                    </div>
                  </>
                )}
              </form>
            )}

            {tab === 'create' && (
              <form onSubmit={handleCreate} className="space-y-3 max-w-md">
                <label className="block">
                  <span className="block font-medium text-fg-muted mb-1">名称</span>
                  <input
                    type="text"
                    name="new_project_name"
                    required
                    autoComplete="off"
                    value={newName}
                    onChange={(e) => setNewName(e.target.value)}
                    placeholder="订单核心服务"
                    className={inputCls}
                  />
                </label>
                <label className="block">
                  <span className="block font-medium text-fg-muted mb-1">说明</span>
                  <input
                    type="text"
                    name="new_project_description"
                    autoComplete="off"
                    value={newDesc}
                    onChange={(e) => setNewDesc(e.target.value)}
                    className={inputCls}
                  />
                </label>
                <button
                  type="submit"
                  disabled={busy}
                  className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
                >
                  <FolderPlus className="w-3.5 h-3.5" /> 创建并切换
                </button>
              </form>
            )}

            {tab === 'import' && (
              <form onSubmit={handleImportArchive} className="space-y-3 max-w-md">
                <label className="flex items-center justify-center gap-2 border-2 border-dashed border-line-strong hover:border-sky-400 rounded-lg p-5 cursor-pointer bg-surface-subtle min-w-0">
                  <Upload className="w-4 h-4 text-sky-600 shrink-0" />
                  <span className={`truncate ${archiveFile ? 'text-fg font-mono' : 'text-fg-muted'}`}>
                    {archiveFile ? archiveFile.name : '选择 .tidegate / .zip 文件'}
                  </span>
                  <input
                    type="file"
                    accept=".tidegate,.zip"
                    onChange={(e) => setArchiveFile(e.target.files?.[0] || null)}
                    className="sr-only"
                  />
                </label>
                <label className="block">
                  <span className="block font-medium text-fg-muted mb-1">项目名称</span>
                  <input
                    type="text"
                    name="archive_project_name"
                    autoComplete="off"
                    value={archiveName}
                    onChange={(e) => setArchiveName(e.target.value)}
                    placeholder="沿用归档内名称"
                    className={inputCls}
                  />
                </label>
                <button
                  type="submit"
                  disabled={busy || !archiveFile}
                  className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
                >
                  <ArchiveRestore className="w-3.5 h-3.5" /> 导入为新项目
                </button>
              </form>
            )}
          </div>
        </>
      )}
    </dialog>
  )
}
