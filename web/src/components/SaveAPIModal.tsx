import { useEffect, useState, type FormEvent } from 'react'
import { Save, X } from 'lucide-react'
import type { TreeNode } from '../lib/types'

interface Props {
  open: boolean
  initialName: string
  initialFolderId: string
  treeNodes: TreeNode[]
  onClose: () => void
  onConfirm: (name: string, folderId: string) => Promise<void>
}

function collectFolderOptions(nodes: TreeNode[], prefix = ''): Array<{ id: string; label: string }> {
  const out: Array<{ id: string; label: string }> = []
  for (const n of nodes) {
    if (n.type === 'folder') {
      const label = prefix ? `${prefix} / ${n.name}` : n.name
      out.push({ id: n.id, label })
      if (n.children) out.push(...collectFolderOptions(n.children, label))
    }
  }
  return out
}

export function SaveAPIModal({ open, initialName, initialFolderId, treeNodes, onClose, onConfirm }: Props) {
  const [name, setName] = useState(initialName)
  const [folderId, setFolderId] = useState(initialFolderId)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (open) {
      setName(initialName)
      setFolderId(initialFolderId)
    }
  }, [open, initialName, initialFolderId])

  if (!open) return null

  const folders = collectFolderOptions(treeNodes)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    try {
      await onConfirm(name.trim(), folderId)
      onClose()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 z-40 bg-slate-900/40 flex items-center justify-center p-4">
      <div className="w-full max-w-md bg-white rounded-xl border border-slate-200 shadow-xl overflow-hidden">
        <div className="px-5 py-3.5 border-b border-slate-200 flex items-center justify-between bg-slate-50">
          <h2 className="text-sm font-semibold text-slate-900">保存接口到项目目录</h2>
          <button type="button" onClick={onClose} className="p-1 text-slate-400 hover:text-slate-600 rounded">
            <X className="w-4 h-4" />
          </button>
        </div>
        <form onSubmit={submit} className="p-5 space-y-3 text-xs">
          <div>
            <label className="block font-medium text-slate-700 mb-1">接口名称</label>
            <input
              type="text"
              required
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如 获取订单详情"
              className="w-full px-3 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
            />
          </div>
          <div>
            <label className="block font-medium text-slate-700 mb-1">存放目录</label>
            <select
              value={folderId}
              onChange={(e) => setFolderId(e.target.value)}
              className="w-full px-3 py-1.5 border border-slate-300 rounded-md bg-white focus:border-sky-500 focus:outline-none"
            >
              <option value="">根目录</option>
              {folders.map((f) => (
                <option key={f.id} value={f.id}>
                  {f.label}
                </option>
              ))}
            </select>
          </div>
          <div className="flex justify-end gap-2 pt-2">
            <button
              type="button"
              onClick={onClose}
              className="px-3 py-1.5 rounded-md border border-slate-300 bg-white text-slate-700 hover:bg-slate-50"
            >
              取消
            </button>
            <button
              type="submit"
              disabled={busy}
              className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
            >
              <Save className="w-3.5 h-3.5" /> 确认保存
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
