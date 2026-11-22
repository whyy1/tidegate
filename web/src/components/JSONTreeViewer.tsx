import { Check, Filter } from 'lucide-react'
import { useState } from 'react'

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

function JSONNode({
  label,
  value,
  path,
  depth,
  onPickExtract,
  onPickAssert,
}: {
  label?: string
  value: unknown
  path: string
  depth: number
  onPickExtract: (expr: string, name: string) => void
  onPickAssert: (expr: string, val: string) => void
}) {
  const isObj = value !== null && typeof value === 'object'
  const isArr = Array.isArray(value)
  const [expanded, setExpanded] = useState(depth < 3)

  const leafName =
    label && /^[A-Za-z_][A-Za-z0-9_\-]*$/.test(label) ? label : 'extracted_val'

  if (!isObj) {
    const valStr = value === null ? 'null' : String(value)
    return (
      <div className="group flex items-center gap-1.5 py-0.5 hover:bg-sky-50/70 rounded px-1">
        {label !== undefined && (
          <span className="text-sky-800 font-medium">{JSON.stringify(label)}:</span>
        )}
        <span
          className={
            typeof value === 'string'
              ? 'text-emerald-700 break-all'
              : typeof value === 'number'
                ? 'text-amber-700'
                : typeof value === 'boolean'
                  ? 'text-purple-700'
                  : 'text-slate-500'
          }
        >
          {formatScalar(value)}
        </span>
        <span className="opacity-0 group-hover:opacity-100 inline-flex items-center gap-1 ml-2 shrink-0 transition-opacity">
          <button
            type="button"
            title={`提取 ${path}`}
            onClick={() => onPickExtract(path, leafName)}
            className="px-1.5 py-0.2 text-[10px] rounded bg-amber-100 text-amber-800 hover:bg-amber-200 inline-flex items-center gap-0.5"
          >
            <Filter className="w-2.5 h-2.5" /> 提取
          </button>
          <button
            type="button"
            title={`断言 ${path} == ${valStr}`}
            onClick={() => onPickAssert(path, valStr)}
            className="px-1.5 py-0.2 text-[10px] rounded bg-emerald-100 text-emerald-800 hover:bg-emerald-200 inline-flex items-center gap-0.5"
          >
            <Check className="w-2.5 h-2.5" /> 断言
          </button>
        </span>
      </div>
    )
  }

  const entries = isArr
    ? (value as unknown[]).map((v, idx) => [String(idx), v, idx] as const)
    : Object.entries(value as Record<string, unknown>).map(([k, v]) => [k, v, k] as const)

  return (
    <div className="py-0.5">
      <button
        type="button"
        onClick={() => setExpanded((e) => !e)}
        className="text-left hover:bg-slate-100 rounded px-1 inline-flex items-center gap-1"
      >
        <span className="text-slate-400 text-[10px]">{expanded ? '▼' : '▶'}</span>
        {label !== undefined && <span className="text-sky-800 font-medium">{JSON.stringify(label)}:</span>}
        <span className="text-slate-500">
          {isArr ? `[${entries.length}]` : `{${entries.length}}`}
        </span>
      </button>
      {expanded && (
        <div className="pl-4 border-l border-slate-200 ml-2">
          {entries.map(([k, child, rawKey]) => (
            <JSONNode
              key={k}
              label={isArr ? undefined : k}
              value={child}
              path={buildChildPath(path, rawKey)}
              depth={depth + 1}
              onPickExtract={onPickExtract}
              onPickAssert={onPickAssert}
            />
          ))}
        </div>
      )}
    </div>
  )
}

export function JSONTreeViewer({ rawText, onPickExtractJSONPath, onPickAssertJSONPath }: Props) {
  let parsed: unknown
  let isJSON = false
  try {
    if (rawText.trim().startsWith('{') || rawText.trim().startsWith('[')) {
      parsed = JSON.parse(rawText)
      isJSON = true
    }
  } catch {
    isJSON = false
  }

  if (!isJSON) {
    return (
      <pre className="p-3 rounded-lg bg-slate-900 text-slate-100 text-xs font-mono overflow-auto max-h-[360px] whitespace-pre-wrap break-all">
        {rawText || '(空响应体)'}
      </pre>
    )
  }

  return (
    <div className="p-3 rounded-lg border border-slate-200 bg-white font-mono text-xs overflow-auto max-h-[360px]">
      <div className="text-[11px] text-slate-400 mb-2">
        悬停任意字段可一键生成 JSONPath 提取器或断言规则
      </div>
      <JSONNode
        value={parsed}
        path="$"
        depth={0}
        onPickExtract={onPickExtractJSONPath}
        onPickAssert={onPickAssertJSONPath}
      />
    </div>
  )
}
