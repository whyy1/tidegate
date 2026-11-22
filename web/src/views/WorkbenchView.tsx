import { useEffect, useRef, useState } from 'react'
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  Copy,
  Eye,
  FilePlus,
  Folder as FolderIcon,
  FolderOpen,
  FolderPlus,
  Import,
  Pencil,
  Play,
  Plus,
  Save,
  Search,
  Sparkles,
  Terminal,
  Trash2,
  X,
  Zap,
} from 'lucide-react'
import { AuthEditor } from '../components/AuthEditor'
import { CurlModal } from '../components/CurlModal'
import { FolderModal } from '../components/FolderModal'
import { ImportModal } from '../components/ImportModal'
import { KeyValueEditor } from '../components/KeyValueEditor'
import { ResponsePanel } from '../components/ResponsePanel'
import { SaveAPIModal } from '../components/SaveAPIModal'
import { api, APIError } from '../lib/api'
import type {
  API,
  AssertRule,
  DataFile,
  DebugAPIResponse,
  DebugRecord,
  EffectiveAPIPreview,
  ExtractRule,
  Folder,
  KeyValue,
  Tree,
  TreeNode,
} from '../lib/types'

interface Props {
  projectId: string
  activeEnvId: string
  tempVars: Record<string, string>
  onTempVarsChange: (next: Record<string, string>) => void
  onWorkspaceMutated: () => Promise<void>
  onQuickLoadTest: (apiDraft: Partial<API>) => void
  incomingReplay?: { seq: number; api: Partial<API> } | null
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

type ReqSubTab = 'params' | 'headers' | 'body' | 'auth' | 'extract' | 'assert'

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
  activeEnvId,
  tempVars,
  onTempVarsChange,
  onWorkspaceMutated,
  onQuickLoadTest,
  incomingReplay,
  onToast,
}: Props) {
  const [tree, setTree] = useState<Tree>({ children: [] })
  const [files, setFiles] = useState<DataFile[]>([])
  const [history, setHistory] = useState<DebugRecord[]>([])
  const [search, setSearch] = useState('')
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})

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
    modalOpen: false,
  })
  actionsRef.current = {
    save: handleSave,
    send: handleDebugSend,
    modalOpen: importOpen || curlOpen || saveModalOpen || folderModal.open,
  }

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (actionsRef.current.modalOpen) return
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
  const body = req.body || { type: 'none' }
  const extracts = activeTab.api.extract || []
  const asserts = activeTab.api.assert || []

  const updateBody = (patch: Partial<NonNullable<API['request']['body']>>) => {
    updateActiveAPI((cur) => ({
      ...cur,
      request: {
        ...req,
        body: { ...body, ...patch },
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
    <div className="flex flex-1 min-h-0 bg-slate-100">
      {/* Left API Tree */}
      <aside className="w-68 bg-slate-50 border-r border-slate-200 flex flex-col shrink-0">
        <div className="p-2.5 border-b border-slate-200 space-y-2">
          <div className="relative">
            <Search className="w-3.5 h-3.5 text-slate-400 absolute left-2.5 top-2" />
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="搜索接口名称、路径或方法..."
              className="w-full pl-8 pr-2.5 py-1.5 text-xs bg-white border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
            />
          </div>
          <div className="grid grid-cols-2 gap-1.5 text-xs">
            <button
              type="button"
              onClick={() => addScratchTab()}
              className="inline-flex items-center justify-center gap-1 py-1.5 px-2 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium"
            >
              <FilePlus className="w-3.5 h-3.5" /> 新建 / 调试
            </button>
            <button
              type="button"
              onClick={() => setImportOpen(true)}
              className="inline-flex items-center justify-center gap-1 py-1.5 px-2 rounded-md border border-slate-300 bg-white hover:bg-slate-100 text-slate-700 font-medium"
            >
              <Import className="w-3.5 h-3.5 text-sky-600" /> 导入接口
            </button>
          </div>
          <div className="flex items-center justify-between text-[11px] px-0.5">
            <span className="text-slate-500 font-medium">接口目录树</span>
            <button
              type="button"
              onClick={() => setFolderModal({ open: true, folder: null, parentId: '' })}
              className="inline-flex items-center gap-1 text-sky-700 hover:underline font-medium"
            >
              <FolderPlus className="w-3 h-3" /> 新建目录
            </button>
          </div>
        </div>

        <div
          className="flex-1 overflow-y-auto p-1.5 space-y-0.5"
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
              <p>可直接在右侧粘贴完整 URL 或 cURL 发起调试，或点击「导入接口」。</p>
            </div>
          ) : (
            renderTreeNodes(filterTree(tree.children))
          )}
        </div>
      </aside>

      {/* Main Editor + Response Area */}
      <main className="flex-1 min-w-0 flex flex-col bg-white">
        {/* Multi-Tabs Bar */}
        <div className="flex items-center gap-1 px-2 pt-1.5 bg-slate-100 border-b border-slate-200 overflow-x-auto shrink-0">
          {tabs.map((tab) => {
            const isActive = tab.tabKey === activeTab.tabKey
            return (
              <div
                key={tab.tabKey}
                onClick={() => setActiveTabKey(tab.tabKey)}
                className={`group flex items-center gap-1.5 px-3 py-1.5 rounded-t-lg border text-xs cursor-pointer max-w-[200px] shrink-0 ${
                  isActive
                    ? 'bg-white border-slate-200 border-b-white -mb-px text-slate-900 font-semibold'
                    : 'bg-slate-200/60 border-transparent text-slate-600 hover:bg-slate-200'
                }`}
              >
                <span className="font-mono text-[10px] font-bold text-sky-700">
                  {tab.api.request?.method || 'GET'}
                </span>
                <span className="truncate">{tab.api.name || '未命名'}</span>
                {tab.dirty && <span className="w-1.5 h-1.5 rounded-full bg-amber-500 shrink-0" title="未保存更改" />}
                <button
                  type="button"
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
            className="p-1.5 text-slate-500 hover:text-sky-700 rounded mb-1"
          >
            <Plus className="w-3.5 h-3.5" />
          </button>
        </div>

        {/* URL & Actions Bar */}
        <div className="p-3 border-b border-slate-200 space-y-2 bg-white shrink-0">
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

            <input
              type="text"
              value={req.url || ''}
              onPaste={(e) => {
                const txt = e.clipboardData.getData('text')
                void handleURLPaste(txt, e)
              }}
              onChange={(e) => {
                const nextURL = e.target.value
                updateActiveAPI((cur) => ({
                  ...cur,
                  request: {
                    ...req,
                    url: nextURL,
                    path_params: syncPathParamsFromURL(nextURL, req.path_params),
                  },
                }))
              }}
              placeholder="输入相对路径 /users/:id、完整 URL，或直接粘贴 curl 命令..."
              className="flex-1 min-w-[240px] px-3 py-2 rounded-lg border border-slate-300 font-mono text-xs focus:border-sky-500 focus:outline-none"
            />

            <button
              type="button"
              disabled={sending}
              onClick={() => void handleDebugSend()}
              className="inline-flex items-center gap-1.5 px-4 py-2 rounded-lg bg-[#0B2A4A] hover:bg-[#123d69] text-white text-xs font-semibold disabled:opacity-50"
            >
              <Play className="w-3.5 h-3.5 fill-current" />
              {sending ? '发送中...' : '发送调试'}
            </button>

            <button
              type="button"
              disabled={saving}
              onClick={() => void handleSave()}
              className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg border border-slate-300 bg-white hover:bg-slate-50 text-slate-700 text-xs font-medium"
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
              onClick={() => onQuickLoadTest(activeTab.api)}
              className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg bg-amber-500 hover:bg-amber-600 text-slate-950 text-xs font-semibold"
              title="将当前接口或草稿直接带入压测大盘发起压测"
            >
              <Zap className="w-3.5 h-3.5" /> 一键压测
            </button>
          </div>
        </div>

        {/* Split Request Config & Response Panel */}
        <div className="flex-1 min-h-0 overflow-y-auto flex flex-col">
          {/* Request Config Sub-tabs */}
          <div className="px-4 pt-2 border-b border-slate-200 flex flex-wrap items-center justify-between gap-2 bg-slate-50/50">
            <div className="flex items-center gap-1 text-xs font-medium">
              {(
                [
                  {
                    id: 'params' as const,
                    label: `Params (${(req.path_params?.length || 0) + (req.query?.length || 0)})`,
                  },
                  { id: 'headers' as const, label: `Headers (${req.headers?.length || 0})` },
                  { id: 'body' as const, label: `Body (${body.type || 'none'})` },
                  { id: 'auth' as const, label: `Auth (${activeTab.api.auth?.type || 'inherit'})` },
                  { id: 'extract' as const, label: `提取器 (${extracts.length})` },
                  { id: 'assert' as const, label: `断言 (${asserts.length})` },
                ] as const
              ).map((st) => (
                <button
                  key={st.id}
                  type="button"
                  onClick={() => setReqSubTab(st.id)}
                  className={`px-3 py-2 border-b-2 transition-colors ${
                    reqSubTab === st.id
                      ? 'border-sky-500 text-sky-700 font-semibold'
                      : 'border-transparent text-slate-600 hover:text-slate-900'
                  }`}
                >
                  {st.label}
                </button>
              ))}
            </div>

            {reqSubTab === 'headers' && (
              <button
                type="button"
                onClick={() => void handleToggleEffectivePreview()}
                className={`mb-1 inline-flex items-center gap-1 px-2.5 py-1 rounded border text-xs font-medium ${
                  activeTab.showEffective
                    ? 'bg-sky-100 border-sky-300 text-sky-800'
                    : 'bg-white border-slate-300 text-slate-700 hover:bg-slate-50'
                }`}
              >
                <Eye className="w-3.5 h-3.5" /> {activeTab.showEffective ? '隐藏生效预览' : '查看继承与变量生效预览'}
              </button>
            )}
          </div>

          <div className="p-4 text-xs space-y-4">
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
                      <option value="regex">正则 (Regex)</option>
                      <option value="header">响应头 (Header)</option>
                    </select>
                    <input
                      type="text"
                      value={ex.expr}
                      placeholder="$.data.token"
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
                          else updateAs({ type: t, expr: '$.code', equals: '0', max: undefined, value: undefined })
                        }}
                        className="col-span-3 px-2 py-1 border border-slate-300 rounded bg-white"
                      >
                        <option value="status">HTTP 状态码 (status)</option>
                        <option value="duration">最大耗时 (duration)</option>
                        <option value="body_contains">响应体包含 (body_contains)</option>
                        <option value="jsonpath">JSONPath 校验</option>
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
          </div>

          <ResponsePanel
            projectId={projectId}
            debugResp={activeTab.debugResp}
            historyList={history}
            loadingDebug={sending}
            autoSyncTempVars={autoSyncTempVars}
            onToggleAutoSync={setAutoSyncTempVars}
            onPickExtractJSONPath={addExtractFromJSON}
            onPickAssertJSONPath={addAssertFromJSON}
            onPushTempVars={(vars) => {
              onTempVarsChange({ ...tempVars, ...vars })
              onToast('success', '已写入顶部 ⚡ 临时变量池')
            }}
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
    </div>
  )
}
