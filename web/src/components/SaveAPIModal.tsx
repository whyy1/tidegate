import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Save, X } from 'lucide-react'
import type { TreeNode } from '../lib/types'
import { useNativeDialog } from '../lib/useNativeDialog'

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

const fieldCls =
  'w-full px-3 py-1.5 border border-line-strong rounded-md bg-surface text-fg focus:border-sky-500 focus:outline-none'

export function SaveAPIModal({ open, initialName, initialFolderId, treeNodes, onClose, onConfirm }: Props) {
  const ref = useNativeDialog(open, onClose)
  const nameRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState(initialName)
  const [folderId, setFolderId] = useState(initialFolderId)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (open) {
      setName(initialName)
      setFolderId(initialFolderId)
    }
  }, [open, initialName, initialFolderId])

  // showModal() runs after React's autoFocus; re-focus the name field once the dialog is open.
  useEffect(() => {
    if (open) nameRef.current?.focus()
  }, [open])

  const folders = open ? collectFolderOptions(treeNodes) : []

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
    <dialog
      ref={ref}
      {...({ closedby: 'closerequest' } as Record<string, string>)}
      aria-labelledby="tg-save-api-title"
      className="tg-dialog w-[min(28rem,calc(100vw-2rem))] max-h-[86dvh]"
    >
      {open && (
        <form onSubmit={submit} className="flex flex-col min-h-0 flex-1 text-xs">
          <header className="tg-dialog-header">
            <h2 id="tg-save-api-title" className="text-sm font-semibold text-fg">
              保存接口
            </h2>
            <button
              type="button"
              onClick={onClose}
              aria-label="关闭保存接口"
              className="p-1 text-fg-subtle hover:text-fg rounded-lg hover:bg-surface-hover"
            >
              <X className="w-4 h-4" />
            </button>
          </header>

          <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 space-y-3">
            <div>
              <label htmlFor="tg-save-api-name" className="block font-medium text-fg-muted mb-1">
                接口名称
              </label>
              <input
                ref={nameRef}
                id="tg-save-api-name"
                name="api-name"
                type="text"
                required
                autoFocus
                autoComplete="off"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例如 获取订单详情"
                className={fieldCls}
              />
            </div>
            <div>
              <label htmlFor="tg-save-api-folder" className="block font-medium text-fg-muted mb-1">
                存放目录
              </label>
              <select
                id="tg-save-api-folder"
                name="api-folder"
                value={folderId}
                onChange={(e) => setFolderId(e.target.value)}
                className={`tg-select ${fieldCls}`}
              >
                <option value="">根目录</option>
                {folders.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.label}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <footer className="tg-dialog-footer">
            <button
              type="button"
              onClick={onClose}
              className="px-3 py-1.5 rounded-md border border-line-strong bg-surface text-fg hover:bg-surface-hover"
            >
              取消
            </button>
            <button
              type="submit"
              disabled={busy}
              className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
            >
              <Save className="w-3.5 h-3.5" /> 保存
            </button>
          </footer>
        </form>
      )}
    </dialog>
  )
}
