import { useEffect, useRef, useState, type FormEvent } from 'react'
import { CircleCheck, Copy, Plus, Save, Trash2, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { Environment, KeyValue } from '../lib/types'
import { useNativeDialog } from '../lib/useNativeDialog'
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

interface Draft {
  name: string
  baseURL: string
  vars: KeyValue[]
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
  const dialogRef = useNativeDialog(open, onClose)
  const [selectedId, setSelectedId] = useState<string>('')
  const [name, setName] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [vars, setVars] = useState<KeyValue[]>([])
  const [version, setVersion] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmDel, setConfirmDel] = useState(false)
  const [cloneSource, setCloneSource] = useState('')
  const draftRef = useRef<Draft | null>(null)
  const confirmTimer = useRef<number>(0)

  useEffect(() => {
    if (!open) return
    const targetId = activeEnvId || environments[0]?.id || 'new'
    setSelectedId(targetId)
  }, [open, activeEnvId, environments.length])

  useEffect(() => {
    setConfirmDel(false)
    if (selectedId === 'new') {
      const draft = draftRef.current
      draftRef.current = null
      setName(draft?.name ?? '')
      setBaseURL(draft?.baseURL ?? '')
      setVars(draft?.vars ?? [])
      setVersion('')
      if (!draft) setCloneSource('')
      return
    }
    setCloneSource('')
    const found = environments.find((e) => e.id === selectedId)
    if (found) {
      setName(found.name)
      setBaseURL(found.base_url || '')
      setVars(found.variables || [])
      setVersion(found.version)
    }
  }, [selectedId, environments])

  useEffect(() => () => window.clearTimeout(confirmTimer.current), [])

  const isNew = selectedId === 'new'

  const handleClone = () => {
    if (isNew) return
    const secretCount = vars.filter((v) => v.secret).length
    draftRef.current = {
      name: `${name.trim()} 副本`,
      baseURL,
      vars: vars.map((v) => (v.secret ? { ...v, value: '' } : { ...v })),
    }
    setCloneSource(name.trim())
    setSelectedId('new')
    if (secretCount > 0) onToast('info', `${secretCount} 个密文变量需重新填写`)
  }

  const handleSave = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    const cleanedVars = vars.filter((v) => v.name.trim() !== '' || v.value !== '')
    try {
      if (isNew) {
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
    if (isNew || !selectedId) return
    if (!confirmDel) {
      setConfirmDel(true)
      window.clearTimeout(confirmTimer.current)
      confirmTimer.current = window.setTimeout(() => setConfirmDel(false), 3000)
      return
    }
    window.clearTimeout(confirmTimer.current)
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
    <dialog
      ref={dialogRef}
      closedby="any"
      aria-labelledby="env-modal-title"
      className="tg-dialog w-[min(56rem,calc(100vw-2rem))] h-[min(40rem,86dvh)] max-h-[86dvh]"
    >
      {open && (
        <>
          <header className="px-5 py-3 border-b border-line flex items-center justify-between bg-surface-subtle shrink-0">
            <h2 id="env-modal-title" className="text-sm font-semibold text-fg">
              环境
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

          <div className="flex flex-1 min-h-0">
            <nav
              aria-label="环境列表"
              className="w-56 shrink-0 border-r border-line bg-surface-subtle p-2.5 flex flex-col gap-1 overflow-y-auto tg-scroller"
            >
              <button
                type="button"
                onClick={() => {
                  draftRef.current = null
                  setCloneSource('')
                  if (selectedId === 'new') {
                    setName('')
                    setBaseURL('')
                    setVars([])
                  } else {
                    setSelectedId('new')
                  }
                }}
                className={`flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs font-medium ${
                  isNew && !cloneSource
                    ? 'bg-sky-600 text-white'
                    : 'text-sky-700 hover:bg-sky-50 border border-dashed border-sky-300'
                }`}
              >
                <Plus className="w-3.5 h-3.5" /> 新建环境
              </button>
              {isNew && cloneSource && (
                <div className="flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-xs bg-sky-600 text-white min-w-0">
                  <Copy className="w-3.5 h-3.5 shrink-0" />
                  <span className="truncate">克隆自 {cloneSource}</span>
                </div>
              )}
              <ul className="mt-1 space-y-0.5">
                {environments.map((env) => {
                  const selected = selectedId === env.id
                  const active = activeEnvId === env.id
                  return (
                    <li key={env.id}>
                      <button
                        type="button"
                        onClick={() => setSelectedId(env.id)}
                        aria-current={selected ? 'true' : undefined}
                        className={`w-full text-left px-2.5 py-1.5 rounded-md text-xs border transition-colors ${
                          selected
                            ? 'bg-surface border-sky-400 text-fg font-semibold shadow-xs'
                            : 'border-transparent text-fg-muted hover:bg-surface-hover'
                        }`}
                      >
                        <span className="flex items-center gap-1.5 min-w-0">
                          <span className="truncate">{env.name}</span>
                          {active && <CircleCheck className="w-3.5 h-3.5 text-emerald-600 shrink-0" aria-label="当前环境" />}
                        </span>
                        {env.base_url && (
                          <span className="block text-[11px] text-fg-subtle truncate font-mono font-normal">{env.base_url}</span>
                        )}
                      </button>
                    </li>
                  )
                })}
              </ul>
            </nav>

            <form onSubmit={handleSave} className="flex-1 min-w-0 flex flex-col text-xs">
              <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 space-y-4">
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <label className="block">
                    <span className="block font-medium text-fg-muted mb-1">名称</span>
                    <input
                      type="text"
                      name="env_name"
                      required
                      autoComplete="off"
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      placeholder="测试环境"
                      className="w-full px-2.5 py-1.5 border border-line-strong rounded-md bg-surface text-fg focus:border-sky-500 focus:outline-none"
                    />
                  </label>
                  <label className="block">
                    <span className="block font-medium text-fg-muted mb-1">Base URL</span>
                    <input
                      type="text"
                      name="base_url"
                      inputMode="url"
                      autoComplete="off"
                      autoCorrect="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      value={baseURL}
                      onChange={(e) => setBaseURL(e.target.value)}
                      placeholder="https://api.example.com"
                      className="w-full px-2.5 py-1.5 border border-line-strong rounded-md bg-surface text-fg font-mono focus:border-sky-500 focus:outline-none"
                    />
                  </label>
                </div>

                <fieldset className="min-w-0">
                  <legend className="font-medium text-fg-muted mb-1.5">变量</legend>
                  <KeyValueEditor items={vars} onChange={setVars} keyPlaceholder="变量名" valuePlaceholder="变量值" />
                </fieldset>
              </div>

              <footer className="shrink-0 flex flex-wrap items-center justify-between gap-2 px-5 py-3 border-t border-line bg-surface-subtle">
                <div className="flex items-center gap-2">
                  {!isNew && (
                    <>
                      <button
                        type="button"
                        onClick={() => void handleDelete()}
                        onBlur={() => setConfirmDel(false)}
                        disabled={busy}
                        className={`inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md border font-medium ${
                          confirmDel
                            ? 'bg-rose-600 border-rose-600 text-white hover:bg-rose-solid'
                            : 'border-rose-200 text-rose-700 hover:bg-rose-50'
                        }`}
                      >
                        <Trash2 className="w-3.5 h-3.5" /> {confirmDel ? '确认删除？' : '删除'}
                      </button>
                      <button
                        type="button"
                        onClick={handleClone}
                        disabled={busy}
                        className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md border border-line-strong bg-surface hover:bg-surface-hover text-fg-muted"
                      >
                        <Copy className="w-3.5 h-3.5" /> 克隆环境
                      </button>
                    </>
                  )}
                </div>
                <div className="flex items-center gap-2">
                  {!isNew && selectedId !== activeEnvId && (
                    <button
                      type="button"
                      onClick={() => onSelectEnv(selectedId)}
                      className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md border border-line-strong bg-surface hover:bg-surface-hover text-fg-muted"
                    >
                      <CircleCheck className="w-3.5 h-3.5" /> 设为当前
                    </button>
                  )}
                  <button
                    type="submit"
                    disabled={busy}
                    className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
                  >
                    <Save className="w-3.5 h-3.5" /> {isNew ? '创建环境' : '保存'}
                  </button>
                </div>
              </footer>
            </form>
          </div>
        </>
      )}
    </dialog>
  )
}
