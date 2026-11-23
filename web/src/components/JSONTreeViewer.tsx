import { Check, ChevronDown, ChevronRight, ChevronsDownUp, ChevronsUpDown, Filter, Search, X } from 'lucide-react'
import { Fragment, useEffect, useMemo, useState, type ReactNode } from 'react'

interface Props {
  rawText: string
  onPickExtractJSONPath: (expr: string, suggestedName: string) => void
  onPickAssertJSONPath: (expr: string, sampleValue: string) => void
}

function formatScalar(val: unknown): string {
  if (val === null) return 'null'
  if (typeof val === 'string') return `"${val}"`
  return String(val)
}

function buildChildPath(parentPath: string, key: string | number): string {
  if (typeof key === 'number') return `${parentPath}[${key}]`
  if (/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) return `${parentPath}.${key}`
  return `${parentPath}['${key}']`
}

function childEntries(value: unknown): Array<readonly [string, unknown, string | number]> {
  if (Array.isArray(value)) return value.map((v, idx) => [String(idx), v, idx] as const)
  return Object.entries(value as Record<string, unknown>).map(([k, v]) => [k, v, k] as const)
}

const isContainer = (v: unknown) => v !== null && typeof v === 'object'

interface Analysis {
  containers: string[]
  defaultOpen: string[]
  matches: Set<string>
  matchAncestors: Set<string>
}

/** Walk the JSON once: collect container paths, default-open set, and search hits (with ancestors to auto-expand). */
function analyse(root: unknown, query: string): Analysis {
  const q = query.trim().toLowerCase()
  const byPath = q.startsWith('$')
  const containers: string[] = []
  const defaultOpen: string[] = []
  const matches = new Set<string>()
  const matchAncestors = new Set<string>()

  const walk = (value: unknown, path: string, label: string | undefined, depth: number, ancestors: string[]) => {
    let hit = false
    if (q) {
      if (byPath) hit = path.toLowerCase().includes(q)
      else {
        hit = (label !== undefined && label.toLowerCase().includes(q)) ||
          (!isContainer(value) && formatScalar(value).toLowerCase().includes(q))
      }
    }
    if (hit) {
      matches.add(path)
      for (const a of ancestors) matchAncestors.add(a)
    }
    if (!isContainer(value)) return
    containers.push(path)
    if (depth < 3) defaultOpen.push(path)
    const nextAnc = [...ancestors, path]
    for (const [k, child, rawKey] of childEntries(value)) {
      walk(child, buildChildPath(path, rawKey), Array.isArray(value) ? undefined : k, depth + 1, nextAnc)
    }
  }
  walk(root, '$', undefined, 0, [])
  return { containers, defaultOpen, matches, matchAncestors }
}

function Highlight({ text, query }: { text: string; query: string }) {
  const q = query.trim()
  if (!q || q.startsWith('$')) return <>{text}</>
  const lower = text.toLowerCase()
  const ql = q.toLowerCase()
  const parts: ReactNode[] = []
  let i = 0
  let k = 0
  while (i < text.length) {
    const at = lower.indexOf(ql, i)
    if (at < 0) {
      parts.push(text.slice(i))
      break
    }
    if (at > i) parts.push(text.slice(i, at))
    parts.push(
      <mark key={k++} className="bg-amber-200 text-amber-950 rounded-sm px-px">
        {text.slice(at, at + q.length)}
      </mark>
    )
    i = at + q.length
  }
  return (
    <>
      {parts.map((p, idx) => (
        <Fragment key={idx}>{p}</Fragment>
      ))}
    </>
  )
}

interface NodeCtx {
  open: Set<string>
  toggle: (path: string) => void
  query: string
  matches: Set<string>
  onPickExtract: (expr: string, name: string) => void
  onPickAssert: (expr: string, val: string) => void
}

