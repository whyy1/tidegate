import { Eye, EyeOff, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import type { KeyValue } from '../lib/types'

interface Props {
  items: KeyValue[]
  onChange: (next: KeyValue[]) => void
  keyPlaceholder?: string
  valuePlaceholder?: string
  allowDisable?: boolean
  allowSecret?: boolean
  allowDescription?: boolean
  compact?: boolean
}

export function KeyValueEditor({
  items,
  onChange,
  keyPlaceholder = '参数名',
  valuePlaceholder = '值（支持 {{变量}}）',
  allowDisable = true,
  allowSecret = true,
  allowDescription = true,
  compact = false,
}: Props) {
  const [revealed, setRevealed] = useState<Record<number, boolean>>({})

  const updateRow = (idx: number, patch: Partial<KeyValue>) => {
    const next = items.map((row, i) => (i === idx ? { ...row, ...patch } : row))
    onChange(next)
  }

  const removeRow = (idx: number) => {
    onChange(items.filter((_, i) => i !== idx))
  }

  const addRow = () => {
    onChange([...items, { name: '', value: '', description: '', disabled: false, secret: false }])
  }

  return (
    <div className="space-y-1.5">
      {items.length > 0 && (
        <div className="border border-slate-200 rounded-md overflow-hidden bg-white">
          <table className="w-full text-xs border-collapse">
            <thead>
              <tr className="bg-slate-50 text-slate-500 border-b border-slate-200 text-left">
                {allowDisable && <th className="w-8 px-2 py-1.5 text-center">启用</th>}
                <th className="px-2 py-1.5 w-[28%]">名称</th>
                <th className="px-2 py-1.5">值</th>
                {allowDescription && !compact && <th className="px-2 py-1.5 w-[24%]">说明</th>}
                <th className="w-16 px-2 py-1.5 text-right">操作</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {items.map((row, idx) => {
                const isSecret = Boolean(row.secret)
                const isShown = Boolean(revealed[idx])
                return (
                  <tr key={idx} className={row.disabled ? 'bg-slate-50/70 opacity-65' : ''}>
                    {allowDisable && (
                      <td className="px-2 py-1 text-center">
                        <input
                          type="checkbox"
                          checked={!row.disabled}
                          onChange={(e) => updateRow(idx, { disabled: !e.target.checked })}
                          className="rounded border-slate-300 text-sky-600 focus:ring-sky-500"
                        />
                      </td>
                    )}
                    <td className="px-1.5 py-1">
                      <input
                        type="text"
                        value={row.name}
                        placeholder={keyPlaceholder}
                        onChange={(e) => updateRow(idx, { name: e.target.value })}
                        className="w-full px-2 py-1 text-xs font-mono border border-transparent hover:border-slate-200 focus:border-sky-400 focus:outline-none rounded bg-transparent"
                      />
                    </td>
                    <td className="px-1.5 py-1">
                      <div className="flex items-center gap-1">
                        <input
                          type={isSecret && !isShown ? 'password' : 'text'}
                          value={row.value}
                          placeholder={valuePlaceholder}
                          onChange={(e) => updateRow(idx, { value: e.target.value })}
                          className="w-full px-2 py-1 text-xs font-mono border border-transparent hover:border-slate-200 focus:border-sky-400 focus:outline-none rounded bg-transparent"
                        />
                        {isSecret && (
                          <button
                            type="button"
                            title={isShown ? '隐藏值' : '显示值'}
                            onClick={() => setRevealed((r) => ({ ...r, [idx]: !r[idx] }))}
                            className="p-1 text-slate-400 hover:text-slate-600"
                          >
                            {isShown ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                          </button>
                        )}
                      </div>
                    </td>
                    {allowDescription && !compact && (
                      <td className="px-1.5 py-1">
                        <input
                          type="text"
                          value={row.description || ''}
                          placeholder="说明（可选）"
                          onChange={(e) => updateRow(idx, { description: e.target.value })}
                          className="w-full px-2 py-1 text-xs border border-transparent hover:border-slate-200 focus:border-sky-400 focus:outline-none rounded bg-transparent"
                        />
                      </td>
                    )}
                    <td className="px-1.5 py-1 text-right">
                      <div className="inline-flex items-center gap-1">
                        {allowSecret && (
                          <button
                            type="button"
                            title={isSecret ? '取消敏感标记' : '标记为敏感值（导出时自动清空）'}
                            onClick={() => updateRow(idx, { secret: !isSecret })}
                            className={`px-1.5 py-0.5 text-[10px] rounded border ${
                              isSecret
                                ? 'bg-amber-50 text-amber-700 border-amber-300'
                                : 'bg-white text-slate-400 border-slate-200 hover:text-slate-600'
                            }`}
                          >
                            密
                          </button>
                        )}
                        <button
                          type="button"
                          title="删除"
                          onClick={() => removeRow(idx)}
                          className="p-1 text-slate-400 hover:text-rose-600 rounded"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      <button
        type="button"
        onClick={addRow}
        className="inline-flex items-center gap-1 text-xs text-sky-600 hover:text-sky-700 font-medium px-1.5 py-1 rounded hover:bg-sky-50"
      >
        <Plus className="w-3.5 h-3.5" /> 添加行
      </button>
    </div>
  )
}
