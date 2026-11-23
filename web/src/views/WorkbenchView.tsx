import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  Columns2,
  Copy,
  CopyPlus,
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
import { CurlModal, type CurlModalTab } from '../components/CurlModal'
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
  /** Debug-history record currently replayed into this tab, if any. */
  historyId?: string
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

/** Semantic tone per HTTP method / protocol: [text, tinted bg, border]. Shared by tree badges, tabs and the URL capsule. */
const METHOD_TONES: Record<string, { text: string; bg: string; border: string }> = {
  GET: { text: 'text-emerald-700', bg: 'bg-emerald-50', border: 'border-emerald-300' },
  POST: { text: 'text-sky-700', bg: 'bg-sky-50', border: 'border-sky-300' },
  PUT: { text: 'text-amber-700', bg: 'bg-amber-50', border: 'border-amber-300' },
  PATCH: { text: 'text-violet-700', bg: 'bg-violet-50', border: 'border-violet-300' },
  DELETE: { text: 'text-rose-700', bg: 'bg-rose-50', border: 'border-rose-300' },
  GRPC: { text: 'text-indigo-700', bg: 'bg-indigo-50', border: 'border-indigo-300' },
  WS: { text: 'text-purple-700', bg: 'bg-purple-50', border: 'border-purple-300' },
  SSE: { text: 'text-teal-700', bg: 'bg-teal-50', border: 'border-teal-300' },
}
const NEUTRAL_TONE = { text: 'text-fg-muted', bg: 'bg-slate-100', border: 'border-line-strong' }

function methodTone(method?: string) {
  return METHOD_TONES[(method || 'GET').toUpperCase()] || NEUTRAL_TONE
}

/** Display verb for a request: HTTP method, or the protocol name for WS / gRPC. */
function requestVerb(req?: Partial<API['request']>): string {
  const proto = (req?.protocol || 'http').toLowerCase()
  if (proto === 'ws') return 'WS'
  if (proto === 'grpc') return 'GRPC'
  return (req?.method || 'GET').toUpperCase()
}

function methodBadgeColor(method?: string): string {
  const t = methodTone(method)
  return `${t.text} ${t.bg} ${t.border}`
}

const HOP_BY_HOP_HEADERS = new Set(['content-length', 'host', 'connection', 'accept-encoding', 'transfer-encoding'])

