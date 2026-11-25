import {
  createContext,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from 'react'
import {
  ArrowDown,
  ArrowUp,
  Boxes,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  CodeXml,
  Columns3,
  Copy,
  Database,
  Download,
  ExternalLink,
  Eye,
  FlaskConical,
  GitBranch,
  Globe,
  Plus,
  Repeat,
  Rocket,
  Save,
  Search,
  SlidersHorizontal,
  Split,
  Trash2,
  Upload,
  X,
  XCircle,
  type LucideIcon,
} from 'lucide-react'
import { CodeEditor, type CodeSnippet } from '../components/CodeEditor'
import { RollingNumber } from '../components/DropletFilter'
import { GlassSelect } from '../components/GlassSelect'
import { KeyValueEditor } from '../components/KeyValueEditor'
import { LoadConfigEditor } from '../components/LoadConfigEditor'
import { BottomModuleStrip, ModuleZoomButton } from '../components/ModuleStage'
import { api, APIError } from '../lib/api'
import { copyText } from '../lib/download'
import { flipFromRect } from '../lib/glassPhysics'
import { useNativeDialog } from '../lib/useNativeDialog'
import type {
  DataFile,
  DataSource,
  DebugRunResult,
  Environment,
  FilePreview,
  Scenario,
  ScenarioStep,
  ScenarioStepType,
  ScenarioWeightedBranch,
  TreeNode,
} from '../lib/types'

interface Props {
  projectId: string
  environments: Environment[]
  activeEnvId: string
  tempVars: Record<string, string>
  canWrite?: boolean
  isActive?: boolean
  onLaunchScenario: (scenarioId: string) => void
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
  /** Jump to the API workbench with the given API selected. The button is hidden when omitted. */
  onEditAPI?: (apiId: string) => void
}

interface FlatAPI {
  id: string
  name: string
  method: string
  url: string
  pathLabel: string
}

const UPLOAD_ACCEPT = '.csv,.json,.jsonl,.txt,.bin,.proto,.pb,.desc'
const DATA_EXT = ['.csv', '.json', '.jsonl']
const isDataFile = (name: string) => DATA_EXT.some((ext) => name.toLowerCase().endsWith(ext))

const SCRIPT_SNIPPETS: CodeSnippet[] = [
  { label: 'UUID', code: 'vars.trace_id = uuid()' },
  { label: '时间戳', code: 'vars.ts = now_ms()' },
  { label: 'HMAC 签名', code: 'vars.sign = hmac_sha256(vars.secret, vars.payload)' },
]

function flattenAPIs(nodes: TreeNode[], prefix = ''): FlatAPI[] {
  const out: FlatAPI[] = []
  for (const n of nodes) {
    if (n.type === 'folder') {
      const next = prefix ? `${prefix} / ${n.name}` : n.name
      if (n.children) out.push(...flattenAPIs(n.children, next))
    } else {
      out.push({
        id: n.id,
        name: n.name,
        method: n.method || 'GET',
        url: n.url || '',
        pathLabel: prefix ? `${prefix} / ${n.name}` : n.name,
      })
    }
  }
  return out
}

function walkSteps(steps: ScenarioStep[], visit: (s: ScenarioStep) => void) {
  for (const s of steps) {
    visit(s)
    if (s.children) walkSteps(s.children, visit)
    if (s.else) walkSteps(s.else, visit)
    if (s.branches) for (const b of s.branches) if (b.steps) walkSteps(b.steps, visit)
  }
}

function collectStepOptions(steps: ScenarioStep[]): Array<{ id: string; name?: string }> {
  const out: Array<{ id: string; name?: string }> = []
  walkSteps(steps, (s) => {
    if (s.id) out.push({ id: s.id, name: s.name })
  })
  return out
}

const CONTROL_KINDS = new Set<ScenarioStepType>(['group', 'loop', 'if', 'weighted'])
const isAPIKind = (k: ScenarioStepType) => k === 'http' || k === 'ws' || k === 'grpc' || k === 'sse'

function kindPrefix(kind: ScenarioStepType): string {
  return isAPIKind(kind) ? 'step' : kind
}

/** Prefix used when allocating a fresh id for a cloned step: the original id minus its trailing number. */
function clonePrefix(s: ScenarioStep): string {
  const base = (s.id || '').replace(/[_-]?\d+$/, '')
  return base || kindPrefix((s.type || 'http') as ScenarioStepType)
}

type IdAllocator = (prefix: string) => string

function makeStep(kind: ScenarioStepType, id: string, ref?: FlatAPI): ScenarioStep {
  switch (kind) {
    case 'group':
      return { id, type: 'group', name: '事务分组', children: [] }
    case 'loop':
      return { id, type: 'loop', name: '循环', loop_count: 3, children: [] }
    case 'if':
      return { id, type: 'if', name: '条件分支', expr: '{{status}} == "ok"', children: [], else: [] }
    case 'weighted':
      return {
        id,
        type: 'weighted',
        name: '权重分流',
        branches: [
          { name: '分支 A', weight: 70, steps: [] },
          { name: '分支 B', weight: 30, steps: [] },
        ],
      }
    case 'script':
      return { id, type: 'script', name: '脚本', script: 'vars.trace_id = uuid()\n' }
    default:
      return { id, type: 'http', name: ref?.name || '接口步骤', api_id: ref?.id }
  }
}

/** Deep copy a step and give it (and every nested step) a fresh, unique id. */
function cloneStep(src: ScenarioStep, alloc: IdAllocator): ScenarioStep {
  const copy: ScenarioStep = JSON.parse(JSON.stringify(src))
  const prefixes = new Map<ScenarioStep, string>()
  walkSteps([copy], (s) => prefixes.set(s, clonePrefix(s)))
  walkSteps([copy], (s) => {
    s.id = alloc(prefixes.get(s) || 'step')
  })
  if (copy.name) copy.name = `${copy.name} 副本`
  return copy
}

function childCount(s: ScenarioStep): number {
  return (
    (s.children?.length || 0) +
    (s.else?.length || 0) +
    (s.branches || []).reduce((sum, b) => sum + (b.steps?.length || 0), 0)
  )
}

const KIND_META: Record<string, { label: string; badge: string; accent: string }> = {
  group: { label: '分组', badge: 'bg-sky-50 border-sky-200 text-sky-700', accent: 'border-l-sky-500' },
  loop: { label: '循环', badge: 'bg-indigo-50 border-indigo-200 text-indigo-700', accent: 'border-l-indigo-500' },
  if: { label: '条件', badge: 'bg-emerald-50 border-emerald-200 text-emerald-700', accent: 'border-l-emerald-500' },
  weighted: { label: '权重', badge: 'bg-amber-50 border-amber-200 text-amber-700', accent: 'border-l-amber-500' },
  script: { label: '脚本', badge: 'bg-violet-50 border-violet-200 text-violet-700', accent: 'border-l-violet-500' },
}

const METHOD_TONE: Record<string, string> = {
  GET: 'text-emerald-700',
  POST: 'text-sky-700',
  PUT: 'text-amber-700',
  PATCH: 'text-violet-700',
  DELETE: 'text-rose-700',
}

function MethodTag({ method }: { method: string }) {
  const m = (method || 'GET').toUpperCase()
  return <span className={`font-mono font-semibold text-[10px] w-11 shrink-0 ${METHOD_TONE[m] || 'text-fg-subtle'}`}>{m}</span>
}

/**
 * Extract the top-level keys of the first JSON object in `text`. Tolerates truncated previews
 * (JSON arrays cut mid-way, JSONL) because it scans rather than parses.
 */
function extractJSONKeys(text: string): string[] {
  const start = text.indexOf('{')
  if (start < 0) return []
  const keys: string[] = []
  let depth = 0
  let i = start
  while (i < text.length) {
    const c = text[i]
    if (c === '"') {
      let j = i + 1
      let s = ''
      while (j < text.length && text[j] !== '"') {
        if (text[j] === '\\') {
          s += text[j + 1] ?? ''
          j += 2
          continue
        }
        s += text[j]
        j++
      }
      if (depth === 1) {
        let k = j + 1
        while (k < text.length && /\s/.test(text[k])) k++
        if (text[k] === ':' && !keys.includes(s)) keys.push(s)
      }
      i = j + 1
      continue
    }
    if (c === '{' || c === '[') depth++
    else if (c === '}' || c === ']') {
      depth--
      if (depth === 0) break
    }
    i++
  }
  return keys
}

function blankScenario(envId = ''): Partial<Scenario> {
  return {
    name: '新压测场景',
    description: '',
    environment_id: envId,
    variables: [],
    data: [],
    steps: [],
    load: {
      mode: 'vus',
      stages: [
        { duration: '0s', target: 10 },
        { duration: '30s', target: 10 },
      ],
    },
    http: { keep_alive: true },
  }
}

/* ------------------------------------------------------------------------------------------ */
/* Shared bits                                                                                 */

interface StepCtx {
  apis: FlatAPI[]
  apiMap: Map<string, FlatAPI>
  onEditAPI?: (apiId: string) => void
  newAllocator: () => IdAllocator
  keyOf: (s: ScenarioStep) => string
  preserveKey: (from: ScenarioStep, to: ScenarioStep) => void
}

const StepEditorContext = createContext<StepCtx | null>(null)

function useStepCtx(): StepCtx {
  const ctx = useContext(StepEditorContext)
  if (!ctx) throw new Error('StepEditorContext missing')
  return ctx
}

/** Close a floating panel on outside pointer-down, focus-out, and Escape (capture phase). */
function useDismiss(
  open: boolean,
  wrapRef: RefObject<HTMLElement | null>,
  onDismiss: (refocus: boolean) => void,
  panelRef?: RefObject<HTMLElement | null>
) {
  const cb = useRef(onDismiss)
  cb.current = onDismiss
  useEffect(() => {
    if (!open) return
    const isInside = (target: Node | null) =>
      Boolean(target && (wrapRef.current?.contains(target) || panelRef?.current?.contains(target)))
    const onDown = (e: PointerEvent) => {
      if (!isInside(e.target as Node | null)) cb.current(false)
    }
    const onFocusIn = (e: FocusEvent) => {
      if (!isInside(e.target as Node | null)) cb.current(false)
    }
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        cb.current(true)
      }
    }
    document.addEventListener('pointerdown', onDown, true)
    document.addEventListener('focusin', onFocusIn, true)
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('pointerdown', onDown, true)
      document.removeEventListener('focusin', onFocusIn, true)
      document.removeEventListener('keydown', onKey, true)
    }
  }, [open, wrapRef, panelRef])
}

