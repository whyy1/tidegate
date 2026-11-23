import { useEffect, useRef, useState, type FormEvent } from 'react'
import { Save, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { AuthConfig, Folder, KeyValue } from '../lib/types'
import { useNativeDialog } from '../lib/useNativeDialog'
import { AuthEditor } from './AuthEditor'
import { KeyValueEditor } from './KeyValueEditor'

interface Props {
  open: boolean
  projectId: string
  folder: Folder | null
  parentId: string
  onClose: () => void
  onSaved: () => Promise<void>
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

export function FolderModal({ open, projectId, folder, parentId, onClose, onSaved, onToast }: Props) {
  const ref = useNativeDialog(open, onClose)
  const nameRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState('')
  const [headers, setHeaders] = useState<KeyValue[]>([])
  const [auth, setAuth] = useState<AuthConfig>({ type: 'inherit' })
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) return
    if (folder) {
      setName(folder.name)
      setHeaders(folder.headers || [])
      setAuth(folder.auth || { type: 'inherit' })
    } else {
      setName('')
      setHeaders([])
      setAuth({ type: 'inherit' })
    }
  }, [open, folder])

  // showModal() runs after React's autoFocus; re-focus the name field once the dialog is open.
  useEffect(() => {
    if (open) nameRef.current?.focus()
  }, [open])

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    const cleanedHeaders = headers.filter((h) => h.name.trim() !== '' || h.value !== '')
    try {
      if (folder) {
        await api.patchFolder(projectId, folder.id, {
          name: name.trim(),
          headers: cleanedHeaders,
          auth,
          version: folder.version,
        })
        onToast('success', '目录配置已更新')
      } else {
        await api.createFolder(projectId, {
          name: name.trim(),
          parent_id: parentId,
          headers: cleanedHeaders,
          auth,
        })
        onToast('success', '已创建目录')
      }
      await onSaved()
      onClose()
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '保存目录失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <dialog
      ref={ref}
      {...({ closedby: 'any' } as Record<string, string>)}
      aria-labelledby="tg-folder-title"
      className="tg-dialog w-[min(42rem,calc(100vw-2rem))] max-h-[86dvh]"
    >
      {open && (
        <form onSubmit={handleSubmit} className="flex flex-col min-h-0 flex-1 text-xs">
          <header className="px-5 py-3 border-b border-line flex items-center justify-between gap-3 bg-surface-subtle shrink-0">
            <h2 id="tg-folder-title" className="text-sm font-semibold text-fg truncate min-w-0">
              {folder ? `编辑目录「${folder.name}」` : '新建目录'}
            </h2>
            <button
              type="button"
              onClick={onClose}
              aria-label="关闭目录设置"
              className="p-1 text-fg-subtle hover:text-fg rounded hover:bg-surface-hover shrink-0"
            >
              <X className="w-4 h-4" />
            </button>
          </header>

          <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 space-y-4">
            <div>
              <label htmlFor="tg-folder-name" className="block font-medium text-fg-muted mb-1">
                目录名称
              </label>
              <input
                ref={nameRef}
                id="tg-folder-name"
                name="folder-name"
                type="text"
                required
                autoFocus
                autoComplete="off"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例如 订单服务 / 用户认证"
                className="w-full px-3 py-1.5 border border-line-strong rounded-md bg-surface text-fg focus:border-sky-500 focus:outline-none"
              />
            </div>

            <fieldset>
              <legend className="font-medium text-fg-muted mb-1.5">公共请求头</legend>
              <KeyValueEditor
                items={headers}
                onChange={setHeaders}
                keyPlaceholder="Header 名称"
                valuePlaceholder="Header 值"
              />
            </fieldset>

            <fieldset className="rounded-lg border border-line p-3 bg-surface-subtle">
              <legend className="px-1 font-medium text-fg-muted">默认鉴权</legend>
              <AuthEditor value={auth} onChange={setAuth} allowInherit />
            </fieldset>
          </div>

          <footer className="px-5 py-3 border-t border-line flex justify-end gap-2 shrink-0">
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
              <Save className="w-3.5 h-3.5" /> 保存目录
            </button>
          </footer>
        </form>
      )}
    </dialog>
  )
}