/** Rebuild an editable request from a debug-history record's actual (expanded) request. */
function draftRequestFromRecord(rec: DebugRecord, fallbackProtocol: APIProtocol): API['request'] {
  const actual = rec.data?.result?.actual_request
  const url = actual?.url || rec.url
  const method = (actual?.method || rec.method || 'GET').toUpperCase()
  const headers: KeyValue[] = (actual?.headers || [])
    .filter((h) => h.name && !HOP_BY_HOP_HEADERS.has(h.name.toLowerCase()) && !/^\*{3,}$/.test(h.value.trim()))
    .map((h) => ({ name: h.name, value: h.value }))
  const ctHeader = headers.find((h) => h.name.toLowerCase() === 'content-type')?.value || ''
  const raw = actual?.body || ''
  let body: NonNullable<API['request']['body']> = { type: 'none' }
  if (raw) {
    const t = raw.trim()
    let isJSON = /json/i.test(ctHeader)
    if (!isJSON && (t.startsWith('{') || t.startsWith('['))) {
      try {
        JSON.parse(t)
        isJSON = true
      } catch {
        isJSON = false
      }
    }
    if (isJSON) {
      body = { type: 'json', content: raw }
    } else if (/application\/x-www-form-urlencoded/i.test(ctHeader)) {
      const fields: KeyValue[] = []
      new URLSearchParams(raw).forEach((value, name) => fields.push({ name, value }))
      body = fields.length > 0 ? { type: 'form', fields } : { type: 'raw', content: raw, content_type: ctHeader }
    } else {
      body = { type: 'raw', content: raw, content_type: ctHeader || undefined }
    }
  }
  const protocol: APIProtocol =
    /^wss?:\/\//i.test(url) || method === 'WS'
      ? 'ws'
      : method === 'GRPC'
        ? 'grpc'
        : method === 'SSE'
          ? 'sse'
          : ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].includes(method)
            ? fallbackProtocol === 'sse'
              ? 'sse'
              : 'http'
            : fallbackProtocol
  return {
    protocol,
    method: protocol === 'http' || protocol === 'sse' ? (method === 'SSE' ? 'GET' : method) : '',
    url,
    path_params: [],
    query: [],
    headers,
    body,
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
  const [curlTab, setCurlTab] = useState<CurlModalTab>('export')
  const [savingCopy, setSavingCopy] = useState(false)
  const [confirmDeleteId, setConfirmDeleteId] = useState('')
  const confirmTimerRef = useRef<number | undefined>(undefined)
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
    try {
      const created = await api.createAPI(projectId, payload)
      setTabs((prev) =>
        prev.map((t) => (t.tabKey === activeTab.tabKey ? { ...t, tabKey: created.id, api: created, dirty: false } : t))
      )
      setActiveTabKey(created.id)
      await loadSidebarData()
      onToast('success', `已保存到目录「${created.name}」`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '保存接口失败')
      throw err
    }
  }

  /** Create a new saved API from the current draft (same folder, "副本" suffix) and open it in a new tab. */
  const handleSaveAsCopy = async () => {
    if (!activeTab || savingCopy) return
    const { id: _id, version: _version, order: _order, ...rest } = activeTab.api
    const baseName = (activeTab.api.name || '未命名接口').trim()
    setSavingCopy(true)
    try {
      const created = await api.createAPI(projectId, {
        ...rest,
        name: `${baseName} 副本`,
        folder_id: activeTab.api.folder_id || '',
      })
      const tab: OpenTab = {
        tabKey: created.id,
        api: created,
        dirty: false,
        debugResp: activeTab.debugResp,
        effective: null,
        showEffective: false,
      }
      setTabs((prev) => [...prev, tab])
      setActiveTabKey(created.id)
      await loadSidebarData()
      onToast('success', `已另存为「${created.name}」`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '另存副本失败')
    } finally {
      setSavingCopy(false)
    }
  }

  /** Two-step inline delete: first click arms for 3s, second click within the window executes. */
  const armOrRunDelete = (nodeId: string, run: () => Promise<void>) => {
    window.clearTimeout(confirmTimerRef.current)
    if (confirmDeleteId !== nodeId) {
      setConfirmDeleteId(nodeId)
      confirmTimerRef.current = window.setTimeout(() => setConfirmDeleteId(''), 3000)
      return
    }
    setConfirmDeleteId('')
    void run()
  }

  useEffect(() => () => window.clearTimeout(confirmTimerRef.current), [])

  const applyParsedToDraft = (parsed: API) => {
    updateActiveAPI((cur) => ({
      ...cur,
      name: cur.id ? cur.name : parsed.name || cur.name,
      request: parsed.request,
      auth: parsed.auth || { type: 'inherit' },
    }))
    patchActiveTab((t) => ({ ...t, historyId: undefined }))
    setReqSubTab(parsed.request?.body && parsed.request.body.type !== 'none' ? 'body' : 'params')
    onToast('success', '已回填 cURL 到当前草稿')
  }

  const openParsedInNewTab = (parsed: API) => {
    const key = `scratch-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`
    const base = newScratchAPI(activeTab?.api.folder_id || '')
    const tab: OpenTab = {
      tabKey: key,
      api: {
        ...base,
        name: parsed.name || base.name,
        request: parsed.request,
        auth: parsed.auth || { type: 'inherit' },
        extract: parsed.extract || [],
        assert: parsed.assert || [],
      },
      dirty: true,
      debugResp: null,
      effective: null,
      showEffective: false,
    }
    setTabs((prev) => [...prev, tab])
    setActiveTabKey(key)
    onToast('success', '已在新页签打开 cURL 请求')
  }

  const saveResponseAsExample = (item: APIResponseExample) => {
    updateActiveAPI((cur) => ({ ...cur, examples: [...(cur.examples || []), item] }))
    onToast('success', activeTab.api.id ? '已追加响应示例，保存接口后生效' : '已追加响应示例')
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
      patchActiveTab((t) => ({ ...t, debugResp: res, historyId: undefined }))
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
    modalOpen: importOpen || curlOpen || saveModalOpen || folderModal.open || runnerModal.open,
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

  const nodeActionBtn = 'p-1 rounded text-fg-subtle hover:bg-surface hover:text-fg focus-visible:outline-2 focus-visible:outline-sky-500'

  const renderDeleteButton = (node: TreeNode, label: string, run: () => Promise<void>) => {
    const armed = confirmDeleteId === node.id
    return (
      <button
        type="button"
        title={armed ? '再次点击确认删除' : label}
        aria-label={armed ? `确认删除「${node.name}」` : `${label}「${node.name}」`}
        onClick={() => armOrRunDelete(node.id, run)}
        onBlur={() => {
          if (armed) setConfirmDeleteId('')
        }}
        className={
          armed
            ? 'inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded bg-rose-600 hover:bg-rose-solid text-white text-[10px] font-semibold whitespace-nowrap'
            : `${nodeActionBtn} hover:text-rose-600`
        }
      >
        <Trash2 className="w-3 h-3" />
        {armed && '确认删除？'}
      </button>
    )
  }

  const renderTreeNodes = (nodes: TreeNode[], parentId = '', depth = 0) => {
    return nodes.map((node, idx) => {
      if (node.type === 'folder') {
        const isCollapsed = Boolean(collapsed[node.id]) && !search.trim()
        const armed = confirmDeleteId === node.id
        const toggleFolder = () => setCollapsed((c) => ({ ...c, [node.id]: !c[node.id] }))
        return (
          <div key={node.id} className="select-none">
            <div
              draggable
              role="button"
              tabIndex={0}
              aria-expanded={!isCollapsed}
              onDragStart={() => setDragNodeId(node.id)}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                e.preventDefault()
                void handleDropIntoFolder(node.id)
              }}
              onKeyDown={(e) => {
                if (e.target !== e.currentTarget) return
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  toggleFolder()
                }
              }}
              style={{ paddingLeft: `${4 + depth * 10}px` }}
              className="group flex items-center gap-1 pr-1 py-1 rounded-md text-xs hover:bg-surface-hover text-fg cursor-pointer focus:outline-none focus-visible:ring-2 focus-visible:ring-sky-500/50"
              onClick={toggleFolder}
            >
              {isCollapsed ? (
                <ChevronRight className="w-3.5 h-3.5 text-fg-subtle shrink-0" />
              ) : (
                <ChevronDown className="w-3.5 h-3.5 text-fg-subtle shrink-0" />
              )}
              {isCollapsed ? (
                <FolderIcon className="w-3.5 h-3.5 text-amber-500 shrink-0" />
              ) : (
                <FolderOpen className="w-3.5 h-3.5 text-amber-500 shrink-0" />
              )}
              <span className="truncate font-medium flex-1 min-w-0">{node.name}</span>
              <div
                className={`${
                  armed ? 'w-auto opacity-100' : 'w-0 overflow-hidden opacity-0'
                } group-hover:w-auto group-hover:overflow-visible group-hover:opacity-100 focus-within:w-auto focus-within:overflow-visible focus-within:opacity-100 inline-flex items-center gap-0.5 shrink-0 transition-opacity`}
                onClick={(e) => e.stopPropagation()}
                onKeyDown={(e) => e.stopPropagation()}
              >
                {!armed && (
                  <>
                    <button
                      type="button"
                      title="批量测试此目录"
                      aria-label={`批量测试目录「${node.name}」`}
                      onClick={() => setRunnerModal({ open: true, folderId: node.id })}
                      className={`${nodeActionBtn} hover:text-emerald-600`}
                    >
                      <Play className="w-3 h-3" />
                    </button>
                    <button
                      type="button"
                      title="在此目录新建请求"
                      aria-label={`在「${node.name}」新建请求`}
                      onClick={() => addScratchTab(node.id)}
                      className={`${nodeActionBtn} hover:text-sky-600`}
                    >
                      <Plus className="w-3 h-3" />
                    </button>
                    {canWrite && (
                      <button
                        type="button"
                        title="编辑目录"
                        aria-label={`编辑目录「${node.name}」`}
                        onClick={async () => {
                          try {
                            const f = await api.getFolder(projectId, node.id)
                            setFolderModal({ open: true, folder: f, parentId })
                          } catch (err) {
                            onToast('error', err instanceof APIError ? err.message : '读取目录失败')
                          }
                        }}
                        className={`${nodeActionBtn} hover:text-sky-600`}
                      >
                        <Pencil className="w-3 h-3" />
                      </button>
                    )}
                  </>
                )}
                {canWrite &&
                  renderDeleteButton(node, '删除目录', async () => {
                    try {
                      await api.deleteFolder(projectId, node.id, node.version)
                      await loadSidebarData()
                      onToast('success', `已删除目录「${node.name}」`)
                    } catch (err) {
                      onToast('error', err instanceof APIError ? err.message : '删除目录失败')
                    }
                  })}
              </div>
            </div>
            {!isCollapsed && node.children && renderTreeNodes(node.children, node.id, depth + 1)}
          </div>
        )
      }

      const active = activeTab?.api.id === node.id
      const armed = confirmDeleteId === node.id
      return (
        <div
          key={node.id}
          draggable
          role="button"
          tabIndex={0}
          aria-current={active || undefined}
          onDragStart={() => setDragNodeId(node.id)}
          style={{ paddingLeft: `${6 + depth * 10}px` }}
          onClick={() => void openSavedAPI(node.id)}
          onKeyDown={(e) => {
            if (e.target !== e.currentTarget) return
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault()
              void openSavedAPI(node.id)
            }
          }}
          title={node.url ? `${node.name}\n${node.url}` : node.name}
          className={`group flex items-center gap-1 pr-1 py-1 rounded-md text-xs cursor-pointer transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-sky-500/50 ${
            active
              ? 'bg-sky-100 text-sky-900 font-semibold shadow-[inset_2px_0_0_var(--tg-tidal-600)]'
              : 'text-fg-muted hover:bg-surface-hover hover:text-fg'
          }`}
        >
          <span
            className={`px-1 rounded border text-[10px] leading-4 font-mono font-bold w-9 text-center shrink-0 ${methodBadgeColor(
              node.method
            )}`}
          >
            {(node.method || 'GET').slice(0, 4)}
          </span>
          <span className="truncate flex-1 min-w-0">{node.name}</span>
          <div
            className={`${
              armed ? 'w-auto opacity-100' : 'w-0 overflow-hidden opacity-0'
            } group-hover:w-auto group-hover:overflow-visible group-hover:opacity-100 focus-within:w-auto focus-within:overflow-visible focus-within:opacity-100 inline-flex items-center gap-0.5 shrink-0 transition-opacity`}
            onClick={(e) => e.stopPropagation()}
            onKeyDown={(e) => e.stopPropagation()}
          >
            {!armed && canWrite && (
              <>
                <button
                  type="button"
                  title="上移"
                  aria-label={`上移「${node.name}」`}
                  disabled={idx === 0}
                  onClick={() => void moveSibling(nodes, parentId, idx, -1)}
                  className={`${nodeActionBtn} disabled:opacity-30 disabled:pointer-events-none`}
                >
                  <ArrowUp className="w-3 h-3" />
                </button>
                <button
                  type="button"
                  title="下移"
                  aria-label={`下移「${node.name}」`}
                  disabled={idx === nodes.length - 1}
                  onClick={() => void moveSibling(nodes, parentId, idx, 1)}
                  className={`${nodeActionBtn} disabled:opacity-30 disabled:pointer-events-none`}
                >
                  <ArrowDown className="w-3 h-3" />
                </button>
                <button
                  type="button"
                  title="复制接口"
                  aria-label={`复制接口「${node.name}」`}
                  onClick={async () => {
                    try {
                      const cp = await api.copyAPI(projectId, node.id)
                      await loadSidebarData()
                      void openSavedAPI(cp.id)
                    } catch (err) {
                      onToast('error', err instanceof APIError ? err.message : '复制接口失败')
                    }
                  }}
                  className={`${nodeActionBtn} hover:text-sky-600`}
                >
                  <Copy className="w-3 h-3" />
                </button>
              </>
            )}
            {canWrite &&
              renderDeleteButton(node, '删除接口', async () => {
                try {
                  await api.deleteAPI(projectId, node.id, node.version)
                  setTabs((prev) => prev.filter((t) => t.api.id !== node.id))
                  await loadSidebarData()
                  onToast('success', `已删除接口「${node.name}」`)
                } catch (err) {
                  onToast('error', err instanceof APIError ? err.message : '删除接口失败')
                }
              })}
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
      const result = rec.data?.result
      const request = draftRequestFromRecord(rec, protocol)
      const resp: DebugAPIResponse | null = result
        ? { record_id: rec.id, result, final_vars: rec.data?.final_vars }
        : null
      // Never overwrite a different saved API: replay into a fresh scratch tab instead.
      const intoNewTab = Boolean(activeTab.api.id && rec.api_id !== activeTab.api.id)
      if (intoNewTab) {
        const key = `replay-h-${rec.id}-${Date.now()}`
        let path = request.url
        try {
          path = new URL(request.url).pathname || request.url
        } catch {
          // relative or templated URL
        }
        const tab: OpenTab = {
          tabKey: key,
          api: { ...newScratchAPI(activeTab.api.folder_id || ''), name: `回放 ${requestVerb(request)} ${path}`.slice(0, 80), request },
          dirty: true,
          debugResp: resp,
          effective: null,
          showEffective: false,
          historyId: rec.id,
        }
        setTabs((prev) => [...prev, tab])
        setActiveTabKey(key)
      } else {
        patchActiveTab((t) => ({
          ...t,
          api: { ...t.api, request },
          dirty: true,
          debugResp: resp,
          showEffective: false,
          historyId: rec.id,
        }))
      }
      setReqSubTab(request.body && request.body.type !== 'none' ? 'body' : 'headers')
      onToast('info', `已回填 ${rec.method} ${rec.url}${intoNewTab ? ' · 新页签' : ''}`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '读取调试记录失败')
    }
  }

  const verb = requestVerb(req)
  const verbTone = methodTone(verb)
  const protoTone = protocol === 'http' ? NEUTRAL_TONE : methodTone(protocol === 'grpc' ? 'GRPC' : protocol.toUpperCase())
  const quietBtn =
    'inline-flex items-center gap-1.5 px-3 py-2 rounded-lg border border-line-strong bg-surface hover:bg-surface-hover text-fg text-xs font-medium disabled:opacity-45 transition-colors'
  const railIconBtn =
    'p-1.5 rounded text-fg-subtle hover:text-fg hover:bg-surface-hover disabled:opacity-40 disabled:pointer-events-none shrink-0'

  return (
    <div className="flex flex-1 min-h-0 bg-canvas">
      {/* Left API Tree */}
      {!treeCollapsed && (
        <aside className="w-[272px] bg-surface border-r border-line flex flex-col shrink-0">
          <div className="p-2 border-b border-line space-y-2">
            <search className="relative">
              <Search className="w-3.5 h-3.5 text-fg-subtle absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
              <input
                type="search"
                name="api-tree-filter"
                aria-label="过滤接口"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="名称 / 路径 / 方法"
                autoComplete="off"
                autoCorrect="off"
                autoCapitalize="none"
                spellCheck={false}
                className="w-full pl-8 pr-2.5 py-1.5 text-xs bg-surface-subtle text-fg border border-line rounded-md focus:bg-surface focus:border-sky-500 focus:outline-none"
              />
            </search>
            <div className="grid grid-cols-3 gap-1 text-[11px]">
              <button
                type="button"
                onClick={() => addScratchTab()}
                className="inline-flex items-center justify-center gap-1 py-1.5 px-1.5 rounded-md bg-primary hover:bg-primary-hover text-white font-medium"
              >
                <FilePlus className="w-3.5 h-3.5 shrink-0" /> 新建
              </button>
              <button
                type="button"
                disabled={!canWrite}
                onClick={() => setImportOpen(true)}
                className="inline-flex items-center justify-center gap-1 py-1.5 px-1.5 rounded-md border border-line-strong bg-surface hover:bg-surface-hover text-fg font-medium disabled:opacity-45"
              >
                <Import className="w-3.5 h-3.5 shrink-0 text-tidal" /> 导入
              </button>
              <button
                type="button"
                disabled={!canWrite}
                onClick={() => setRunnerModal({ open: true, folderId: '' })}
                className="inline-flex items-center justify-center gap-1 py-1.5 px-1.5 rounded-md border border-line-strong bg-surface hover:bg-surface-hover text-fg font-medium disabled:opacity-45"
                title="批量测试全部接口"
              >
                <ListChecks className="w-3.5 h-3.5 shrink-0 text-emerald-600" /> 批量测试
              </button>
            </div>
          </div>

          <div className="flex items-center justify-between px-3 pt-2 pb-1 text-[11px]">
            <span className="text-fg-subtle font-medium">目录</span>
            {canWrite && (
              <button
                type="button"
                onClick={() => setFolderModal({ open: true, folder: null, parentId: '' })}
                title="新建目录"
                aria-label="新建目录"
                className="p-1 -mr-1 rounded text-fg-subtle hover:text-tidal hover:bg-surface-hover"
              >
                <FolderPlus className="w-3.5 h-3.5" />
              </button>
            )}
          </div>

          <div
            className="flex-1 overflow-y-auto tg-scroller px-1.5 pb-1.5 space-y-px"
            onDragOver={(e) => e.preventDefault()}
            onDrop={(e) => {
              if (e.target === e.currentTarget && dragNodeId) {
                void handleDropIntoFolder('')
              }
            }}
          >
            {tree.children.length === 0 ? (
              <div className="text-center py-8 px-3 text-xs text-fg-subtle">暂无接口</div>
            ) : (
              renderTreeNodes(filterTree(tree.children))
            )}
          </div>
        </aside>
      )}

      {/* Main Editor + Split Response Area */}
      <main className="flex-1 min-w-0 flex flex-col bg-surface">
        {/* Multi-Tabs Bar + View Controls */}
        <div className="flex items-end justify-between gap-2 px-2 pt-1.5 bg-surface-subtle border-b border-line shrink-0">
          <div className="flex items-end gap-1 overflow-x-auto min-w-0 flex-1 [scrollbar-width:none]">
            <div className="flex items-center gap-0.5 mb-1 shrink-0">
              <button
                type="button"
                onClick={() => setTreeCollapsed((v) => !v)}
                title={treeCollapsed ? '展开目录树' : '收起目录树'}
                aria-label={treeCollapsed ? '展开目录树' : '收起目录树'}
                aria-expanded={!treeCollapsed}
                className={railIconBtn}
              >
                {treeCollapsed ? <PanelLeftOpen className="w-4 h-4" /> : <PanelLeftClose className="w-4 h-4" />}
              </button>
              {treeCollapsed && (
                <>
                  <button type="button" onClick={() => addScratchTab()} title="新建请求" aria-label="新建请求" className={railIconBtn}>
                    <FilePlus className="w-4 h-4" />
                  </button>
                  <button
                    type="button"
                    disabled={!canWrite}
                    onClick={() => setImportOpen(true)}
                    title="导入"
                    aria-label="导入"
                    className={railIconBtn}
                  >
                    <Import className="w-4 h-4" />
                  </button>
                  <button
                    type="button"
                    disabled={!canWrite}
                    onClick={() => setRunnerModal({ open: true, folderId: '' })}
                    title="批量测试"
                    aria-label="批量测试"
                    className={`${railIconBtn} hover:text-emerald-600`}
                  >
                    <ListChecks className="w-4 h-4" />
                  </button>
                </>
              )}
              <span className="w-px h-4 bg-[var(--tg-border-strong)] mx-1" aria-hidden="true" />
            </div>

            {tabs.map((tab) => {
              const isActiveTab = tab.tabKey === activeTab.tabKey
              const tabVerb = requestVerb(tab.api.request)
              return (
                <div
                  key={tab.tabKey}
                  role="tab"
                  tabIndex={0}
                  aria-selected={isActiveTab}
                  onClick={() => setActiveTabKey(tab.tabKey)}
                  onKeyDown={(e) => {
                    if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) {
                      e.preventDefault()
                      setActiveTabKey(tab.tabKey)
                    }
                  }}
                  onAuxClick={(e) => {
                    if (e.button === 1) {
                      e.preventDefault()
                      closeTab(tab.tabKey)
                    }
                  }}
                  title={tab.api.name || '未命名'}
                  className={`group flex items-center gap-1.5 pl-2.5 pr-1 py-1.5 rounded-t-lg border text-xs cursor-pointer max-w-[200px] shrink-0 focus:outline-none focus-visible:ring-2 focus-visible:ring-sky-500/50 ${
                    isActiveTab
                      ? 'bg-surface border-line border-b-surface -mb-px text-fg font-semibold'
                      : 'border-transparent text-fg-muted hover:bg-surface-hover hover:text-fg'
                  }`}
                >
                  <span className={`font-mono text-[10px] font-bold ${methodTone(tabVerb).text}`}>
                    {tabVerb === 'GRPC' ? 'gRPC' : tabVerb}
                  </span>
                  <span className="truncate min-w-0">{tab.api.name || '未命名'}</span>
                  {tab.dirty && <span className="w-1.5 h-1.5 rounded-full bg-amber-500 shrink-0" title="未保存" />}
                  <button
                    type="button"
                    aria-label={`关闭「${tab.api.name || '未命名'}」`}
                    onClick={(e) => {
                      e.stopPropagation()
                      closeTab(tab.tabKey)
                    }}
                    className="text-fg-subtle hover:text-fg hover:bg-surface-hover p-0.5 rounded"
                  >
                    <X className="w-3 h-3" />
                  </button>
                </div>
              )
            })}
            <button
              type="button"
              title="新建页签"
              aria-label="新建页签"
              onClick={() => addScratchTab()}
              className={`${railIconBtn} mb-1`}
            >
              <Plus className="w-3.5 h-3.5" />
            </button>
          </div>

          {/* Layout Split Mode Toggle */}
          <div
            role="radiogroup"
            aria-label="分屏方向"
            className="inline-flex items-center gap-0.5 bg-surface border border-line rounded-md p-0.5 mb-1 shrink-0 text-[11px]"
          >
            <button
              type="button"
              role="radio"
              aria-checked={splitMode === 'horizontal'}
              onClick={() => changeSplitMode('horizontal')}
              title="左右分屏"
              className={`inline-flex items-center gap-1 px-2 py-0.5 rounded ${
                splitMode === 'horizontal' ? 'bg-primary text-white font-semibold' : 'text-fg-muted hover:bg-surface-hover'
              }`}
            >
              <Columns2 className="w-3 h-3" />
              <span className="hidden md:inline">左右</span>
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={splitMode === 'vertical'}
              onClick={() => changeSplitMode('vertical')}
              title="上下分屏"
              className={`inline-flex items-center gap-1 px-2 py-0.5 rounded ${
                splitMode === 'vertical' ? 'bg-primary text-white font-semibold' : 'text-fg-muted hover:bg-surface-hover'
              }`}
            >
              <Rows2 className="w-3 h-3" />
              <span className="hidden md:inline">上下</span>
            </button>
          </div>
        </div>

        {/* URL & Actions Bar */}
        <div className="px-3 py-2.5 border-b border-line space-y-2 bg-surface shrink-0">
          <div className="flex items-center gap-2 text-xs min-w-0">
            <input
              type="text"
              name="api-name"
              aria-label="接口名称"
              value={activeTab.api.name || ''}
              onChange={(e) => updateActiveAPI((cur) => ({ ...cur, name: e.target.value }))}
              placeholder="接口名称"
              autoComplete="off"
              className="flex-1 min-w-0 max-w-md px-2 py-1 -ml-2 rounded border border-transparent hover:border-line focus:border-sky-500 focus:outline-none font-medium text-fg bg-transparent focus:bg-surface"
            />
            {!canWrite && (
              <span className="px-2 py-0.5 rounded-full border border-amber-200 bg-amber-50 text-amber-800 text-[11px] shrink-0">
                只读
              </span>
            )}
            {activeTab.historyId && (
              <span className="px-2 py-0.5 rounded-full border border-sky-200 bg-sky-50 text-sky-800 text-[11px] shrink-0">
                历史回放
              </span>
            )}
            {activeTab.api.id && (
              <span className="ml-auto font-mono text-[10px] text-fg-subtle hidden sm:inline truncate" title="接口 ID">
                {activeTab.api.id}
              </span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {/* Unified request capsule: protocol | method | URL */}
            <div className="flex items-center gap-1 p-1 flex-1 min-w-[min(100%,300px)] rounded-lg border border-line-strong bg-surface focus-within:border-sky-500 focus-within:ring-2 focus-within:ring-sky-500/20 transition-shadow">
              <select
                aria-label="协议"
                name="protocol"
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
                className={`px-2 py-1 rounded-md border font-mono text-[11px] font-bold focus:outline-none cursor-pointer shrink-0 ${
                  protocol === 'http'
                    ? 'bg-surface-subtle text-fg-muted border-line'
                    : `${protoTone.bg} ${protoTone.text} ${protoTone.border}`
                }`}
              >
                <option value="http" className="bg-surface text-fg">HTTP</option>
                <option value="ws" className="bg-surface text-purple-700">WS</option>
                <option value="grpc" className="bg-surface text-indigo-700">gRPC</option>
                <option value="sse" className="bg-surface text-teal-700">SSE</option>
              </select>

              {(protocol === 'http' || protocol === 'sse') && (
                <select
                  aria-label="HTTP 方法"
                  name="method"
                  value={req.method || 'GET'}
                  onChange={(e) =>
                    updateActiveAPI((cur) => ({
                      ...cur,
                      request: { ...req, method: e.target.value },
                    }))
                  }
                  className={`px-2 py-1 rounded-md border font-mono text-xs font-bold focus:outline-none cursor-pointer shrink-0 ${verbTone.bg} ${verbTone.text} ${verbTone.border}`}
                >
                  {['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].map((m) => (
                    <option key={m} value={m} className={`bg-surface ${methodTone(m).text}`}>
                      {m}
                    </option>
                  ))}
                </select>
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
                    ? 'wss://host/path · {{ 补全变量'
                    : protocol === 'grpc'
                      ? '127.0.0.1:9090 或 grpc://host:9090'
                      : protocol === 'sse'
                        ? '/api/events'
                        : '/users/:id、完整 URL、{{ 补全变量，或粘贴 cURL'
                }
                className="px-2 py-1 bg-transparent border-0 rounded-md font-mono text-xs text-fg focus:outline-none"
              />
            </div>

            <button
              type="button"
              disabled={!canWrite || sending}
              onClick={() => void handleDebugSend()}
              title="发送 (Ctrl+Enter)"
              className="inline-flex items-center gap-1.5 px-4 py-2 rounded-lg bg-primary hover:bg-primary-hover text-white text-xs font-semibold disabled:opacity-50"
            >
              <Play className="w-3.5 h-3.5 fill-current" />
              {sending ? '发送中…' : '发送'}
            </button>

            <div className="inline-flex items-stretch rounded-lg border border-line-strong bg-surface overflow-hidden text-xs font-medium">
              <button
                type="button"
                disabled={!canWrite || saving}
                onClick={() => void handleSave()}
                title="保存 (Ctrl+S)"
                className="inline-flex items-center gap-1.5 px-3 py-2 text-fg hover:bg-surface-hover disabled:opacity-45"
              >
                <Save className="w-3.5 h-3.5" /> {activeTab.api.id ? '保存' : '保存到目录'}
              </button>
              {activeTab.api.id && (
                <button
                  type="button"
                  disabled={!canWrite || savingCopy}
                  onClick={() => void handleSaveAsCopy()}
                  title="用当前草稿新建一个副本接口"
                  className="inline-flex items-center gap-1.5 px-3 py-2 border-l border-line text-fg hover:bg-surface-hover disabled:opacity-45"
                >
                  <CopyPlus className="w-3.5 h-3.5" /> {savingCopy ? '另存中…' : '另存副本'}
                </button>
              )}
            </div>

            <button
              type="button"
              onClick={() => {
                setCurlTab(req.url?.trim() ? 'export' : 'import')
                setCurlOpen(true)
              }}
              className={quietBtn}
            >
              <Terminal className="w-3.5 h-3.5" /> cURL
            </button>

            <button
              type="button"
              disabled={!canWrite}
              onClick={() => onQuickLoadTest(activeTab.api)}
              className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg bg-amber-500 hover:bg-amber-400 text-[#1f1300] text-xs font-semibold disabled:opacity-45"
              title="带入压测"
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
            <div className="px-2 border-b border-line flex items-center justify-between gap-2 bg-surface-subtle shrink-0 min-w-0">
              <div role="tablist" aria-label="请求配置页签" className="tg-tablist flex-1 min-w-0 pr-1 gap-0.5 text-xs font-medium">
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
                    {
                      id: 'auth' as const,
                      label:
                        activeTab.api.auth?.type && activeTab.api.auth.type !== 'inherit'
                          ? `认证 · ${activeTab.api.auth.type}`
                          : '认证',
                      count: 0,
                    },
                    { id: 'extract' as const, label: '提取', count: extracts.length },
                    { id: 'assert' as const, label: '断言', count: asserts.length },
                    {
                      id: 'scripts' as const,
                      label: '脚本',
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
                      className={`tg-tab inline-flex items-center gap-1 px-2 py-2 transition-colors ${
                        selected ? 'text-tidal' : 'text-fg-muted hover:text-fg'
                      }`}
                    >
                      <span>{st.label}</span>
                      {st.count > 0 && (
                        <span className="tg-tabular px-1 py-0 rounded-full bg-sky-100 text-sky-800 text-[10px] font-mono">
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
                      : 'bg-surface border-line-strong text-fg-muted hover:bg-surface-hover'
                  }`}
                >
                  <Eye className="w-3 h-3" /> 生效预览
                </button>
              )}
            </div>

            <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-4 text-xs space-y-4">
            {reqSubTab === 'params' && (
              <div className="space-y-4">
                {(req.path_params?.length || 0) > 0 && (
                  <div>
                    <div className="font-semibold text-fg mb-1.5">路径参数</div>
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
                  <div className="font-semibold text-fg mb-1.5">Query</div>
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
                      <span className="font-semibold text-sky-900">生效预览</span>
                      <span className="font-mono text-sky-800">
                        {activeTab.effective.method} {activeTab.effective.url}
                      </span>
                    </div>
                    <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                      <div className="bg-surface rounded border border-sky-100 p-2.5">
                        <div className="font-medium text-fg-muted mb-1">
                          继承请求头 <span className="tg-tabular text-fg-subtle">{activeTab.effective.inherited_headers.length}</span>
                        </div>
                        {activeTab.effective.inherited_headers.length === 0 ? (
                          <div className="text-fg-subtle">无</div>
                        ) : (
                          <div className="space-y-1 font-mono">
                            {activeTab.effective.inherited_headers.map((ih, idx) => (
                              <div key={idx} className={`flex items-center gap-2 ${ih.overridden ? 'line-through text-fg-subtle' : ''}`}>
                                <span className="font-semibold">{ih.name}:</span>
                                <span className="truncate flex-1">{ih.value}</span>
                                <span className="text-[10px] px-1 rounded bg-slate-100 text-fg-subtle">
                                  {ih.overridden ? '已被本接口覆盖' : `来自 ${ih.source_folder_name}`}
                                </span>
                              </div>
                            ))}
                          </div>
                        )}
                      </div>
                      <div className="bg-surface rounded border border-sky-100 p-2.5">
                        <div className="font-medium text-fg-muted mb-1">
                          鉴权来源：<code>{activeTab.effective.effective_auth.source}</code>
                          {activeTab.effective.effective_auth.source_folder_name
                            ? ` · ${activeTab.effective.effective_auth.source_folder_name}`
                            : ''}
                        </div>
                        <div className="font-medium text-fg-muted mt-2 mb-1">
                          可用变量 <span className="tg-tabular text-fg-subtle">{activeTab.effective.variables.length}</span>
                        </div>
                        <div className="max-h-24 overflow-y-auto space-y-0.5 font-mono text-[11px]">
                          {activeTab.effective.variables.map((rv) => (
                            <div key={rv.name} className="flex items-center justify-between gap-2">
                              <span className="text-sky-800">{`{{${rv.name}}}`}</span>
                              <span className="truncate text-fg-muted">{rv.secret ? '******' : rv.value}</span>
                              <span className="px-1 rounded bg-slate-100 text-fg-subtle text-[10px]">{rv.source}</span>
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
                    <div className="font-semibold text-purple-900">WebSocket</div>
                    <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                      <div>
                        <label className="block text-fg-muted mb-1">子协议</label>
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
                          placeholder="chat, json"
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-fg-muted mb-1">接收消息数</label>
                        <input
                          type="number"
                          min={0}
                          value={req.ws?.recv_count ?? 1}
                          onChange={(e) => updateWS({ recv_count: Math.max(0, Number(e.target.value) || 0) })}
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-fg-muted mb-1">空闲超时</label>
                        <input
                          type="text"
                          value={req.ws?.idle_timeout || ''}
                          onChange={(e) => updateWS({ idle_timeout: e.target.value || undefined })}
                          placeholder="默认 5s"
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface font-mono"
                        />
                      </div>
                    </div>
                    <div className="space-y-2">
                      <div className="flex items-center justify-between">
                        <span className="font-medium text-fg">发送帧</span>
                        <button
                          type="button"
                          onClick={() =>
                            updateWS({
                              messages: [...(req.ws?.messages || []), { type: 'text', data: '{"type":"ping"}' }],
                            })
                          }
                          className="inline-flex items-center gap-1 text-sky-700 hover:underline font-medium"
                        >
                          <Plus className="w-3.5 h-3.5" /> 添加
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
                            className="col-span-2 px-2 py-1.5 border border-line-strong rounded bg-surface"
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
                            className="col-span-9 px-2.5 py-1.5 border border-line-strong rounded bg-surface font-mono"
                          />
                          <button
                            type="button"
                            onClick={() =>
                              updateWS({ messages: (req.ws?.messages || []).filter((_, idx) => idx !== i) })
                            }
                            className="col-span-1 justify-self-end p-1 text-fg-subtle hover:text-rose-600"
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
                      <div className="font-semibold text-indigo-900">gRPC · Unary</div>
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
                          className="px-2 py-1 border border-line-strong rounded bg-surface text-[11px]"
                        >
                          <option value="">不绑定 .proto</option>
                          {files
                            .filter((f) => /\.(proto|pb|desc)$/i.test(f.name))
                            .map((f) => (
                              <option key={f.name} value={f.name}>
                                {f.name}
                              </option>
                            ))}
                        </select>
                        <label className="inline-flex items-center gap-1 text-[11px] text-fg-muted cursor-pointer">
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
                          className="inline-flex items-center gap-1 px-2.5 py-1 rounded bg-indigo-600 hover:bg-indigo-solid text-white text-[11px] font-medium disabled:opacity-50"
                        >
                          <Sparkles className="w-3 h-3" /> {grpcDiscoverBusy ? '探测中…' : '探测服务'}
                        </button>
                      </div>
                    </div>

                    {grpcCatalog && grpcCatalog.services?.length > 0 && (
                      <div className="rounded border border-indigo-200 bg-surface p-2 flex flex-wrap items-center gap-2 text-[11px]">
                        
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
                          className="px-2 py-1 border border-line-strong rounded font-mono bg-surface-subtle"
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
                          className="px-2 py-1 border border-line-strong rounded font-mono bg-surface-subtle"
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
                        <label className="block text-fg-muted mb-1">Service</label>
                        <input
                          type="text"
                          value={req.grpc?.service || ''}
                          onChange={(e) => updateGRPC({ service: e.target.value })}
                          placeholder="example.Greeter"
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-fg-muted mb-1">Method</label>
                        <input
                          type="text"
                          value={req.grpc?.method || ''}
                          onChange={(e) => updateGRPC({ method: e.target.value })}
                          placeholder="SayHello"
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-fg-muted mb-1">超时</label>
                        <input
                          type="text"
                          value={req.grpc?.timeout || ''}
                          onChange={(e) => updateGRPC({ timeout: e.target.value || undefined })}
                          placeholder="默认 5s"
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-fg-muted mb-1">请求编码</label>
                        <select
                          value={req.grpc?.payload_encoding || 'json'}
                          onChange={(e) => updateGRPC({ payload_encoding: e.target.value })}
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface"
                        >
                          <option value="proto_json">Proto JSON 动态编码（反射 / .proto）</option>
                          <option value="json">原始 JSON/UTF-8 直传</option>
                          <option value="proto_string">单字符串 Protobuf 封装</option>
                          <option value="base64">Raw Protobuf (Base64)</option>
                          <option value="hex">Raw Protobuf (Hex)</option>
                        </select>
                      </div>
                      <div>
                        <label className="block text-fg-muted mb-1">响应编码</label>
                        <select
                          value={req.grpc?.response_encoding || 'raw'}
                          onChange={(e) => updateGRPC({ response_encoding: e.target.value })}
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface"
                        >
                          <option value="proto_json">Proto JSON 动态解码（反射 / .proto）</option>
                          <option value="raw">原始 UTF-8 响应</option>
                          <option value="proto_string">单字符串 Protobuf 解包</option>
                          <option value="base64">Raw Protobuf (Base64)</option>
                        </select>
                      </div>
                      <div className="grid grid-cols-2 gap-2">
                        <div>
                          <label className="block text-fg-muted mb-1">期望状态码</label>
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
                            className="w-full px-2 py-1.5 border border-line-strong rounded bg-surface font-mono"
                          />
                        </div>
                        <label className="flex items-center gap-1.5 pt-5 text-fg-muted cursor-pointer">
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
                      <label className="block text-fg-muted font-medium mb-1">
                        请求载荷
                      </label>
                      <textarea
                        rows={6}
                        value={body.content || ''}
                        onChange={(e) => updateBody({ type: body.type && body.type !== 'none' ? body.type : 'json', content: e.target.value })}
                        placeholder='{\n  "name": "{{user}}"\n}'
                        className="w-full p-2.5 border border-line-strong rounded bg-surface font-mono text-xs"
                      />
                    </div>
                  </div>
                )}

                {protocol === 'sse' && (
                  <div className="rounded-lg border border-teal-200 bg-teal-50/40 p-3 space-y-3">
                    <div className="font-semibold text-teal-900">SSE</div>
                    <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                      <div>
                        <label className="block text-fg-muted mb-1">最多事件数</label>
                        <input
                          type="number"
                          min={0}
                          value={req.sse?.max_events ?? ''}
                          onChange={(e) =>
                            updateSSE({
                              max_events: e.target.value === '' ? undefined : Math.max(0, Number(e.target.value) || 0),
                            })
                          }
                          placeholder="0 = 不限"
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-fg-muted mb-1">终止关键字</label>
                        <input
                          type="text"
                          value={req.sse?.stop_contains || ''}
                          onChange={(e) => updateSSE({ stop_contains: e.target.value || undefined })}
                          placeholder="[DONE]"
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface font-mono"
                        />
                      </div>
                      <div>
                        <label className="block text-fg-muted mb-1">流超时</label>
                        <input
                          type="text"
                          value={req.sse?.timeout || ''}
                          onChange={(e) => updateSSE({ timeout: e.target.value || undefined })}
                          placeholder="默认 30s"
                          className="w-full px-2.5 py-1.5 border border-line-strong rounded bg-surface font-mono"
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
                        { id: 'raw' as const, label: 'Raw' },
                        { id: 'binary' as const, label: 'Binary' },
                      ] as const
                    ).map((bt) => (
                      <button
                        key={bt.id}
                        type="button"
                        onClick={() => updateBody({ type: bt.id })}
                        className={`px-2.5 py-1 rounded border font-medium ${
                          (body.type || 'none') === bt.id
                            ? 'bg-sky-50 border-sky-400 text-sky-700'
                            : 'bg-surface border-line text-fg-muted hover:bg-surface-hover'
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
                          onToast('info', '不是合法 JSON')
                        }
                      }}
                      className="inline-flex items-center gap-1 px-2 py-1 rounded border border-line text-fg-muted hover:bg-surface-hover"
                    >
                      <Sparkles className="w-3 h-3 text-tidal" /> 格式化
                    </button>
                  )}
                </div>

                {(!body.type || body.type === 'none') && (
                  <p className="text-fg-subtle">无请求体</p>
                )}

                {body.type === 'json' && (
                  <textarea
                    rows={8}
                    value={body.content || ''}
                    onChange={(e) => updateBody({ content: e.target.value })}
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    placeholder='{\n  "user_id": "{{uid}}"\n}'
                    className="w-full p-3 border border-line-strong rounded-lg bg-surface text-fg font-mono text-xs focus:border-sky-500 focus:outline-none"
                  />
                )}

                {body.type === 'raw' && (
                  <div className="space-y-2">
                    <input
                      type="text"
                      value={body.content_type || ''}
                      onChange={(e) => updateBody({ content_type: e.target.value })}
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      placeholder="Content-Type · text/plain"
                      className="w-64 px-2.5 py-1.5 border border-line-strong rounded-md bg-surface text-fg font-mono text-xs focus:border-sky-500 focus:outline-none"
                    />
                    <textarea
                      rows={7}
                      value={body.content || ''}
                      onChange={(e) => updateBody({ content: e.target.value })}
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      placeholder="请求体文本，支持 {{变量}}"
                      className="w-full p-3 border border-line-strong rounded-lg bg-surface text-fg font-mono text-xs focus:border-sky-500 focus:outline-none"
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
                      <div className="font-medium text-fg mb-1">文本字段</div>
                      <KeyValueEditor
                        items={body.fields || []}
                        onChange={(next) => updateBody({ fields: next })}
                        keyPlaceholder="字段名"
                        valuePlaceholder="字段值"
                      />
                    </div>
                    <div>
                      <div className="flex items-center justify-between mb-1">
                        <span className="font-medium text-fg">文件字段</span>
                        <button
                          type="button"
                          onClick={() =>
                            updateBody({
                              files: [...(body.files || []), { field: 'file', path: files[0]?.name || '' }],
                            })
                          }
                          className="inline-flex items-center gap-1 text-tidal hover:underline font-medium"
                        >
                          <Plus className="w-3.5 h-3.5" /> 添加
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
                            className="w-36 px-2 py-1 border border-line-strong rounded bg-surface text-fg font-mono"
                          />
                          <select
                            value={bf.path}
                            onChange={(e) => {
                              const next = (body.files || []).map((x, j) =>
                                j === i ? { ...x, path: e.target.value } : x
                              )
                              updateBody({ files: next })
                            }}
                            className="flex-1 px-2 py-1 border border-line-strong rounded font-mono bg-surface text-fg"
                          >
                            <option value="">选择项目文件</option>
                            {files.map((df) => (
                              <option key={df.name} value={df.name}>
                                {df.name}
                              </option>
                            ))}
                          </select>
                          <button
                            type="button"
                            onClick={() => updateBody({ files: (body.files || []).filter((_, j) => j !== i) })}
                            className="p-1 text-fg-subtle hover:text-rose-600"
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
                    <label className="block font-medium text-fg">项目文件</label>
                    <select
                      value={body.file || ''}
                      onChange={(e) => updateBody({ file: e.target.value })}
                      className="w-full px-2.5 py-1.5 border border-line-strong rounded-md font-mono bg-surface"
                    >
                      <option value="">选择文件…</option>
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
                      placeholder="Content-Type · application/octet-stream"
                      className="w-full px-2.5 py-1.5 border border-line-strong rounded-md bg-surface text-fg font-mono"
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
                {extracts.map((ex, i) => (
                  <div key={i} className="grid grid-cols-12 gap-2 items-center p-2 rounded border border-line bg-surface-subtle">
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
                      className="col-span-3 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
                    />
                    <select
                      value={ex.from}
                      onChange={(e) => {
                        const next: ExtractRule[] = extracts.map((row, idx) =>
                          idx === i ? { ...row, from: e.target.value as ExtractRule['from'] } : row
                        )
                        updateActiveAPI((cur) => ({ ...cur, extract: next }))
                      }}
                      className="col-span-2 px-2 py-1 border border-line-strong rounded bg-surface"
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
                      className="col-span-4 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
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
                      className="col-span-2 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
                    />
                    <button
                      type="button"
                      onClick={() => updateActiveAPI((cur) => ({ ...cur, extract: extracts.filter((_, idx) => idx !== i) }))}
                      className="col-span-1 justify-self-end p-1 text-fg-subtle hover:text-rose-600"
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
                      className="grid grid-cols-12 gap-2 items-center p-2 rounded border border-line bg-surface-subtle"
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
                        className="col-span-3 px-2 py-1 border border-line-strong rounded bg-surface"
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
                          className="col-span-8 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
                        />
                      )}

                      {as.type === 'duration' && (
                        <input
                          type="text"
                          value={as.max || ''}
                          placeholder="最大允许耗时，例如 500ms 或 1.5s"
                          onChange={(e) => updateAs({ max: e.target.value })}
                          className="col-span-8 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
                        />
                      )}

                      {as.type === 'body_contains' && (
                        <input
                          type="text"
                          value={as.value || ''}
                          placeholder="必须包含的子串"
                          onChange={(e) => updateAs({ value: e.target.value })}
                          className="col-span-8 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
                        />
                      )}

                      {as.type === 'jsonpath' && (
                        <>
                          <input
                            type="text"
                            value={as.expr || ''}
                            placeholder="JSONPath，例如 $.code"
                            onChange={(e) => updateAs({ expr: e.target.value })}
                            className="col-span-4 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
                          />
                          <input
                            type="text"
                            value={as.equals !== undefined ? as.equals : as.contains || ''}
                            placeholder="期望相等值（equals）"
                            onChange={(e) => updateAs({ equals: e.target.value, contains: undefined, exists: undefined })}
                            className="col-span-4 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
                          />
                        </>
                      )}

                      {as.type === 'json_schema' && (
                        <textarea
                          rows={2}
                          value={as.value || ''}
                          placeholder='{"type":"object","required":["code","data"]}'
                          onChange={(e) => updateAs({ value: e.target.value })}
                          className="col-span-8 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
                        />
                      )}

                      <button
                        type="button"
                        onClick={() => updateActiveAPI((cur) => ({ ...cur, assert: asserts.filter((_, idx) => idx !== i) }))}
                        className="col-span-1 justify-self-end p-1 text-fg-subtle hover:text-rose-600"
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
                <div className="flex flex-wrap items-center gap-1 text-[11px]" aria-label="脚本可用函数">
                  {['vars.name', 'now_sec()', 'now_ms()', 'uuid()', 'hmac_sha256(k, d)', 'base64_encode(s)', 'resp.status', 'resp.header(n)', 'resp.json(p)', 'jsonpath(s, p)', 'assert(c, msg)'].map((fn) => (
                    <code key={fn} className="px-1.5 py-0.5 rounded border border-line bg-surface-subtle font-mono text-fg-muted">
                      {fn}
                    </code>
                  ))}
                </div>

                <div className="space-y-2">
                  <div>
                    <h4 className="font-semibold text-fg">前置脚本</h4>
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
                    <h4 className="font-semibold text-fg">后置脚本</h4>
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
                  <label className="block font-semibold text-fg mb-1">接口说明</label>
                  <textarea
                    rows={2}
                    value={activeTab.api.description || ''}
                    onChange={(e) => updateActiveAPI((cur) => ({ ...cur, description: e.target.value }))}
                    placeholder="用途、前置条件、注意事项"
                    className="w-full px-2.5 py-1.5 border border-line-strong rounded-md bg-surface text-fg focus:border-sky-500 focus:outline-none"
                  />
                </div>

                <div className="space-y-2">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="font-semibold text-fg">响应示例</span>
                    <div className="flex items-center gap-2">
                      {activeTab.debugResp?.result && (
                        <button
                          type="button"
                          onClick={() => {
                            const res = activeTab.debugResp!.result
                            saveResponseAsExample({
                              name: `响应 ${res.status || 200}`,
                              status: res.status || 200,
                              content_type: res.content_type || 'application/json',
                              body: res.body || '',
                            })
                          }}
                          className="inline-flex items-center gap-1 px-2 py-1 rounded-md border border-sky-300 bg-sky-50 text-sky-800 hover:bg-sky-100 font-medium"
                        >
                          <Sparkles className="w-3 h-3" /> 存入当前响应
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
                    <p className="text-fg-subtle">暂无示例</p>
                  ) : (
                    examples.map((ex, i) => (
                      <div key={i} className="p-2.5 rounded-lg border border-line bg-surface-subtle space-y-2">
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
                            className="col-span-4 px-2 py-1 border border-line-strong rounded bg-surface"
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
                            className="col-span-2 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
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
                            className="col-span-5 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
                          />
                          <button
                            type="button"
                            onClick={() =>
                              updateActiveAPI((cur) => ({ ...cur, examples: examples.filter((_, idx) => idx !== i) }))
                            }
                            className="col-span-1 justify-self-end p-1 text-fg-subtle hover:text-rose-600"
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
                          className="w-full px-2 py-1 border border-line-strong rounded font-mono bg-surface"
                        />
                      </div>
                    ))
                  )}
                </div>

                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <span className="font-semibold text-fg">响应字段</span>
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
                      <Plus className="w-3.5 h-3.5" /> 添加字段
                    </button>
                  </div>
                  {responseFields.length === 0 ? (
                    <p className="text-fg-subtle">暂无字段</p>
                  ) : (
                    responseFields.map((fd, i) => (
                      <div key={i} className="grid grid-cols-12 gap-2 items-center p-2 rounded border border-line bg-surface-subtle">
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
                          className="col-span-3 px-2 py-1 border border-line-strong rounded font-mono bg-surface"
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
                          className="col-span-2 px-2 py-1 border border-line-strong rounded bg-surface"
                        >
                          <option value="string">string</option>
                          <option value="number">number</option>
                          <option value="integer">integer</option>
                          <option value="boolean">boolean</option>
                          <option value="object">object</option>
                          <option value="array">array</option>
                        </select>
                        <label className="col-span-2 inline-flex items-center gap-1 text-fg-muted cursor-pointer">
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
                          placeholder="含义"
                          onChange={(e) =>
                            updateActiveAPI((cur) => ({
                              ...cur,
                              response_fields: responseFields.map((x, idx) =>
                                idx === i ? { ...x, description: e.target.value } : x
                              ),
                            }))
                          }
                          className="col-span-4 px-2 py-1 border border-line-strong rounded bg-surface"
                        />
                        <button
                          type="button"
                          onClick={() =>
                            updateActiveAPI((cur) => ({
                              ...cur,
                              response_fields: responseFields.filter((_, idx) => idx !== i),
                            }))
                          }
                          className="col-span-1 justify-self-end p-1 text-fg-subtle hover:text-rose-600"
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
            className={`shrink-0 bg-[var(--tg-border)] hover:bg-sky-500 focus:bg-sky-600 focus:outline-none transition-colors flex items-center justify-center ${
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
              fileBaseName={activeTab.api.name}
              activeHistoryId={activeTab.historyId}
              onSaveExample={canWrite ? saveResponseAsExample : undefined}
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
        initialTab={curlTab}
        onClose={() => setCurlOpen(false)}
        onApplyToDraft={applyParsedToDraft}
        onOpenInNewTab={openParsedInNewTab}
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