function JSONNode({ label, value, path, ctx }: { label?: string; value: unknown; path: string; ctx: NodeCtx }) {
  const hit = ctx.matches.has(path)
  const pathHit = hit && ctx.query.trim().startsWith('$')
  const labelEl =
    label !== undefined ? (
      <span className="text-sky-800 font-medium">
        "<Highlight text={label} query={ctx.query} />":
      </span>
    ) : null

  if (!isContainer(value)) {
    const valStr = value === null ? 'null' : String(value)
    const leafName = label && /^[A-Za-z_][A-Za-z0-9_-]*$/.test(label) ? label : 'extracted_val'
    return (
      <div
        className={`group flex items-center gap-1.5 py-0.5 rounded px-1 hover:bg-sky-50 focus-within:bg-sky-50 ${
          pathHit ? 'ring-1 ring-amber-400/70' : ''
        }`}
      >
        {labelEl}
        <span
          className={
            typeof value === 'string'
              ? 'text-emerald-700 break-all'
              : typeof value === 'number'
                ? 'text-amber-700'
                : typeof value === 'boolean'
                  ? 'text-purple-700'
                  : 'text-fg-subtle'
          }
        >
          <Highlight text={formatScalar(value)} query={ctx.query} />
        </span>
        <span className="opacity-0 group-hover:opacity-100 focus-within:opacity-100 inline-flex items-center gap-1 ml-2 shrink-0 transition-opacity">
          <button
            type="button"
            title={`提取 ${path}`}
            onClick={() => ctx.onPickExtract(path, leafName)}
            className="px-1.5 text-[10px] rounded bg-amber-100 text-amber-800 hover:bg-amber-200 inline-flex items-center gap-0.5"
          >
            <Filter className="w-2.5 h-2.5" /> 提取
          </button>
          <button
            type="button"
            title={`断言 ${path} == ${valStr}`}
            onClick={() => ctx.onPickAssert(path, valStr)}
            className="px-1.5 text-[10px] rounded bg-emerald-100 text-emerald-800 hover:bg-emerald-200 inline-flex items-center gap-0.5"
          >
            <Check className="w-2.5 h-2.5" /> 断言
          </button>
        </span>
      </div>
    )
  }

  const isArr = Array.isArray(value)
  const entries = childEntries(value)
  const expanded = ctx.open.has(path)
  return (
    <div className="py-0.5">
      <button
        type="button"
        aria-expanded={expanded}
        title={path}
        onClick={() => ctx.toggle(path)}
        className={`text-left hover:bg-surface-hover rounded px-1 inline-flex items-center gap-1 ${
          pathHit ? 'ring-1 ring-amber-400/70' : ''
        }`}
      >
        {expanded ? (
          <ChevronDown className="w-3 h-3 text-fg-subtle shrink-0" />
        ) : (
          <ChevronRight className="w-3 h-3 text-fg-subtle shrink-0" />
        )}
        {labelEl}
        <span className="text-fg-subtle tg-tabular">{isArr ? `[${entries.length}]` : `{${entries.length}}`}</span>
      </button>
      {expanded && (
        <div className="pl-4 border-l border-line ml-2">
          {entries.map(([k, child, rawKey]) => (
            <JSONNode
              key={k}
              label={isArr ? undefined : k}
              value={child}
              path={buildChildPath(path, rawKey)}
              ctx={ctx}
            />
          ))}
        </div>
      )}
    </div>
  )
}

