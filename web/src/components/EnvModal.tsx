import { useEffect, useState, type FormEvent } from 'react'
import { Plus, Save, Trash2, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { Environment, KeyValue } from '../lib/types'
import { KeyValueEditor } from './KeyValueEditor'

interface Props {
  open: boolean
  projectId: string
  environments: Environment[]
  activeEnvId: string
  onClose: () => void
  onSelectEnv: (id: string) => void
  onEnvironmentsChanged: () => Promise<void>
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

export function EnvModal({
  open,
  projectId,
  environments,
  activeEnvId,
  onClose,
  onSelectEnv,
  onEnvironmentsChanged,
  onToast,
}: Props) {
  const [selectedId, setSelectedId] = useState<string>('')
  const [name, setName] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [vars, setVars] = useState<KeyValue[]>([])
  const [version, setVersion] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmDel, setConfirmDel] = useState(false)

  useEffect(() => {
    if (!open) return
    const targetId = activeEnvId || environments[0]?.id || 'new'
    setSelectedId(targetId)
  }, [open, activeEnvId, environments.length])

  useEffect(() => {
    setConfirmDel(false)
    if (selectedId === 'new') {
      setName('')
      setBaseURL('http://127.0.0.1:8080')
      setVars([])
      setVersion('')
      return
    }
    const found = environments.find((e) => e.id === selectedId)
    if (found) {
      setName(found.name)
      setBaseURL(found.base_url || '')
      setVars(found.variables || [])
      setVersion(found.version)
    }
  }, [selectedId, environments])

  if (!open) return null

  const handleSave = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    const cleanedVars = vars.filter((v) => v.name.trim() !== '' || v.value !== '')
    try {
      if (selectedId === 'new') {
        const created = await api.createEnvironment(projectId, {
          name: name.trim(),
          base_url: baseURL.trim(),
          variables: cleanedVars,
        })
        await onEnvironmentsChanged()
        setSelectedId(created.id)
        onSelectEnv(created.id)
        onToast('success', `已创建环境「${created.name}」`)
      } else {
        await api.updateEnvironment(projectId, selectedId, {
          id: selectedId,
          name: name.trim(),
          base_url: baseURL.trim(),
          variables: cleanedVars,
          version,
        })
        await onEnvironmentsChanged()
        onToast('success', '环境配置已更新')
      }
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '保存环境失败')
    } finally {
      setBusy(false)
    }
  }

  const handleDelete = async () => {
    if (selectedId === 'new' || !selectedId) return
    if (!confirmDel) {
      setConfirmDel(true)
      return
    }
    setBusy(true)
    try {
      await api.deleteEnvironment(projectId, selectedId, version)
      await onEnvironmentsChanged()
      if (activeEnvId === selectedId) {
        onSelectEnv('')
      }
      setSelectedId('new')
      onToast('success', '环境已删除')
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '删除环境失败')
    } finally {
      setBusy(false)
      setConfirmDel(false)
    }
  }

  return (
    <div className="fixed inset-0 z-40 bg-slate-900/40 flex items-center justify-center p-4">
      <div className="w-full max-w-3xl bg-white rounded-xl border border-slate-200 shadow-xl overflow-hidden flex flex-col max-h-[82vh]">
        <div className="px-5 py-3.5 border-b border-slate-200 flex items-center justify-between bg-slate-50">
          <h2 className="text-sm font-semibold text-slate-900">环境与环境变量管理</h2>
          <button type="button" onClick={onClose} className="p-1 text-slate-400 hover:text-slate-600 rounded">
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="flex flex-1 min-h-0">
          <div className="w-52 border-r border-slate-200 bg-slate-50/70 p-3 flex flex-col gap-1 overflow-y-auto">
            <button
              type="button"
              onClick={() => setSelectedId('new')}
              className={`flex items-center gap-1.5 px-2.5 py-2 rounded-md text-xs font-medium ${
                selectedId === 'new'
                  ? 'bg-sky-600 text-white'
                  : 'text-sky-700 hover:bg-sky-50 border border-dashed border-sky-300'
              }`}
            >
              <Plus className="w-3.5 h-3.5" /> 新建环境
            </button>
            <div className="mt-1 space-y-1">
              {environments.map((env) => (
                <button
                  key={env.id}
                  type="button"
                  onClick={() => setSelectedId(env.id)}
                  className={`w-full text-left px-2.5 py-2 rounded-md text-xs transition-colors ${
                    selectedId === env.id
                      ? 'bg-white border border-sky-400 text-slate-900 font-semibold shadow-xs'
                      : 'text-slate-700 hover:bg-slate-100'
                  }`}
                >
                  <div className="truncate">{env.name}</div>
                  {env.base_url && <div className="text-[10px] text-slate-400 truncate font-mono">{env.base_url}</div>}
                </button>
              ))}
            </div>
          </div>

          <form onSubmit={handleSave} className="flex-1 p-5 overflow-y-auto space-y-4 text-xs">
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="block font-medium text-slate-700 mb-1">环境名称</label>
                <input
                  type="text"
                  required
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="例如 测试环境 / 生产环境"
                  className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
                />
              </div>
              <div>
                <label className="block font-medium text-slate-700 mb-1">Base URL（基础地址）</label>
                <input
                  type="text"
                  value={baseURL}
                  onChange={(e) => setBaseURL(e.target.value)}
                  placeholder="https://api.example.com"
                  className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono focus:border-sky-500 focus:outline-none"
                />
              </div>
            </div>

            <div>
              <label className="block font-medium text-slate-700 mb-1">
                环境变量（优先级高于项目变量）
              </label>
              <p className="text-[11px] text-slate-500 mb-1.5">
                勾选「密」的敏感变量会采用 AES-256-GCM 加密写入本地密钥保险箱（Vault），不再明文落盘至 YAML。
              </p>
              <KeyValueEditor items={vars} onChange={setVars} keyPlaceholder="变量名" valuePlaceholder="变量值" />
            </div>

            <div className="flex items-center justify-between pt-3 border-t border-slate-200">
              <div>
                {selectedId !== 'new' && (
                  <button
                    type="button"
                    onClick={() => void handleDelete()}
                    disabled={busy}
                    className={`inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md border font-medium ${
                      confirmDel
                        ? 'bg-rose-600 border-rose-600 text-white'
                        : 'border-rose-200 text-rose-700 hover:bg-rose-50'
                    }`}
                  >
                    <Trash2 className="w-3.5 h-3.5" /> {confirmDel ? '再次点击确认删除' : '删除此环境'}
                  </button>
                )}
              </div>
              <div className="flex items-center gap-2">
                {selectedId !== 'new' && selectedId !== activeEnvId && (
                  <button
                    type="button"
                    onClick={() => onSelectEnv(selectedId)}
                    className="px-3 py-1.5 rounded-md border border-slate-300 bg-white hover:bg-slate-50 text-slate-700"
                  >
                    设为当前激活环境
                  </button>
                )}
                <button
                  type="submit"
                  disabled={busy}
                  className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
                >
                  <Save className="w-3.5 h-3.5" /> {selectedId === 'new' ? '创建环境' : '保存环境'}
                </button>
              </div>
            </div>
          </form>
        </div>
      </div>
    </div>
  )
}
