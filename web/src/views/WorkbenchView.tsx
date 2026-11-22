import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  Columns2,
  Copy,
  Eye,
  FilePlus,
  Folder as FolderIcon,
  FolderOpen,
  FolderPlus,
  Import,
  ListChecks,
  PanelLeftClose,
  PanelLeftOpen,
  Pencil,
  Play,
  Plus,
  Rows2,
  Save,
  Search,
  Sparkles,
  Terminal,
  Trash2,
  X,
  Zap,
} from 'lucide-react'
import { AuthEditor } from '../components/AuthEditor'
import { CodeEditor } from '../components/CodeEditor'
import { CollectionRunnerModal } from '../components/CollectionRunnerModal'
import { CurlModal } from '../components/CurlModal'
import { FolderModal } from '../components/FolderModal'
import { ImportModal } from '../components/ImportModal'
import { KeyValueEditor } from '../components/KeyValueEditor'
import { ResponsePanel } from '../components/ResponsePanel'
import { SaveAPIModal } from '../components/SaveAPIModal'
import { VariableInput } from '../components/VariableInput'
import { api, APIError } from '../lib/api'
import type {
  API,
  APIFieldDoc,
  APIProtocol,
  APIResponseExample,
  AssertRule,
  DataFile,
  DebugAPIResponse,
  DebugRecord,
  EffectiveAPIPreview,
  Environment,
  ExtractRule,
  Folder,
  GRPCSchemaCatalog,
  KeyValue,
  ResolvedVariable,
  Tree,
  TreeNode,
} from '../lib/types'

interface Props {
  projectId: string
  environments?: Environment[]
  projectVariables?: KeyValue[]
  activeEnvId: string
  tempVars: Record<string, string>
  canWrite?: boolean
  onTempVarsChange: (next: Record<string, string>) => void
  onWorkspaceMutated: () => Promise<void>
  onQuickLoadTest: (apiDraft: Partial<API>) => void
  incomingReplay?: { seq: number; api: Partial<API> } | null
  openRequest?: { seq: number; apiId?: string; scratch?: boolean } | null
  isActive?: boolean
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

type ReqSubTab = 'params' | 'headers' | 'body' | 'auth' | 'extract' | 'assert' | 'scripts' | 'docs'

interface OpenTab {
  tabKey: string
  api: Partial<API>
  dirty: boolean
  debugResp: DebugAPIResponse | null
  effective: EffectiveAPIPreview | null
  showEffective: boolean
}

let scratchCounter = 1

function newScratchAPI(folderId = ''): Partial<API> {
  return {
    name: `临时调试 ${scratchCounter++}`,
    folder_id: folderId,
    request: {
      method: 'GET',
      url: '',
      path_params: [],
      query: [],
      headers: [],
      body: { type: 'none' },
    },
    auth: { type: 'inherit' },
    extract: [],
    assert: [],
  }
}

function methodBadgeColor(method?: string): string {
  switch ((method || 'GET').toUpperCase()) {
    case 'GET':
      return 'text-emerald-700 bg-emerald-50 border-emerald-200'
    case 'POST':
      return 'text-sky-700 bg-sky-50 border-sky-200'
    case 'PUT':
    case 'PATCH':
      return 'text-amber-700 bg-amber-50 border-amber-200'
    case 'DELETE':
      return 'text-rose-700 bg-rose-50 border-rose-200'
    default:
      return 'text-slate-600 bg-slate-100 border-slate-200'
  }
}

function syncPathParamsFromURL(url: string, current: KeyValue[] = []): KeyValue[] {
  let clean = url.split(/[?#]/)[0] || ''
  clean = clean.replace(/^[a-zA-Z]+:\/\/[^/]+/, '')
  const matches = Array.from(clean.matchAll(/\/:([A-Za-z_][A-Za-z0-9_-]*)/g)).map((m) => m[1])
  if (matches.length === 0) return current
  const byName = new Map(current.map((p) => [p.name, p]))
  const next: KeyValue[] = []
  const seen = new Set<string>()
  for (const name of matches) {
    if (seen.has(name)) continue
    seen.add(name)
    next.push(byName.get(name) || { name, value: '' })
  }
  for (const p of current) {
    if (p.name && !seen.has(p.name)) next.push(p)
  }
  return next
}

function looksLikeCurl(text: string): boolean {
  const first = text.trim().split(/\r?\n/)[0]?.trim() || ''
  const cleaned = first.replace(/^(PS>\s*|PS\s+|\$\s*|>\s*)/, '').toLowerCase()
  return cleaned === 'curl' || cleaned === 'curl.exe' || cleaned.startsWith('curl ') || cleaned.startsWith('curl.exe ')
}

export function WorkbenchView({
  projectId,
  environments = [],
  projectVariables = [],
  activeEnvId,
  tempVars,
  canWrite = true,
  onTempVarsChange,
  onWorkspaceMutated,
  onQuickLoadTest,
  incomingReplay,
  openRequest,
  isActive = true,
  onToast,
}: Props) {
  const [tree, setTree] = useState<Tree>({ children: [] })
  const [files, setFiles] = useState<DataFile[]>([])
  const [history, setHistory] = useState<DebugRecord[]>([])
  const [runnerModal, setRunnerModal] = useState<{ open: boolean; folderId: string }>({
    open: false,
    folderId: '',
  })

  const variableHints = useMemo<ResolvedVariable[]>(() => {
    const byName = new Map<string, ResolvedVariable>()
    for (const kv of projectVariables) {
      if (!kv.disabled && kv.name.trim()) {
        byName.set(kv.name.trim(), {
          name: kv.name.trim(),
          value: kv.value,
          source: 'project',
          description: kv.description,
          secret: kv.secret,
        })
      }
    }
    const envObj = environments.find((e) => e.id === activeEnvId)
    for (const kv of envObj?.variables || []) {
      if (!kv.disabled && kv.name.trim()) {
        byName.set(kv.name.trim(), {
          name: kv.name.trim(),
          value: kv.value,
          source: 'environment',
          description: kv.description,
          secret: kv.secret,
        })
      }
    }
    for (const [k, v] of Object.entries(tempVars)) {
      if (k.trim()) {
        byName.set(k.trim(), { name: k.trim(), value: v, source: 'override' })
      }
    }
    return Array.from(byName.values())
  }, [projectVariables, environments, activeEnvId, tempVars])

  const handleSaveEnvVars = async (vars: Record<string, string>) => {
    if (!activeEnvId) {
      onToast('error', '请先在顶部选择一个目标环境')
      return
    }
    try {
      const freshEnvs = await api.listEnvironments(projectId)
      const envObj = freshEnvs.find((item) => item.id === activeEnvId)
      if (!envObj) {
        onToast('error', '未找到当前选中环境')
        return
      }
      const list = [...(envObj.variables || [])]
      for (const [k, val] of Object.entries(vars)) {
        const idx = list.findIndex((item) => item.name === k)
        if (idx >= 0) {
          list[idx] = { ...list[idx], value: val, disabled: false }
        } else {
          list.push({ name: k, value: val })
        }
      }
      await api.updateEnvironment(projectId, activeEnvId, {
        name: envObj.name,
        base_url: envObj.base_url,
        variables: list,
        version: envObj.version,
      })
      await onWorkspaceMutated()
      onToast('success', `已写入环境「${envObj.name}」（${Object.keys(vars).length} 项变量）`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '写入环境变量失败')
    }
  }
  const [search, setSearch] = useState('')
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const [treeCollapsed, setTreeCollapsed] = useState(false)
  const [splitMode, setSplitMode] = useState<'horizontal' | 'vertical'>(() => {
    try {
      return window.localStorage.getItem('tg.workbench.splitMode') === 'vertical' ? 'vertical' : 'horizontal'
    } catch {
      return 'horizontal'
    }
  })
  const [splitPct, setSplitPct] = useState<number>(() => {
    try {
      const n = Number(window.localStorage.getItem('tg.workbench.splitPct'))
      return Number.isFinite(n) && n >= 28 && n <= 74 ? n : 52
    } catch {
      return 52
    }
  })
  const splitBoxRef = useRef<HTMLDivElement | null>(null)
  const [resizingSplit, setResizingSplit] = useState(false)

  const [tabs, setTabs] = useState<OpenTab[]>(() => [
    {
      tabKey: `scratch-init`,
      api: newScratchAPI(),
      dirty: false,
      debugResp: null,
      effective: null,
      showEffective: false,
    },
  ])
  const [activeTabKey, setActiveTabKey] = useState<string>('scratch-init')
  const [reqSubTab, setReqSubTab] = useState<ReqSubTab>('params')
  const [sending, setSending] = useState(false)
  const [saving, setSaving] = useState(false)
  const [autoSyncTempVars, setAutoSyncTempVars] = useState(true)

  const [importOpen, setImportOpen] = useState(false)
  const [curlOpen, setCurlOpen] = useState(false)
  const [saveModalOpen, setSaveModalOpen] = useState(false)
  const [folderModal, setFolderModal] = useState<{ open: boolean; folder: Folder | null; parentId: string }>({
    open: false,
    folder: null,
    parentId: '',
  })
  const [dragNodeId, setDragNodeId] = useState<string>('')
  const [grpcCatalog, setGrpcCatalog] = useState<GRPCSchemaCatalog | null>(null)
  const [grpcDiscoverBusy, setGrpcDiscoverBusy] = useState(false)

  const activeTab = tabs.find((t) => t.tabKey === activeTabKey) || tabs[0]

  const loadSidebarData = async () => {
    try {
      const [t, fl, hist] = await Promise.all([
        api.getTree(projectId),
        api.listFiles(projectId),
        api.listDebugHistory(projectId, 40, false),
      ])
      setTree({ children: t.children || [], problems: t.problems })
      setFiles(fl)
      setHistory(hist)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '加载项目接口树失败')
    }
  }

  useEffect(() => {
    void loadSidebarData()
    setTabs([
      {
        tabKey: `scratch-${Date.now()}`,
        api: newScratchAPI(),
        dirty: false,
        debugResp: null,
        effective: null,
        showEffective: false,
      },
    ])
  }, [projectId])

  useEffect(() => {
    if (tabs.length > 0 && !tabs.some((t) => t.tabKey === activeTabKey)) {
      setActiveTabKey(tabs[0].tabKey)
    }
  }, [tabs, activeTabKey])

  useEffect(() => {
    if (!incomingReplay) return
    const key = `replay-${incomingReplay.seq}`
    setTabs((prev) => [
      ...prev,
      {
        tabKey: key,
        api: incomingReplay.api,
        dirty: true,
        debugResp: null,
        effective: null,
        showEffective: false,
      },
    ])
    setActiveTabKey(key)
  }, [incomingReplay])

  useEffect(() => {
    if (!openRequest) return
    if (openRequest.scratch) {
      addScratchTab()
      return
    }
    if (openRequest.apiId) {
      void openSavedAPI(openRequest.apiId)
    }
  }, [openRequest])

  const changeSplitMode = (mode: 'horizontal' | 'vertical') => {
    setSplitMode(mode)
    try {
      window.localStorage.setItem('tg.workbench.splitMode', mode)
    } catch {
      // ignore
    }
  }

  const updateSplitPct = (nextPct: number) => {
    const clamped = Math.max(28, Math.min(74, Math.round(nextPct * 10) / 10))
    setSplitPct(clamped)
    try {
      window.localStorage.setItem('tg.workbench.splitPct', String(clamped))
    } catch {
      // ignore
    }
  }

  useEffect(() => {
    if (!resizingSplit) return
    const onMove = (e: PointerEvent) => {
      const box = splitBoxRef.current
      if (!box) return
      const r = box.getBoundingClientRect()
      if (splitMode === 'horizontal' && r.width > 0) {
        updateSplitPct(((e.clientX - r.left) / r.width) * 100)
      } else if (splitMode === 'vertical' && r.height > 0) {
        updateSplitPct(((e.clientY - r.top) / r.height) * 100)
      }
    }
    const onUp = () => setResizingSplit(false)
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
  }, [resizingSplit, splitMode])

  const patchActiveTab = (updater: (tab: OpenTab) => OpenTab) => {
    setTabs((prev) => prev.map((t) => (t.tabKey === activeTab.tabKey ? updater(t) : t)))
  }

  const updateActiveAPI = (updater: (cur: Partial<API>) => Partial<API>) => {
    patchActiveTab((t) => ({
      ...t,
      api: updater(t.api),
      dirty: true,
    }))
  }

  const openSavedAPI = async (id: string) => {
    const existing = tabs.find((t) => t.api.id === id)
    if (existing) {
      setActiveTabKey(existing.tabKey)
      return
    }
    try {
      const loaded = await api.getAPI(projectId, id)
      const tab: OpenTab = {
        tabKey: loaded.id,
        api: loaded,
        dirty: false,
        debugResp: null,
        effective: null,
        showEffective: false,
      }
      setTabs((prev) => [...prev, tab])
      setActiveTabKey(tab.tabKey)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '读取接口详情失败')
    }
  }

  const addScratchTab = (folderId = '') => {
    const key = `scratch-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`
    const tab: OpenTab = {
      tabKey: key,
      api: newScratchAPI(folderId),
      dirty: false,
      debugResp: null,
      effective: null,
      showEffective: false,
    }
    setTabs((prev) => [...prev, tab])
    setActiveTabKey(key)
  }

  const closeTab = (key: string) => {
    setTabs((prev) => {
      if (prev.length === 1) {
        const freshKey = `scratch-${Date.now()}`
        setActiveTabKey(freshKey)
        return [
          {
            tabKey: freshKey,
            api: newScratchAPI(),
            dirty: false,
            debugResp: null,
            effective: null,
            showEffective: false,
          },
        ]
      }
      const next = prev.filter((t) => t.tabKey !== key)
      if (activeTabKey === key && next.length > 0) {
        setActiveTabKey(next[next.length - 1].tabKey)
      }
      return next
    })
  }

  const handleSave = async () => {
    if (!activeTab) return
    if (!activeTab.api.id) {
      setSaveModalOpen(true)
      return
    }
    setSaving(true)
    try {
      const saved = await api.updateAPI(projectId, activeTab.api.id, activeTab.api)
      patchActiveTab((t) => ({ ...t, api: saved, dirty: false }))
      await loadSidebarData()
      onToast('success', `已保存「${saved.name}」`)
    } catch (err) {
      if (err instanceof APIError && err.status === 409) {
        onToast('error', '接口版本冲突（可能已被外部修改），请关闭标签重新打开后再保存')
      } else {
        onToast('error', err instanceof APIError ? err.message : '保存接口失败')
      }
    } finally {
      setSaving(false)
    }
  }

  const handleConfirmCreateSaved = async (name: string, folderId: string) => {
    const payload: Partial<API> = {
      ...activeTab.api,
      name,
      folder_id: folderId,
    }
    const created = await api.createAPI(projectId, payload)
    setTabs((prev) =>
      prev.map((t) => (t.tabKey === activeTab.tabKey ? { ...t, tabKey: created.id, api: created, dirty: false } : t))
    )
    setActiveTabKey(created.id)
    await loadSidebarData()
    onToast('success', `已保存到目录「${created.name}」`)
  }

  const handleDebugSend = async () => {
    if (!activeTab || sending) return
    if (!activeTab.api.request?.url?.trim() && !activeTab.api.id) {
      onToast('error', '请先输入请求地址或粘贴 cURL 命令')
      return
    }
    setSending(true)
    try {
      const res = await api.debugAPI(projectId, {
        api_id: activeTab.api.id,
        api: activeTab.api,
        environment_id: activeEnvId,
        variables: tempVars,
        save_history: true,
      })
      patchActiveTab((t) => ({ ...t, debugResp: res }))
      if (autoSyncTempVars && res.result.extracted_vars && Object.keys(res.result.extracted_vars).length > 0) {
        onTempVarsChange({ ...tempVars, ...res.result.extracted_vars })
      }
      const hist = await api.listDebugHistory(projectId, 40, false)
      setHistory(hist)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '调试发送失败')
    } finally {
      setSending(false)
    }
  }

