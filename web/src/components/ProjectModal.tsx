import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  ArchiveRestore,
  ArrowRight,
  CircleCheck,
  Download,
  FolderKanban,
  FolderPlus,
  Save,
  Search,
  Shield,
  Trash2,
  Upload,
  X,
} from 'lucide-react'
import { api, APIError } from '../lib/api'
import {
  computeEffectiveProjectAccess,
  getGlobalRoleLabel,
  getProjectRoleLabel,
  PROJECT_ROLE_LABELS,
  ROLE_BADGE_TONE,
} from '../lib/roles'
import type { KeyValue, Project, ProjectAccessRole, User, UserRole } from '../lib/types'
import { useNativeDialog } from '../lib/useNativeDialog'
import { KeyValueEditor } from './KeyValueEditor'
import { LiquidCapsule } from './LiquidCapsule'

interface Props {
  open: boolean
  projects: Project[]
  activeProject: Project | null
  userRole?: UserRole
  isAdmin?: boolean
  onClose: () => void
  onSelectProject: (id: string) => void
  onProjectsChanged: (selectId?: string) => Promise<void>
  onOpenGovernanceUsers?: () => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

type LeftMode = 'project' | 'create' | 'import'
type RightTab = 'basic' | 'vars' | 'acl'

const inputCls =
  'w-full px-2.5 py-1.5 border border-line-strong rounded-[10px] bg-surface text-fg focus:border-sky-500 focus:outline-none'

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
  userRole = 'member',
  isAdmin = false,
  onClose,
  onSelectProject,
  onProjectsChanged,
  onOpenGovernanceUsers,
  onToast,
}: Props) {
  const canCreateProject = userRole !== 'viewer'
  const dialogRef = useNativeDialog(open, onClose)
  const [mode, setMode] = useState<LeftMode>('project')
  const [selectedId, setSelectedId] = useState<string>('')
  const [rightTab, setRightTab] = useState<RightTab>('basic')
  const [query, setQuery] = useState('')
  const rightTabsRef = useRef<HTMLDivElement | null>(null)

  const [name, setName] = useState('')
  const [desc, setDesc] = useState('')
  const [vars, setVars] = useState<KeyValue[]>([])
  const [newName, setNewName] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [newRestricted, setNewRestricted] = useState(false)
  const [newMembers, setNewMembers] = useState<Record<number, ProjectAccessRole>>({})
  const [createUsers, setCreateUsers] = useState<User[]>([])
  const [archiveFile, setArchiveFile] = useState<File | null>(null)
  const [archiveName, setArchiveName] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const confirmTimer = useRef<number>(0)

  const [aclLoading, setAclLoading] = useState(false)
  const [aclSaving, setAclSaving] = useState(false)
  const [aclRestricted, setAclRestricted] = useState(false)
  const [aclUsers, setAclUsers] = useState<User[]>([])
  const [aclRoles, setAclRoles] = useState<Record<number, ProjectAccessRole>>({})
  const wasOpenRef = useRef(false)

  useEffect(() => {
    if (!open) {
      wasOpenRef.current = false
      return
    }
    if (wasOpenRef.current) return
    wasOpenRef.current = true
    setQuery('')
    setMode(projects.length > 0 || !canCreateProject ? 'project' : 'create')
    setSelectedId(activeProject?.id || projects[0]?.id || '')
    setRightTab('basic')
    setNewName('')
    setNewDesc('')
    setNewRestricted(false)
    setNewMembers({})
    setArchiveFile(null)
    setArchiveName('')
    setConfirmDelete(false)
  }, [open, activeProject?.id, projects, canCreateProject])

  useEffect(() => {
    if (!open || mode !== 'create' || !isAdmin || !newRestricted) return
    let active = true
    api.listUsers().then((list) => {
      if (active) setCreateUsers(list)
    }).catch(() => undefined)
    return () => {
      active = false
    }
  }, [open, mode, isAdmin, newRestricted])

  const selectedProject = useMemo(
    () => projects.find((p) => p.id === selectedId) || activeProject || null,
    [projects, selectedId, activeProject]
  )

  useEffect(() => {
    if (!open || !selectedProject) return
    setName(selectedProject.name)
    setDesc(selectedProject.description || '')
    setVars(selectedProject.variables || [])
    setConfirmDelete(false)
  }, [open, selectedProject?.id, selectedProject?.version])

  useEffect(() => {
    if (!open || mode !== 'project' || rightTab !== 'acl' || !isAdmin || !selectedProject) return
    let active = true
    setAclLoading(true)
    Promise.all([api.getProjectACL(selectedProject.id), api.listUsers()])
      .then(([acl, users]) => {
        if (!active) return
        setAclRestricted(Boolean(acl.restricted))
        setAclUsers(users)
        const roleMap: Record<number, ProjectAccessRole> = {}
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
  }, [open, mode, rightTab, isAdmin, selectedProject?.id])

  useEffect(() => {
    if (!isAdmin && rightTab === 'acl') setRightTab('basic')
  }, [isAdmin, rightTab])

  useEffect(() => () => window.clearTimeout(confirmTimer.current), [])

  const filteredProjects = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return projects
    return projects.filter(
      (p) => p.name.toLowerCase().includes(q) || (p.description || '').toLowerCase().includes(q)
    )
  }, [projects, query])

  const handleSaveCurrent = async (e: FormEvent) => {
    e.preventDefault()
    if (!selectedProject) return
    setBusy(true)
    try {
      await api.patchProject(selectedProject.id, {
        name: name.trim(),
        description: desc.trim(),
        variables: vars.filter((v) => v.name.trim() !== '' || v.value !== ''),
        version: selectedProject.version,
      })
      await onProjectsChanged(selectedProject.id)
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
      const members =
        isAdmin && newRestricted
          ? Object.entries(newMembers)
              .map(([uid, role]) => ({ user_id: Number(uid), role }))
              .filter(
                (m): m is { user_id: number; role: 'member' | 'viewer' | 'none' } =>
                  m.user_id > 0 && (m.role === 'member' || m.role === 'viewer' || m.role === 'none')
              )
          : undefined
      const created = await api.createProject({
        name: newName.trim(),
        description: newDesc.trim(),
        restricted: newRestricted,
        members,
      })
      setNewName('')
      setNewDesc('')
      setNewRestricted(false)
      setNewMembers({})
      await onProjectsChanged(created.id)
      onSelectProject(created.id)
      setSelectedId(created.id)
      setMode('project')
      setRightTab('basic')
      onToast('success', `已创建项目「${created.name}」`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '创建项目失败')
    } finally {
      setBusy(false)
    }
  }

  const handleDelete = async () => {
    if (!selectedProject) return
    if (!confirmDelete) {
      setConfirmDelete(true)
      window.clearTimeout(confirmTimer.current)
      confirmTimer.current = window.setTimeout(() => setConfirmDelete(false), 3000)
      return
    }
    window.clearTimeout(confirmTimer.current)
    setBusy(true)
    try {
      await api.deleteProject(selectedProject.id, selectedProject.version)
      await onProjectsChanged()
      onToast('success', `已删除项目「${selectedProject.name}」`)
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
    if (!selectedProject) return
    setAclSaving(true)
    try {
      const members = aclUsers
        .filter((u) => u.role !== 'admin')
        .map((u) => ({ user_id: u.id, role: aclRoles[u.id] }))
        .filter(
          (m): m is { user_id: number; role: 'member' | 'viewer' | 'none' } =>
            m.role === 'member' || m.role === 'viewer' || m.role === 'none'
        )
      const saved = await api.putProjectACL(selectedProject.id, {
        restricted: aclRestricted,
        members,
      })
      const roleMap: Record<number, ProjectAccessRole> = {}
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

  const canWriteSelected = selectedProject?.effective_role !== 'viewer'
  const canDeleteSelected = projects.length > 1 && (selectedProject?.can_delete ?? isAdmin)

  return (
    <dialog
      ref={dialogRef}
      {...({ closedby: 'closerequest' } as Record<string, string>)}
      aria-labelledby="project-modal-title"
      className="tg-dialog w-[min(56rem,calc(100vw-2rem))] h-[min(40rem,86dvh)] max-h-[86dvh]"
    >
      {open && (
        <>
          <header className="tg-dialog-header">
            <h2 id="project-modal-title" className="text-sm font-semibold text-fg flex items-center gap-2 min-w-0">
              <FolderKanban className="w-4 h-4 text-tidal shrink-0" />
              <span>项目</span>
              {selectedProject && mode === 'project' && (
                <span className="font-normal text-fg-subtle truncate">· {selectedProject.name}</span>
              )}
            </h2>
            <button
              type="button"
              onClick={onClose}
              aria-label="关闭"
              className="p-1 text-fg-subtle hover:text-fg rounded-lg hover:bg-surface-hover"
            >
              <X className="w-4 h-4" />
            </button>
          </header>

          <div className="flex flex-1 min-h-0">
            {/* Master Rail (Aligned with EnvModal) */}
            <nav
              aria-label="项目列表"
              className="w-56 shrink-0 border-r border-line bg-surface-subtle p-2.5 flex flex-col gap-1.5 overflow-y-auto tg-scroller"
            >
              {canCreateProject && (
                <div className="grid grid-cols-2 gap-1">
                  <button
                    type="button"
                    onClick={() => setMode('create')}
                    className={`inline-flex items-center justify-center gap-1 px-2 py-1.5 rounded-[10px] text-xs font-medium transition-colors ${
                      mode === 'create'
                        ? 'bg-primary text-white'
                        : 'text-tidal hover:bg-sky-500/10 border border-dashed border-sky-400/50'
                    }`}
                  >
                    <FolderPlus className="w-3.5 h-3.5" /> 新建
                  </button>
                  <button
                    type="button"
                    onClick={() => setMode('import')}
                    className={`inline-flex items-center justify-center gap-1 px-2 py-1.5 rounded-[10px] text-xs font-medium transition-colors ${
                      mode === 'import'
                        ? 'bg-primary text-white'
                        : 'text-fg-muted hover:bg-surface-hover border border-line'
                    }`}
                  >
                    <ArchiveRestore className="w-3.5 h-3.5" /> 导入
                  </button>
                </div>
              )}

              {projects.length > 4 && (
                <search className="relative">
                  <Search className="w-3.5 h-3.5 text-fg-subtle absolute left-2 top-1/2 -translate-y-1/2 pointer-events-none" />
                  <input
                    type="search"
                    aria-label="筛选项目"
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    placeholder="筛选项目…"
                    className="w-full pl-7 pr-2 py-1 rounded-[10px] border border-line bg-surface text-xs text-fg focus:border-sky-500 focus:outline-none"
                  />
                </search>
              )}

              <ul className="space-y-1">
                {filteredProjects.map((pr) => {
                  const selected = mode === 'project' && selectedProject?.id === pr.id
                  const active = activeProject?.id === pr.id
                  return (
                    <li key={pr.id}>
                      <button
                        type="button"
                        onClick={() => {
                          setMode('project')
                          setSelectedId(pr.id)
                        }}
                        aria-current={selected ? 'true' : undefined}
                        className={`w-full text-left px-2.5 py-2 rounded-[10px] text-xs border transition-colors ${
                          selected
                            ? 'bg-surface border-sky-400 text-fg font-semibold shadow-xs'
                            : 'border-transparent text-fg-muted hover:bg-surface-hover'
                        }`}
                      >
                        <div className="flex items-center justify-between gap-1.5 min-w-0">
                          <span className="truncate">{pr.name}</span>
                          <div className="flex items-center gap-1 shrink-0">
                            {pr.restricted && (
                              <span className="px-1 rounded bg-sky-500/12 text-tidal text-[10px]">私有</span>
                            )}
                            {pr.effective_role === 'viewer' && (
                              <span className="px-1 rounded bg-amber-500/15 text-amber-700 text-[10px]">只读</span>
                            )}
                            {active && (
                              <CircleCheck className="w-3.5 h-3.5 text-emerald-600 shrink-0" aria-label="当前项目" />
                            )}
                          </div>
                        </div>
                        {pr.description && (
                          <span className="block text-[11px] text-fg-subtle truncate font-normal mt-0.5">
                            {pr.description}
                          </span>
                        )}
                      </button>
                    </li>
                  )
                })}
              </ul>
            </nav>

            {/* Detail Workspace */}
            <div className="flex-1 min-w-0 flex flex-col text-xs">
              {mode === 'create' && (
                <form onSubmit={handleCreate} className="flex-1 min-h-0 flex flex-col">
                  <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 space-y-5">
                    <div className="font-semibold text-sm text-fg">新建项目</div>
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 max-w-2xl">
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
                    </div>

                    <fieldset className="rounded-xl border border-line bg-surface-subtle/60 p-3.5 space-y-3 max-w-2xl">
                      <legend className="px-1.5 font-medium text-fg-muted">访问范围</legend>
                      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
                        {(
                          [
                            {
                              val: false,
                              title: '团队开放',
                              desc: '按成员全局角色（未隔离）开放访问',
                            },
                            {
                              val: true,
                              title: '私有白名单',
                              desc: isAdmin ? '仅下列显式授权的成员可访问' : '仅管理员与创建者本人可访问',
                            },
                          ] as const
                        ).map((opt) => {
                          const active = newRestricted === opt.val
                          return (
                            <label
                              key={opt.title}
                              className={`flex items-start gap-2.5 p-2.5 rounded-xl border cursor-pointer transition-colors ${
                                active ? 'border-sky-400 bg-surface shadow-xs' : 'border-line bg-surface/60 hover:bg-surface'
                              }`}
                            >
                              <input
                                type="radio"
                                name="new_project_visibility"
                                checked={active}
                                onChange={() => setNewRestricted(opt.val)}
                                className="mt-0.5"
                              />
                              <span className="min-w-0">
                                <span className="block font-semibold text-fg">{opt.title}</span>
                                <span className="block text-[11px] text-fg-subtle">{opt.desc}</span>
                              </span>
                            </label>
                          )
                        })}
                      </div>

                      {newRestricted && isAdmin && createUsers.filter((u) => u.role !== 'admin').length > 0 && (
                        <div className="space-y-2 pt-1">
                          <div className="flex flex-wrap items-center justify-between gap-2">
                            <span className="font-medium text-fg">初始白名单成员</span>
                            <div className="inline-flex items-center gap-1.5">
                              <button
                                type="button"
                                onClick={() => {
                                  const next: Record<number, ProjectAccessRole> = {}
                                  for (const u of createUsers) if (u.role !== 'admin') next[u.id] = 'member'
                                  setNewMembers(next)
                                }}
                                className="tg-btn-secondary !py-1 !px-2 !text-[11px]"
                              >
                                全部读写
                              </button>
                              <button
                                type="button"
                                onClick={() => {
                                  const next: Record<number, ProjectAccessRole> = {}
                                  for (const u of createUsers) if (u.role !== 'admin') next[u.id] = 'viewer'
                                  setNewMembers(next)
                                }}
                                className="tg-btn-secondary !py-1 !px-2 !text-[11px]"
                              >
                                全部只读
                              </button>
                              <button
                                type="button"
                                onClick={() => setNewMembers({})}
                                className="tg-btn-secondary !py-1 !px-2 !text-[11px]"
                              >
                                清空授权
                              </button>
                            </div>
                          </div>
                          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                            {createUsers
                              .filter((u) => u.role !== 'admin')
                              .map((u) => {
                                const val = newMembers[u.id] || ''
                                return (
                                  <div
                                    key={u.id}
                                    className="flex items-center justify-between gap-2 px-2.5 py-1.5 rounded-lg border border-line bg-surface"
                                  >
                                    <span className="truncate font-medium text-fg">
                                      {u.username}{' '}
                                      <span className="text-[11px] font-normal text-fg-subtle">
                                        · {getGlobalRoleLabel(u.role, true)}
                                      </span>
                                    </span>
                                    <div className="inline-flex rounded-md border border-line bg-surface-subtle p-0.5 shrink-0">
                                      {(['', 'member', 'viewer'] as const).map((r) => (
                                        <button
                                          key={r || 'none'}
                                          type="button"
                                          onClick={() => setNewMembers((prev) => ({ ...prev, [u.id]: r }))}
                                          className={`px-2 py-0.5 rounded text-[11px] font-medium ${
                                            val === r
                                              ? r === ''
                                                ? 'bg-slate-600 text-white'
                                                : 'bg-primary text-white'
                                              : 'text-fg-muted hover:text-fg'
                                          }`}
                                        >
                                          {r === '' ? '未授权' : PROJECT_ROLE_LABELS[r]}
                                        </button>
                                      ))}
                                    </div>
                                  </div>
                                )
                              })}
                          </div>
                        </div>
                      )}
                    </fieldset>
                  </div>
                  <footer className="tg-dialog-footer">
                    <button type="button" onClick={onClose} className="tg-btn-secondary">
                      取消
                    </button>
                    <button type="submit" disabled={busy} className="tg-btn-primary">
                      <FolderPlus className="w-3.5 h-3.5" /> 创建并切换
                    </button>
                  </footer>
                </form>
              )}

              {mode === 'import' && (
                <form onSubmit={handleImportArchive} className="flex-1 min-h-0 flex flex-col">
                  <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 space-y-4">
                    <div className="font-semibold text-sm text-fg">导入项目归档</div>
                    <div className="space-y-3 max-w-md">
                      <label className="flex items-center justify-center gap-2 border-2 border-dashed border-line-strong hover:border-sky-400 rounded-xl p-6 cursor-pointer bg-surface-subtle min-w-0">
                        <Upload className="w-4 h-4 text-sky-600 shrink-0" />
                        <span className={`truncate ${archiveFile ? 'text-fg font-mono' : 'text-fg-muted'}`}>
                          {archiveFile ? archiveFile.name : '选择 .tidegate / .zip 文件'}
                        </span>
                        <input
                          type="file"
                          accept=".tidegate,.zip"
                          onChange={(e) => {
                            setArchiveFile(e.target.files?.[0] || null)
                            e.currentTarget.value = ''
                          }}
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
                    </div>
                  </div>
                  <footer className="tg-dialog-footer">
                    <button type="button" onClick={onClose} className="tg-btn-secondary">
                      取消
                    </button>
                    <button type="submit" disabled={busy || !archiveFile} className="tg-btn-primary">
                      <ArchiveRestore className="w-3.5 h-3.5" /> 导入为新项目
                    </button>
                  </footer>
                </form>
              )}

              {mode === 'project' && !selectedProject && (
                <div className="flex-1 flex items-center justify-center text-fg-subtle p-6">暂无项目</div>
              )}

              {mode === 'project' && selectedProject && (
                <>
                  <div className="px-5 py-2 border-b border-line bg-surface-subtle/40 shrink-0">
                    <div
                      ref={rightTabsRef}
                      role="tablist"
                      aria-label="项目详情页签"
                      className="relative tg-tablist inline-flex gap-1"
                    >
                      <LiquidCapsule
                        containerRef={rightTabsRef}
                        activeId={rightTab}
                        orientation="horizontal"
                        className="rounded-lg"
                      />
                      {(
                        [
                          { id: 'basic' as const, label: '基本信息与导出', show: true },
                          { id: 'vars' as const, label: `项目变量 (${vars.length})`, show: true },
                          { id: 'acl' as const, label: '成员权限', show: isAdmin },
                        ] as const
                      )
                        .filter((t) => t.show)
                        .map((t) => {
                          const selected = rightTab === t.id
                          return (
                            <button
                              key={t.id}
                              data-liq-id={t.id}
                              type="button"
                              role="tab"
                              aria-selected={selected}
                              onClick={() => setRightTab(t.id)}
                              className={`relative z-10 px-3 py-1.5 rounded-lg font-semibold transition-colors whitespace-nowrap ${
                                selected ? 'text-tidal' : 'text-fg-muted hover:text-fg'
                              }`}
                            >
                              {t.label}
                            </button>
                          )
                        })}
                    </div>
                  </div>

                  {rightTab === 'acl' && isAdmin ? (
                    <form onSubmit={handleSaveACL} className="flex-1 min-h-0 flex flex-col">
                      <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 space-y-4">
                        {aclLoading ? (
                          <p className="text-fg-subtle py-6 text-center">加载中…</p>
                        ) : (
                          <>
                            <div className="flex flex-wrap items-center justify-between gap-2 p-3 rounded-xl border border-line bg-surface-subtle">
                              <label className="flex items-center gap-2 font-semibold text-fg cursor-pointer">
                                <input
                                  type="checkbox"
                                  checked={aclRestricted}
                                  onChange={(e) => setAclRestricted(e.target.checked)}
                                />
                                <Shield className="w-3.5 h-3.5 text-tidal" />
                                <span>私有白名单模式</span>
                                <span className="font-normal text-fg-subtle">仅明确授权的成员可访问</span>
                              </label>
                              <div className="flex flex-wrap items-center gap-1.5">
                                <button
                                  type="button"
                                  onClick={() => {
                                    const next: Record<number, ProjectAccessRole> = {}
                                    for (const u of aclUsers) if (u.role !== 'admin') next[u.id] = 'viewer'
                                    setAclRoles(next)
                                  }}
                                  className="tg-btn-secondary !py-1 !px-2 !text-[11px]"
                                >
                                  全部只读
                                </button>
                                <button
                                  type="button"
                                  onClick={() => {
                                    const next: Record<number, ProjectAccessRole> = {}
                                    for (const u of aclUsers) if (u.role !== 'admin') next[u.id] = 'none'
                                    setAclRoles(next)
                                  }}
                                  className="tg-btn-secondary !py-1 !px-2 !text-[11px] hover:!text-rose-600"
                                >
                                  全部禁止
                                </button>
                                <button
                                  type="button"
                                  onClick={() => setAclRoles({})}
                                  className="tg-btn-secondary !py-1 !px-2 !text-[11px]"
                                >
                                  全部恢复继承
                                </button>
                                {onOpenGovernanceUsers && (
                                  <button
                                    type="button"
                                    onClick={() => {
                                      onClose()
                                      onOpenGovernanceUsers()
                                    }}
                                    className="inline-flex items-center gap-1 text-tidal hover:underline font-medium ml-1"
                                  >
                                    <span>全局权限矩阵</span>
                                    <ArrowRight className="w-3 h-3" />
                                  </button>
                                )}
                              </div>
                            </div>

                            <div className="border border-line rounded-xl overflow-hidden bg-surface">
                              <table className="w-full text-left border-collapse">
                                <thead className="bg-surface-subtle border-b border-line text-fg-subtle">
                                  <tr>
                                    <th className="px-3 py-2 font-medium">用户</th>
                                    <th className="px-3 py-2 font-medium">全局角色</th>
                                    <th className="px-3 py-2 font-medium">实时生效</th>
                                    <th className="px-3 py-2 font-medium">本项目权限</th>
                                  </tr>
                                </thead>
                                <tbody className="divide-y divide-line">
                                  {aclUsers.map((u) => {
                                    const preview = computeEffectiveProjectAccess(
                                      u.role,
                                      u.default_project_access,
                                      aclRestricted,
                                      aclRoles[u.id]
                                    )
                                    return (
                                      <tr key={u.id}>
                                        <td className="px-3 py-2 font-medium text-fg">
                                          {u.username}
                                          {u.disabled && (
                                            <span className="ml-1.5 px-1.5 py-px rounded bg-rose-500/15 text-rose-700 text-[10px]">
                                              已停用
                                            </span>
                                          )}
                                          {u.default_project_access === 'deny' && u.role !== 'admin' && (
                                            <span className="ml-1.5 px-1.5 py-px rounded bg-surface-subtle border border-line text-fg-subtle text-[10px]">
                                              白名单账号
                                            </span>
                                          )}
                                        </td>
                                        <td className="px-3 py-2 text-fg-muted">
                                          <span className={`inline-flex px-2 py-0.5 rounded-full border text-[11px] ${ROLE_BADGE_TONE[u.role]}`}>
                                            {getGlobalRoleLabel(u.role)}
                                          </span>
                                        </td>
                                        <td className="px-3 py-2">
                                          <span
                                            className={`inline-flex px-2 py-0.5 rounded-full border text-[11px] font-medium ${
                                              preview.allowed
                                                ? ROLE_BADGE_TONE[preview.effectiveRole || 'viewer']
                                                : ROLE_BADGE_TONE.none
                                            }`}
                                          >
                                            {preview.label}
                                          </span>
                                        </td>
                                        <td className="px-3 py-2">
                                          {u.role === 'admin' ? (
                                            <span className="px-2 py-0.5 rounded-full bg-sky-500/15 text-tidal text-[11px] font-medium">
                                              始终可管理
                                            </span>
                                          ) : (
                                            <div className="inline-flex rounded-lg border border-line bg-surface-subtle p-0.5 gap-0.5">
                                              {(['', 'member', 'viewer', 'none'] as const).map((r) => {
                                                const cur = aclRoles[u.id] || ''
                                                const active = cur === r
                                                return (
                                                  <button
                                                    key={r || 'inherit'}
                                                    type="button"
                                                    onClick={() => setAclRoles((prev) => ({ ...prev, [u.id]: r }))}
                                                    className={`px-2 py-0.5 rounded-md text-[11px] font-medium transition-colors ${
                                                      active
                                                        ? r === 'none'
                                                          ? 'bg-rose-600 text-white'
                                                          : 'bg-primary text-white'
                                                        : 'text-fg-muted hover:text-fg hover:bg-surface-hover'
                                                    }`}
                                                  >
                                                    {r === ''
                                                      ? getProjectRoleLabel('', aclRestricted || u.default_project_access === 'deny')
                                                      : PROJECT_ROLE_LABELS[r]}
                                                  </button>
                                                )
                                              })}
                                            </div>
                                          )}
                                        </td>
                                      </tr>
                                    )
                                  })}
                                </tbody>
                              </table>
                            </div>
                          </>
                        )}
                      </div>
                      <footer className="tg-dialog-footer justify-between">
                        <div />
                        <button type="submit" disabled={aclSaving} className="tg-btn-primary">
                          <Save className="w-3.5 h-3.5" /> {aclSaving ? '保存中…' : '保存项目权限'}
                        </button>
                      </footer>
                    </form>
                  ) : (
                    <form onSubmit={handleSaveCurrent} className="flex-1 min-h-0 flex flex-col">
                      <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 space-y-4">
                        {rightTab === 'basic' && (
                          <>
                            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                              <label className="block">
                                <span className="block font-medium text-fg-muted mb-1">名称</span>
                                <input
                                  type="text"
                                  name="project_name"
                                  required
                                  disabled={!canWriteSelected}
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
                                  disabled={!canWriteSelected}
                                  autoComplete="off"
                                  value={desc}
                                  onChange={(e) => setDesc(e.target.value)}
                                  className={inputCls}
                                />
                              </label>
                            </div>

                            <fieldset className="rounded-xl border border-line bg-surface-subtle/50 p-3.5">
                              <legend className="px-1.5 font-medium text-fg-muted">导出 · 密文自动脱敏</legend>
                              <div className="flex flex-wrap gap-2">
                                {EXPORTS.map((item) => (
                                  <a
                                    key={item.fmt}
                                    href={api.exportProjectURL(selectedProject.id, item.fmt)}
                                    download
                                    className="tg-btn-secondary"
                                  >
                                    <Download className="w-3.5 h-3.5 text-tidal" /> {item.label}
                                  </a>
                                ))}
                              </div>
                            </fieldset>
                          </>
                        )}

                        {rightTab === 'vars' && (
                          <fieldset className="min-w-0">
                            <legend className="font-medium text-fg-muted mb-1.5">项目全局变量（所有环境继承）</legend>
                            <KeyValueEditor
                              resetKey={selectedProject.id}
                              items={vars}
                              onChange={setVars}
                              keyPlaceholder="变量名"
                              valuePlaceholder="默认值"
                            />
                          </fieldset>
                        )}
                      </div>

                      <footer className="tg-dialog-footer justify-between">
                        <div>
                          {canDeleteSelected && (
                            <button
                              type="button"
                              onClick={() => void handleDelete()}
                              onBlur={() => setConfirmDelete(false)}
                              disabled={busy}
                              className={confirmDelete ? 'tg-btn-primary !bg-rose-600' : 'tg-btn-danger'}
                            >
                              <Trash2 className="w-3.5 h-3.5" /> {confirmDelete ? '确认删除？' : '删除项目'}
                            </button>
                          )}
                        </div>
                        <div className="flex items-center gap-2">
                          {selectedProject.id !== activeProject?.id && (
                            <button
                              type="button"
                              onClick={() => {
                                onSelectProject(selectedProject.id)
                                onToast('info', `已切换至「${selectedProject.name}」`)
                              }}
                              className="tg-btn-secondary"
                            >
                              <CircleCheck className="w-3.5 h-3.5 text-emerald-600" /> 设为当前
                            </button>
                          )}
                          {canWriteSelected && (
                            <button type="submit" disabled={busy} className="tg-btn-primary">
                              <Save className="w-3.5 h-3.5" /> 保存项目
                            </button>
                          )}
                        </div>
                      </footer>
                    </form>
                  )}
                </>
              )}
            </div>
          </div>
        </>
      )}
    </dialog>
  )
}