/** Promote a popover="manual" panel into Top-Layer and keep it viewport-clamped near its trigger. */
function useTopLayerPopover(
  open: boolean,
  triggerRef: RefObject<HTMLElement | null>,
  panelRef: RefObject<HTMLElement | null>,
  onClose: () => void,
  align: 'left' | 'right' = 'left',
  repositionKey?: unknown
) {
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose

  useLayoutEffect(() => {
    if (!open) return
    const el = panelRef.current
    if (!el) return

    if (typeof el.showPopover === 'function') {
      try {
        if (!el.matches(':popover-open')) el.showPopover()
      } catch {
        // popover unsupported or already promoted
      }
    }

    const updatePosition = () => {
      const trigger = triggerRef.current
      const panel = panelRef.current
      if (!trigger || !panel) return
      const tr = trigger.getBoundingClientRect()
      const vw = window.innerWidth
      const vh = window.innerHeight
      if (tr.bottom < 0 || tr.top > vh || tr.right < 0 || tr.left > vw) {
        onCloseRef.current()
        return
      }
      const gap = 6
      const margin = 8
      panel.style.maxHeight = ''
      const pr = panel.getBoundingClientRect()
      const spaceBelow = vh - tr.bottom - margin
      const spaceAbove = tr.top - margin
      const flipUp = spaceBelow < pr.height + gap && spaceAbove > spaceBelow
      const avail = flipUp ? spaceAbove : spaceBelow
      const maxH = Math.max(0, Math.floor(avail - gap))
      panel.style.maxHeight = `${maxH}px`

      const updatedPr = panel.getBoundingClientRect()
      const top = flipUp
        ? Math.max(margin, tr.top - gap - updatedPr.height)
        : Math.min(tr.bottom + gap, Math.max(margin, vh - margin - updatedPr.height))
      const desiredLeft = align === 'right' ? tr.right - updatedPr.width : tr.left
      const maxLeft = Math.max(margin, vw - margin - updatedPr.width)
      const left = Math.min(Math.max(margin, desiredLeft), maxLeft)

      panel.style.top = `${Math.round(top)}px`
      panel.style.left = `${Math.round(left)}px`
      panel.style.transformOrigin = `${flipUp ? 'bottom' : 'top'} ${align === 'right' ? 'right' : 'left'}`
    }

    const onScroll = (e: Event) => {
      const target = e.target as Node | null
      if (target && panelRef.current?.contains(target)) return
      updatePosition()
    }

    updatePosition()
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', onScroll, true)
    return () => {
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', onScroll, true)
      if (typeof el.hidePopover === 'function') {
        try {
          if (el.matches(':popover-open')) el.hidePopover()
        } catch {
          // ignore
        }
      }
    }
  }, [open, align, repositionKey])
}

/** Inline two-step delete: first click arms ("确认删除？"), second click within 3s executes; blur/timeout resets. */
function ConfirmDeleteButton({
  label,
  onConfirm,
  disabled,
  idleClassName = '',
}: {
  label: string
  onConfirm: () => void | Promise<void>
  disabled?: boolean
  idleClassName?: string
}) {
  const [armed, setArmed] = useState(false)
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const disarm = () => {
    window.clearTimeout(timer.current)
    setArmed(false)
  }
  return (
    <button
      type="button"
      disabled={disabled}
      title={armed ? '再次点击确认删除' : label}
      aria-label={armed ? `确认${label}` : label}
      onClick={(e) => {
        e.stopPropagation()
        if (!armed) {
          setArmed(true)
          window.clearTimeout(timer.current)
          timer.current = window.setTimeout(() => setArmed(false), 3000)
          return
        }
        disarm()
        void onConfirm()
      }}
      onBlur={disarm}
      className={`inline-flex items-center gap-1 rounded shrink-0 disabled:opacity-45 ${
        armed
          ? 'px-1.5 py-0.5 bg-rose-600 hover:bg-rose-solid text-white text-[11px] font-medium'
          : `p-1 text-fg-subtle hover:text-rose-600 ${idleClassName}`
      }`}
    >
      <Trash2 className="w-3.5 h-3.5" />
      {armed && '确认删除？'}
    </button>
  )
}

const STEP_KINDS: Array<{ kind: ScenarioStepType; label: string; Icon: LucideIcon }> = [
  { kind: 'http', label: '接口', Icon: Globe },
  { kind: 'group', label: '分组', Icon: Boxes },
  { kind: 'loop', label: '循环', Icon: Repeat },
  { kind: 'if', label: '条件', Icon: GitBranch },
  { kind: 'weighted', label: '权重', Icon: Split },
  { kind: 'script', label: '脚本', Icon: CodeXml },
]

