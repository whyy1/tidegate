import { useState, type FormEvent } from 'react'
import { Pin, Plus, Trash2, X, Zap } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { Environment } from '../lib/types'

interface Props {
  projectId: string
  activeEnv: Environment | null
  tempVars: Record<string, string>
  onChange: (next: Record<string, string>) => void
  onEnvironmentsChanged: () => Promise<void>
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

export function TempVarsPopover({
  projectId,
  activeEnv,
  tempVars,
  onChange,
  onEnvironmentsChanged,
  onToast,
}: Props) {
  const [open, setOpen] = useState(false)
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')
  const [pinning, setPinning] = useState<string>('')

  const entries = Object.entries(tempVars)

  const handleAdd = (e: FormEvent) => {
    e.preventDefault()
    const k = newKey.trim()
    if (!k) return
    onChange({ ...tempVars, [k]: newVal })
    setNewKey('')
    setNewVal('')
  }

  const handlePinToEnv = async (k: string, val: string) => {
    if (!activeEnv) {
      onToast('info', '请先在顶部选择一个激活环境，再固定变量')
      return
    }
    setPinning(k)
    try {
      const existing = activeEnv.variables || []
      const idx = existing.findIndex((v) => v.name === k)
      const nextVars =
        idx >= 0
          ? existing.map((v, i) => (i === idx ? { ...v, value: val, disabled: false } : v))
          : [...existing, { name: k, value: val, description: '由调试临时变量池固定' }]

      await api.updateEnvironment(projectId, activeEnv.id, {
        id: activeEnv.id,
        name: activeEnv.name,
        base_url: activeEnv.base_url || '',
        variables: nextVars,
        version: activeEnv.version,
      })
      await onEnvironmentsChanged()
      onToast('success', `已将 {{${k}}} 固定到环境「${activeEnv.name}」`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '固定到环境失败')
    } finally {
      setPinning('')
    }
  }

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs font-medium border transition-colors ${
          entries.length > 0
            ? 'bg-amber-400/20 border-amber-300/60 text-amber-100 hover:bg-amber-400/30'
            : 'bg-white/10 border-white/15 text-slate-200 hover:bg-white/20'
        }`}
        title="调试提取的临时会话变量（最高覆盖优先级，不写脏环境文件）"
      >
        <Zap className="w-3.5 h-3.5 text-amber-300" />
        <span>临时变量</span>
        <span className="px-1.5 py-0.2 rounded-full bg-white/15 text-[11px] font-mono">{entries.length}</span>
      </button>

      {open && (
        <div className="fixed inset-0 z-30" onClick={() => setOpen(false)} />
      )}
      {open && (
        <div className="absolute right-0 mt-2 w-96 rounded-xl border border-slate-200 bg-white text-slate-900 shadow-2xl z-40 overflow-hidden">
          <div className="px-4 py-2.5 bg-slate-50 border-b border-slate-200 flex items-center justify-between">
            <div>
              <div className="text-xs font-semibold">会话临时变量池（最高优先级）</div>
              <div className="text-[11px] text-slate-500">仅驻留当前浏览器会话，不会自动污染环境变量 YAML</div>
            </div>
            <button type="button" onClick={() => setOpen(false)} className="text-slate-400 hover:text-slate-600 p-1">
              <X className="w-4 h-4" />
            </button>
          </div>

          <div className="p-3 max-h-72 overflow-y-auto space-y-2 text-xs">
            {entries.length === 0 ? (
              <div className="text-slate-400 text-center py-4">
                暂无临时变量。运行带「提取器」的接口调试后会自动写入此处。
              </div>
            ) : (
              entries.map(([k, val]) => (
                <div key={k} className="flex items-center gap-1.5 p-1.5 rounded border border-slate-200 bg-slate-50/70">
                  <span className="font-mono font-semibold text-sky-800 px-1 truncate max-w-[100px]" title={k}>
                    {k}
                  </span>
                  <input
                    type="text"
                    value={val}
                    onChange={(e) => onChange({ ...tempVars, [k]: e.target.value })}
                    className="flex-1 min-w-0 px-2 py-1 border border-slate-200 rounded bg-white font-mono text-xs focus:border-sky-500 focus:outline-none"
                  />
                  <button
                    type="button"
                    disabled={pinning === k}
                    title={activeEnv ? `固定到环境「${activeEnv.name}」` : '请先激活一个环境'}
                    onClick={() => void handlePinToEnv(k, val)}
                    className="p-1 text-slate-500 hover:text-sky-600 disabled:opacity-40"
                  >
                    <Pin className="w-3.5 h-3.5" />
                  </button>
                  <button
                    type="button"
                    title="移除"
                    onClick={() => {
                      const next = { ...tempVars }
                      delete next[k]
                      onChange(next)
                    }}
                    className="p-1 text-slate-400 hover:text-rose-600"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                </div>
              ))
            )}
          </div>

          <form onSubmit={handleAdd} className="p-3 border-t border-slate-200 bg-slate-50 space-y-2 text-xs">
            <div className="flex items-center gap-1.5">
              <input
                type="text"
                placeholder="变量名"
                value={newKey}
                onChange={(e) => setNewKey(e.target.value)}
                className="w-28 px-2 py-1 border border-slate-300 rounded font-mono bg-white focus:border-sky-500 focus:outline-none"
              />
              <input
                type="text"
                placeholder="变量值"
                value={newVal}
                onChange={(e) => setNewVal(e.target.value)}
                className="flex-1 px-2 py-1 border border-slate-300 rounded font-mono bg-white focus:border-sky-500 focus:outline-none"
              />
              <button
                type="submit"
                className="px-2.5 py-1 rounded bg-sky-600 hover:bg-sky-700 text-white font-medium inline-flex items-center gap-1"
              >
                <Plus className="w-3 h-3" /> 添加
              </button>
            </div>
            {entries.length > 0 && (
              <div className="flex justify-end">
                <button
                  type="button"
                  onClick={() => onChange({})}
                  className="text-[11px] text-rose-600 hover:text-rose-700"
                >
                  清空全部临时变量
                </button>
              </div>
            )}
          </form>
        </div>
      )}
    </div>
  )
}