export function JSONTreeViewer({ rawText, onPickExtractJSONPath, onPickAssertJSONPath }: Props) {
  const parsed = useMemo<{ ok: boolean; value?: unknown }>(() => {
    const t = rawText.trim()
    if (!t.startsWith('{') && !t.startsWith('[')) return { ok: false }
    try {
      return { ok: true, value: JSON.parse(t) }
    } catch {
      return { ok: false }
    }
  }, [rawText])

  const [query, setQuery] = useState('')
  const base = useMemo(() => (parsed.ok ? analyse(parsed.value, '') : null), [parsed])
  const found = useMemo(
    () => (parsed.ok && query.trim() ? analyse(parsed.value, query) : null),
    [parsed, query]
  )
  const [open, setOpen] = useState<Set<string>>(() => new Set(base?.defaultOpen || []))

  useEffect(() => {
    setOpen(new Set(base?.defaultOpen || []))
  }, [base])

  useEffect(() => {
    if (!found || found.matchAncestors.size === 0) return
    setOpen((prev) => {
      const next = new Set(prev)
      found.matchAncestors.forEach((p) => next.add(p))
      return next
    })
  }, [found])

  if (!parsed.ok) {
    return (
      <pre className="p-3 rounded-lg border tg-code-surface tg-scroller-dark text-xs font-mono overflow-auto max-h-[60dvh] whitespace-pre-wrap break-all">
        {rawText || '(空响应体)'}
      </pre>
    )
  }

  const ctx: NodeCtx = {
    open,
    toggle: (p) =>
      setOpen((prev) => {
        const next = new Set(prev)
        if (next.has(p)) next.delete(p)
        else next.add(p)
        return next
      }),
    query,
    matches: found?.matches || new Set(),
    onPickExtract: onPickExtractJSONPath,
    onPickAssert: onPickAssertJSONPath,
  }
  const hitCount = found?.matches.size ?? 0

  return (
    <div className="rounded-lg border border-line bg-surface font-mono text-xs flex flex-col max-h-[min(64dvh,640px)]">
      <div className="flex items-center gap-1.5 px-2 py-1.5 border-b border-line bg-surface-subtle rounded-t-lg shrink-0 font-sans">
        <search className="relative flex-1 min-w-0">
          <Search className="w-3.5 h-3.5 text-fg-subtle absolute left-2 top-1/2 -translate-y-1/2 pointer-events-none" />
          <input
            type="search"
            name="json-search"
            aria-label="搜索字段、值或 JSONPath"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Escape' && query) {
                e.preventDefault()
                e.stopPropagation()
                setQuery('')
              }
            }}
            placeholder="字段 / 值 / $.data.id"
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="none"
            spellCheck={false}
            className="w-full pl-7 pr-7 py-1 rounded border border-line bg-surface text-fg focus:border-sky-500 focus:outline-none [&::-webkit-search-cancel-button]:hidden"
          />
          {query && (
            <button
              type="button"
              aria-label="清除搜索"
              onClick={() => setQuery('')}
              className="absolute right-1.5 top-1/2 -translate-y-1/2 p-0.5 text-fg-subtle hover:text-fg rounded"
            >
              <X className="w-3 h-3" />
            </button>
          )}
        </search>
        {query.trim() && (
          <span
            className={`tg-tabular shrink-0 px-1.5 py-0.5 rounded-full text-[11px] ${
              hitCount > 0 ? 'bg-amber-100 text-amber-800' : 'bg-slate-100 text-fg-subtle'
            }`}
          >
            {hitCount} 处
          </span>
        )}
        <button
          type="button"
          title="全部展开"
          aria-label="全部展开"
          onClick={() => setOpen(new Set(base?.containers || []))}
          className="p-1 rounded text-fg-muted hover:text-fg hover:bg-surface-hover shrink-0"
        >
          <ChevronsUpDown className="w-3.5 h-3.5" />
        </button>
        <button
          type="button"
          title="全部折叠"
          aria-label="全部折叠"
          onClick={() => setOpen(new Set())}
          className="p-1 rounded text-fg-muted hover:text-fg hover:bg-surface-hover shrink-0"
        >
          <ChevronsDownUp className="w-3.5 h-3.5" />
        </button>
      </div>
      <div className="flex-1 min-h-0 overflow-auto tg-scroller p-2">
        <JSONNode value={parsed.value} path="$" ctx={ctx} />
      </div>
    </div>
  )
}