/** "+ 子步骤" dropdown menu: step kinds, with an inline searchable API picker for 接口. Keyboard operable. */
function AddStepMenu({
  label,
  allowControl,
  onAdd,
  variant = 'ghost',
  align = 'left',
  disabled,
}: {
  label: string
  allowControl: boolean
  onAdd: (kind: ScenarioStepType, ref?: FlatAPI) => void
  variant?: 'primary' | 'ghost'
  align?: 'left' | 'right'
  disabled?: boolean
}) {
  const { apis } = useStepCtx()
  const [open, setOpen] = useState(false)
  const [view, setView] = useState<'root' | 'api'>('root')
  const [query, setQuery] = useState('')
  const wrapRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const menuId = useId()

  const close = (refocus = false) => {
    setOpen(false)
    setView('root')
    setQuery('')
    if (refocus) triggerRef.current?.focus()
  }
  useDismiss(open, wrapRef, close, menuRef)
  useTopLayerPopover(open, triggerRef, menuRef, () => close(false), align, view)

  useEffect(() => {
    if (!open) return
    const raf = requestAnimationFrame(() => {
      const m = menuRef.current
      if (!m) return
      if (view === 'api') m.querySelector<HTMLInputElement>('input')?.focus()
      else m.querySelector<HTMLElement>('[role="menuitem"]:not([disabled])')?.focus()
    })
    return () => cancelAnimationFrame(raf)
  }, [open, view])

  const q = query.trim().toLowerCase()
  const filtered = q
    ? apis.filter((a) => `${a.method} ${a.pathLabel} ${a.url}`.toLowerCase().includes(q))
    : apis

  const pick = (kind: ScenarioStepType, ref?: FlatAPI) => {
    onAdd(kind, ref)
    close(true)
  }

  const onMenuKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Tab') {
      close()
      return
    }
    const isInput = (e.target as HTMLElement).tagName === 'INPUT'
    if (view === 'root' && e.key === 'ArrowRight' && document.activeElement?.getAttribute('data-kind') === 'http') {
      e.preventDefault()
      setView('api')
      return
    }
    if (view === 'api' && e.key === 'ArrowLeft' && !isInput) {
      e.preventDefault()
      setView('root')
      return
    }
    if (isInput && (e.key === 'Home' || e.key === 'End')) return
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) return
    const list = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([disabled])') || [])
    if (list.length === 0) return
    e.preventDefault()
    const cur = list.indexOf(document.activeElement as HTMLElement)
    let next = 0
    if (e.key === 'ArrowDown') next = cur < 0 ? 0 : (cur + 1) % list.length
    else if (e.key === 'ArrowUp') next = cur <= 0 ? list.length - 1 : cur - 1
    else if (e.key === 'End') next = list.length - 1
    list[next].focus()
  }

  const itemCls =
    'w-full flex items-center gap-2 px-2 py-1.5 rounded text-left text-fg hover:bg-surface-hover focus:bg-surface-hover focus:outline-none disabled:opacity-45 disabled:hover:bg-transparent'

  return (
    <div ref={wrapRef} className="relative inline-block">
      <button
        ref={triggerRef}
        type="button"
        disabled={disabled}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => (open ? close() : setOpen(true))}
        onKeyDown={(e) => {
          if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && !open) {
            e.preventDefault()
            setOpen(true)
          }
        }}
        className={
          variant === 'primary'
            ? 'inline-flex items-center gap-1 px-2.5 py-1.5 rounded-md bg-sky-600 hover:bg-sky-solid text-white font-medium disabled:opacity-45'
            : 'inline-flex items-center gap-1 px-2 py-1 rounded-md border border-dashed border-line-strong text-fg-muted hover:text-tidal hover:border-sky-400 hover:bg-surface-hover text-[11px] font-medium disabled:opacity-45'
        }
      >
        <Plus className="w-3.5 h-3.5" />
        {label}
        <ChevronDown className={`w-3 h-3 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && (
        <div
          ref={menuRef}
          id={menuId}
          popover="manual"
          role="menu"
          aria-label={label}
          onKeyDown={onMenuKey}
          className={`tg-glass-popover fixed inset-auto m-0 z-50 flex flex-col overflow-hidden ${
            view === 'api' ? 'w-80 max-w-[calc(100vw-1rem)]' : 'w-44'
          } p-1.5 text-xs`}
        >
          {view === 'root' ? (
            STEP_KINDS.map(({ kind, label: itemLabel, Icon }) => {
              const isControl = CONTROL_KINDS.has(kind)
              const noAPI = kind === 'http' && apis.length === 0
              const off = (isControl && !allowControl) || noAPI
              return (
                <button
                  key={kind}
                  type="button"
                  role="menuitem"
                  data-kind={kind}
                  disabled={off}
                  title={noAPI ? '暂无接口' : isControl && !allowControl ? '嵌套层级已达上限' : undefined}
                  onClick={() => (kind === 'http' ? setView('api') : pick(kind))}
                  className={itemCls}
                >
                  <Icon className="w-3.5 h-3.5 text-fg-subtle" />
                  <span className="flex-1">{itemLabel}</span>
                  {kind === 'http' && <ChevronRight className="w-3 h-3 text-fg-subtle" />}
                </button>
              )
            })
          ) : (
            <>
              <div className="flex items-center gap-1 pb-1 mb-1 border-b border-line">
                <button
                  type="button"
                  aria-label="返回"
                  onClick={() => setView('root')}
                  className="p-1 rounded text-fg-subtle hover:text-fg hover:bg-surface-hover"
                >
                  <ChevronLeft className="w-3.5 h-3.5" />
                </button>
                <search className="relative flex-1 min-w-0">
                  <Search className="w-3.5 h-3.5 absolute left-2 top-1/2 -translate-y-1/2 text-fg-subtle pointer-events-none" />
                  <input
                    type="search"
                    name="api-search"
                    aria-label="搜索接口"
                    placeholder="搜索接口"
                    value={query}
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    onChange={(e) => setQuery(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.nativeEvent.isComposing || e.keyCode === 229) return
                      if (e.key === 'Enter' && filtered[0]) {
                        e.preventDefault()
                        pick('http', filtered[0])
                      }
                    }}
                    className="w-full pl-7 pr-2 py-1 rounded border border-line-strong bg-surface text-fg focus:border-sky-500 focus:outline-none"
                  />
                </search>
              </div>
              <div className="min-h-0 flex-1 max-h-64 overflow-y-auto tg-scroller">
                {filtered.length === 0 ? (
                  <div className="px-2 py-3 text-center text-fg-subtle">无匹配接口</div>
                ) : (
                  filtered.map((a) => (
                    <button
                      key={a.id}
                      type="button"
                      role="menuitem"
                      title={a.url}
                      onClick={() => pick('http', a)}
                      className={`${itemCls} min-w-0`}
                    >
                      <MethodTag method={a.method} />
                      <span className="truncate min-w-0">{a.pathLabel}</span>
                    </button>
                  ))
                )}
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}

/** Inline "预览列名" button for a data-source row; click a field to copy its {{alias.column}} reference. */
function ColumnPeek({
  projectId,
  file,
  alias,
  onToast,
}: {
  projectId: string
  file: string
  alias: string
  onToast: Props['onToast']
}) {
  const [open, setOpen] = useState(false)
  const [state, setState] = useState<{ file: string; cols: string[]; loading: boolean; error: string }>({
    file: '',
    cols: [],
    loading: false,
    error: '',
  })
  const [copied, setCopied] = useState('')
  const wrapRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const panelId = useId()
  useDismiss(
    open,
    wrapRef,
    (refocus) => {
      setOpen(false)
      if (refocus) triggerRef.current?.focus()
    },
    panelRef
  )
  useTopLayerPopover(open, triggerRef, panelRef, () => setOpen(false), 'right', `${state.loading}:${state.cols.length}:${state.error}`)

  useEffect(() => {
    setOpen(false)
    setState({ file: '', cols: [], loading: false, error: '' })
  }, [file])

  const load = async () => {
    if (!file) return
    setState((prev) => ({ file, cols: prev.file === file ? prev.cols : [], loading: prev.file !== file || prev.cols.length === 0, error: '' }))
    try {
      const pv = await api.previewFile(projectId, file, 5)
      const cols = pv.columns?.length ? pv.columns : extractJSONKeys(pv.text || '')
      setState({ file, cols, loading: false, error: cols.length ? '' : '未识别到字段' })
    } catch (err) {
      setState({ file, cols: [], loading: false, error: err instanceof APIError ? err.message : '读取失败' })
    }
  }

  const copy = async (col: string) => {
    const ref = `{{${alias || 'ds'}.${col}}}`
    if (await copyText(ref)) {
      setCopied(col)
      window.setTimeout(() => setCopied((c) => (c === col ? '' : c)), 1500)
    } else {
      onToast('error', '复制失败')
    }
  }

  return (
    <div ref={wrapRef} className="relative shrink-0">
      <button
        ref={triggerRef}
        type="button"
        disabled={!file}
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        onClick={() => {
          if (open) {
            setOpen(false)
            return
          }
          setOpen(true)
          void load()
        }}
        className={`inline-flex items-center gap-1 px-2 py-1 rounded border text-[11px] font-medium disabled:opacity-40 ${
          open ? 'border-sky-300 bg-sky-50 text-sky-700' : 'border-line bg-surface text-fg-muted hover:text-tidal hover:bg-surface-hover'
        }`}
      >
        <Columns3 className="w-3.5 h-3.5" />
        预览列名
      </button>
      {open && (
        <div
          ref={panelRef}
          id={panelId}
          popover="manual"
          role="dialog"
          aria-label={`${file} 字段`}
          className="tg-glass-popover fixed inset-auto m-0 z-50 flex flex-col overflow-hidden w-72 max-w-[calc(100vw-1rem)] p-2.5 space-y-2"
        >
          <div className="flex items-center gap-2 min-w-0">
            <span className="font-mono text-fg truncate min-w-0 flex-1">{file}</span>
            {state.cols.length > 0 && (
              <span className="px-1.5 py-0.5 rounded-full bg-sky-50 border border-sky-200 text-sky-700 text-[10px] font-mono tg-tabular">
                {state.cols.length}
              </span>
            )}
          </div>
          {state.loading ? (
            <div className="text-fg-subtle py-2">读取中…</div>
          ) : state.error ? (
            <div className="text-rose-700 py-1">{state.error}</div>
          ) : (
            <div className="flex flex-wrap gap-1 max-h-48 overflow-y-auto tg-scroller">
              {state.cols.map((col) => (
                <button
                  key={col}
                  type="button"
                  title={`复制 {{${alias || 'ds'}.${col}}}`}
                  onClick={() => void copy(col)}
                  className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded border border-line bg-surface-subtle hover:border-sky-300 hover:bg-sky-50 font-mono text-[11px] text-fg"
                >
                  {copied === col ? (
                    <Check className="w-3 h-3 text-emerald-600" />
                  ) : (
                    <Copy className="w-3 h-3 text-fg-subtle" />
                  )}
                  {col}
                </button>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

/* ------------------------------------------------------------------------------------------ */

export function ScenariosView({
  projectId,
  environments,
  activeEnvId,
  tempVars,
  canWrite = true,
  isActive = true,
  onLaunchScenario,
  onToast,
  onEditAPI,
}: Props) {
  const [scenarios, setScenarios] = useState<Scenario[]>([])
  const [apis, setApis] = useState<FlatAPI[]>([])
  const [files, setFiles] = useState<DataFile[]>([])
  const [selectedId, setSelectedId] = useState<string>('')
  const [draft, setDraft] = useState<Partial<Scenario>>(() => blankScenario(activeEnvId))
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [dryRunning, setDryRunning] = useState(false)
  const [dryResult, setDryResult] = useState<DebugRunResult | null>(null)
  const [stopOnError, setStopOnError] = useState(false)
  const [previewModal, setPreviewModal] = useState<FilePreview | null>(null)
  const previewRef = useNativeDialog(previewModal !== null, () => setPreviewModal(null))
  const [zoomedMod, setZoomedMod] = useState<'' | 'list' | 'steps' | 'load'>('')
  const [exportMenuOpen, setExportMenuOpen] = useState(false)
  const exportWrapRef = useRef<HTMLDivElement>(null)
  const exportTriggerRef = useRef<HTMLButtonElement>(null)
  const exportMenuRef = useRef<HTMLDivElement>(null)
  const loadSeqRef = useRef(0)
  const refreshSeqRef = useRef(0)
  const listPaneRef = useRef<HTMLElement>(null)
  const stepsPaneRef = useRef<HTMLElement>(null)
  const loadPaneRef = useRef<HTMLElement>(null)
  useDismiss(
    exportMenuOpen,
    exportWrapRef,
    (refocus) => {
      setExportMenuOpen(false)
      if (refocus) exportTriggerRef.current?.focus()
    },
    exportMenuRef
  )
  useTopLayerPopover(exportMenuOpen, exportTriggerRef, exportMenuRef, () => setExportMenuOpen(false), 'right')

  const paneRefFor = (id: 'list' | 'steps' | 'load') => {
    if (id === 'list') return listPaneRef.current
    if (id === 'steps') return stepsPaneRef.current
    return loadPaneRef.current
  }

  const toggleZoomMod = (id: 'list' | 'steps' | 'load', el?: HTMLElement | null) => {
    const rect = (el ?? paneRefFor(id))?.getBoundingClientRect() ?? null
    setZoomedMod((cur) => {
      const next = cur === id ? '' : id
      requestAnimationFrame(() => flipFromRect(paneRefFor(id), rect))
      return next
    })
  }

  useEffect(() => {
    if (!isActive || !zoomedMod) return
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('dialog[open]')) setZoomedMod('')
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [isActive, zoomedMod])

  const loadAll = async (selectId?: string) => {
    const seq = ++loadSeqRef.current
    ++refreshSeqRef.current
    try {
      const [scList, tree, flList] = await Promise.all([
        api.listScenarios(projectId),
        api.getTree(projectId),
        api.listFiles(projectId),
      ])
      if (seq !== loadSeqRef.current) return
      setScenarios(scList)
      setApis(flattenAPIs(tree.children || []))
      setFiles(flList)

      const candidateId = selectId !== undefined ? selectId : selectedId
      const targetId = candidateId && scList.some((s) => s.id === candidateId) ? candidateId : scList[0]?.id || ''
      if (targetId) {
        const full = await api.getScenario(projectId, targetId)
        if (seq !== loadSeqRef.current) return
        setSelectedId(full.id)
        setDraft(full)
        setDirty(false)
      } else {
        setSelectedId('new')
        setDraft(blankScenario(activeEnvId))
        setDirty(false)
      }
    } catch (err) {
      if (seq !== loadSeqRef.current) return
      onToast('error', err instanceof APIError ? err.message : '加载场景数据失败')
    }
  }

  useEffect(() => {
    setSelectedId('')
    setDraft(blankScenario(activeEnvId))
    setDirty(false)
    setDryResult(null)
    setZoomedMod('')
    setExportMenuOpen(false)
    void loadAll('')
  }, [projectId])

  const isActiveRef = useRef(isActive)
  isActiveRef.current = isActive
  const canWriteRef = useRef(canWrite)
  canWriteRef.current = canWrite
  const dirtyRef = useRef(dirty)
  dirtyRef.current = dirty
  const wasActiveRef = useRef(isActive)
  const prevProjectIdRef = useRef(projectId)

  useEffect(() => {
    const prevActive = wasActiveRef.current
    const prevPid = prevProjectIdRef.current
    wasActiveRef.current = isActive
    prevProjectIdRef.current = projectId
    if (!isActive || !projectId || prevPid !== projectId || prevActive) return
    const seq = ++refreshSeqRef.current
    void (async () => {
      try {
        const [tree, flList, scList] = await Promise.all([
          api.getTree(projectId),
          api.listFiles(projectId),
          !dirtyRef.current ? api.listScenarios(projectId) : Promise.resolve(null),
        ])
        if (seq !== refreshSeqRef.current) return
        setApis(flattenAPIs(tree.children || []))
        setFiles(flList)
        if (scList) setScenarios(scList)
      } catch {
        // silent background refresh when returning to scenarios tab
      }
    })()
  }, [isActive, projectId])

  const selectScenario = async (id: string) => {
    setDryResult(null)
    if (id === 'new') {
      setSelectedId('new')
      setDraft(blankScenario(activeEnvId))
      setDirty(true)
      return
    }
    try {
      const full = await api.getScenario(projectId, id)
      setSelectedId(full.id)
      setDraft(full)
      setDirty(false)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '读取场景详情失败')
    }
  }

  const updateDraft = (patch: Partial<Scenario>) => {
    setDraft((prev) => ({ ...prev, ...patch }))
    setDirty(true)
  }

  const handleSave = async (): Promise<Scenario | null> => {
    if (!draft.name?.trim()) {
      onToast('error', '请填写场景名称')
      return null
    }
    if (!draft.steps || draft.steps.length === 0) {
      onToast('error', '场景至少需要包含 1 个步骤')
      return null
    }
    setSaving(true)
    try {
      if (draft.id) {
        const saved = await api.updateScenario(projectId, draft.id, draft)
        setDraft(saved)
        setSelectedId(saved.id)
        setDirty(false)
        setScenarios((prev) => prev.map((s) => (s.id === saved.id ? saved : s)))
        onToast('success', `已保存场景「${saved.name}」`)
        return saved
      } else {
        const created = await api.createScenario(projectId, draft)
        setDraft(created)
        setSelectedId(created.id)
        setDirty(false)
        await loadAll(created.id)
        onToast('success', `已创建场景「${created.name}」`)
        return created
      }
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '保存场景失败')
      return null
    } finally {
      setSaving(false)
    }
  }

  const saveRef = useRef(handleSave)
  saveRef.current = handleSave
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (!isActiveRef.current || !canWriteRef.current || document.querySelector('dialog[open]')) return
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        e.preventDefault()
        void saveRef.current()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const handleDryRun = async () => {
    if (!draft.steps || draft.steps.length === 0) {
      onToast('error', '请先添加至少 1 个步骤后再试跑')
      return
    }
    setDryRunning(true)
    setDryResult(null)
    try {
      const res = await api.debugScenario(projectId, {
        scenario: draft,
        environment_id: activeEnvId || draft.environment_id || '',
        variables: tempVars,
        skip_think: true,
        stop_on_error: stopOnError,
      })
      setDryResult(res)
      onToast(
        res.passed ? 'success' : 'error',
        res.passed ? `试跑通过 · ${res.duration_ms.toFixed(1)} ms` : '试跑存在未通过步骤'
      )
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '场景试跑失败')
    } finally {
      setDryRunning(false)
    }
  }

  const handleUploadFile = async (file: File) => {
    const existing = files.find((f) => f.name === file.name)
    try {
      await api.uploadFile(projectId, file, existing?.version || '')
      setFiles(await api.listFiles(projectId))
      onToast('success', `已上传「${file.name}」`)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '上传文件失败')
    }
  }

  const handlePreviewFile = async (name: string) => {
    try {
      const pv = await api.previewFile(projectId, name, 25)
      setPreviewModal(pv)
    } catch (err) {
      onToast('error', err instanceof APIError ? err.message : '预览文件失败')
    }
  }

  const steps = draft.steps || []
  const dataSources = draft.data || []
  const dataFiles = files.filter((f) => isDataFile(f.name))
  const totalSteps = useMemo(() => collectStepOptions(steps).length, [steps])

  // Persist pending edits before jumping to the workbench.
  const editAPIRef = useRef(onEditAPI)
  editAPIRef.current = onEditAPI
  const hasEditAPI = Boolean(onEditAPI)
  const goEditAPI = useMemo(
    () =>
      hasEditAPI
        ? async (apiId: string) => {
            if (dirtyRef.current && canWrite) {
              const saved = await saveRef.current()
              if (!saved) return
            }
            editAPIRef.current?.(apiId)
          }
        : undefined,
    [hasEditAPI, canWrite]
  )

  const stepsRef = useRef(steps)
  stepsRef.current = steps
  const stepKeyMapRef = useRef(new WeakMap<ScenarioStep, string>())
  const stepKeySeqRef = useRef(0)
  const stepCtx = useMemo<StepCtx>(() => {
    const keyOf = (s: ScenarioStep) => {
      const m = stepKeyMapRef.current
      let k = m.get(s)
      if (!k) {
        k = `sk_${++stepKeySeqRef.current}`
        m.set(s, k)
      }
      return k
    }
    const preserveKey = (from: ScenarioStep, to: ScenarioStep) => {
      stepKeyMapRef.current.set(to, keyOf(from))
    }
    return {
      apis,
      apiMap: new Map(apis.map((a) => [a.id, a])),
      onEditAPI: goEditAPI ? (id: string) => void goEditAPI(id) : undefined,
      newAllocator: () => {
        const taken = new Set(collectStepOptions(stepsRef.current).map((s) => s.id))
        return (prefix: string) => {
          let n = 1
          while (taken.has(`${prefix}${n}`)) n++
          const id = `${prefix}${n}`
          taken.add(id)
          return id
        }
      },
      keyOf,
      preserveKey,
    }
  }, [apis, goEditAPI])

  const addRootStep = (kind: ScenarioStepType, ref?: FlatAPI) => {
    const alloc = stepCtx.newAllocator()
    updateDraft({ steps: [...steps, makeStep(kind, alloc(kindPrefix(kind)), ref)] })
  }

  const stripBtn =
    'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-line-strong bg-surface hover:bg-surface-hover text-fg font-medium disabled:opacity-45'

  return (
    <StepEditorContext.Provider value={stepCtx}>
      <div className={`flex flex-1 min-h-0 gap-2.5 ${zoomedMod ? 'pb-[116px]' : 'pb-[64px]'}`}>
        {/* Left Sidebar: Scenarios + Data Files */}
        <aside
          ref={listPaneRef}
          className={`tg-glass-card ${
            zoomedMod && zoomedMod !== 'list' ? 'hidden' : 'flex flex-col shrink-0'
          } ${zoomedMod === 'list' ? 'flex-1 min-w-0' : 'w-72'}`}
        >
          <div className="p-3 border-b border-line flex items-center justify-between gap-2">
            <span className="text-xs font-semibold text-fg inline-flex items-center gap-1.5">
              场景
              <RollingNumber
                value={scenarios.length}
                className="px-1.5 py-0.5 rounded-full bg-surface-subtle border border-line text-fg-muted text-[10px] font-mono"
              />
            </span>
            <div className="flex items-center gap-1.5">
              <button
                type="button"
                disabled={!canWrite}
                onClick={() => void selectScenario('new')}
                className="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg bg-sky-600 hover:bg-sky-solid text-white text-xs font-medium disabled:opacity-45"
              >
                <Plus className="w-3.5 h-3.5" /> 新建场景
              </button>
              <ModuleZoomButton
                expanded={zoomedMod === 'list'}
                onToggle={() => toggleZoomMod('list', listPaneRef.current)}
              />
            </div>
          </div>

          <div className="flex-1 overflow-y-auto tg-scroller p-2 space-y-1">
            {scenarios.map((sc) => (
              <div
                key={sc.id}
                className={`group flex items-center justify-between gap-1 px-2 py-1.5 rounded-lg text-xs ${
                  selectedId === sc.id ? 'bg-sky-500/15 text-tidal font-semibold' : 'text-fg-muted hover:bg-surface-hover'
                }`}
              >
                <button
                  type="button"
                  onClick={() => void selectScenario(sc.id)}
                  className="flex-1 min-w-0 text-left px-0.5 py-0.5 cursor-pointer"
                >
                  <div className="truncate">{sc.name}</div>
                  <div className="text-[10px] text-fg-subtle font-normal">
                    <span className="tg-tabular">{sc.steps?.length || 0}</span> 步骤 ·{' '}
                    {sc.load?.mode === 'rate' ? 'QPS' : 'VU'}
                  </div>
                </button>
                {canWrite && (
                  <ConfirmDeleteButton
                    label="删除场景"
                    idleClassName="opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
                    onConfirm={async () => {
                      try {
                        await api.deleteScenario(projectId, sc.id, sc.version)
                        onToast('success', `已删除场景「${sc.name}」`)
                        if (selectedId === sc.id) setSelectedId('')
                        await loadAll()
                      } catch (err) {
                        onToast('error', err instanceof APIError ? err.message : '删除场景失败')
                      }
                    }}
                  />
                )}
              </div>
            ))}
          </div>

          {/* Data Files Panel */}
          <div className="border-t border-line p-3 bg-surface-subtle/40 space-y-2 max-h-64 flex flex-col">
            <div className="flex items-center justify-between">
              <span className="text-xs font-semibold text-fg flex items-center gap-1.5">
                <Database className="w-3.5 h-3.5 text-tidal" /> 数据文件
                <span className="px-1.5 py-0.5 rounded-full bg-surface-subtle border border-line text-fg-muted text-[10px] font-mono tg-tabular">
                  {files.length}
                </span>
              </span>
              <label
                title={UPLOAD_ACCEPT.split(',').join(' ')}
                className={`inline-flex items-center gap-1 px-2 py-1 rounded border border-line-strong bg-surface-subtle text-[11px] font-medium text-fg-muted focus-within:ring-2 focus-within:ring-sky-500/40 ${
                  canWrite ? 'hover:bg-surface-hover hover:text-fg cursor-pointer' : 'opacity-45 cursor-not-allowed'
                }`}
              >
                <Upload className="w-3 h-3 text-tidal" /> 上传
                <input
                  type="file"
                  disabled={!canWrite}
                  accept={UPLOAD_ACCEPT}
                  onChange={(e) => {
                    const f = e.target.files?.[0]
                    if (f) void handleUploadFile(f)
                    e.target.value = ''
                  }}
                  className="sr-only"
                />
              </label>
            </div>
            <div className="overflow-y-auto tg-scroller space-y-1 text-xs">
              {files.length === 0 ? (
                <p className="text-[11px] text-fg-subtle py-2">暂无文件</p>
              ) : (
                files.map((df) => (
                  <div
                    key={df.name}
                    className="flex items-center justify-between gap-1.5 px-2 py-1 rounded border border-line bg-surface-subtle"
                  >
                    <div className="min-w-0">
                      <div className="font-mono truncate text-fg">{df.name}</div>
                      <div className="text-[10px] text-fg-subtle tg-tabular">
                        {Math.max(1, Math.round(df.size / 1024))} KB
                      </div>
                    </div>
                    <div className="inline-flex items-center gap-1 shrink-0">
                      <button
                        type="button"
                        title="预览"
                        aria-label={`预览 ${df.name}`}
                        onClick={() => void handlePreviewFile(df.name)}
                        className="p-1 text-fg-subtle hover:text-tidal"
                      >
                        <Eye className="w-3.5 h-3.5" />
                      </button>
                      {canWrite && (
                        <ConfirmDeleteButton
                          label="删除文件"
                          onConfirm={async () => {
                            try {
                              await api.deleteFile(projectId, df.name, df.version)
                              setFiles(await api.listFiles(projectId))
                              onToast('success', `已删除「${df.name}」`)
                            } catch (err) {
                              onToast('error', err instanceof APIError ? err.message : '删除文件失败')
                            }
                          }}
                        />
                      )}
                    </div>
                  </div>
                ))
              )}
            </div>
          </div>
        </aside>

        {/* Main Scenario Builder */}
        <main
          className={`flex-1 min-w-0 overflow-y-auto tg-scroller bg-transparent space-y-3 text-xs pr-0.5 ${
            zoomedMod === 'list' ? 'hidden' : ''
          }`}
        >
          <div
            className={`tg-accent-card !overflow-visible p-4 flex flex-wrap items-center justify-between gap-3 ${
              zoomedMod ? 'hidden' : ''
            }`}
          >
            <div className="flex flex-wrap items-center gap-3 flex-1 min-w-[260px]">
              <input
                type="text"
                name="scenario-name"
                aria-label="场景名称"
                value={draft.name || ''}
                onChange={(e) => updateDraft({ name: e.target.value })}
                placeholder="场景名称"
                className="px-3 py-1.5 border border-line-strong rounded-lg font-semibold text-sm text-fg placeholder:text-fg-subtle bg-surface focus:border-sky-500 focus:outline-none min-w-[220px]"
              />
              <div className="min-w-[170px]">
                <GlassSelect
                  ariaLabel="场景默认环境"
                  value={draft.environment_id || ''}
                  onChange={(val) => updateDraft({ environment_id: val })}
                  options={[
                    { value: '', label: '跟随顶部环境' },
                    ...environments.map((env) => ({
                      value: env.id,
                      label: env.name,
                      description: env.base_url || undefined,
                    })),
                  ]}
                />
              </div>
              {dirty && (
                <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-amber-50 border border-amber-300 text-amber-800 text-[11px] font-medium">
                  <span className="w-1.5 h-1.5 rounded-full bg-amber-500" />
                  未保存
                </span>
              )}
            </div>

            <div className="flex flex-wrap items-center gap-2">
              {draft.id && selectedId !== 'new' && (
                <div ref={exportWrapRef} className="relative">
                  <button
                    ref={exportTriggerRef}
                    type="button"
                    aria-expanded={exportMenuOpen}
                    aria-haspopup="listbox"
                    onClick={() => setExportMenuOpen((v) => !v)}
                    onKeyDown={(e) => {
                      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
                        e.preventDefault()
                        if (!exportMenuOpen) setExportMenuOpen(true)
                        requestAnimationFrame(() => {
                          const opts = exportMenuRef.current?.querySelectorAll<HTMLElement>('[role="option"]')
                          if (opts?.length) opts[e.key === 'ArrowUp' ? opts.length - 1 : 0].focus()
                        })
                      }
                    }}
                    className={stripBtn}
                    title="导出场景定义（Tidegate 原生 YAML/JSON 或 k6 脚本）"
                  >
                    <Download className="w-3.5 h-3.5 text-tidal" />
                    <span>导出场景</span>
                    <ChevronDown className={`w-3 h-3 text-fg-subtle transition-transform ${exportMenuOpen ? 'rotate-180' : ''}`} />
                  </button>
                  {exportMenuOpen && (
                    <div
                      ref={exportMenuRef}
                      popover="manual"
                      role="listbox"
                      aria-label="选择场景导出格式"
                      onKeyDown={(e) => {
                        if (e.key === 'Tab') {
                          setExportMenuOpen(false)
                          return
                        }
                        if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) return
                        const opts = Array.from(exportMenuRef.current?.querySelectorAll<HTMLElement>('[role="option"]') || [])
                        if (!opts.length) return
                        e.preventDefault()
                        const cur = opts.indexOf(document.activeElement as HTMLElement)
                        let next = 0
                        if (e.key === 'ArrowDown') next = cur < 0 ? 0 : (cur + 1) % opts.length
                        else if (e.key === 'ArrowUp') next = cur <= 0 ? opts.length - 1 : cur - 1
                        else if (e.key === 'End') next = opts.length - 1
                        opts[next]?.focus()
                      }}
                      className="tg-glass-popover fixed inset-auto m-0 z-50 w-64 max-w-[calc(100vw-1rem)] overflow-y-auto tg-scroller p-1.5 space-y-0.5 text-xs"
                    >
                      {(
                        [
                          {
                            fmt: 'yaml' as const,
                            title: 'Tidegate 原生场景 (.yaml)',
                            desc: '可直接 tidegate run 运行',
                            badge: '推荐',
                          },
                          {
                            fmt: 'json' as const,
                            title: 'Tidegate 原生 JSON (.json)',
                            desc: '标准 JSON 场景定义',
                          },
                          {
                            fmt: 'k6' as const,
                            title: 'k6 脚本 (.js)',
                            desc: '跨工具迁移 / 对比',
                          },
                        ] as const
                      ).map((opt) => (
                        <a
                          key={opt.fmt}
                          role="option"
                          href={api.exportScenarioURL(
                            projectId,
                            draft.id!,
                            opt.fmt,
                            draft.environment_id || activeEnvId || ''
                          )}
                          download
                          onClick={() => setExportMenuOpen(false)}
                          className="flex items-start justify-between gap-2 px-2.5 py-2 rounded-xl hover:bg-surface-hover focus:bg-surface-hover focus:outline-none text-fg transition-colors"
                        >
                          <div className="min-w-0">
                            <div className="font-semibold text-fg">{opt.title}</div>
                            <div className="text-[11px] text-fg-subtle">{opt.desc}</div>
                          </div>
                          {'badge' in opt && opt.badge && (
                            <span className="shrink-0 px-1.5 py-0.5 rounded-full bg-sky-500/15 text-tidal text-[10px] font-semibold">
                              {opt.badge}
                            </span>
                          )}
                        </a>
                      ))}
                    </div>
                  )}
                </div>
              )}
              <div className="inline-flex items-stretch rounded-lg border border-line-strong bg-surface overflow-hidden">
                <button
                  type="button"
                  disabled={!canWrite || dryRunning}
                  onClick={() => void handleDryRun()}
                  className="inline-flex items-center gap-1.5 px-3 py-1.5 hover:bg-surface-hover text-fg font-medium disabled:opacity-45"
                >
                  <FlaskConical className="w-3.5 h-3.5 text-tidal" />
                  {dryRunning ? '试跑中…' : '试跑'}
                </button>
                <label
                  title="遇到失败步骤立即停止"
                  className={`inline-flex items-center gap-1.5 px-2.5 border-l border-line text-fg-muted select-none ${
                    !canWrite || dryRunning ? 'opacity-45 cursor-not-allowed' : 'cursor-pointer hover:bg-surface-hover'
                  }`}
                >
                  <input
                    type="checkbox"
                    disabled={!canWrite || dryRunning}
                    checked={stopOnError}
                    onChange={(e) => setStopOnError(e.target.checked)}
                  />
                  遇错即停
                </label>
              </div>
              <button
                type="button"
                disabled={!canWrite || saving}
                onClick={() => void handleSave()}
                className="inline-flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-primary hover:bg-primary-hover text-white font-medium disabled:opacity-45"
              >
                <Save className="w-3.5 h-3.5" /> 保存
              </button>
              <button
                type="button"
                disabled={!canWrite || saving || dryRunning}
                onClick={async () => {
                  if (dirty || !draft.id) {
                    const saved = await handleSave()
                    if (!saved) return
                    onLaunchScenario(saved.id)
                    return
                  }
                  onLaunchScenario(draft.id)
                }}
                className="inline-flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-amber-500 hover:bg-amber-600 text-[#1f1300] font-semibold disabled:opacity-45"
              >
                <Rocket className="w-3.5 h-3.5" /> 启动压测
              </button>
            </div>
          </div>

          {/* Scenario Variables & Data Binding */}
          <div className={`grid grid-cols-1 lg:grid-cols-2 gap-3 ${zoomedMod ? 'hidden' : ''}`}>
            <section className="tg-glass-card tg-tilt-card p-4 space-y-2.5">
              <h3 className="font-semibold text-fg">场景变量</h3>
              <KeyValueEditor items={draft.variables || []} onChange={(next) => updateDraft({ variables: next })} compact />
            </section>

            <section className="tg-glass-card tg-tilt-card p-4 space-y-2.5">
              <div className="flex items-center justify-between">
                <h3 className="font-semibold text-fg">数据源</h3>
                <button
                  type="button"
                  onClick={() => {
                    const dataFile = dataFiles[0]?.name || files[0]?.name || ''
                    updateDraft({
                      data: [...dataSources, { name: `ds${dataSources.length + 1}`, file: dataFile, mode: 'sequential' }],
                    })
                  }}
                  className="inline-flex items-center gap-1 text-tidal hover:underline font-medium"
                >
                  <Plus className="w-3.5 h-3.5" /> 绑定文件
                </button>
              </div>
              {dataSources.length === 0 ? (
                <p className="text-fg-subtle">未绑定</p>
              ) : (
                <div className="space-y-2">
                  {dataSources.map((ds, i) => {
                    const updateDS = (patch: Partial<DataSource>) =>
                      updateDraft({ data: dataSources.map((x, idx) => (idx === i ? { ...x, ...patch } : x)) })
                    return (
                      <div key={i} className="flex items-center gap-1.5">
                        <input
                          type="text"
                          name={`ds-alias-${i}`}
                          aria-label="引用别名"
                          value={ds.name}
                          placeholder="别名"
                          autoCorrect="off"
                          autoCapitalize="none"
                          spellCheck={false}
                          onChange={(e) => updateDS({ name: e.target.value })}
                          className="w-20 px-2 py-1 border border-line-strong rounded font-mono bg-surface text-fg"
                        />
                        <select
                          aria-label="数据文件"
                          value={ds.file}
                          onChange={(e) => updateDS({ file: e.target.value })}
                          className="tg-select flex-1 min-w-0 px-2 py-1 border border-line-strong rounded font-mono bg-surface text-fg"
                        >
                          <option value="">选择文件</option>
                          {ds.file && !dataFiles.some((f) => f.name === ds.file) && (
                            <option value={ds.file}>{ds.file}</option>
                          )}
                          {dataFiles.map((f) => (
                            <option key={f.name} value={f.name}>
                              {f.name}
                            </option>
                          ))}
                        </select>
                        <select
                          aria-label="读取方式"
                          value={ds.mode || 'sequential'}
                          onChange={(e) => updateDS({ mode: e.target.value as DataSource['mode'] })}
                          className="tg-select w-28 px-1.5 py-1 border border-line-strong rounded bg-surface text-fg"
                        >
                          <option value="sequential">顺序循环</option>
                          <option value="random">随机读取</option>
                          <option value="unique">唯一分配</option>
                        </select>
                        <ColumnPeek projectId={projectId} file={ds.file} alias={ds.name} onToast={onToast} />
                        <button
                          type="button"
                          title="解除绑定"
                          aria-label="解除绑定"
                          onClick={() => updateDraft({ data: dataSources.filter((_, idx) => idx !== i) })}
                          className="p-1 text-fg-subtle hover:text-rose-600 shrink-0"
                        >
                          <X className="w-3.5 h-3.5" />
                        </button>
                      </div>
                    )
                  })}
                </div>
              )}
            </section>
          </div>

          {/* Steps Sequence */}
          <section
            ref={stepsPaneRef}
            className={`tg-glass-card !overflow-visible p-4 space-y-3 ${
              zoomedMod && zoomedMod !== 'steps' ? 'hidden' : ''
            }`}
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3 className="font-semibold text-fg inline-flex items-center gap-1.5">
                步骤编排
                <RollingNumber
                  value={totalSteps}
                  className="px-1.5 py-0.5 rounded-full bg-surface-subtle border border-line text-fg-muted text-[10px] font-mono"
                />
              </h3>
              <div className="flex items-center gap-2">
                <AddStepMenu label="添加步骤" variant="primary" align="right" allowControl onAdd={addRootStep} />
                <ModuleZoomButton
                  expanded={zoomedMod === 'steps'}
                  onToggle={() => toggleZoomMod('steps', stepsPaneRef.current)}
                />
              </div>
            </div>

            <ScenarioStepListEditor steps={steps} onChange={(nextSteps) => updateDraft({ steps: nextSteps })} depth={0} />
          </section>

          {/* Load Profile + SLA Thresholds */}
          <section
            ref={loadPaneRef}
            className={`tg-glass-card p-4 space-y-2 ${
              zoomedMod && zoomedMod !== 'load' ? 'hidden' : ''
            }`}
          >
            <div className="flex items-center justify-between gap-2">
              <h3 className="font-semibold text-fg">负载与阈值</h3>
              <ModuleZoomButton
                expanded={zoomedMod === 'load'}
                onToggle={() => toggleZoomMod('load', loadPaneRef.current)}
              />
            </div>
            <LoadConfigEditor
              load={draft.load || { mode: 'vus', stages: [{ duration: '0s', target: 10 }, { duration: '30s', target: 10 }] }}
              onChange={(nextLoad) => updateDraft({ load: nextLoad })}
              http={draft.http}
              onHTTPChange={(nextHTTP) => updateDraft({ http: nextHTTP })}
              stepOptions={collectStepOptions(steps)}
            />
          </section>

          {/* Functional Dry-Run Result */}
          {dryResult && !zoomedMod && (
            <section className="tg-glass-card p-4 space-y-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2 font-semibold text-sm">
                  {dryResult.passed ? (
                    <CheckCircle2 className="w-4 h-4 text-emerald-600" />
                  ) : (
                    <XCircle className="w-4 h-4 text-rose-600" />
                  )}
                  <span className={dryResult.passed ? 'text-emerald-700' : 'text-rose-700'}>
                    {dryResult.passed ? '试跑通过' : '试跑失败'}
                  </span>
                  <span className="text-xs font-mono font-normal text-fg-subtle tg-tabular">
                    {dryResult.steps.filter((s) => s.passed).length}/{dryResult.steps.length} ·{' '}
                    {dryResult.duration_ms.toFixed(1)} ms
                  </span>
                </div>
                {dryResult.final_vars && Object.keys(dryResult.final_vars).length > 0 && (
                  <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
                    <span className="text-fg-subtle">变量终值</span>
                    {Object.entries(dryResult.final_vars).map(([k, v]) => (
                      <span key={k} className="px-2 py-0.5 rounded bg-sky-50 border border-sky-200 font-mono text-sky-800">
                        {k}={v}
                      </span>
                    ))}
                  </div>
                )}
              </div>

              <div className="space-y-2">
                {dryResult.steps.map((step, i) => (
                  <DryRunStepCard key={i} index={i} step={step} />
                ))}
              </div>
            </section>
          )}
        </main>

        {/* File Preview Dialog */}
        <dialog
          ref={previewRef}
          {...({ closedby: 'any' } as Record<string, string>)}
          aria-labelledby="tg-file-preview-title"
          className="tg-dialog w-[min(48rem,calc(100vw-2rem))] max-h-[86dvh]"
        >
          {previewModal && (
            <>
              <header className="tg-dialog-header">
                <div id="tg-file-preview-title" className="flex items-center gap-2 min-w-0 text-sm font-semibold text-fg">
                  <span className="font-mono truncate min-w-0">{previewModal.name}</span>
                  {previewModal.is_csv && (
                    <span className="shrink-0 px-1.5 py-0.5 rounded-full bg-sky-50 border border-sky-200 text-sky-700 text-[10px] font-mono font-normal tg-tabular">
                      {previewModal.total_rows || 0} 行
                    </span>
                  )}
                  {previewModal.truncated && (
                    <span className="shrink-0 px-1.5 py-0.5 rounded-full bg-amber-50 border border-amber-200 text-amber-700 text-[10px] font-normal">
                      已截断
                    </span>
                  )}
                </div>
                <button
                  type="button"
                  aria-label="关闭"
                  onClick={() => setPreviewModal(null)}
                  className="p-1 rounded text-fg-subtle hover:text-fg hover:bg-surface-hover"
                >
                  <X className="w-4 h-4" />
                </button>
              </header>
              <div className="flex-1 min-h-0 overflow-auto tg-scroller p-4 text-xs">
                {previewModal.is_csv ? (
                  <table className="w-full text-left border-collapse border border-line font-mono tg-tabular">
                    <thead className="bg-surface-subtle border-b border-line sticky top-0">
                      <tr>
                        {(previewModal.columns || []).map((col, idx) => (
                          <th key={idx} className="px-2.5 py-1.5 border-r border-line text-tidal font-semibold">
                            {col}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-line">
                      {(previewModal.rows || []).map((row, rIdx) => (
                        <tr key={rIdx} className="hover:bg-surface-hover">
                          {row.map((cell, cIdx) => (
                            <td key={cIdx} className="px-2.5 py-1 border-r border-line text-fg">
                              {cell}
                            </td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : (
                  <pre className="p-3 rounded-lg border tg-code-surface font-mono overflow-auto whitespace-pre-wrap">
                    {previewModal.text || '(非文本或空文件)'}
                  </pre>
                )}
              </div>
            </>
          )}
        </dialog>

        {zoomedMod && (
          <BottomModuleStrip
            activeId={zoomedMod}
            onSelect={(id) => toggleZoomMod(id as 'list' | 'steps' | 'load', null)}
            onClose={() => setZoomedMod('')}
            items={[
              { id: 'list', title: '场景与数据', metric: `${scenarios.length} 场景` },
              { id: 'steps', title: '步骤编排', metric: `${totalSteps} 步` },
              {
                id: 'load',
                title: '负载与阈值',
                metric: draft.load?.mode === 'rate' ? 'QPS' : 'VU',
              },
            ]}
          />
        )}
      </div>
    </StepEditorContext.Provider>
  )
}

function DryRunStepCard({ index, step }: { index: number; step: DebugRunResult['steps'][number] }) {
  const [open, setOpen] = useState(!step.passed)
  return (
    <div className={`border rounded-lg overflow-hidden ${step.passed ? 'border-line' : 'border-rose-200'}`}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="w-full px-3 py-2 bg-surface-subtle flex items-center justify-between gap-2 text-left hover:bg-surface-hover"
      >
        <div className="flex items-center gap-2 font-mono min-w-0">
          {open ? <ChevronDown className="w-3.5 h-3.5 shrink-0" /> : <ChevronRight className="w-3.5 h-3.5 shrink-0" />}
          {step.passed ? (
            <CheckCircle2 className="w-3.5 h-3.5 text-emerald-700 shrink-0" aria-label="通过" />
          ) : (
            <XCircle className="w-3.5 h-3.5 text-rose-700 shrink-0" aria-label="失败" />
          )}
          <span className="font-semibold text-fg shrink-0">
            <span className="tg-tabular">#{index + 1}</span> {step.step_name || step.step_id}
          </span>
          <span className="text-fg-muted truncate min-w-0">
            {step.actual_request.method} {step.actual_request.url}
          </span>
        </div>
        <div className="flex items-center gap-2 font-mono tg-tabular shrink-0">
          <span
            className={`px-1.5 py-0.5 rounded border text-[10px] ${
              step.status >= 200 && step.status < 400
                ? 'bg-emerald-50 border-emerald-200 text-emerald-700'
                : 'bg-rose-50 border-rose-200 text-rose-700'
            }`}
          >
            {step.status || 'ERR'}
          </span>
          <span className="text-fg-muted">{step.timing.total_ms.toFixed(1)} ms</span>
        </div>
      </button>
      {open && (
        <div className="p-3 bg-surface border-t border-line space-y-2 text-xs">
          {step.error && <div className="text-rose-700 font-medium">{step.error}</div>}
          {(step.extracts?.length || 0) > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {step.extracts!.map((ex, idx) => (
                <span
                  key={idx}
                  className={`inline-flex items-center gap-1 px-2 py-0.5 rounded border font-mono ${
                    ex.matched ? 'bg-emerald-50 border-emerald-200 text-emerald-800' : 'bg-rose-50 border-rose-200 text-rose-800'
                  }`}
                >
                  {ex.matched ? <CheckCircle2 className="w-3 h-3" /> : <XCircle className="w-3 h-3" />}
                  {ex.name}={ex.matched ? ex.value : ex.error || '未匹配'}
                </span>
              ))}
            </div>
          )}
          {(step.asserts?.length || 0) > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {step.asserts!.map((as, idx) => (
                <span
                  key={idx}
                  className={`inline-flex items-center gap-1 px-2 py-0.5 rounded border ${
                    as.passed ? 'bg-emerald-50 border-emerald-200 text-emerald-800' : 'bg-rose-50 border-rose-200 text-rose-800'
                  }`}
                >
                  {as.passed ? <CheckCircle2 className="w-3 h-3" /> : <XCircle className="w-3 h-3" />}
                  {as.description || as.desc} {as.message}
                </span>
              ))}
            </div>
          )}
          <pre className="p-2.5 rounded-lg border tg-code-surface font-mono overflow-auto max-h-40 whitespace-pre-wrap">
            {step.body || '(空响应体)'}
          </pre>
        </div>
      )}
    </div>
  )
}

const fieldCls = 'px-2 py-1 border border-line-strong rounded bg-surface text-fg focus:border-sky-500 focus:outline-none'
const monoFieldCls = `${fieldCls} font-mono`
const subLabelCls = 'block text-[11px] text-fg-muted mb-0.5'
const idInputProps = { autoCorrect: 'off', autoCapitalize: 'none', spellCheck: false } as const

function ScenarioStepListEditor({
  steps,
  onChange,
  depth,
}: {
  steps: ScenarioStep[]
  onChange: (next: ScenarioStep[]) => void
  depth: number
}) {
  const { apiMap, onEditAPI, newAllocator, keyOf, preserveKey } = useStepCtx()
  const [overrideOpen, setOverrideOpen] = useState<Record<string, boolean>>({})
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const allowControl = depth < 3

  const addStep = (kind: ScenarioStepType, ref?: FlatAPI) => {
    const alloc = newAllocator()
    onChange([...steps, makeStep(kind, alloc(kindPrefix(kind)), ref)])
  }

  const moveStep = (idx: number, delta: number) => {
    const target = idx + delta
    if (target < 0 || target >= steps.length) return
    const next = [...steps]
    const [item] = next.splice(idx, 1)
    next.splice(target, 0, item)
    onChange(next)
  }

  const cloneAt = (idx: number) => {
    const copy = cloneStep(steps[idx], newAllocator())
    const next = [...steps]
    next.splice(idx + 1, 0, copy)
    onChange(next)
  }

  const nested = (list: ScenarioStep[], apply: (next: ScenarioStep[]) => void) => (
    <ScenarioStepListEditor steps={list} onChange={apply} depth={depth + 1} />
  )

  const menu = <AddStepMenu label={depth === 0 ? '添加步骤' : '子步骤'} allowControl={allowControl} onAdd={addStep} />

  if (steps.length === 0) {
    return (
      <div className="flex items-center justify-between gap-2 px-3 py-2 border border-dashed border-line-strong rounded-lg text-fg-subtle">
        <span>{depth === 0 ? '暂无步骤' : '空'}</span>
        {menu}
      </div>
    )
  }

  return (
    <div className="space-y-2">
      {steps.map((st, idx) => {
        const key = keyOf(st)
        const kind = (st.type || 'http') as ScenarioStepType
        const isAPI = isAPIKind(kind)
        const isContainer = CONTROL_KINDS.has(kind)
        const refAPI = isAPI && st.api_id ? apiMap.get(st.api_id) : undefined
        const isOvOpen = Boolean(overrideOpen[key])
        const isCollapsed = Boolean(collapsed[key])
        const meta = KIND_META[kind]
        const count = isContainer ? childCount(st) : 0
        const updateStep = (patch: Partial<ScenarioStep>) =>
          onChange(
            steps.map((row, i) => {
              if (i !== idx) return row
              const nextRow = { ...row, ...patch }
              preserveKey(row, nextRow)
              return nextRow
            })
          )

        let apiChip: ReactNode = null
        if (isAPI) {
          const text = refAPI ? (
            <>
              <MethodTag method={refAPI.method} />
              <span className="truncate min-w-0">{refAPI.url || refAPI.pathLabel}</span>
            </>
          ) : (
            <span className="truncate min-w-0">接口不存在 · {st.api_id || '-'}</span>
          )
          const base = `inline-flex items-center gap-1 font-mono text-[11px] px-2 py-0.5 rounded border max-w-xs min-w-0 ${
            refAPI ? 'border-line bg-surface-subtle text-fg-muted' : 'border-rose-200 bg-rose-50 text-rose-700'
          }`
          apiChip =
            onEditAPI && st.api_id && refAPI ? (
              <button
                type="button"
                title="去工作台编辑"
                aria-label={`去工作台编辑 ${refAPI.name}`}
                onClick={() => onEditAPI(st.api_id!)}
                className={`${base} group/api cursor-pointer hover:bg-sky-50 hover:border-sky-300 hover:text-tidal`}
              >
                {text}
                <ExternalLink className="w-3 h-3 shrink-0 text-tidal/80 group-hover/api:text-tidal" />
              </button>
            ) : (
              <span className={base} title={refAPI?.pathLabel}>
                {text}
              </span>
            )
        }

        return (
          <div
            key={key}
            className={`border rounded-lg p-3 space-y-2.5 ${meta ? `border-l-[3px] ${meta.accent}` : ''} ${
              st.disabled ? 'bg-surface-subtle border-line opacity-65' : 'bg-surface border-line'
            }`}
          >
            <div className="flex flex-wrap items-center gap-2">
              {isContainer && (
                <button
                  type="button"
                  aria-expanded={!isCollapsed}
                  aria-label={isCollapsed ? '展开' : '折叠'}
                  title={isCollapsed ? '展开' : '折叠'}
                  onClick={() => setCollapsed((m) => ({ ...m, [key]: !m[key] }))}
                  className="p-0.5 -ml-1 rounded text-fg-subtle hover:text-fg hover:bg-surface-hover"
                >
                  {isCollapsed ? <ChevronRight className="w-3.5 h-3.5" /> : <ChevronDown className="w-3.5 h-3.5" />}
                </button>
              )}
              <input
                type="checkbox"
                checked={!st.disabled}
                onChange={(e) => updateStep({ disabled: !e.target.checked })}
                title="启用"
                aria-label="启用此步骤"
              />
              <span className="font-mono text-fg-subtle tg-tabular">#{idx + 1}</span>
              {meta && (
                <span className={`px-1.5 py-0.5 rounded border text-[10px] font-semibold ${meta.badge}`}>{meta.label}</span>
              )}
              {isContainer && (
                <span
                  title={`${count} 个子步骤`}
                  className="px-1.5 py-0.5 rounded-full bg-surface-subtle border border-line text-fg-muted text-[10px] font-mono tg-tabular"
                >
                  {count}
                </span>
              )}
              <input
                type="text"
                name="step-id"
                aria-label="步骤 ID"
                value={st.id}
                placeholder="step ID"
                {...idInputProps}
                onChange={(e) => updateStep({ id: e.target.value })}
                className={`w-28 ${monoFieldCls}`}
              />
              <input
                type="text"
                aria-label="步骤名称"
                value={st.name || ''}
                placeholder={refAPI?.name || '步骤名称'}
                onChange={(e) => updateStep({ name: e.target.value })}
                className={`w-36 ${fieldCls}`}
              />
              {apiChip}

              <div className="flex flex-wrap items-center gap-1 ml-auto">
                {!isContainer && (
                  <>
                    <select
                      aria-label="思考时间"
                      title="思考时间"
                      value={st.think?.type || ''}
                      onChange={(e) => {
                        const t = e.target.value as '' | 'fixed' | 'uniform' | 'normal'
                        if (!t) updateStep({ think: null })
                        else if (t === 'fixed') updateStep({ think: { type: 'fixed', min: '500ms' } })
                        else if (t === 'uniform') updateStep({ think: { type: 'uniform', min: '500ms', max: '1500ms' } })
                        else updateStep({ think: { type: 'normal', mean: '800ms', stddev: '200ms', min: '100ms', max: '3s' } })
                      }}
                      className={`tg-select ${fieldCls} px-1.5`}
                    >
                      <option value="">无思考时间</option>
                      <option value="fixed">思考 · 固定</option>
                      <option value="uniform">思考 · 均匀</option>
                      <option value="normal">思考 · 正态</option>
                    </select>
                    {st.think?.type === 'fixed' && (
                      <input
                        type="text"
                        aria-label="固定思考时间"
                        value={st.think.min || ''}
                        {...idInputProps}
                        onChange={(e) => updateStep({ think: { ...st.think!, min: e.target.value } })}
                        placeholder="500ms"
                        className={`w-16 ${monoFieldCls} px-1.5`}
                      />
                    )}
                    {st.think?.type === 'uniform' && (
                      <>
                        <input
                          type="text"
                          aria-label="最小思考时间"
                          value={st.think.min || ''}
                          {...idInputProps}
                          onChange={(e) => updateStep({ think: { ...st.think!, min: e.target.value } })}
                          placeholder="min"
                          className={`w-16 ${monoFieldCls} px-1.5`}
                        />
                        <input
                          type="text"
                          aria-label="最大思考时间"
                          value={st.think.max || ''}
                          {...idInputProps}
                          onChange={(e) => updateStep({ think: { ...st.think!, max: e.target.value } })}
                          placeholder="max"
                          className={`w-16 ${monoFieldCls} px-1.5`}
                        />
                      </>
                    )}
                    {st.think?.type === 'normal' && (
                      <>
                        <input
                          type="text"
                          aria-label="均值"
                          title="均值"
                          value={st.think.mean || ''}
                          {...idInputProps}
                          onChange={(e) => updateStep({ think: { ...st.think!, mean: e.target.value } })}
                          placeholder="均值"
                          className={`w-16 ${monoFieldCls} px-1.5`}
                        />
                        <input
                          type="text"
                          aria-label="标准差"
                          title="标准差"
                          value={st.think.stddev || ''}
                          {...idInputProps}
                          onChange={(e) => updateStep({ think: { ...st.think!, stddev: e.target.value } })}
                          placeholder="标准差"
                          className={`w-16 ${monoFieldCls} px-1.5`}
                        />
                      </>
                    )}
                  </>
                )}

                {isAPI && (
                  <select
                    value={st.on_error || ''}
                    aria-label="失败策略"
                    title="失败策略"
                    onChange={(e) =>
                      updateStep({
                        on_error: (e.target.value || undefined) as '' | 'continue' | 'abort_iteration' | 'abort_vu' | undefined,
                      })
                    }
                    className={`tg-select ${fieldCls} px-1.5`}
                  >
                    <option value="">失败 · 默认</option>
                    <option value="continue">失败 · 继续</option>
                    <option value="abort_iteration">失败 · 中止迭代</option>
                    <option value="abort_vu">失败 · 停止 VU</option>
                  </select>
                )}

                {isAPI && (
                  <button
                    type="button"
                    aria-expanded={isOvOpen}
                    onClick={() => setOverrideOpen((m) => ({ ...m, [key]: !m[key] }))}
                    className={`inline-flex items-center gap-1 px-2 py-1 rounded border ${
                      st.override ? 'bg-sky-50 border-sky-300 text-sky-700' : 'border-line text-fg-muted hover:bg-surface-hover'
                    }`}
                  >
                    <SlidersHorizontal className="w-3 h-3" /> 覆盖
                  </button>
                )}
                <button
                  type="button"
                  title="克隆"
                  aria-label="克隆步骤"
                  onClick={() => cloneAt(idx)}
                  className="p-1 rounded text-fg-subtle hover:text-tidal hover:bg-surface-hover"
                >
                  <Copy className="w-3.5 h-3.5" />
                </button>
                <button
                  type="button"
                  title="上移"
                  aria-label="上移"
                  disabled={idx === 0}
                  onClick={() => moveStep(idx, -1)}
                  className="p-1 rounded text-fg-subtle hover:text-fg hover:bg-surface-hover disabled:opacity-30"
                >
                  <ArrowUp className="w-3.5 h-3.5" />
                </button>
                <button
                  type="button"
                  title="下移"
                  aria-label="下移"
                  disabled={idx === steps.length - 1}
                  onClick={() => moveStep(idx, 1)}
                  className="p-1 rounded text-fg-subtle hover:text-fg hover:bg-surface-hover disabled:opacity-30"
                >
                  <ArrowDown className="w-3.5 h-3.5" />
                </button>
                <ConfirmDeleteButton label="删除步骤" onConfirm={() => onChange(steps.filter((_, i) => i !== idx))} />
              </div>
            </div>

            {isAPI && isOvOpen && (
              <div className="pt-2 border-t border-line space-y-3 bg-surface-subtle p-3 rounded">
                <div className="grid grid-cols-1 sm:grid-cols-4 gap-2">
                  <input
                    type="text"
                    aria-label="覆盖方法"
                    value={st.override?.method || ''}
                    placeholder="方法"
                    {...idInputProps}
                    onChange={(e) => updateStep({ override: { ...(st.override || {}), method: e.target.value || undefined } })}
                    className={monoFieldCls}
                  />
                  <input
                    type="text"
                    aria-label="覆盖 URL"
                    value={st.override?.url || ''}
                    placeholder={refAPI?.url || 'URL'}
                    {...idInputProps}
                    onChange={(e) => updateStep({ override: { ...(st.override || {}), url: e.target.value || undefined } })}
                    className={`sm:col-span-3 ${monoFieldCls}`}
                  />
                </div>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                  <div>
                    <div className="font-medium text-fg-muted mb-1">请求头</div>
                    <KeyValueEditor
                      items={st.override?.headers || []}
                      onChange={(next) => updateStep({ override: { ...(st.override || {}), headers: next } })}
                      compact
                    />
                  </div>
                  <div>
                    <div className="font-medium text-fg-muted mb-1">请求体</div>
                    <textarea
                      rows={3}
                      aria-label="覆盖请求体"
                      value={st.override?.body?.content || ''}
                      placeholder="留空沿用原接口"
                      {...idInputProps}
                      onChange={(e) => {
                        const val = e.target.value
                        updateStep({
                          override: {
                            ...(st.override || {}),
                            body: val
                              ? {
                                  type: val.trim().startsWith('{') || val.trim().startsWith('[') ? 'json' : 'raw',
                                  content: val,
                                }
                              : undefined,
                          },
                        })
                      }}
                      className={`w-full p-2 ${monoFieldCls}`}
                    />
                  </div>
                </div>
              </div>
            )}

            {kind === 'script' && (
              <div className="border-t border-line pt-2">
                <CodeEditor
                  value={st.script || ''}
                  onChange={(next) => updateStep({ script: next })}
                  rows={4}
                  ariaLabel="脚本"
                  placeholder="vars.sign = hmac_sha256(vars.secret, vars.payload)"
                  snippets={SCRIPT_SNIPPETS}
                />
              </div>
            )}

            {isContainer && !isCollapsed && (
              <div className="pl-3 space-y-2">
                {kind === 'group' && nested(st.children || [], (next) => updateStep({ children: next }))}

                {kind === 'loop' && (
                  <>
                    <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
                      <div>
                        <label className={subLabelCls}>次数</label>
                        <input
                          type="number"
                          min={0}
                          value={st.loop_count ?? 1}
                          onChange={(e) => updateStep({ loop_count: Math.max(0, Number(e.target.value) || 0) })}
                          className={`w-full ${monoFieldCls} tg-tabular`}
                        />
                      </div>
                      <div>
                        <label className={subLabelCls}>While 条件</label>
                        <input
                          type="text"
                          value={st.while || ''}
                          {...idInputProps}
                          onChange={(e) => updateStep({ while: e.target.value || undefined })}
                          placeholder="{{has_more}} == true"
                          className={`w-full ${monoFieldCls}`}
                        />
                      </div>
                      <div>
                        <label className={subLabelCls}>最大迭代</label>
                        <input
                          type="number"
                          min={1}
                          value={st.max_iterations ?? 100}
                          onChange={(e) => updateStep({ max_iterations: Math.max(1, Number(e.target.value) || 100) })}
                          className={`w-full ${monoFieldCls} tg-tabular`}
                        />
                      </div>
                    </div>
                    {nested(st.children || [], (next) => updateStep({ children: next }))}
                  </>
                )}

                {kind === 'if' && (
                  <>
                    <div>
                      <label className={subLabelCls}>条件</label>
                      <input
                        type="text"
                        value={st.expr || ''}
                        {...idInputProps}
                        onChange={(e) => updateStep({ expr: e.target.value })}
                        placeholder='{{code}} == "200"'
                        className={`w-full ${monoFieldCls}`}
                      />
                    </div>
                    <div className="space-y-1">
                      <div className="text-[11px] font-semibold font-mono text-emerald-700">THEN</div>
                      {nested(st.children || [], (next) => updateStep({ children: next }))}
                    </div>
                    <div className="space-y-1">
                      <div className="text-[11px] font-semibold font-mono text-fg-muted">ELSE</div>
                      {nested(st.else || [], (next) => updateStep({ else: next }))}
                    </div>
                  </>
                )}

                {kind === 'weighted' && (
                  <WeightedBranches
                    branches={st.branches || []}
                    onChange={(next) => updateStep({ branches: next })}
                    renderSteps={nested}
                  />
                )}
              </div>
            )}
          </div>
        )
      })}
      {depth > 0 && menu}
    </div>
  )
}

function WeightedBranches({
  branches,
  onChange,
  renderSteps,
}: {
  branches: ScenarioWeightedBranch[]
  onChange: (next: ScenarioWeightedBranch[]) => void
  renderSteps: (list: ScenarioStep[], apply: (next: ScenarioStep[]) => void) => ReactNode
}) {
  const total = branches.reduce((s, b) => s + (Number(b.weight) || 0), 0)
  return (
    <div className="space-y-2">
      {total > 0 && branches.length > 0 && (
        <div className="flex h-1.5 rounded-full overflow-hidden bg-surface-subtle" aria-hidden="true">
          {branches.map((b, i) => (
            <div
              key={i}
              style={{ width: `${((Number(b.weight) || 0) / total) * 100}%` }}
              className={i % 2 === 0 ? 'bg-amber-500' : 'bg-amber-400/60'}
            />
          ))}
        </div>
      )}
      {branches.map((br, bIdx) => {
        const updateBr = (patch: Partial<ScenarioWeightedBranch>) =>
          onChange(branches.map((x, j) => (j === bIdx ? { ...x, ...patch } : x)))
        const pct = total > 0 ? ((Number(br.weight) || 0) / total) * 100 : 0
        return (
          <div key={bIdx} className="p-2.5 rounded border border-line bg-surface-subtle space-y-2">
            <div className="flex items-center gap-2">
              <input
                type="text"
                aria-label="分支名称"
                value={br.name || ''}
                placeholder="分支名称"
                onChange={(e) => updateBr({ name: e.target.value })}
                className={`w-36 ${fieldCls}`}
              />
              <label className="inline-flex items-center gap-1 text-fg-muted">
                权重
                <input
                  type="number"
                  min={0.1}
                  step="any"
                  value={br.weight}
                  onChange={(e) => updateBr({ weight: Math.max(0.1, Number(e.target.value) || 1) })}
                  className={`w-20 ${monoFieldCls} tg-tabular`}
                />
              </label>
              <span className="px-1.5 py-0.5 rounded-full bg-amber-50 border border-amber-200 text-amber-700 text-[10px] font-mono tg-tabular">
                {pct.toFixed(pct < 10 ? 1 : 0)}%
              </span>
              <span className="ml-auto" />
              <ConfirmDeleteButton label="删除分支" onConfirm={() => onChange(branches.filter((_, j) => j !== bIdx))} />
            </div>
            {renderSteps(br.steps || [], (next) => updateBr({ steps: next }))}
          </div>
        )
      })}
      <button
        type="button"
        onClick={() => onChange([...branches, { name: `分支 ${branches.length + 1}`, weight: 10, steps: [] }])}
        className="inline-flex items-center gap-1 text-[11px] text-tidal hover:underline font-medium"
      >
        <Plus className="w-3 h-3" /> 分支
      </button>
    </div>
  )
}
