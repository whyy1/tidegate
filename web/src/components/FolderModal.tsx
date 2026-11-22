import { useEffect, useState, type FormEvent } from 'react'
import { Save, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { AuthConfig, Folder, KeyValue } from '../lib/types'
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

  if (!open) return null

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
    <div className="fixed inset-0 z-40 bg-slate-900/40 flex items-center justify-center p-4">
      <div className="w-full max-w-2xl bg-white rounded-xl border border-slate-200 shadow-xl overflow-hidden flex flex-col max-h-[84vh]">
        <div className="px-5 py-3.5 border-b border-slate-200 flex items-center justify-between bg-slate-50">
          <h2 className="text-sm font-semibold text-slate-900">
            {folder ? `编辑目录「${folder.name}」` : '新建接口目录'}
          </h2>
          <button type="button" onClick={onClose} className="p-1 text-slate-400 hover:text-slate-600 rounded">
            <X className="w-4 h-4" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="p-5 overflow-y-auto space-y-4 text-xs">
          <div>
            <label className="block font-medium text-slate-700 mb-1">目录名称</label>
            <input
              type="text"
              required
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如 订单服务 / 用户认证"
              className="w-full px-3 py-1.5 border border-slate-300 rounded-md focus:border-sky-500 focus:outline-none"
            />
          </div>

          <div>
            <label className="block font-medium text-slate-700 mb-1.5">
              目录公共请求头（子目录及接口自动继承）
            </label>
            <KeyValueEditor
              items={headers}
              onChange={setHeaders}
              keyPlaceholder="Header 名称"
              valuePlaceholder="Header 值"
            />
          </div>

          <div className="rounded-lg border border-slate-200 p-3 bg-slate-50/50">
            <div className="font-medium text-slate-700 mb-2">目录默认鉴权</div>
            <AuthEditor value={auth} onChange={setAuth} allowInherit />
          </div>

          <div className="flex justify-end gap-2 pt-2 border-t border-slate-200">
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
              <Save className="w-3.5 h-3.5" /> 保存目录
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