  const handleToggleEffectivePreview = async () => {
    if (activeTab.showEffective) {
      patchActiveTab((t) => ({ ...t, showEffective: false }))
      return
    }
    try {
      const eff = await api.resolvePreviewAPI(projectId, {
        api_id: activeTab.api.id,
        api: activeTab.api,
        environment_id: activeEnvId,
        variables: tempVars,
      })
      patchActiveTab((t) => ({ ...t, effective: eff, showEffective: true }))
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '获取生效预览失败')
    }
  }

  const handleURLPaste = async (pasted: string, e: React.ClipboardEvent<HTMLInputElement>) => {
    if (!looksLikeCurl(pasted)) return
    e.preventDefault()
    try {
      const { api: parsed } = await api.parseCurl(pasted)
      updateActiveAPI((cur) => ({
        ...cur,
        name: cur.id ? cur.name : parsed.name || cur.name,
        request: parsed.request,
        auth: parsed.auth || { type: 'inherit' },
      }))
      onToast('success', '已自动识别粘贴的 cURL 并填充请求参数')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '解析 cURL 失败')
    }
  }

  const actionsRef = useRef({
    save: handleSave,
    send: handleDebugSend,
    active: isActive,
    modalOpen: false,
  })
  actionsRef.current = {
    save: handleSave,
    send: handleDebugSend,
    active: isActive,
    modalOpen: importOpen || curlOpen || saveModalOpen || folderModal.open,
  }

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (!actionsRef.current.active || actionsRef.current.modalOpen) return
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        e.preventDefault()
        void actionsRef.current.save()
      } else if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
        e.preventDefault()
        void actionsRef.current.send()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  const moveSibling = async (siblings: TreeNode[], parentId: string, idx: number, delta: number) => {
    const targetIdx = idx + delta
    if (targetIdx < 0 || targetIdx >= siblings.length) return
    const node = siblings[idx]
    try {
      await api.moveNode(projectId, {
        id: node.id,
        version: node.version,
        parent_id: parentId,
        index: targetIdx,
      })
      await loadSidebarData()
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '调整顺序失败')
    }
  }

  const handleDropIntoFolder = async (folderId: string) => {
    if (!dragNodeId || dragNodeId === folderId) return
    const findNode = (nodes: TreeNode[]): TreeNode | null => {
      for (const n of nodes) {
        if (n.id === dragNodeId) return n
        if (n.children) {
          const found = findNode(n.children)
          if (found) return found
        }
      }
      return null
    }
    const dragged = findNode(tree.children)
    setDragNodeId('')
    if (!dragged) return
    try {
      await api.moveNode(projectId, {
        id: dragged.id,
        version: dragged.version,
        parent_id: folderId,
        index: -1,
      })
      await loadSidebarData()
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '移动失败')
    }
  }

  const filterTree = (nodes: TreeNode[]): TreeNode[] => {
    const kw = search.trim().toLowerCase()
    if (!kw) return nodes
    const out: TreeNode[] = []
    for (const n of nodes) {
      if (n.type === 'folder') {
        const kids = filterTree(n.children || [])
        if (n.name.toLowerCase().includes(kw) || kids.length > 0) {
          out.push({ ...n, children: kids })
        }
      } else if (
        n.name.toLowerCase().includes(kw) ||
        (n.url || '').toLowerCase().includes(kw) ||
        (n.method || '').toLowerCase().includes(kw)
      ) {
        out.push(n)
      }
    }
    return out
  }

  const renderTreeNodes = (nodes: TreeNode[], parentId = '', depth = 0) => {
    return nodes.map((node, idx) => {
      if (node.type === 'folder') {
        const isCollapsed = Boolean(collapsed[node.id]) && !search.trim()
        return (
          <div key={node.id} className="select-none">
            <div
              draggable
              onDragStart={() => setDragNodeId(node.id)}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                e.preventDefault()
                void handleDropIntoFolder(node.id)
              }}
              style={{ paddingLeft: `${8 + depth * 12}px` }}
              className="group flex items-center gap-1 pr-1.5 py-1.5 rounded-md text-xs hover:bg-slate-200/60 text-slate-800 cursor-pointer"
              onClick={() => setCollapsed((c) => ({ ...c, [node.id]: !c[node.id] }))}
            >
              {isCollapsed ? (
                <ChevronRight className="w-3.5 h-3.5 text-slate-400 shrink-0" />
              ) : (
                <ChevronDown className="w-3.5 h-3.5 text-slate-400 shrink-0" />
              )}
              {isCollapsed ? (
                <FolderIcon className="w-3.5 h-3.5 text-amber-500 shrink-0" />
              ) : (
                <FolderOpen className="w-3.5 h-3.5 text-amber-500 shrink-0" />
              )}
              <span className="truncate font-medium flex-1">{node.name}</span>
              <div
                className="opacity-0 group-hover:opacity-100 inline-flex items-center gap-0.5 shrink-0"
                onClick={(e) => e.stopPropagation()}
              >
                <button
                  type="button"
                  title="批量测试此目录"
                  onClick={() => setRunnerModal({ open: true, folderId: node.id })}
                  className="p-0.5 text-slate-500 hover:text-emerald-600 rounded"
                >
                  <Play className="w-3 h-3" />
                </button>
                <button
                  type="button"
                  title="在此目录下新建草稿接口"
                  onClick={() => addScratchTab(node.id)}
                  className="p-0.5 text-slate-500 hover:text-sky-600 rounded"
                >
                  <Plus className="w-3 h-3" />
                </button>
                <button
                  type="button"
                  title="编辑目录请求头与鉴权"
                  onClick={async () => {
                    try {
                      const f = await api.getFolder(projectId, node.id)
                      setFolderModal({ open: true, folder: f, parentId })
                    } catch (err) {
                      onToast('error', err instanceof APIError ? err.message : '读取目录失败')
                    }
                  }}
                  className="p-0.5 text-slate-500 hover:text-sky-600 rounded"
                >
                  <Pencil className="w-3 h-3" />
                </button>
                <button
                  type="button"
                  title="删除目录"
                  onClick={async () => {
                    try {
                      await api.deleteFolder(projectId, node.id, node.version)
                      await loadSidebarData()
                      onToast('success', `已删除目录「${node.name}」`)
                    } catch (err) {
                      onToast('error', err instanceof APIError ? err.message : '删除目录失败')
                    }
                  }}
                  className="p-0.5 text-slate-500 hover:text-rose-600 rounded"
                >
                  <Trash2 className="w-3 h-3" />
                </button>
              </div>
            </div>
            {!isCollapsed && node.children && renderTreeNodes(node.children, node.id, depth + 1)}
          </div>
        )
      }

      const active = activeTab?.api.id === node.id
      return (
        <div
          key={node.id}
          draggable
          onDragStart={() => setDragNodeId(node.id)}
          style={{ paddingLeft: `${12 + depth * 12}px` }}
          onClick={() => void openSavedAPI(node.id)}
          className={`group flex items-center gap-1.5 pr-1.5 py-1.5 rounded-md text-xs cursor-pointer transition-colors ${
            active ? 'bg-sky-100 text-sky-950 font-semibold' : 'text-slate-700 hover:bg-slate-200/60'
          }`}
        >
          <span
            className={`px-1 py-0.2 rounded border text-[9px] font-mono font-bold w-11 text-center shrink-0 ${methodBadgeColor(
              node.method
            )}`}
          >
            {(node.method || 'GET').slice(0, 4)}
          </span>
          <span className="truncate flex-1" title={`${node.name} (${node.url || ''})`}>
            {node.name}
          </span>
          <div
            className="opacity-0 group-hover:opacity-100 inline-flex items-center gap-0.5 shrink-0"
            onClick={(e) => e.stopPropagation()}
          >
            <button
              type="button"
              title="上移"
              onClick={() => void moveSibling(nodes, parentId, idx, -1)}
              className="p-0.5 text-slate-400 hover:text-slate-700"
            >
              <ArrowUp className="w-3 h-3" />
            </button>
            <button
              type="button"
              title="下移"
              onClick={() => void moveSibling(nodes, parentId, idx, 1)}
              className="p-0.5 text-slate-400 hover:text-slate-700"
            >
              <ArrowDown className="w-3 h-3" />
            </button>
            <button
              type="button"
              title="复制接口"
              onClick={async () => {
                try {
                  const cp = await api.copyAPI(projectId, node.id)
                  await loadSidebarData()
                  void openSavedAPI(cp.id)
                } catch (err) {
                  onToast('error', err instanceof APIError ? err.message : '复制接口失败')
                }
              }}
              className="p-0.5 text-slate-400 hover:text-sky-600"
            >
              <Copy className="w-3 h-3" />
            </button>
            <button
              type="button"
              title="删除接口"
              onClick={async () => {
                try {
                  await api.deleteAPI(projectId, node.id, node.version)
                  setTabs((prev) => prev.filter((t) => t.api.id !== node.id))
                  await loadSidebarData()
                  onToast('success', `已删除接口「${node.name}」`)
                } catch (err) {
                  onToast('error', err instanceof APIError ? err.message : '删除接口失败')
                }
              }}
              className="p-0.5 text-slate-400 hover:text-rose-600"
            >
              <Trash2 className="w-3 h-3" />
            </button>
          </div>
        </div>
      )
    })
  }

  const req = activeTab.api.request || { method: 'GET', url: '' }
  const protocol = (req.protocol || 'http') as APIProtocol
  const body = req.body || { type: 'none' }
  const extracts = activeTab.api.extract || []
  const asserts = activeTab.api.assert || []
  const examples = activeTab.api.examples || []
  const responseFields = activeTab.api.response_fields || []

  const updateBody = (patch: Partial<NonNullable<API['request']['body']>>) => {
    updateActiveAPI((cur) => ({
      ...cur,
      request: {
        ...req,
        body: { ...body, ...patch },
      },
    }))
  }

  const updateWS = (patch: Partial<NonNullable<API['request']['ws']>>) => {
    updateActiveAPI((cur) => ({
      ...cur,
      request: {
        ...req,
        ws: { ...(req.ws || {}), ...patch },
      },
    }))
  }

  const updateGRPC = (patch: Partial<NonNullable<API['request']['grpc']>>) => {
    updateActiveAPI((cur) => ({
      ...cur,
      request: {
        ...req,
        grpc: { service: '', method: '', ...(req.grpc || {}), ...patch },
      },
    }))
  }

  const handleDiscoverGRPC = async () => {
    setGrpcDiscoverBusy(true)
    try {
      const cat = await api.discoverGRPC(projectId, {
        environment_id: activeEnvId || undefined,
        target: req.grpc?.proto_file ? undefined : req.url,
        tls: req.grpc?.tls,
        proto_file: req.grpc?.proto_file || undefined,
      })
      setGrpcCatalog(cat)
      const count = cat.services?.reduce((n, s) => n + (s.methods?.length || 0), 0) || 0
      onToast('success', `探测成功：发现 ${cat.services?.length || 0} 个服务、${count} 个方法`)
      if (cat.services?.length && !req.grpc?.service) {
        const firstSvc = cat.services[0]
        const firstMethod = firstSvc.methods?.[0]
        updateGRPC({
          service: firstSvc.name,
          method: firstMethod?.name || '',
          payload_encoding: req.grpc?.payload_encoding || 'proto_json',
          response_encoding: req.grpc?.response_encoding || 'proto_json',
        })
      }
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : 'gRPC 契约探测失败')
    } finally {
      setGrpcDiscoverBusy(false)
    }
  }

  const updateSSE = (patch: Partial<NonNullable<API['request']['sse']>>) => {
    updateActiveAPI((cur) => ({
      ...cur,
      request: {
        ...req,
        sse: { ...(req.sse || {}), ...patch },
      },
    }))
  }

  const addExtractFromJSON = (expr: string, suggestedName: string) => {
    updateActiveAPI((cur) => ({
      ...cur,
      extract: [...(cur.extract || []), { name: suggestedName, from: 'jsonpath', expr }],
    }))
    setReqSubTab('extract')
    onToast('info', `已添加提取器：${suggestedName} = ${expr}`)
  }

  const addAssertFromJSON = (expr: string, sampleVal: string) => {
    updateActiveAPI((cur) => ({
      ...cur,
      assert: [...(cur.assert || []), { type: 'jsonpath', expr, equals: sampleVal }],
    }))
    setReqSubTab('assert')
    onToast('info', `已添加断言：${expr} == ${sampleVal}`)
  }

  const loadHistoryIntoPanel = async (id: string) => {
    try {
      const rec = await api.getDebugRecord(projectId, id)
      if (rec.data?.result) {
        patchActiveTab((t) => ({
          ...t,
          debugResp: { record_id: rec.id, result: rec.data!.result, final_vars: rec.data?.final_vars },
        }))
        onToast('info', `已回放调试记录 ${rec.method} ${rec.url}`)
      }
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '读取调试记录失败')
    }
  }

  return (
    <div className="flex flex-1 min-h-0 bg-[var(--tg-canvas)]">
      {/* Left API Tree */}
      {!treeCollapsed && (
        <aside className="w-64 bg-white border-r border-slate-200 flex flex-col shrink-0">
          <div className="p-2.5 border-b border-slate-200 space-y-2">
            <div className="relative">
              <Search className="w-3.5 h-3.5 text-slate-400 absolute left-2.5 top-2" />
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="过滤接口名称或路径..."
                className="w-full pl-8 pr-2.5 py-1.5 text-xs bg-slate-50 border border-slate-200 rounded-md focus:bg-white focus:border-sky-500 focus:outline-none"
              />
            </div>
            <div className="grid grid-cols-2 gap-1.5 text-xs">
              <button
                type="button"
                onClick={() => addScratchTab()}
                className="inline-flex items-center justify-center gap-1 py-1.5 px-2 rounded-md bg-[#0B2A4A] hover:bg-[#123862] text-white font-medium"
              >
                <FilePlus className="w-3.5 h-3.5" /> 新建请求
              </button>
              <button
                type="button"
                disabled={!canWrite}
                onClick={() => setImportOpen(true)}
                className="inline-flex items-center justify-center gap-1 py-1.5 px-2 rounded-md border border-slate-200 bg-slate-50 hover:bg-slate-100 text-slate-700 font-medium disabled:opacity-45"
              >
                <Import className="w-3.5 h-3.5 text-sky-600" /> 导入集合
              </button>
            </div>
            <div className="flex items-center justify-between text-[11px] px-0.5">
              <span className="text-slate-500 font-medium">接口目录树</span>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  disabled={!canWrite}
                  onClick={() => setRunnerModal({ open: true, folderId: '' })}
                  className="inline-flex items-center gap-1 text-emerald-700 hover:underline font-medium disabled:opacity-45"
                  title="批量回归测试全部接口"
                >
                  <ListChecks className="w-3 h-3" /> 批量测试
                </button>
                {canWrite && (
                  <button
                    type="button"
                    onClick={() => setFolderModal({ open: true, folder: null, parentId: '' })}
                    className="inline-flex items-center gap-1 text-sky-700 hover:underline font-medium"
                  >
                    <FolderPlus className="w-3 h-3" /> 新建目录
                  </button>
                )}
              </div>
            </div>
          </div>

          <div
            className="flex-1 overflow-y-auto tg-scroller p-1.5 space-y-0.5"
            onDragOver={(e) => e.preventDefault()}
            onDrop={(e) => {
              if (e.target === e.currentTarget && dragNodeId) {
                void handleDropIntoFolder('')
              }
            }}
          >
            {tree.children.length === 0 ? (
              <div className="text-center py-8 px-3 text-xs text-slate-400 space-y-2">
                <p>暂无已保存接口。</p>
                <p>可在地址栏直接粘贴 URL 或 cURL 调试，或点击「导入集合」。</p>
              </div>
            ) : (
              renderTreeNodes(filterTree(tree.children))
            )}
          </div>
        </aside>
      )}

      {/* Main Editor + Split Response Area */}
      <main className="flex-1 min-w-0 flex flex-col bg-white">
        {/* Multi-Tabs Bar + View Controls */}
        <div className="flex items-center justify-between gap-2 px-2 pt-1.5 bg-slate-100 border-b border-slate-200 shrink-0">
          <div className="flex items-center gap-1 overflow-x-auto min-w-0 flex-1">
            <button
              type="button"
              onClick={() => setTreeCollapsed((v) => !v)}
              title={treeCollapsed ? '展开接口目录树' : '收起接口目录树'}
              aria-label={treeCollapsed ? '展开接口目录树' : '收起接口目录树'}
              className="p-1.5 mb-1 text-slate-500 hover:text-slate-900 hover:bg-slate-200/70 rounded shrink-0"
            >
              {treeCollapsed ? <PanelLeftOpen className="w-4 h-4" /> : <PanelLeftClose className="w-4 h-4" />}
            </button>

            {tabs.map((tab) => {
              const isActiveTab = tab.tabKey === activeTab.tabKey
              return (
                <div
                  key={tab.tabKey}
                  onClick={() => setActiveTabKey(tab.tabKey)}
                  className={`group flex items-center gap-1.5 px-3 py-1.5 rounded-t-lg border text-xs cursor-pointer max-w-[200px] shrink-0 ${
                    isActiveTab
                      ? 'bg-white border-slate-200 border-b-white -mb-px text-slate-900 font-semibold'
                      : 'bg-slate-200/60 border-transparent text-slate-600 hover:bg-slate-200'
                  }`}
                >
                  <span className="font-mono text-[10px] font-bold text-sky-700">
                    {tab.api.request?.method || (tab.api.request?.protocol || 'GET').toUpperCase()}
                  </span>
                  <span className="truncate">{tab.api.name || '未命名'}</span>
                  {tab.dirty && <span className="w-1.5 h-1.5 rounded-full bg-amber-500 shrink-0" title="未保存更改" />}
                  <button
                    type="button"
                    aria-label="关闭标签页"
                    onClick={(e) => {
                      e.stopPropagation()
                      closeTab(tab.tabKey)
                    }}
                    className="text-slate-400 hover:text-slate-700 p-0.5 rounded"
                  >
                    <X className="w-3 h-3" />
                  </button>
                </div>
              )
            })}
            <button
              type="button"
              title="新建临时调试标签页"
              onClick={() => addScratchTab()}
              className="p-1.5 text-slate-500 hover:text-sky-700 rounded mb-1 shrink-0"
            >
              <Plus className="w-3.5 h-3.5" />
            </button>
          </div>

          {/* Layout Split Mode Toggle */}
          <div className="inline-flex items-center gap-0.5 bg-white border border-slate-200 rounded-md p-0.5 mb-1 shrink-0 text-[11px]">
            <button
              type="button"
              onClick={() => changeSplitMode('horizontal')}
              title="左右并排分屏"
              className={`inline-flex items-center gap-1 px-2 py-0.5 rounded ${
                splitMode === 'horizontal' ? 'bg-[#0B2A4A] text-white font-semibold' : 'text-slate-600 hover:bg-slate-100'
              }`}
            >
              <Columns2 className="w-3 h-3" />
              <span className="hidden md:inline">左右</span>
            </button>
            <button
              type="button"
              onClick={() => changeSplitMode('vertical')}
              title="上下分屏"
              className={`inline-flex items-center gap-1 px-2 py-0.5 rounded ${
                splitMode === 'vertical' ? 'bg-[#0B2A4A] text-white font-semibold' : 'text-slate-600 hover:bg-slate-100'
              }`}
            >
              <Rows2 className="w-3 h-3" />
              <span className="hidden md:inline">上下</span>
            </button>
          </div>
        </div>

        {/* URL & Actions Bar */}
        <div className="p-3 border-b border-slate-200 space-y-2 bg-white shrink-0">
          {!canWrite && (
            <div className="px-3 py-1.5 rounded-md bg-amber-50 border border-amber-200 text-amber-900 text-[11px]">
              当前账号角色为只读访客（viewer），可浏览目录与查看调试历史，但禁止发起实时调试、写入或发起压测。
            </div>
          )}
          <div className="flex items-center gap-2 text-xs">
            <span className="text-slate-400 shrink-0">接口名称：</span>
            <input
              type="text"
              aria-label="接口名称"
              value={activeTab.api.name || ''}
              onChange={(e) => updateActiveAPI((cur) => ({ ...cur, name: e.target.value }))}
              placeholder="输入接口名称"
              className="flex-1 max-w-sm px-2 py-1 rounded border border-transparent hover:border-slate-200 focus:border-sky-500 focus:outline-none font-medium text-slate-800 bg-slate-50/70 focus:bg-white"
            />
            {activeTab.api.id && (
              <span className="ml-auto font-mono text-[10px] text-slate-400 hidden sm:inline">
                ID: {activeTab.api.id}
              </span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <select
              aria-label="协议类型"
              value={protocol}
              onChange={(e) => {
                const nextProto = e.target.value as APIProtocol
                updateActiveAPI((cur) => ({
                  ...cur,
                  request: {
                    ...req,
                    protocol: nextProto,
                    method: nextProto === 'http' || nextProto === 'sse' ? req.method || 'GET' : '',
                  },
                }))
                if (nextProto === 'ws' || nextProto === 'grpc' || nextProto === 'sse') {
                  setReqSubTab('body')
                }
              }}
              className="px-2 py-2 rounded-lg border border-slate-300 bg-slate-100 font-mono text-xs font-semibold text-slate-800 focus:border-sky-500 focus:outline-none"
              title="选择请求协议：HTTP / WebSocket / gRPC / SSE"
            >
              <option value="http">HTTP</option>
              <option value="ws">WS</option>
              <option value="grpc">gRPC</option>
              <option value="sse">SSE</option>
            </select>

            {protocol === 'http' || protocol === 'sse' ? (
              <select
                aria-label="HTTP 方法"
                value={req.method || 'GET'}
                onChange={(e) =>
                  updateActiveAPI((cur) => ({
                    ...cur,
                    request: { ...req, method: e.target.value },
                  }))
                }
                className="px-2.5 py-2 rounded-lg border border-slate-300 bg-slate-50 font-mono text-xs font-bold text-slate-800 focus:border-sky-500 focus:outline-none"
              >
                {['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            ) : (
              <span
                className={`px-2.5 py-2 rounded-lg border font-mono text-xs font-bold ${
                  protocol === 'ws'
                    ? 'bg-purple-50 border-purple-200 text-purple-700'
                    : 'bg-indigo-50 border-indigo-200 text-indigo-700'
                }`}
              >
                {protocol === 'ws' ? 'WS' : 'gRPC'}
              </span>
            )}

            <VariableInput
              value={req.url || ''}
              variables={variableHints}
              onPaste={(e) => {
                const txt = e.clipboardData.getData('text')
                void handleURLPaste(txt, e)
              }}
              onChange={(nextURL) => {
                updateActiveAPI((cur) => ({
                  ...cur,
                  request: {
                    ...req,
                    url: nextURL,
                    path_params: syncPathParamsFromURL(nextURL, req.path_params),
                  },
                }))
              }}
              placeholder={
                protocol === 'ws'
                  ? '输入 WebSocket 地址 ws:// 或 wss://...（输入 {{ 补全变量）'
                  : protocol === 'grpc'
                    ? '输入 gRPC 目标地址，如 127.0.0.1:9090 或 grpc://host:9090'
                    : protocol === 'sse'
                      ? '输入 SSE 流式接口 URL，如 /api/events'
                      : '输入相对路径 /users/:id、完整 URL，输入 {{ 补全变量，或粘贴 cURL...'
              }
              className="px-3 py-2 rounded-lg border border-slate-300 font-mono text-xs focus:border-sky-500 focus:outline-none"
            />

            <button
              type="button"
              disabled={!canWrite || sending}
              onClick={() => void handleDebugSend()}
              className="inline-flex items-center gap-1.5 px-4 py-2 rounded-lg bg-[#0B2A4A] hover:bg-[#123d69] text-white text-xs font-semibold disabled:opacity-50"
            >
              <Play className="w-3.5 h-3.5 fill-current" />
              {sending ? '发送中...' : '发送调试'}
            </button>

            <button
              type="button"
              disabled={!canWrite || saving}
              onClick={() => void handleSave()}
              className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg border border-slate-300 bg-white hover:bg-slate-50 text-slate-700 text-xs font-medium disabled:opacity-45"
            >
              <Save className="w-3.5 h-3.5" /> {activeTab.api.id ? '保存' : '另存到目录'}
            </button>

            <button
              type="button"
              onClick={() => setCurlOpen(true)}
              className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg border border-slate-300 bg-white hover:bg-slate-50 text-slate-700 text-xs font-medium"
            >
              <Terminal className="w-3.5 h-3.5" /> cURL
            </button>

            <button
              type="button"
              disabled={!canWrite}
              onClick={() => onQuickLoadTest(activeTab.api)}
              className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg bg-amber-500 hover:bg-amber-600 text-slate-950 text-xs font-semibold disabled:opacity-45"
              title="将当前接口或草稿直接带入压测大盘发起压测"
            >
              <Zap className="w-3.5 h-3.5" /> 一键压测
            </button>
          </div>
        </div>

        {/* Resizable Split Request Config & Response Inspector */}
        <div
          ref={splitBoxRef}
          className={`flex-1 min-h-0 flex ${splitMode === 'horizontal' ? 'flex-row' : 'flex-col'} ${
            resizingSplit ? 'select-none' : ''
          }`}
        >
          {/* Left / Top: Request Composer Pane */}
          <div
            style={
              splitMode === 'horizontal'
                ? { width: `${splitPct}%` }
                : { height: `${splitPct}%` }
            }
            className="tg-split-pane flex flex-col min-h-0 min-w-0 shrink-0 overflow-hidden"
          >
            {/* Request Config Sub-tabs */}
            <div className="px-4 border-b border-slate-200 flex items-center justify-between gap-2 bg-slate-50/70 shrink-0">
              <div role="tablist" aria-label="请求配置页签" className="tg-tablist gap-1 text-xs font-medium min-w-0">
                {(
                  [
                    {
                      id: 'params' as const,
                      label: `Params`,
                      count: (req.path_params?.length || 0) + (req.query?.length || 0),
                    },
                    {
                      id: 'headers' as const,
                      label: protocol === 'grpc' ? 'Metadata' : 'Headers',
                      count: req.headers?.length || 0,
                    },
                    {
                      id: 'body' as const,
                      label:
                        protocol === 'ws'
                          ? 'WS 帧'
                          : protocol === 'grpc'
                            ? 'gRPC'
                            : protocol === 'sse'
                              ? 'SSE'
                              : `Body${body.type && body.type !== 'none' ? ` · ${body.type}` : ''}`,
                      count: protocol === 'ws' ? req.ws?.messages?.length || 0 : 0,
                    },
                    { id: 'auth' as const, label: `认证 · ${activeTab.api.auth?.type || 'inherit'}`, count: 0 },
                    { id: 'extract' as const, label: '提取', count: extracts.length },
                    { id: 'assert' as const, label: '断言', count: asserts.length },
                    {
                      id: 'scripts' as const,
                      label: '前后置脚本',
                      count: (activeTab.api.pre_script?.trim() ? 1 : 0) + (activeTab.api.post_script?.trim() ? 1 : 0),
                    },
                    { id: 'docs' as const, label: '文档', count: examples.length + responseFields.length },
                  ] as const
                ).map((st) => {
                  const selected = reqSubTab === st.id
                  return (
                    <button
                      key={st.id}
                      type="button"
                      role="tab"
                      aria-selected={selected}
                      onClick={() => setReqSubTab(st.id)}
                      className={`tg-tab inline-flex items-center gap-1.5 px-2.5 py-2 transition-colors ${
                        selected ? 'text-sky-700' : 'text-slate-600 hover:text-slate-900'
                      }`}
                    >
                      <span>{st.label}</span>
                      {st.count > 0 && (
                        <span className="tg-tabular px-1.5 py-0.2 rounded-full bg-sky-100 text-sky-800 text-[10px] font-mono">
                          {st.count}
                        </span>
                      )}
                    </button>
                  )
                })}
              </div>

              {reqSubTab === 'headers' && (
                <button
                  type="button"
                  onClick={() => void handleToggleEffectivePreview()}
                  className={`shrink-0 inline-flex items-center gap-1 px-2 py-1 rounded border text-[11px] font-medium ${
                    activeTab.showEffective
                      ? 'bg-sky-100 border-sky-300 text-sky-800'
                      : 'bg-white border-slate-300 text-slate-700 hover:bg-slate-50'
                  }`}
                >
                  <Eye className="w-3 h-3" /> {activeTab.showEffective ? '隐藏预览' : '生效预览'}
                </button>
              )}
            </div>

            <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-4 text-xs space-y-4">
            {reqSubTab === 'params' && (
              <div className="space-y-4">
                {(req.path_params?.length || 0) > 0 && (
                  <div>
                    <div className="font-semibold text-slate-700 mb-1.5">路径参数（URL 中的 `:参数名`）</div>
                    <KeyValueEditor
                      items={req.path_params || []}
                      onChange={(next) =>
                        updateActiveAPI((cur) => ({ ...cur, request: { ...req, path_params: next } }))
                      }
                      keyPlaceholder="路径参数名"
                      valuePlaceholder="替换值"
                    />
                  </div>
                )}
                <div>
                  <div className="font-semibold text-slate-700 mb-1.5">Query 查询参数</div>
                  <KeyValueEditor
                    items={req.query || []}
                    onChange={(next) => updateActiveAPI((cur) => ({ ...cur, request: { ...req, query: next } }))}
                    keyPlaceholder="Query 参数名"
                    valuePlaceholder="值（支持 {{变量}}）"
                  />
                </div>
              </div>
            )}

            {reqSubTab === 'headers' && (
              <div className="space-y-3">
                {activeTab.showEffective && activeTab.effective && (
                  <div className="rounded-lg border border-sky-200 bg-sky-50/60 p-3 space-y-2.5">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="font-semibold text-sky-900">生效请求预览（目录继承 + 环境变量合并）</span>
                      <span className="font-mono text-sky-800">
                        {activeTab.effective.method} {activeTab.effective.url}
                      </span>
                    </div>
                    <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                      <div className="bg-white rounded border border-sky-100 p-2.5">
                        <div className="font-medium text-slate-700 mb-1">
                          继承请求头（{activeTab.effective.inherited_headers.length}）
                        </div>
                        {activeTab.effective.inherited_headers.length === 0 ? (
                          <div className="text-slate-400">无目录继承请求头</div>
                        ) : (
                          <div className="space-y-1 font-mono">
                            {activeTab.effective.inherited_headers.map((ih, idx) => (
                              <div key={idx} className={`flex items-center gap-2 ${ih.overridden ? 'line-through text-slate-400' : ''}`}>
                                <span className="font-semibold">{ih.name}:</span>
                                <span className="truncate flex-1">{ih.value}</span>
                                <span className="text-[10px] px-1 rounded bg-slate-100 text-slate-500">
                                  {ih.overridden ? '已被本接口覆盖' : `来自 ${ih.source_folder_name}`}
                                </span>
                              </div>
                            ))}
                          </div>
                        )}
                      </div>
                      <div className="bg-white rounded border border-sky-100 p-2.5">
                        <div className="font-medium text-slate-700 mb-1">
                          生效鉴权来源：<code>{activeTab.effective.effective_auth.source}</code>
                          {activeTab.effective.effective_auth.source_folder_name
                            ? `（目录：${activeTab.effective.effective_auth.source_folder_name}）`
                            : ''}
                        </div>
                        <div className="font-medium text-slate-700 mt-2 mb-1">
                          可用变量来源（{activeTab.effective.variables.length}）
                        </div>
                        <div className="max-h-24 overflow-y-auto space-y-0.5 font-mono text-[11px]">
                          {activeTab.effective.variables.map((rv) => (
                            <div key={rv.name} className="flex items-center justify-between gap-2">
                              <span className="text-sky-800">{`{{${rv.name}}}`}</span>
                              <span className="truncate text-slate-600">{rv.secret ? '******' : rv.value}</span>
                              <span className="px-1 rounded bg-slate-100 text-slate-500 text-[10px]">{rv.source}</span>
                            </div>
                          ))}
                        </div>
                      </div>
                    </div>
                  </div>
                )}

                <KeyValueEditor
                  items={req.headers || []}
                  onChange={(next) => updateActiveAPI((cur) => ({ ...cur, request: { ...req, headers: next } }))}
                  keyPlaceholder="Header 名称"
                  valuePlaceholder="值"
                />
              </div>
            )}

            {reqSubTab === 'body' && (
              <div className="space-y-4">
                {protocol === 'ws' && (
                  <div className="rounded-lg border border-purple-200 bg-purple-50/40 p-3 space-y-3">
                    <div className="font-semibold text-purple-900">WebSocket 握手与消息帧设置</div>
                    <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                      <div>
                        <label className="block text-slate-600 mb-1">子协议 Subprotocols（逗号分隔）</label>
                        <input
                          type="text"
                          value={(req.ws?.subprotocols || []).join(',')}
                          onChange={(e) =>
                            updateWS({
                              subprotocols: e.target.value
                                .split(',')
                                .map((s) => s.trim())
                                .filter(Boolean),
                            })
                          }
                          placeholder="chat,json"
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-slate-600 mb-1">期望接收消息数 (recv_count)</label>
                        <input
                          type="number"
                          min={0}
                          value={req.ws?.recv_count ?? 1}
                          onChange={(e) => updateWS({ recv_count: Math.max(0, Number(e.target.value) || 0) })}
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-slate-600 mb-1">读写空闲超时 (idle_timeout)</label>
                        <input
                          type="text"
                          value={req.ws?.idle_timeout || ''}
                          onChange={(e) => updateWS({ idle_timeout: e.target.value || undefined })}
                          placeholder="默认 5s"
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                        />
                      </div>
                    </div>
                    <div className="space-y-2">
                      <div className="flex items-center justify-between">
                        <span className="font-medium text-slate-700">握手后发送的消息帧序列</span>
                        <button
                          type="button"
                          onClick={() =>
                            updateWS({
                              messages: [...(req.ws?.messages || []), { type: 'text', data: '{"type":"ping"}' }],
                            })
                          }
                          className="inline-flex items-center gap-1 text-sky-700 hover:underline font-medium"
                        >
                          <Plus className="w-3.5 h-3.5" /> 添加发送帧
                        </button>
                      </div>
                      {(req.ws?.messages || []).map((msg, i) => (
                        <div key={i} className="grid grid-cols-12 gap-2 items-start">
                          <select
                            value={msg.type || 'text'}
                            onChange={(e) =>
                              updateWS({
                                messages: (req.ws?.messages || []).map((m, idx) =>
                                  idx === i ? { ...m, type: e.target.value as 'text' | 'binary' } : m
                                ),
                              })
                            }
                            className="col-span-2 px-2 py-1.5 border border-slate-300 rounded bg-white"
                          >
                            <option value="text">Text 文本</option>
                            <option value="binary">Binary (Base64)</option>
                          </select>
                          <textarea
                            rows={2}
                            value={msg.data}
                            onChange={(e) =>
                              updateWS({
                                messages: (req.ws?.messages || []).map((m, idx) =>
                                  idx === i ? { ...m, data: e.target.value } : m
                                ),
                              })
                            }
                            placeholder='支持 {{变量}}，如 {"action":"subscribe"}'
                            className="col-span-9 px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                          />
                          <button
                            type="button"
                            onClick={() =>
                              updateWS({ messages: (req.ws?.messages || []).filter((_, idx) => idx !== i) })
                            }
                            className="col-span-1 justify-self-end p-1 text-slate-400 hover:text-rose-600"
                          >
                            <Trash2 className="w-3.5 h-3.5" />
                          </button>
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {protocol === 'grpc' && (
                  <div className="rounded-lg border border-indigo-200 bg-indigo-50/40 p-3 space-y-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="font-semibold text-indigo-900">gRPC 一元调用 (Unary) 契约与载荷</div>
                      <div className="flex flex-wrap items-center gap-2">
                        <select
                          value={req.grpc?.proto_file || ''}
                          onChange={(e) => {
                            const pf = e.target.value || undefined
                            updateGRPC({
                              proto_file: pf,
                              use_reflection: pf ? false : req.grpc?.use_reflection,
                              payload_encoding: pf ? 'proto_json' : req.grpc?.payload_encoding,
                              response_encoding: pf ? 'proto_json' : req.grpc?.response_encoding,
                            })
                          }}
                          className="px-2 py-1 border border-slate-300 rounded bg-white text-[11px]"
                        >
                          <option value="">不绑定 .proto/.desc 文件</option>
                          {files
                            .filter((f) => /\.(proto|pb|desc)$/i.test(f.name))
                            .map((f) => (
                              <option key={f.name} value={f.name}>
                                {f.name}
                              </option>
                            ))}
                        </select>
                        <label className="inline-flex items-center gap-1 text-[11px] text-slate-700 cursor-pointer">
                          <input
                            type="checkbox"
                            checked={Boolean(req.grpc?.use_reflection)}
                            onChange={(e) =>
                              updateGRPC({
                                use_reflection: e.target.checked,
                                payload_encoding: e.target.checked ? 'proto_json' : req.grpc?.payload_encoding,
                                response_encoding: e.target.checked ? 'proto_json' : req.grpc?.response_encoding,
                              })
                            }
                          />
                          <span>Server Reflection</span>
                        </label>
                        <button
                          type="button"
                          disabled={grpcDiscoverBusy}
                          onClick={() => void handleDiscoverGRPC()}
                          className="inline-flex items-center gap-1 px-2.5 py-1 rounded bg-indigo-600 hover:bg-indigo-700 text-white text-[11px] font-medium disabled:opacity-50"
                        >
                          <Sparkles className="w-3 h-3" /> {grpcDiscoverBusy ? '探测中...' : '探测服务/方法'}
                        </button>
                      </div>
                    </div>

                    {grpcCatalog && grpcCatalog.services?.length > 0 && (
                      <div className="rounded border border-indigo-200 bg-white p-2 flex flex-wrap items-center gap-2 text-[11px]">
                        <span className="text-slate-500 font-medium">快速选择：</span>
                        <select
                          value={req.grpc?.service || ''}
                          onChange={(e) => {
                            const svc = grpcCatalog.services.find((s) => s.name === e.target.value)
                            const firstM = svc?.methods?.[0]
                            updateGRPC({
                              service: e.target.value,
                              method: firstM?.name || '',
                              payload_encoding: 'proto_json',
                              response_encoding: 'proto_json',
                            })
                          }}
                          className="px-2 py-1 border border-slate-300 rounded font-mono bg-slate-50"
                        >
                          {grpcCatalog.services.map((s) => (
                            <option key={s.name} value={s.name}>
                              {s.name}
                            </option>
                          ))}
                        </select>
                        <select
                          value={req.grpc?.method || ''}
                          onChange={(e) =>
                            updateGRPC({
                              method: e.target.value,
                              payload_encoding: 'proto_json',
                              response_encoding: 'proto_json',
                            })
                          }
                          className="px-2 py-1 border border-slate-300 rounded font-mono bg-slate-50"
                        >
                          {(grpcCatalog.services.find((s) => s.name === req.grpc?.service)?.methods || []).map((m) => (
                            <option key={m.name} value={m.name}>
                              {m.name}
                            </option>
                          ))}
                        </select>
                        {(() => {
                          const mInfo = grpcCatalog.services
                            .find((s) => s.name === req.grpc?.service)
                            ?.methods?.find((m) => m.name === req.grpc?.method)
                          return mInfo?.sample_json ? (
                            <button
                              type="button"
                              onClick={() => updateBody({ type: 'json', content: mInfo.sample_json! })}
                              className="px-2 py-1 rounded bg-sky-50 hover:bg-sky-100 text-sky-700 border border-sky-200 font-medium"
                            >
                              填入示例 JSON
                            </button>
                          ) : null
                        })()}
                      </div>
                    )}

                    <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                      <div>
                        <label className="block text-slate-600 mb-1">服务名 (Service)</label>
                        <input
                          type="text"
                          value={req.grpc?.service || ''}
                          onChange={(e) => updateGRPC({ service: e.target.value })}
                          placeholder="example.Greeter"
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-slate-600 mb-1">方法名 (Method)</label>
                        <input
                          type="text"
                          value={req.grpc?.method || ''}
                          onChange={(e) => updateGRPC({ method: e.target.value })}
                          placeholder="SayHello"
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-slate-600 mb-1">调用超时 (timeout)</label>
                        <input
                          type="text"
                          value={req.grpc?.timeout || ''}
                          onChange={(e) => updateGRPC({ timeout: e.target.value || undefined })}
                          placeholder="默认 5s"
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-slate-600 mb-1">请求载荷编码</label>
                        <select
                          value={req.grpc?.payload_encoding || 'json'}
                          onChange={(e) => updateGRPC({ payload_encoding: e.target.value })}
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white"
                        >
                          <option value="proto_json">Proto JSON 动态编码（反射 / .proto）</option>
                          <option value="json">原始 JSON/UTF-8 直传</option>
                          <option value="proto_string">单字符串 Protobuf 封装</option>
                          <option value="base64">Raw Protobuf (Base64)</option>
                          <option value="hex">Raw Protobuf (Hex)</option>
                        </select>
                      </div>
                      <div>
                        <label className="block text-slate-600 mb-1">响应载荷编码</label>
                        <select
                          value={req.grpc?.response_encoding || 'raw'}
                          onChange={(e) => updateGRPC({ response_encoding: e.target.value })}
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white"
                        >
                          <option value="proto_json">Proto JSON 动态解码（反射 / .proto）</option>
                          <option value="raw">原始 UTF-8 响应</option>
                          <option value="proto_string">单字符串 Protobuf 解包</option>
                          <option value="base64">Raw Protobuf (Base64)</option>
                        </select>
                      </div>
                      <div className="grid grid-cols-2 gap-2">
                        <div>
                          <label className="block text-slate-600 mb-1">期望状态码</label>
                          <input
                            type="number"
                            min={0}
                            max={16}
                            value={req.grpc?.expected_status ?? ''}
                            onChange={(e) =>
                              updateGRPC({
                                expected_status: e.target.value === '' ? undefined : Number(e.target.value),
                              })
                            }
                            placeholder="0 (OK)"
                            className="w-full px-2 py-1.5 border border-slate-300 rounded bg-white font-mono"
                          />
                        </div>
                        <label className="flex items-center gap-1.5 pt-5 text-slate-700 cursor-pointer">
                          <input
                            type="checkbox"
                            checked={Boolean(req.grpc?.tls)}
                            onChange={(e) => updateGRPC({ tls: e.target.checked })}
                          />
                          <span>启用 TLS</span>
                        </label>
                      </div>
                    </div>
                    <div>
                      <label className="block text-slate-600 font-medium mb-1">
                        请求消息载荷（JSON 或 Base64，支持 `{'{{变量}}'}`）
                      </label>
                      <textarea
                        rows={6}
                        value={body.content || ''}
                        onChange={(e) => updateBody({ type: body.type && body.type !== 'none' ? body.type : 'json', content: e.target.value })}
                        placeholder='{\n  "name": "{{user}}"\n}'
                        className="w-full p-2.5 border border-slate-300 rounded bg-white font-mono text-xs"
                      />
                    </div>
                  </div>
                )}

                {protocol === 'sse' && (
                  <div className="rounded-lg border border-teal-200 bg-teal-50/40 p-3 space-y-3">
                    <div className="font-semibold text-teal-900">Server-Sent Events (SSE) 流读取选项</div>
                    <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                      <div>
                        <label className="block text-slate-600 mb-1">最多接收事件数 (max_events)</label>
                        <input
                          type="number"
                          min={0}
                          value={req.sse?.max_events ?? ''}
                          onChange={(e) =>
                            updateSSE({
                              max_events: e.target.value === '' ? undefined : Math.max(0, Number(e.target.value) || 0),
                            })
                          }
                          placeholder="0=不限制直至关流"
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-slate-600 mb-1">终止关键字 (stop_contains)</label>
                        <input
                          type="text"
                          value={req.sse?.stop_contains || ''}
                          onChange={(e) => updateSSE({ stop_contains: e.target.value || undefined })}
                          placeholder="[DONE]"
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-slate-600 mb-1">流超时 (timeout)</label>
                        <input
                          type="text"
                          value={req.sse?.timeout || ''}
                          onChange={(e) => updateSSE({ timeout: e.target.value || undefined })}
                          placeholder="默认 30s"
                          className="w-full px-2.5 py-1.5 border border-slate-300 rounded bg-white font-mono"
                        />
                      </div>
                    </div>
                  </div>
                )}

                {(protocol === 'http' || protocol === 'sse') && (
                  <div className="space-y-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex flex-wrap gap-1.5">
                    {(
                      [
                        { id: 'none' as const, label: 'None' },
                        { id: 'json' as const, label: 'JSON' },
                        { id: 'form' as const, label: 'x-www-form-urlencoded' },
                        { id: 'multipart' as const, label: 'multipart/form-data' },
                        { id: 'raw' as const, label: 'Raw 文本' },
                        { id: 'binary' as const, label: 'Binary 文件' },
                      ] as const
                    ).map((bt) => (
                      <button
                        key={bt.id}
                        type="button"
                        onClick={() => updateBody({ type: bt.id })}
                        className={`px-2.5 py-1 rounded border font-medium ${
                          (body.type || 'none') === bt.id
                            ? 'bg-sky-50 border-sky-400 text-sky-700'
                            : 'bg-white border-slate-200 text-slate-600 hover:bg-slate-50'
                        }`}
                      >
                        {bt.label}
                      </button>
                    ))}
                  </div>
                  {(body.type === 'json' || body.type === 'raw') && (
                    <button
                      type="button"
                      onClick={() => {
                        try {
                          const formatted = JSON.stringify(JSON.parse(body.content || ''), null, 2)
                          updateBody({ content: formatted })
                        } catch {
                          onToast('info', '当前内容不是合法纯 JSON（含 {{变量}} 时请直接编辑）')
                        }
                      }}
                      className="inline-flex items-center gap-1 px-2 py-1 rounded border border-slate-200 text-slate-600 hover:bg-slate-50"
                    >
                      <Sparkles className="w-3 h-3 text-sky-600" /> 美化 JSON
                    </button>
                  )}
                </div>

                {(!body.type || body.type === 'none') && (
                  <p className="text-slate-400">当前请求不携带请求体。</p>
                )}

                {body.type === 'json' && (
                  <textarea
                    rows={8}
                    value={body.content || ''}
                    onChange={(e) => updateBody({ content: e.target.value })}
                    placeholder='{\n  "user_id": "{{uid}}"\n}'
                    className="w-full p-3 border border-slate-300 rounded-lg font-mono text-xs focus:border-sky-500 focus:outline-none"
                  />
                )}

                {body.type === 'raw' && (
                  <div className="space-y-2">
                    <input
                      type="text"
                      value={body.content_type || ''}
                      onChange={(e) => updateBody({ content_type: e.target.value })}
                      placeholder="Content-Type，默认 text/plain"
                      className="w-64 px-2.5 py-1.5 border border-slate-300 rounded-md font-mono text-xs focus:border-sky-500 focus:outline-none"
                    />
                    <textarea
                      rows={7}
                      value={body.content || ''}
                      onChange={(e) => updateBody({ content: e.target.value })}
                      placeholder="输入任意文本请求体（支持 {{变量}}）"
                      className="w-full p-3 border border-slate-300 rounded-lg font-mono text-xs focus:border-sky-500 focus:outline-none"
                    />
                  </div>
                )}

                {body.type === 'form' && (
                  <KeyValueEditor
                    items={body.fields || []}
                    onChange={(next) => updateBody({ fields: next })}
                    keyPlaceholder="表单字段名"
                    valuePlaceholder="值"
                  />
                )}

                {body.type === 'multipart' && (
                  <div className="space-y-3">
                    <div>
                      <div className="font-medium text-slate-700 mb-1">文本表单字段</div>
                      <KeyValueEditor
                        items={body.fields || []}
                        onChange={(next) => updateBody({ fields: next })}
                        keyPlaceholder="字段名"
                        valuePlaceholder="字段值"
                      />
                    </div>
                    <div>
                      <div className="flex items-center justify-between mb-1">
                        <span className="font-medium text-slate-700">文件字段（引用项目已上传数据文件）</span>
                        <button
                          type="button"
                          onClick={() =>
                            updateBody({
                              files: [...(body.files || []), { field: 'file', path: files[0]?.name || '' }],
                            })
                          }
                          className="text-sky-600 hover:underline font-medium"
                        >
                          + 添加文件部件
                        </button>
                      </div>
                      {(body.files || []).map((bf, i) => (
                        <div key={i} className="flex items-center gap-2 mb-1.5">
                          <input
                            type="text"
                            value={bf.field}
                            placeholder="字段名 file"
                            onChange={(e) => {
                              const next = (body.files || []).map((x, j) =>
                                j === i ? { ...x, field: e.target.value } : x
                              )
                              updateBody({ files: next })
                            }}
                            className="w-36 px-2 py-1 border border-slate-300 rounded font-mono"
                          />
                          <select
                            value={bf.path}
                            onChange={(e) => {
                              const next = (body.files || []).map((x, j) =>
                                j === i ? { ...x, path: e.target.value } : x
                              )
                              updateBody({ files: next })
                            }}
                            className="flex-1 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                          >
                            <option value="">选择项目文件（请在「场景与数据」上传）</option>
                            {files.map((df) => (
                              <option key={df.name} value={df.name}>
                                {df.name}
                              </option>
                            ))}
                          </select>
                          <button
                            type="button"
                            onClick={() => updateBody({ files: (body.files || []).filter((_, j) => j !== i) })}
                            className="p-1 text-slate-400 hover:text-rose-600"
                          >
                            <Trash2 className="w-3.5 h-3.5" />
                          </button>
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {body.type === 'binary' && (
                  <div className="space-y-2 max-w-md">
                    <label className="block font-medium text-slate-700">选择项目文件作为二进制请求体</label>
                    <select
                      value={body.file || ''}
                      onChange={(e) => updateBody({ file: e.target.value })}
                      className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono bg-white"
                    >
                      <option value="">请选择文件...</option>
                      {files.map((df) => (
                        <option key={df.name} value={df.name}>
                          {df.name} ({Math.round(df.size / 1024)} KB)
                        </option>
                      ))}
                    </select>
                    <input
                      type="text"
                      value={body.content_type || ''}
                      onChange={(e) => updateBody({ content_type: e.target.value })}
                      placeholder="Content-Type，默认 application/octet-stream"
                      className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono"
                    />
                  </div>
                )}
                  </div>
                )}
              </div>
            )}

            {reqSubTab === 'auth' && (
              <AuthEditor
                value={activeTab.api.auth}
                onChange={(nextAuth) => updateActiveAPI((cur) => ({ ...cur, auth: nextAuth }))}
                allowInherit
              />
            )}

            {reqSubTab === 'extract' && (
              <div className="space-y-2">
                <p className="text-slate-500">
                  全部启用规则会在每次请求完成后依次求值并写入变量（也可直接点击下方 JSON 响应体字段自动生成）。
                </p>
                {extracts.map((ex, i) => (
                  <div key={i} className="grid grid-cols-12 gap-2 items-center p-2 rounded border border-slate-200 bg-slate-50/60">
                    <input
                      type="text"
                      value={ex.name}
                      placeholder="变量名"
                      onChange={(e) => {
                        const next: ExtractRule[] = extracts.map((row, idx) =>
                          idx === i ? { ...row, name: e.target.value } : row
                        )
                        updateActiveAPI((cur) => ({ ...cur, extract: next }))
                      }}
                      className="col-span-3 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                    />
                    <select
                      value={ex.from}
                      onChange={(e) => {
                        const next: ExtractRule[] = extracts.map((row, idx) =>
                          idx === i ? { ...row, from: e.target.value as ExtractRule['from'] } : row
                        )
                        updateActiveAPI((cur) => ({ ...cur, extract: next }))
                      }}
                      className="col-span-2 px-2 py-1 border border-slate-300 rounded bg-white"
                    >
                      <option value="jsonpath">JSONPath</option>
                      <option value="xpath">XPath / HTML</option>
                      <option value="regex">正则 (Regex)</option>
                      <option value="header">响应头 (Header)</option>
                    </select>
                    <input
                      type="text"
                      value={ex.expr}
                      placeholder={
                        ex.from === 'xpath'
                          ? '//meta[@name="csrf"]/@content'
                          : ex.from === 'regex'
                            ? '"token":"([^"]+)"'
                            : ex.from === 'header'
                              ? 'X-Request-Id'
                              : '$.data.token'
                      }
                      onChange={(e) => {
                        const next: ExtractRule[] = extracts.map((row, idx) =>
                          idx === i ? { ...row, expr: e.target.value } : row
                        )
                        updateActiveAPI((cur) => ({ ...cur, extract: next }))
                      }}
                      className="col-span-4 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                    />
                    <input
                      type="text"
                      value={ex.default || ''}
                      placeholder="默认值（可选）"
                      onChange={(e) => {
                        const next: ExtractRule[] = extracts.map((row, idx) =>
                          idx === i ? { ...row, default: e.target.value || undefined } : row
                        )
                        updateActiveAPI((cur) => ({ ...cur, extract: next }))
                      }}
                      className="col-span-2 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                    />
                    <button
                      type="button"
                      onClick={() => updateActiveAPI((cur) => ({ ...cur, extract: extracts.filter((_, idx) => idx !== i) }))}
                      className="col-span-1 justify-self-end p-1 text-slate-400 hover:text-rose-600"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                ))}
                <button
                  type="button"
                  onClick={() =>
                    updateActiveAPI((cur) => ({
                      ...cur,
                      extract: [...extracts, { name: 'token', from: 'jsonpath', expr: '$.data.token' }],
                    }))
                  }
                  className="inline-flex items-center gap-1 text-sky-600 hover:underline font-medium"
                >
                  <Plus className="w-3.5 h-3.5" /> 添加提取规则
                </button>
              </div>
            )}

            {reqSubTab === 'assert' && (
              <div className="space-y-2">
                {asserts.map((as, i) => {
                  const updateAs = (patch: Partial<AssertRule>) => {
                    const next = asserts.map((row, idx) => (idx === i ? { ...row, ...patch } : row))
                    updateActiveAPI((cur) => ({ ...cur, assert: next }))
                  }
                  return (
                    <div
                      key={i}
                      className="grid grid-cols-12 gap-2 items-center p-2 rounded border border-slate-200 bg-slate-50/60"
                    >
                      <select
                        value={as.type}
                        onChange={(e) => {
                          const t = e.target.value as AssertRule['type']
                          if (t === 'status') updateAs({ type: t, equals: '200', max: undefined, value: undefined, expr: undefined, contains: undefined, exists: undefined })
                          else if (t === 'duration') updateAs({ type: t, max: '500ms', equals: undefined, value: undefined, expr: undefined, contains: undefined, exists: undefined })
                          else if (t === 'body_contains') updateAs({ type: t, value: 'ok', equals: undefined, max: undefined, expr: undefined, contains: undefined, exists: undefined })
                          else if (t === 'json_schema') updateAs({ type: t, value: '{"type":"object"}', equals: undefined, max: undefined, expr: undefined, contains: undefined, exists: undefined })
                          else updateAs({ type: t, expr: '$.code', equals: '0', max: undefined, value: undefined })
                        }}
                        className="col-span-3 px-2 py-1 border border-slate-300 rounded bg-white"
                      >
                        <option value="status">HTTP 状态码 (status)</option>
                        <option value="duration">最大耗时 (duration)</option>
                        <option value="body_contains">响应体包含 (body_contains)</option>
                        <option value="jsonpath">JSONPath 校验</option>
                        <option value="json_schema">JSON Schema 校验</option>
                      </select>

                      {as.type === 'status' && (
                        <input
                          type="text"
                          value={as.in ? as.in.join(',') : as.equals || '200'}
                          placeholder="状态码（如 200 或 200,201）"
                          onChange={(e) => {
                            const raw = e.target.value.trim()
                            if (raw.includes(',')) {
                              const nums = raw
                                .split(',')
                                .map((s) => Number(s.trim()))
                                .filter((n) => Number.isFinite(n) && n > 0)
                              updateAs({ equals: undefined, in: nums })
                            } else {
                              updateAs({ equals: raw, in: undefined })
                            }
                          }}
                          className="col-span-8 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                        />
                      )}

                      {as.type === 'duration' && (
                        <input
                          type="text"
                          value={as.max || ''}
                          placeholder="最大允许耗时，例如 500ms 或 1.5s"
                          onChange={(e) => updateAs({ max: e.target.value })}
                          className="col-span-8 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                        />
                      )}

                      {as.type === 'body_contains' && (
                        <input
                          type="text"
                          value={as.value || ''}
                          placeholder="必须包含的子串"
                          onChange={(e) => updateAs({ value: e.target.value })}
                          className="col-span-8 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                        />
                      )}

                      {as.type === 'jsonpath' && (
                        <>
                          <input
                            type="text"
                            value={as.expr || ''}
                            placeholder="JSONPath，例如 $.code"
                            onChange={(e) => updateAs({ expr: e.target.value })}
                            className="col-span-4 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                          />
                          <input
                            type="text"
                            value={as.equals !== undefined ? as.equals : as.contains || ''}
                            placeholder="期望相等值（equals）"
                            onChange={(e) => updateAs({ equals: e.target.value, contains: undefined, exists: undefined })}
                            className="col-span-4 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                          />
                        </>
                      )}

                      {as.type === 'json_schema' && (
                        <textarea
                          rows={2}
                          value={as.value || ''}
                          placeholder='{"type":"object","required":["code","data"]}'
                          onChange={(e) => updateAs({ value: e.target.value })}
                          className="col-span-8 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                        />
                      )}

                      <button
                        type="button"
                        onClick={() => updateActiveAPI((cur) => ({ ...cur, assert: asserts.filter((_, idx) => idx !== i) }))}
                        className="col-span-1 justify-self-end p-1 text-slate-400 hover:text-rose-600"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  )
                })}
                <button
                  type="button"
                  onClick={() =>
                    updateActiveAPI((cur) => ({
                      ...cur,
                      assert: [...asserts, { type: 'status', equals: '200' }],
                    }))
                  }
                  className="inline-flex items-center gap-1 text-sky-600 hover:underline font-medium"
                >
                  <Plus className="w-3.5 h-3.5" /> 添加断言规则
                </button>
              </div>
            )}

            {reqSubTab === 'scripts' && (
              <div className="space-y-5">
                <div className="rounded-lg border border-sky-200 bg-sky-50/60 p-3 text-[11px] text-sky-900 leading-relaxed">
                  内置安全沙箱脚本支持读写场景变量 <code className="font-mono font-semibold">vars.name</code>、时间戳与加密函数（
                  <code className="font-mono">now_sec()</code> / <code className="font-mono">uuid()</code> /{' '}
                  <code className="font-mono">hmac_sha256(key, data)</code>）；后置脚本额外支持响应检查{' '}
                  <code className="font-mono">resp.status</code>、<code className="font-mono">resp.header("X-Req-Id")</code>、{' '}
                  <code className="font-mono">resp.json("$.data.id")</code>、<code className="font-mono">jsonpath(s, expr)</code> 与{' '}
                  <code className="font-mono">assert(cond, msg)</code>。
                </div>

                <div className="space-y-2">
                  <div>
                    <h4 className="font-semibold text-slate-800">前置脚本 (Pre-request Script)</h4>
                    <p className="text-[11px] text-slate-500">在发送请求与渲染请求模板前执行，可将签名/时间戳写入变量供请求引用</p>
                  </div>
                  <CodeEditor
                    ariaLabel="前置脚本编辑器"
                    rows={6}
                    disabled={!canWrite}
                    value={activeTab.api.pre_script || ''}
                    onChange={(next) => updateActiveAPI((cur) => ({ ...cur, pre_script: next }))}
                    placeholder={'// 例如生成时间戳与签名：\nvars.ts = to_string(now_sec());\nvars.sig = hmac_sha256("secret", vars.ts);'}
                    snippets={[
                      {
                        label: 'HMAC-SHA256 签名',
                        code: 'vars.ts = to_string(now_sec());\nvars.sig = hmac_sha256("secret", vars.ts);',
                      },
                      { label: '生成 UUID', code: 'vars.req_id = uuid();' },
                      { label: '毫秒时间戳', code: 'vars.nonce_ms = to_string(now_ms());' },
                      { label: 'Base64 编码', code: 'vars.auth_b64 = base64_encode("user:pass");' },
                    ]}
                  />
                </div>

                <div className="space-y-2">
                  <div>
                    <h4 className="font-semibold text-slate-800">后置脚本 (Post-response Script)</h4>
                    <p className="text-[11px] text-slate-500">在提取与断言之后执行，可直接读取 resp 状态码、响应头和 JSON 字段并进行脚本断言</p>
                  </div>
                  <CodeEditor
                    ariaLabel="后置脚本编辑器"
                    rows={6}
                    disabled={!canWrite}
                    value={activeTab.api.post_script || ''}
                    onChange={(next) => updateActiveAPI((cur) => ({ ...cur, post_script: next }))}
                    placeholder={'// 例如校验响应并提取 token：\nassert(resp.status == 200, "响应状态必须为 200");\nvars.token = resp.json("$.token");'}
                    snippets={[
                      { label: '断言状态 200', code: 'assert(resp.status == 200, "状态码必须为 200");' },
                      { label: 'JSONPath 提取', code: 'vars.token = resp.json("$.data.token");' },
                      { label: '读取响应头', code: 'vars.trace_id = resp.header("X-Trace-Id");' },
                      { label: '断言业务码', code: 'assert(resp.json("$.code") == "0", "业务 code 异常");' },
                    ]}
                  />
                </div>
              </div>
            )}

            {reqSubTab === 'docs' && (
              <div className="space-y-4">
                <div>
                  <label className="block font-semibold text-slate-700 mb-1">接口用途说明（导出 OpenAPI 文档描述）</label>
                  <textarea
                    rows={2}
                    value={activeTab.api.description || ''}
                    onChange={(e) => updateActiveAPI((cur) => ({ ...cur, description: e.target.value }))}
                    placeholder="简要说明接口用途、调用前提和注意事项..."
                    className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                  />
                </div>

                <div className="space-y-2">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="font-semibold text-slate-700">响应示例（OpenAPI Examples / Mock 参考）</span>
                    <div className="flex items-center gap-2">
                      {activeTab.debugResp?.result && (
                        <button
                          type="button"
                          onClick={() => {
                            const res = activeTab.debugResp!.result
                            const item: APIResponseExample = {
                              name: `响应 ${res.status || 200}`,
                              status: res.status || 200,
                              content_type: res.content_type || 'application/json',
                              body: res.body || '',
                            }
                            updateActiveAPI((cur) => ({ ...cur, examples: [...examples, item] }))
                            onToast('success', '已从最近一次调试响应保存示例')
                          }}
                          className="inline-flex items-center gap-1 px-2 py-1 rounded border border-sky-300 bg-sky-50 text-sky-800 hover:bg-sky-100 font-medium"
                        >
                          <Sparkles className="w-3 h-3" /> 将当前调试响应存为示例
                        </button>
                      )}
                      <button
                        type="button"
                        onClick={() =>
                          updateActiveAPI((cur) => ({
                            ...cur,
                            examples: [
                              ...examples,
                              { name: '成功响应', status: 200, content_type: 'application/json', body: '{\n  "ok": true\n}' },
                            ],
                          }))
                        }
                        className="inline-flex items-center gap-1 text-sky-600 hover:underline font-medium"
                      >
                        <Plus className="w-3.5 h-3.5" /> 添加示例
                      </button>
                    </div>
                  </div>
                  {examples.length === 0 ? (
                    <p className="text-slate-400">暂无响应示例。可在调试接口后一键保存，或手动添加。</p>
                  ) : (
                    examples.map((ex, i) => (
                      <div key={i} className="p-2.5 rounded-lg border border-slate-200 bg-slate-50/70 space-y-2">
                        <div className="grid grid-cols-12 gap-2 items-center">
                          <input
                            type="text"
                            value={ex.name}
                            placeholder="示例名称"
                            onChange={(e) =>
                              updateActiveAPI((cur) => ({
                                ...cur,
                                examples: examples.map((x, idx) => (idx === i ? { ...x, name: e.target.value } : x)),
                              }))
                            }
                            className="col-span-4 px-2 py-1 border border-slate-300 rounded bg-white"
                          />
                          <input
                            type="number"
                            value={ex.status}
                            placeholder="状态码"
                            onChange={(e) =>
                              updateActiveAPI((cur) => ({
                                ...cur,
                                examples: examples.map((x, idx) =>
                                  idx === i ? { ...x, status: Number(e.target.value) || 200 } : x
                                ),
                              }))
                            }
                            className="col-span-2 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                          />
                          <input
                            type="text"
                            value={ex.content_type || ''}
                            placeholder="application/json"
                            onChange={(e) =>
                              updateActiveAPI((cur) => ({
                                ...cur,
                                examples: examples.map((x, idx) =>
                                  idx === i ? { ...x, content_type: e.target.value } : x
                                ),
                              }))
                            }
                            className="col-span-5 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                          />
                          <button
                            type="button"
                            onClick={() =>
                              updateActiveAPI((cur) => ({ ...cur, examples: examples.filter((_, idx) => idx !== i) }))
                            }
                            className="col-span-1 justify-self-end p-1 text-slate-400 hover:text-rose-600"
                          >
                            <Trash2 className="w-3.5 h-3.5" />
                          </button>
                        </div>
                        <textarea
                          rows={3}
                          value={ex.body || ''}
                          onChange={(e) =>
                            updateActiveAPI((cur) => ({
                              ...cur,
                              examples: examples.map((x, idx) => (idx === i ? { ...x, body: e.target.value } : x)),
                            }))
                          }
                          className="w-full px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                        />
                      </div>
                    ))
                  )}
                </div>

                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <span className="font-semibold text-slate-700">响应字段文档（用于 OpenAPI Schema 导出）</span>
                    <button
                      type="button"
                      onClick={() =>
                        updateActiveAPI((cur) => ({
                          ...cur,
                          response_fields: [
                            ...responseFields,
                            { path: 'data.id', type: 'string', required: true, description: '' },
                          ] as APIFieldDoc[],
                        }))
                      }
                      className="inline-flex items-center gap-1 text-sky-600 hover:underline font-medium"
                    >
                      <Plus className="w-3.5 h-3.5" /> 添加字段说明
                    </button>
                  </div>
                  {responseFields.length === 0 ? (
                    <p className="text-slate-400">暂无响应字段文档定义。</p>
                  ) : (
                    responseFields.map((fd, i) => (
                      <div key={i} className="grid grid-cols-12 gap-2 items-center p-2 rounded border border-slate-200 bg-slate-50/60">
                        <input
                          type="text"
                          value={fd.path}
                          placeholder="字段路径，如 data.token"
                          onChange={(e) =>
                            updateActiveAPI((cur) => ({
                              ...cur,
                              response_fields: responseFields.map((x, idx) =>
                                idx === i ? { ...x, path: e.target.value } : x
                              ),
                            }))
                          }
                          className="col-span-3 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                        />
                        <select
                          value={fd.type || 'string'}
                          onChange={(e) =>
                            updateActiveAPI((cur) => ({
                              ...cur,
                              response_fields: responseFields.map((x, idx) =>
                                idx === i ? { ...x, type: e.target.value } : x
                              ),
                            }))
                          }
                          className="col-span-2 px-2 py-1 border border-slate-300 rounded bg-white"
                        >
                          <option value="string">string</option>
                          <option value="number">number</option>
                          <option value="integer">integer</option>
                          <option value="boolean">boolean</option>
                          <option value="object">object</option>
                          <option value="array">array</option>
                        </select>
                        <label className="col-span-2 inline-flex items-center gap-1 text-slate-700 cursor-pointer">
                          <input
                            type="checkbox"
                            checked={Boolean(fd.required)}
                            onChange={(e) =>
                              updateActiveAPI((cur) => ({
                                ...cur,
                                response_fields: responseFields.map((x, idx) =>
                                  idx === i ? { ...x, required: e.target.checked } : x
                                ),
                              }))
                            }
                          />
                          <span>必填</span>
                        </label>
                        <input
                          type="text"
                          value={fd.description || ''}
                          placeholder="字段业务含义说明"
                          onChange={(e) =>
                            updateActiveAPI((cur) => ({
                              ...cur,
                              response_fields: responseFields.map((x, idx) =>
                                idx === i ? { ...x, description: e.target.value } : x
                              ),
                            }))
                          }
                          className="col-span-4 px-2 py-1 border border-slate-300 rounded bg-white"
                        />
                        <button
                          type="button"
                          onClick={() =>
                            updateActiveAPI((cur) => ({
                              ...cur,
                              response_fields: responseFields.filter((_, idx) => idx !== i),
                            }))
                          }
                          className="col-span-1 justify-self-end p-1 text-slate-400 hover:text-rose-600"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    ))
                  )}
                </div>
              </div>
            )}
            </div>
          </div>

          {/* Draggable & Keyboard-Accessible Split Separator */}
          <div
            role="separator"
            tabIndex={0}
            aria-orientation={splitMode === 'horizontal' ? 'vertical' : 'horizontal'}
            aria-label="拖拽或按方向键调整请求与响应分屏比例"
            aria-valuenow={Math.round(splitPct)}
            aria-valuemin={28}
            aria-valuemax={74}
            onPointerDown={(e) => {
              e.preventDefault()
              setResizingSplit(true)
            }}
            onKeyDown={(e) => {
              if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
                e.preventDefault()
                updateSplitPct(splitPct - 4)
              } else if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
                e.preventDefault()
                updateSplitPct(splitPct + 4)
              }
            }}
            className={`shrink-0 bg-slate-200 hover:bg-sky-500 focus:bg-sky-600 focus:outline-none transition-colors flex items-center justify-center ${
              splitMode === 'horizontal' ? 'w-1.5 cursor-col-resize' : 'h-1.5 cursor-row-resize'
            }`}
          />

          {/* Right / Bottom: Response Telemetry Inspector */}
          <div className="tg-split-pane flex-1 min-w-0 min-h-0 flex flex-col overflow-hidden">
            <ResponsePanel
              projectId={projectId}
              activeEnvId={activeEnvId}
              debugResp={activeTab.debugResp}
              historyList={history}
              loadingDebug={sending}
              autoSyncTempVars={autoSyncTempVars}
              onToggleAutoSync={setAutoSyncTempVars}
              onPickExtractJSONPath={addExtractFromJSON}
              onPickAssertJSONPath={addAssertFromJSON}
              onPushTempVars={(vars) => {
                onTempVarsChange({ ...tempVars, ...vars })
                onToast('success', '已写入顶部临时变量池')
              }}
              onSaveEnvVars={canWrite ? handleSaveEnvVars : undefined}
              onRefreshHistory={async () => {
                try {
                  setHistory(await api.listDebugHistory(projectId, 40, false))
                } catch {
                  // ignore
                }
              }}
              onLoadHistoryRecord={loadHistoryIntoPanel}
              onClearHistory={async () => {
                try {
                  await api.clearDebugHistory(projectId)
                  setHistory([])
                  onToast('success', '已清空项目调试历史')
                } catch (err) {
                  onToast('error', err instanceof APIError ? err.message : '清空历史失败')
                }
              }}
            />
          </div>
        </div>
      </main>

      <ImportModal
        open={importOpen}
        projectId={projectId}
        treeNodes={tree.children}
        onClose={() => setImportOpen(false)}
        onApplied={async () => {
          await loadSidebarData()
          await onWorkspaceMutated()
        }}
        onToast={onToast}
      />

      <FolderModal
        open={folderModal.open}
        projectId={projectId}
        folder={folderModal.folder}
        parentId={folderModal.parentId}
        onClose={() => setFolderModal({ open: false, folder: null, parentId: '' })}
        onSaved={loadSidebarData}
        onToast={onToast}
      />

      <CurlModal
        open={curlOpen}
        projectId={projectId}
        apiDraft={activeTab.api}
        environmentId={activeEnvId}
        tempVars={tempVars}
        onClose={() => setCurlOpen(false)}
        onToast={onToast}
      />

      <SaveAPIModal
        open={saveModalOpen}
        initialName={activeTab.api.name || '未命名接口'}
        initialFolderId={activeTab.api.folder_id || ''}
        treeNodes={tree.children}
        onClose={() => setSaveModalOpen(false)}
        onConfirm={handleConfirmCreateSaved}
      />

      <CollectionRunnerModal
        open={runnerModal.open}
        projectId={projectId}
        treeNodes={tree.children}
        environments={environments}
        initialFolderId={runnerModal.folderId}
        initialEnvId={activeEnvId}
        tempVars={tempVars}
        canWrite={canWrite}
        onClose={() => setRunnerModal({ open: false, folderId: '' })}
        onToast={onToast}
      />
    </div>
  )
}
