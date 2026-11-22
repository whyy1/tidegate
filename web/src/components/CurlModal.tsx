import { useEffect, useState } from 'react'
import { Check, Copy, Terminal, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { API } from '../lib/types'

interface Props {
  open: boolean
  projectId: string
  apiDraft: Partial<API> | null
  environmentId: string
  tempVars: Record<string, string>
  onClose: () => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

export function CurlModal({ open, projectId, apiDraft, environmentId, tempVars, onClose, onToast }: Props) {
  const [mode, setMode] = useState<'expanded' | 'template'>('expanded')
  const [shell, setShell] = useState<'bash' | 'powershell'>('bash')
  const [redact, setRedact] = useState(true)
  const [curlText, setCurlText] = useState('')
  const [loading, setLoading] = useState(false)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    if (!open || !apiDraft) return
    let cancelled = false
    setLoading(true)
    setCopied(false)
    api
      .apiToCurl(projectId, {
        api_id: apiDraft.id,
        api: apiDraft,
        environment_id: environmentId,
        variables: tempVars,
        mode,
        shell,
        redact_secrets: mode === 'expanded' ? redact : false,
      })
      .then((res) => {
        if (!cancelled) setCurlText(res.curl)
      })
      .catch((err) => {
        if (!cancelled) {
          setCurlText('')
          onToast('error', err instanceof APIError ? err.message : '生成 cURL 失败')
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, projectId, apiDraft, environmentId, tempVars, mode, shell, redact])

  if (!open || !apiDraft) return null

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(curlText)
      setCopied(true)
      onToast('success', '已复制 cURL 命令到剪贴板')
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      onToast('error', '无法自动写入剪贴板，请手动全选复制')
    }
  }

  return (
    <div className="fixed inset-0 z-40 bg-slate-900/40 flex items-center justify-center p-4">
      <div className="w-full max-w-2xl bg-white rounded-xl border border-slate-200 shadow-xl overflow-hidden">
        <div className="px-5 py-3.5 border-b border-slate-200 flex items-center justify-between bg-slate-50">
          <div className="flex items-center gap-2 text-sm font-semibold text-slate-900">
            <Terminal className="w-4 h-4 text-sky-600" />
            <span>导出 cURL 命令</span>
          </div>
          <button type="button" onClick={onClose} className="p-1 text-slate-400 hover:text-slate-600 rounded">
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="p-5 space-y-3 text-xs">
          <div className="flex flex-wrap items-center gap-3">
            <div className="inline-flex rounded border border-slate-200 p-0.5 bg-slate-50">
              <button
                type="button"
                onClick={() => setMode('expanded')}
                className={`px-2.5 py-1 rounded font-medium ${
                  mode === 'expanded' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
                }`}
              >
                展开环境变量与目录继承
              </button>
              <button
                type="button"
                onClick={() => setMode('template')}
                className={`px-2.5 py-1 rounded font-medium ${
                  mode === 'template' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
                }`}
              >
                保留 {'{{变量}}'} 模板
              </button>
            </div>

            <div className="inline-flex rounded border border-slate-200 p-0.5 bg-slate-50">
              <button
                type="button"
                onClick={() => setShell('bash')}
                className={`px-2.5 py-1 rounded font-medium ${
                  shell === 'bash' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
                }`}
              >
                Bash
              </button>
              <button
                type="button"
                onClick={() => setShell('powershell')}
                className={`px-2.5 py-1 rounded font-medium ${
                  shell === 'powershell' ? 'bg-white shadow-xs text-sky-700' : 'text-slate-600'
                }`}
              >
                PowerShell
              </button>
            </div>

            {mode === 'expanded' && (
              <label className="inline-flex items-center gap-1.5 text-slate-700 cursor-pointer">
                <input
                  type="checkbox"
                  checked={redact}
                  onChange={(e) => setRedact(e.target.checked)}
                  className="rounded border-slate-300 text-sky-600"
                />
                <span>敏感头与密钥值脱敏（推荐分享时开启）</span>
              </label>
            )}
          </div>

          <pre className="p-3.5 rounded-lg bg-slate-900 text-sky-100 font-mono text-xs overflow-auto max-h-72 whitespace-pre-wrap break-all">
            {loading ? '正在生成...' : curlText || '(无内容)'}
          </pre>

          <div className="flex justify-end gap-2">
            <button
              type="button"
              onClick={onClose}
              className="px-3 py-1.5 rounded-md border border-slate-300 bg-white text-slate-700 hover:bg-slate-50"
            >
              关闭
            </button>
            <button
              type="button"
              disabled={!curlText || loading}
              onClick={() => void handleCopy()}
              className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-sky-600 hover:bg-sky-700 text-white font-medium disabled:opacity-50"
            >
              {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
              {copied ? '已复制' : '复制命令'}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
