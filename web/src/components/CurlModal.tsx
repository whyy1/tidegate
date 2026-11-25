import { useEffect, useRef, useState } from 'react'
import { Check, ClipboardPaste, Copy, FilePlus, Terminal, Wand2, X } from 'lucide-react'
import { api, APIError } from '../lib/api'
import { copyText } from '../lib/download'
import type { API } from '../lib/types'
import { useNativeDialog } from '../lib/useNativeDialog'

export type CurlModalTab = 'export' | 'import'

interface Props {
  open: boolean
  projectId: string
  apiDraft: Partial<API> | null
  environmentId: string
  tempVars: Record<string, string>
  initialTab?: CurlModalTab
  onClose: () => void
  onApplyToDraft: (parsed: API) => void
  onOpenInNewTab: (parsed: API) => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

function Segmented<T extends string>({
  value,
  options,
  onChange,
  label,
}: {
  value: T
  options: Array<{ id: T; label: string }>
  onChange: (v: T) => void
  label: string
}) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex rounded-md border border-line p-0.5 bg-surface-subtle">
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          role="radio"
          aria-checked={value === o.id}
          onClick={() => onChange(o.id)}
          className={`px-2.5 py-1 rounded font-medium transition-colors ${
            value === o.id ? 'bg-surface shadow-xs text-tidal' : 'text-fg-muted hover:text-fg'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

export function CurlModal({
  open,
  projectId,
  apiDraft,
  environmentId,
  tempVars,
  initialTab = 'export',
  onClose,
  onApplyToDraft,
  onOpenInNewTab,
  onToast,
}: Props) {
  const ref = useNativeDialog(open, onClose)
  const [tab, setTab] = useState<CurlModalTab>(initialTab)
  const [mode, setMode] = useState<'expanded' | 'template'>('expanded')
  const [shell, setShell] = useState<'bash' | 'powershell'>('bash')
  const [redact, setRedact] = useState(true)
  const [curlText, setCurlText] = useState('')
  const [loading, setLoading] = useState(false)
  const [copied, setCopied] = useState(false)
  const [pasteText, setPasteText] = useState('')
  const [parsing, setParsing] = useState<'' | 'draft' | 'tab'>('')
  const wasOpenRef = useRef(false)

  useEffect(() => {
    if (!open) {
      wasOpenRef.current = false
      return
    }
    if (!wasOpenRef.current) {
      wasOpenRef.current = true
      setPasteText('')
    }
    setTab(initialTab)
  }, [open, initialTab])

  useEffect(() => {
    if (!open || tab !== 'export' || !apiDraft) return
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
  }, [open, tab, projectId, apiDraft, environmentId, tempVars, mode, shell, redact])

  const handleCopy = async () => {
    if (await copyText(curlText)) {
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } else {
      onToast('error', '无法写入剪贴板，请手动全选复制')
    }
  }

  const handleParse = async (target: 'draft' | 'tab') => {
    const cmd = pasteText.trim()
    if (!cmd) return
    setParsing(target)
    try {
      const { api: parsed } = await api.parseCurl(cmd)
      if (target === 'draft') onApplyToDraft(parsed)
      else onOpenInNewTab(parsed)
      setPasteText('')
      onClose()
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '解析 cURL 失败')
    } finally {
      setParsing('')
    }
  }

  return (
    <dialog
      ref={ref}
      {...({ closedby: 'closerequest' } as Record<string, string>)}
      aria-labelledby="tg-curl-title"
      className="tg-dialog w-[min(44rem,calc(100vw-2rem))] max-h-[86dvh]"
    >
      {open && (
        <>
          <header className="tg-dialog-header !py-0">
            <div className="flex items-center gap-3 min-w-0">
              <h2 id="tg-curl-title" className="flex items-center gap-1.5 text-sm font-semibold text-fg py-2.5 shrink-0">
                <Terminal className="w-4 h-4 text-tidal" /> cURL
              </h2>
              <div role="tablist" aria-label="cURL 操作" className="tg-tablist gap-1 text-xs font-medium">
                {(
                  [
                    { id: 'export' as const, label: '导出为 cURL' },
                    { id: 'import' as const, label: '粘贴导入' },
                  ] as const
                ).map((t) => (
                  <button
                    key={t.id}
                    type="button"
                    role="tab"
                    aria-selected={tab === t.id}
                    onClick={() => setTab(t.id)}
                    className={`tg-tab px-2.5 py-3 transition-colors ${
                      tab === t.id ? 'text-tidal' : 'text-fg-muted hover:text-fg'
                    }`}
                  >
                    {t.label}
                  </button>
                ))}
              </div>
            </div>
            <button
              type="button"
              onClick={onClose}
              aria-label="关闭"
              className="p-1 text-fg-subtle hover:text-fg rounded shrink-0"
            >
              <X className="w-4 h-4" />
            </button>
          </header>

          {tab === 'export' ? (
            <>
              <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-4 space-y-3 text-xs">
                <div className="flex flex-wrap items-center gap-2">
                  <Segmented
                    label="Shell"
                    value={shell}
                    onChange={setShell}
                    options={[
                      { id: 'bash', label: 'Bash' },
                      { id: 'powershell', label: 'PowerShell' },
                    ]}
                  />
                  <Segmented
                    label="变量处理"
                    value={mode}
                    onChange={setMode}
                    options={[
                      { id: 'expanded', label: '展开变量' },
                      { id: 'template', label: "保留 {{变量}}" },
                    ]}
                  />
                  {mode === 'expanded' && (
                    <label className="inline-flex items-center gap-1.5 text-fg-muted cursor-pointer">
                      <input
                        type="checkbox"
                        checked={redact}
                        onChange={(e) => setRedact(e.target.checked)}
                        className="rounded border-line-strong"
                      />
                      <span>密钥脱敏</span>
                    </label>
                  )}
                </div>

                <pre
                  aria-busy={loading}
                  className="p-3.5 rounded-lg border tg-code-surface tg-scroller-dark font-mono text-xs overflow-auto max-h-[50dvh] whitespace-pre-wrap break-all"
                >
                  {loading ? '生成中…' : curlText || '—'}
                </pre>
              </div>
              <div className="tg-dialog-footer text-xs">
                <button
                  type="button"
                  disabled={!curlText || loading}
                  onClick={() => void handleCopy()}
                  className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
                >
                  {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
                  {copied ? '已复制' : '复制命令'}
                </button>
              </div>
            </>
          ) : (
            <>
              <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-4 text-xs">
                <label htmlFor="tg-curl-paste" className="sr-only">
                  cURL 命令
                </label>
                <textarea
                  id="tg-curl-paste"
                  name="curl-command"
                  rows={10}
                  autoFocus
                  value={pasteText}
                  onChange={(e) => setPasteText(e.target.value)}
                  onKeyDown={(e) => {
                    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
                      e.preventDefault()
                      void handleParse('draft')
                    }
                  }}
                  spellCheck={false}
                  autoCorrect="off"
                  autoCapitalize="none"
                  placeholder={"curl -X POST 'https://api.example.com/v1/orders' -H 'Content-Type: application/json' -d '{\"id\":1}'"}
                  className="w-full p-3 rounded-lg border tg-code-surface tg-scroller-dark font-mono text-xs leading-relaxed focus:outline-none focus:ring-2 focus:ring-sky-500/40 resize-y"
                />
              </div>
              <div className="tg-dialog-footer flex-wrap text-xs">
                <button
                  type="button"
                  disabled={!pasteText.trim() || parsing !== ''}
                  onClick={() => void handleParse('tab')}
                  className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md border border-line-strong bg-surface hover:bg-surface-hover text-fg font-medium disabled:opacity-50"
                >
                  <FilePlus className="w-3.5 h-3.5" /> {parsing === 'tab' ? '解析中…' : '新开页签'}
                </button>
                <button
                  type="button"
                  disabled={!pasteText.trim() || parsing !== ''}
                  onClick={() => void handleParse('draft')}
                  className="inline-flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-50"
                >
                  {parsing === 'draft' ? <Wand2 className="w-3.5 h-3.5 animate-pulse" /> : <ClipboardPaste className="w-3.5 h-3.5" />}
                  {parsing === 'draft' ? '解析中…' : '回填当前草稿'}
                </button>
              </div>
            </>
          )}
        </>
      )}
    </dialog>
  )
}
