import { useEffect, useRef, useState } from 'react'
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  Database,
  Eye,
  FlaskConical,
  Plus,
  Rocket,
  Save,
  SlidersHorizontal,
  Trash2,
  Upload,
  X,
} from 'lucide-react'
import { KeyValueEditor } from '../components/KeyValueEditor'
import { LoadConfigEditor } from '../components/LoadConfigEditor'
import { api, APIError } from '../lib/api'
import type {
  DataFile,
  DataSource,
  DebugRunResult,
  Environment,
  FilePreview,
  Scenario,
  ScenarioStep,
  TreeNode,
} from '../lib/types'

interface Props {
  projectId: string
  environments: Environment[]
  activeEnvId: string
  tempVars: Record<string, string>
  onLaunchScenario: (scenarioId: string) => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

interface FlatAPI {
  id: string
  name: string
  method: string
  url: string
  pathLabel: string
}

function flattenAPIs(nodes: TreeNode[], prefix = ''): FlatAPI[] {
  const out: FlatAPI[] = []
  for (const n of nodes) {
    if (n.type === 'folder') {
      const next = prefix ? `${prefix} / ${n.name}` : n.name
      if (n.children) out.push(...flattenAPIs(n.children, next))
    } else {
      out.push({
        id: n.id,
        name: n.name,
        method: n.method || 'GET',
        url: n.url || '',
        pathLabel: prefix ? `${prefix} / ${n.name}` : n.name,
      })
    }
  }
  return out
}

function blankScenario(envId = ''): Partial<Scenario> {
  return {
    name: '新压测场景',
    description: '',
    environment_id: envId,
    variables: [],
    data: [],
    steps: [],
    load: {
      mode: 'vus',
      stages: [
        { duration: '0s', target: 10 },
        { duration: '30s', target: 10 },
      ],
    },
    http: { keep_alive: true },
  }
}

export function ScenariosView({
  projectId,
  environments,
  activeEnvId,
  tempVars,
  onLaunchScenario,
  onToast,
}: Props) {
  const [scenarios, setScenarios] = useState<Scenario[]>([])
  const [apis, setApis] = useState<FlatAPI[]>([])
  const [files, setFiles] = useState<DataFile[]>([])
  const [selectedId, setSelectedId] = useState<string>('')
  const [draft, setDraft] = useState<Partial<Scenario>>(() => blankScenario(activeEnvId))
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [dryRunning, setDryRunning] = useState(false)
  const [dryResult, setDryResult] = useState<DebugRunResult | null>(null)
  const [expandedOverride, setExpandedOverride] = useState<Record<number, boolean>>({})
  const [addApiId, setAddApiId] = useState('')
  const [previewModal, setPreviewModal] = useState<FilePreview | null>(null)
  const [confirmDelScen, setConfirmDelScen] = useState<string>('')
  const [confirmDelFile, setConfirmDelFile] = useState<string>('')

  const loadAll = async (selectId?: string) => {
    try {
      const [scList, tree, flList] = await Promise.all([
        api.listScenarios(projectId),
        api.getTree(projectId),
        api.listFiles(projectId),
      ])
      setScenarios(scList)
      const flat = flattenAPIs(tree.children || [])
      setApis(flat)
      setAddApiId((cur) => (cur && flat.some((a) => a.id === cur) ? cur : flat[0]?.id || ''))
      setFiles(flList)

      const targetId = selectId || selectedId || scList[0]?.id || ''
      if (targetId && scList.some((s) => s.id === targetId)) {
        const full = await api.getScenario(projectId, targetId)
        setSelectedId(full.id)
        setDraft(full)
        setDirty(false)
      } else if (scList.length === 0) {
        setSelectedId('new')
        setDraft(blankScenario(activeEnvId))
        setDirty(false)
      }
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '加载场景数据失败')
    }
  }

  useEffect(() => {
    setSelectedId('')
    setDryResult(null)
    void loadAll()
  }, [projectId])

  const selectScenario = async (id: string) => {
    setConfirmDelScen('')
    setDryResult(null)
    if (id === 'new') {
      setSelectedId('new')
      setDraft(blankScenario(activeEnvId))
      setDirty(true)
      return
    }
    try {
      const full = await api.getScenario(projectId, id)
      setSelectedId(full.id)
      setDraft(full)
      setDirty(false)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '读取场景详情失败')
    }
  }

  const updateDraft = (patch: Partial<Scenario>) => {
    setDraft((prev) => ({ ...prev, ...patch }))
    setDirty(true)
  }

  const handleSave = async (): Promise<Scenario | null> => {
    if (!draft.name?.trim()) {
      onToast('error', '请填写场景名称')
      return null
    }
    if (!draft.steps || draft.steps.length === 0) {
      onToast('error', '场景至少需要包含 1 个接口步骤')
      return null
    }
    setSaving(true)
    try {
      if (draft.id) {
        const saved = await api.updateScenario(projectId, draft.id, draft)
        setDraft(saved)
        setSelectedId(saved.id)
        setDirty(false)
        setScenarios((prev) => prev.map((s) => (s.id === saved.id ? saved : s)))
        onToast('success', `已保存场景「${saved.name}」`)
        return saved
      } else {
        const created = await api.createScenario(projectId, draft)
        setDraft(created)
        setSelectedId(created.id)
        setDirty(false)
        await loadAll(created.id)
        onToast('success', `已创建场景「${created.name}」`)
        return created
      }
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '保存场景失败')
      return null
    } finally {
      setSaving(false)
    }
  }

  const saveRef = useRef(handleSave)
  saveRef.current = handleSave
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        e.preventDefault()
        void saveRef.current()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const handleDryRun = async () => {
    if (!draft.steps || draft.steps.length === 0) {
      onToast('error', '请先添加至少 1 个步骤后再试跑')
      return
    }
    setDryRunning(true)
    setDryResult(null)
    try {
      const res = await api.debugScenario(projectId, {
        scenario: draft,
        environment_id: activeEnvId || draft.environment_id || '',
        variables: tempVars,
        skip_think: true,
      })
      setDryResult(res)
      onToast(
        res.passed ? 'success' : 'error',
        res.passed ? `场景功能试跑全部通过 (${res.duration_ms.toFixed(1)} ms)` : '场景试跑存在未通过步骤，请展开排查'
      )
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '场景试跑失败')
    } finally {
      setDryRunning(false)
    }
  }

  const handleUploadFile = async (file: File) => {
    const existing = files.find((f) => f.name === file.name)
    try {
      await api.uploadFile(projectId, file, existing?.version || '')
      setFiles(await api.listFiles(projectId))
      onToast('success', `已上传数据文件「${file.name}」`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '上传数据文件失败')
    }
  }

  const handlePreviewFile = async (name: string) => {
    try {
      const pv = await api.previewFile(projectId, name, 25)
      setPreviewModal(pv)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '预览文件失败')
    }
  }

  const steps = draft.steps || []
  const dataSources = draft.data || []

  const addStep = () => {
    if (!addApiId) {
      onToast('info', '请先在「接口工作台」创建或导入接口')
      return
    }
    const found = apis.find((a) => a.id === addApiId)
    const nextIdx = steps.length + 1
    const nextStep: ScenarioStep = {
      id: `step${nextIdx}`,
      name: found?.name || `步骤 ${nextIdx}`,
      api_id: addApiId,
    }
    updateDraft({ steps: [...steps, nextStep] })
  }

  const moveStep = (idx: number, delta: number) => {
    const target = idx + delta
    if (target < 0 || target >= steps.length) return
    const next = [...steps]
    const [item] = next.splice(idx, 1)
    next.splice(target, 0, item)
    updateDraft({ steps: next })
  }

  return (
    <div className="flex flex-1 min-h-0 bg-slate-100">
      {/* Left Sidebar: Scenarios + Data Files */}
      <aside className="w-72 bg-slate-50 border-r border-slate-200 flex flex-col shrink-0">
        <div className="p-3 border-b border-slate-200 flex items-center justify-between">
          <span className="text-xs font-semibold text-slate-800">压测场景列表 ({scenarios.length})</span>
          <button
            type="button"
            onClick={() => void selectScenario('new')}
            className="inline-flex items-center gap-1 px-2.5 py-1 rounded bg-sky-600 hover:bg-sky-700 text-white text-xs font-medium"
          >
            <Plus className="w-3.5 h-3.5" /> 新建场景
          </button>
        </div>

        <div className="flex-1 overflow-y-auto p-2 space-y-1">
          {scenarios.map((sc) => (
            <div
              key={sc.id}
              onClick={() => void selectScenario(sc.id)}
              className={`group flex items-center justify-between gap-2 px-2.5 py-2 rounded-lg text-xs cursor-pointer ${
                selectedId === sc.id
                  ? 'bg-sky-100 text-sky-950 font-semibold'
                  : 'text-slate-700 hover:bg-slate-200/60'
              }`}
            >
              <div className="min-w-0">
                <div className="truncate">{sc.name}</div>
                <div className="text-[10px] text-slate-500">
                  {sc.steps?.length || 0} 步骤 · {sc.load?.mode === 'rate' ? '速率' : '并发'}
                </div>
              </div>
              <button
                type="button"
                title={confirmDelScen === sc.id ? '再次点击确认删除' : '删除场景'}
                onClick={async (e) => {
                  e.stopPropagation()
                  if (confirmDelScen !== sc.id) {
                    setConfirmDelScen(sc.id)
                    return
                  }
                  try {
                    await api.deleteScenario(projectId, sc.id, sc.version)
                    onToast('success', `已删除场景「${sc.name}」`)
                    setConfirmDelScen('')
                    if (selectedId === sc.id) setSelectedId('')
                    await loadAll()
                  } catch (err) {
                    onToast('error', err instanceof APIError ? err.message : '删除场景失败')
                  }
                }}
                className={`p-1 rounded shrink-0 ${
                  confirmDelScen === sc.id
                    ? 'bg-rose-600 text-white opacity-100'
                    : 'opacity-0 group-hover:opacity-100 text-slate-400 hover:text-rose-600'
                }`}
              >
                <Trash2 className="w-3.5 h-3.5" />
              </button>
            </div>
          ))}
        </div>

        {/* Data Files Panel */}
        <div className="border-t border-slate-200 p-3 bg-white space-y-2 max-h-64 flex flex-col">
          <div className="flex items-center justify-between">
            <span className="text-xs font-semibold text-slate-800 flex items-center gap-1.5">
              <Database className="w-3.5 h-3.5 text-sky-600" /> 数据文件 ({files.length})
            </span>
            <label className="inline-flex items-center gap-1 px-2 py-1 rounded border border-slate-300 bg-slate-50 hover:bg-slate-100 text-[11px] font-medium text-slate-700 cursor-pointer">
              <Upload className="w-3 h-3 text-sky-600" /> 上传 CSV/文件
              <input
                type="file"
                onChange={(e) => {
                  const f = e.target.files?.[0]
                  if (f) void handleUploadFile(f)
                  e.target.value = ''
                }}
                className="hidden"
              />
            </label>
          </div>
          <div className="overflow-y-auto space-y-1 text-xs">
            {files.length === 0 ? (
              <p className="text-[11px] text-slate-400 py-2">暂无数据文件（支持 CSV 参数化与二进制上传）。</p>
            ) : (
              files.map((df) => (
                <div
                  key={df.name}
                  className="flex items-center justify-between gap-1.5 px-2 py-1 rounded border border-slate-100 bg-slate-50"
                >
                  <div className="min-w-0">
                    <div className="font-mono truncate text-slate-800">{df.name}</div>
                    <div className="text-[10px] text-slate-400">{Math.max(1, Math.round(df.size / 1024))} KB</div>
                  </div>
                  <div className="inline-flex items-center gap-1 shrink-0">
                    <button
                      type="button"
                      title="预览文件内容"
                      onClick={() => void handlePreviewFile(df.name)}
                      className="p-1 text-slate-500 hover:text-sky-600"
                    >
                      <Eye className="w-3.5 h-3.5" />
                    </button>
                    <button
                      type="button"
                      title={confirmDelFile === df.name ? '再次点击确认删除' : '删除文件'}
                      onClick={async () => {
                        if (confirmDelFile !== df.name) {
                          setConfirmDelFile(df.name)
                          return
                        }
                        try {
                          await api.deleteFile(projectId, df.name, df.version)
                          setConfirmDelFile('')
                          setFiles(await api.listFiles(projectId))
                          onToast('success', `已删除数据文件「${df.name}」`)
                        } catch (err) {
                          onToast('error', err instanceof APIError ? err.message : '删除文件失败')
                        }
                      }}
                      className={`p-1 rounded ${
                        confirmDelFile === df.name ? 'bg-rose-600 text-white' : 'text-slate-400 hover:text-rose-600'
                      }`}
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                </div>
              ))
            )}
          </div>
        </div>
      </aside>

      {/* Main Scenario Builder */}
      <main className="flex-1 min-w-0 overflow-y-auto p-5 space-y-5 text-xs">
        <div className="bg-white rounded-xl border border-slate-200 p-4 shadow-xs flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-wrap items-center gap-3 flex-1 min-w-[260px]">
            <input
              type="text"
              value={draft.name || ''}
              onChange={(e) => updateDraft({ name: e.target.value })}
              placeholder="输入场景名称"
              className="px-3 py-1.5 border border-slate-300 rounded-lg font-semibold text-sm text-slate-900 focus:border-sky-500 focus:outline-none min-w-[220px]"
            />
            <select
              value={draft.environment_id || ''}
              onChange={(e) => updateDraft({ environment_id: e.target.value })}
              className="px-2.5 py-1.5 border border-slate-300 rounded-lg bg-slate-50 text-xs"
            >
              <option value="">跟随顶部激活环境</option>
              {environments.map((env) => (
                <option key={env.id} value={env.id}>
                  默认环境：{env.name}
                </option>
              ))}
            </select>
            {dirty && <span className="text-[11px] text-amber-600 font-medium">● 有未保存修改</span>}
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={dryRunning}
              onClick={() => void handleDryRun()}
              className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-sky-300 bg-sky-50 hover:bg-sky-100 text-sky-800 font-medium disabled:opacity-50"
            >
              <FlaskConical className="w-3.5 h-3.5" />
              {dryRunning ? '试跑中...' : '功能试跑 (单轮)'}
            </button>
            <button
              type="button"
              disabled={saving}
              onClick={() => void handleSave()}
              className="inline-flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-[#0B2A4A] hover:bg-[#123d69] text-white font-medium disabled:opacity-50"
            >
              <Save className="w-3.5 h-3.5" /> 保存场景
            </button>
            <button
              type="button"
              disabled={saving || dryRunning}
              onClick={async () => {
                if (dirty || !draft.id) {
                  const saved = await handleSave()
                  if (!saved) return
                  onLaunchScenario(saved.id)
                  return
                }
                onLaunchScenario(draft.id)
              }}
              className="inline-flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-amber-500 hover:bg-amber-600 text-slate-950 font-semibold disabled:opacity-50"
            >
              <Rocket className="w-3.5 h-3.5" /> 启动压测
            </button>
          </div>
        </div>

        {/* Scenario Variables & CSV Binding */}
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          <div className="bg-white rounded-xl border border-slate-200 p-4 space-y-2.5">
            <h3 className="font-semibold text-slate-800">场景级变量（可被环境变量与临时变量覆盖）</h3>
            <KeyValueEditor
              items={draft.variables || []}
              onChange={(next) => updateDraft({ variables: next })}
              compact
            />
          </div>

          <div className="bg-white rounded-xl border border-slate-200 p-4 space-y-2.5">
            <div className="flex items-center justify-between">
              <h3 className="font-semibold text-slate-800">CSV 参数化数据源（模板引用 `{'{{别名.列名}}'}`）</h3>
              <button
                type="button"
                onClick={() => {
                  const csvFile = files.find((f) => f.name.toLowerCase().endsWith('.csv'))?.name || files[0]?.name || ''
                  updateDraft({
                    data: [
                      ...dataSources,
                      { name: `ds${dataSources.length + 1}`, file: csvFile, mode: 'sequential' },
                    ],
                  })
                }}
                className="inline-flex items-center gap-1 text-sky-600 hover:underline font-medium"
              >
                <Plus className="w-3.5 h-3.5" /> 绑定数据文件
              </button>
            </div>
            {dataSources.length === 0 ? (
              <p className="text-slate-400">未绑定 CSV 数据源。</p>
            ) : (
              <div className="space-y-2">
                {dataSources.map((ds, i) => {
                  const updateDS = (patch: Partial<DataSource>) =>
                    updateDraft({ data: dataSources.map((x, idx) => (idx === i ? { ...x, ...patch } : x)) })
                  return (
                    <div key={i} className="grid grid-cols-12 gap-1.5 items-center">
                      <input
                        type="text"
                        value={ds.name}
                        placeholder="引用别名"
                        onChange={(e) => updateDS({ name: e.target.value })}
                        className="col-span-3 px-2 py-1 border border-slate-300 rounded font-mono"
                      />
                      <select
                        value={ds.file}
                        onChange={(e) => updateDS({ file: e.target.value })}
                        className="col-span-5 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                      >
                        <option value="">选择 CSV 文件...</option>
                        {files
                          .filter((f) => f.name.toLowerCase().endsWith('.csv'))
                          .map((f) => (
                            <option key={f.name} value={f.name}>
                              {f.name}
                            </option>
                          ))}
                      </select>
                      <select
                        value={ds.mode || 'sequential'}
                        onChange={(e) => updateDS({ mode: e.target.value as DataSource['mode'] })}
                        className="col-span-3 px-1.5 py-1 border border-slate-300 rounded bg-white"
                      >
                        <option value="sequential">顺序循环</option>
                        <option value="random">随机读取</option>
                        <option value="unique">唯一分配</option>
                      </select>
                      <button
                        type="button"
                        onClick={() => updateDraft({ data: dataSources.filter((_, idx) => idx !== i) })}
                        className="col-span-1 justify-self-end p-1 text-slate-400 hover:text-rose-600"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
        </div>

        {/* Steps Sequence */}
        <div className="bg-white rounded-xl border border-slate-200 p-4 space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3 className="font-semibold text-slate-800">请求步骤编排（按顺序串行执行，支持单步参数覆盖）</h3>
            <div className="flex items-center gap-2">
              <select
                value={addApiId}
                onChange={(e) => setAddApiId(e.target.value)}
                className="px-2.5 py-1.5 border border-slate-300 rounded-md bg-white max-w-xs"
              >
                <option value="">选择已有接口...</option>
                {apis.map((a) => (
                  <option key={a.id} value={a.id}>
                    [{a.method}] {a.pathLabel}
                  </option>
                ))}
              </select>
              <button
                type="button"
                onClick={addStep}
                className="inline-flex items-center gap-1 px-3 py-1.5 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium"
              >
                <Plus className="w-3.5 h-3.5" /> 添加步骤
              </button>
            </div>
          </div>

          {steps.length === 0 ? (
            <div className="text-center py-6 text-slate-400 border border-dashed border-slate-200 rounded-lg">
              请从上方下拉框选择一个接口添加为场景步骤。
            </div>
          ) : (
            <div className="space-y-2">
              {steps.map((st, idx) => {
                const refAPI = apis.find((a) => a.id === st.api_id)
                const isOvOpen = Boolean(expandedOverride[idx])
                const updateStep = (patch: Partial<ScenarioStep>) =>
                  updateDraft({ steps: steps.map((row, i) => (i === idx ? { ...row, ...patch } : row)) })

                return (
                  <div
                    key={idx}
                    className={`border rounded-lg p-3 ${
                      st.disabled ? 'bg-slate-50 border-slate-200 opacity-65' : 'bg-white border-slate-200'
                    }`}
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <input
                        type="checkbox"
                        checked={!st.disabled}
                        onChange={(e) => updateStep({ disabled: !e.target.checked })}
                        title="启用或禁用此步骤"
                      />
                      <span className="font-mono text-slate-400">#{idx + 1}</span>
                      <input
                        type="text"
                        value={st.id}
                        placeholder="step ID"
                        onChange={(e) => updateStep({ id: e.target.value })}
                        className="w-28 px-2 py-1 border border-slate-300 rounded font-mono"
                      />
                      <input
                        type="text"
                        value={st.name || ''}
                        placeholder={refAPI?.name || '步骤名称'}
                        onChange={(e) => updateStep({ name: e.target.value })}
                        className="w-40 px-2 py-1 border border-slate-300 rounded"
                      />
                      <span className="font-mono text-[11px] px-2 py-0.5 rounded bg-slate-100 text-slate-700 truncate max-w-xs">
                        {refAPI ? `${refAPI.method} ${refAPI.url}` : `API ID: ${st.api_id}`}
                      </span>

                      <div className="flex items-center gap-1 ml-auto">
                        <span className="text-slate-500">思考时间：</span>
                        <select
                          value={st.think?.type || ''}
                          onChange={(e) => {
                            const t = e.target.value as '' | 'fixed' | 'uniform'
                            updateStep({
                              think: t ? { type: t, min: '500ms', max: t === 'uniform' ? '1500ms' : undefined } : null,
                            })
                          }}
                          className="px-1.5 py-1 border border-slate-300 rounded bg-white"
                        >
                          <option value="">无</option>
                          <option value="fixed">固定</option>
                          <option value="uniform">随机区间</option>
                        </select>
                        {st.think?.type === 'fixed' && (
                          <input
                            type="text"
                            value={st.think.min || ''}
                            onChange={(e) => updateStep({ think: { ...st.think!, min: e.target.value } })}
                            placeholder="500ms"
                            className="w-16 px-1.5 py-1 border border-slate-300 rounded font-mono"
                          />
                        )}
                        {st.think?.type === 'uniform' && (
                          <>
                            <input
                              type="text"
                              value={st.think.min || ''}
                              onChange={(e) => updateStep({ think: { ...st.think!, min: e.target.value } })}
                              placeholder="min"
                              className="w-16 px-1.5 py-1 border border-slate-300 rounded font-mono"
                            />
                            <input
                              type="text"
                              value={st.think.max || ''}
                              onChange={(e) => updateStep({ think: { ...st.think!, max: e.target.value } })}
                              placeholder="max"
                              className="w-16 px-1.5 py-1 border border-slate-300 rounded font-mono"
                            />
                          </>
                        )}

                        <button
                          type="button"
                          onClick={() => setExpandedOverride((m) => ({ ...m, [idx]: !m[idx] }))}
                          className={`inline-flex items-center gap-1 px-2 py-1 rounded border ${
                            st.override
                              ? 'bg-sky-50 border-sky-300 text-sky-700'
                              : 'border-slate-200 text-slate-600 hover:bg-slate-50'
                          }`}
                        >
                          <SlidersHorizontal className="w-3 h-3" /> 覆盖参数
                        </button>
                        <button type="button" onClick={() => moveStep(idx, -1)} className="p-1 text-slate-400 hover:text-slate-700">
                          <ArrowUp className="w-3.5 h-3.5" />
                        </button>
                        <button type="button" onClick={() => moveStep(idx, 1)} className="p-1 text-slate-400 hover:text-slate-700">
                          <ArrowDown className="w-3.5 h-3.5" />
                        </button>
                        <button
                          type="button"
                          onClick={() => updateDraft({ steps: steps.filter((_, i) => i !== idx) })}
                          className="p-1 text-slate-400 hover:text-rose-600"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    </div>

                    {isOvOpen && (
                      <div className="mt-3 pt-3 border-t border-slate-200 space-y-3 bg-slate-50/60 p-3 rounded">
                        <div className="grid grid-cols-1 sm:grid-cols-4 gap-2">
                          <input
                            type="text"
                            value={st.override?.method || ''}
                            placeholder="覆盖方法 (如 POST)"
                            onChange={(e) =>
                              updateStep({ override: { ...(st.override || {}), method: e.target.value || undefined } })
                            }
                            className="px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                          />
                          <input
                            type="text"
                            value={st.override?.url || ''}
                            placeholder="覆盖 URL（留空沿用原接口）"
                            onChange={(e) =>
                              updateStep({ override: { ...(st.override || {}), url: e.target.value || undefined } })
                            }
                            className="sm:col-span-3 px-2 py-1 border border-slate-300 rounded font-mono bg-white"
                          />
                        </div>
                        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                          <div>
                            <div className="font-medium text-slate-600 mb-1">追加/覆盖请求头</div>
                            <KeyValueEditor
                              items={st.override?.headers || []}
                              onChange={(next) =>
                                updateStep({ override: { ...(st.override || {}), headers: next } })
                              }
                              compact
                            />
                          </div>
                          <div>
                            <div className="font-medium text-slate-600 mb-1">覆盖 JSON/文本请求体</div>
                            <textarea
                              rows={3}
                              value={st.override?.body?.content || ''}
                              placeholder='留空则继承原接口请求体；输入 JSON 将以 JSON 类型覆盖'
                              onChange={(e) => {
                                const val = e.target.value
                                updateStep({
                                  override: {
                                    ...(st.override || {}),
                                    body: val ? { type: val.trim().startsWith('{') || val.trim().startsWith('[') ? 'json' : 'raw', content: val } : undefined,
                                  },
                                })
                              }}
                              className="w-full p-2 border border-slate-300 rounded font-mono bg-white"
                            />
                          </div>
                        </div>
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </div>

        {/* Load Profile + SLA Thresholds */}
        <div className="bg-white rounded-xl border border-slate-200 p-4 space-y-2">
          <h3 className="font-semibold text-slate-800">负载阶段编排、阈值 SLA 与连接选项</h3>
          <LoadConfigEditor
            load={draft.load || { mode: 'vus', stages: [{ duration: '0s', target: 10 }, { duration: '30s', target: 10 }] }}
            onChange={(nextLoad) => updateDraft({ load: nextLoad })}
            http={draft.http}
            onHTTPChange={(nextHTTP) => updateDraft({ http: nextHTTP })}
            stepOptions={steps.map((s) => ({ id: s.id, name: s.name }))}
          />
        </div>

        {/* Functional Dry-Run Result */}
        {dryResult && (
          <div className="bg-white rounded-xl border border-slate-200 p-4 space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2 font-semibold text-sm">
                <span className={dryResult.passed ? 'text-emerald-700' : 'text-rose-700'}>
                  功能试跑{dryResult.passed ? '全部通过' : '存在失败'}
                </span>
                <span className="text-xs font-mono text-slate-500">总耗时 {dryResult.duration_ms.toFixed(1)} ms</span>
              </div>
              {dryResult.final_vars && Object.keys(dryResult.final_vars).length > 0 && (
                <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
                  <span className="text-slate-500">传递变量终值：</span>
                  {Object.entries(dryResult.final_vars).map(([k, v]) => (
                    <span key={k} className="px-2 py-0.5 rounded bg-sky-50 border border-sky-200 font-mono text-sky-900">
                      {k}={v}
                    </span>
                  ))}
                </div>
              )}
            </div>

            <div className="space-y-2">
              {dryResult.steps.map((step, i) => (
                <DryRunStepCard key={i} index={i} step={step} />
              ))}
            </div>
          </div>
        )}
      </main>

      {/* File Preview Modal */}
      {previewModal && (
        <div className="fixed inset-0 z-40 bg-slate-900/40 flex items-center justify-center p-4">
          <div className="w-full max-w-3xl bg-white rounded-xl border border-slate-200 shadow-xl overflow-hidden flex flex-col max-h-[82vh]">
            <div className="px-5 py-3 border-b border-slate-200 flex items-center justify-between bg-slate-50">
              <div className="text-sm font-semibold text-slate-900 font-mono">
                文件预览：{previewModal.name}
                {previewModal.is_csv && `（共 ${previewModal.total_rows || 0} 行数据）`}
              </div>
              <button type="button" onClick={() => setPreviewModal(null)} className="p-1 text-slate-400 hover:text-slate-600">
                <X className="w-4 h-4" />
              </button>
            </div>
            <div className="p-4 overflow-auto text-xs">
              {previewModal.is_csv ? (
                <table className="w-full text-left border-collapse border border-slate-200 font-mono">
                  <thead className="bg-slate-50 border-b border-slate-200">
                    <tr>
                      {(previewModal.columns || []).map((col, idx) => (
                        <th key={idx} className="px-2.5 py-1.5 border-r border-slate-200 text-sky-800">
                          {col}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-100">
                    {(previewModal.rows || []).map((row, rIdx) => (
                      <tr key={rIdx}>
                        {row.map((cell, cIdx) => (
                          <td key={cIdx} className="px-2.5 py-1 border-r border-slate-100">
                            {cell}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <pre className="p-3 rounded bg-slate-900 text-slate-100 font-mono overflow-auto whitespace-pre-wrap">
                  {previewModal.text || '(非 UTF-8 文本文件或空文件)'}
                </pre>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function DryRunStepCard({ index, step }: { index: number; step: DebugRunResult['steps'][number] }) {
  const [open, setOpen] = useState(!step.passed)
  return (
    <div className="border border-slate-200 rounded-lg overflow-hidden">
      <div
        onClick={() => setOpen((o) => !o)}
        className="px-3 py-2 bg-slate-50 flex items-center justify-between gap-2 cursor-pointer hover:bg-slate-100"
      >
        <div className="flex items-center gap-2 font-mono">
          {open ? <ChevronDown className="w-3.5 h-3.5" /> : <ChevronRight className="w-3.5 h-3.5" />}
          <span className="font-semibold">
            #{index + 1} {step.step_name || step.step_id}
          </span>
          <span className="text-slate-600">
            {step.actual_request.method} {step.actual_request.url}
          </span>
        </div>
        <div className="flex items-center gap-2 font-mono">
          <span>HTTP {step.status || 'ERR'}</span>
          <span>{step.timing.total_ms.toFixed(1)} ms</span>
          <span className={step.passed ? 'text-emerald-700 font-semibold' : 'text-rose-700 font-semibold'}>
            {step.passed ? '✓ 通过' : '✗ 失败'}
          </span>
        </div>
      </div>
      {open && (
        <div className="p-3 bg-white border-t border-slate-200 space-y-2 text-xs">
          {step.error && <div className="text-rose-700 font-medium">{step.error}</div>}
          {(step.extracts?.length || 0) > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {step.extracts!.map((ex, idx) => (
                <span
                  key={idx}
                  className={`px-2 py-0.5 rounded border font-mono ${
                    ex.matched ? 'bg-emerald-50 border-emerald-200 text-emerald-800' : 'bg-rose-50 border-rose-200 text-rose-800'
                  }`}
                >
                  提取 {ex.name}={ex.matched ? ex.value : `失败(${ex.error || '未匹配'})`}
                </span>
              ))}
            </div>
          )}
          {(step.asserts?.length || 0) > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {step.asserts!.map((as, idx) => (
                <span
                  key={idx}
                  className={`px-2 py-0.5 rounded border ${
                    as.passed ? 'bg-emerald-50 border-emerald-200 text-emerald-800' : 'bg-rose-50 border-rose-200 text-rose-800'
                  }`}
                >
                  {as.passed ? '✓' : '✗'} {as.description || as.desc} {as.message}
                </span>
              ))}
            </div>
          )}
          <pre className="p-2.5 rounded bg-slate-900 text-slate-100 font-mono overflow-auto max-h-40 whitespace-pre-wrap">
            {step.body || '(空响应体)'}
          </pre>
        </div>
      )}
    </div>
  )
}
