import { useEffect, useMemo, useRef, useState } from 'react'
import { Bot, CornerDownLeft, FilePlus, Globe, Layers, Play, Search, Server, ShieldCheck, Terminal, Workflow, X } from 'lucide-react'
import { api } from '../lib/api'
import type { Environment, Scenario, TreeNode } from '../lib/types'
import type { NavTab } from './TopBar'

interface CommandItem {
  id: string
  category: 'api' | 'scenario' | 'env' | 'action'
  label: string
  hint: string
  badge?: string
  run: () => void
}

interface Props {
  open: boolean
  projectId: string
  environments: Environment[]
  activeEnvId: string
  onClose: () => void
  onChangeTab: (tab: NavTab) => void
  onSelectEnv: (envId: string) => void
  onOpenAPI: (apiId: string) => void
  onNewScratchAPI: () => void
  onOpenMCPConfig: () => void
}

function flattenAPINodes(nodes: TreeNode[], prefix = ''): Array<{ id: string; name: string; method: string; url: string; path: string }> {
  const out: Array<{ id: string; name: string; method: string; url: string; path: string }> = []
  for (const n of nodes) {
    if (n.type === 'folder') {
      const next = prefix ? `${prefix} / ${n.name}` : n.name
      if (n.children) out.push(...flattenAPINodes(n.children, next))
    } else {
      out.push({
        id: n.id,
        name: n.name,
        method: n.method || 'GET',
        url: n.url || '',
        path: prefix || '根目录',
      })
    }
  }
  return out
}

export function CommandPalette({
  open,
  projectId,
  environments,
  activeEnvId,
  onClose,
  onChangeTab,
  onSelectEnv,
  onOpenAPI,
  onNewScratchAPI,
  onOpenMCPConfig,
}: Props) {
  const dialogRef = useRef<HTMLDialogElement | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)
  const [query, setQuery] = useState('')
  const [selectedIdx, setSelectedIdx] = useState(0)
  const [apis, setApis] = useState<Array<{ id: string; name: string; method: string; url: string; path: string }>>([])
  const [scenarios, setScenarios] = useState<Scenario[]>([])

  // Native <dialog> open/close sync + Safari closedby fallback.
  useEffect(() => {
    const dlg = dialogRef.current
    if (!dlg) return
    if (open) {
      setQuery('')
      setSelectedIdx(0)
      if (!dlg.open) {
        dlg.showModal()
      }
      window.requestAnimationFrame(() => inputRef.current?.focus())
    } else if (dlg.open) {
      dlg.close()
    }
  }, [open])

  useEffect(() => {
    const dlg = dialogRef.current
    if (!dlg) return
    const onCloseEv = () => onClose()
    dlg.addEventListener('close', onCloseEv)

    // Mandatory fallback when browser lacks HTMLDialogElement closedby="any" (e.g. Safari).
    let pressedOutside = false
    const outside = (e: PointerEvent) => {
      const r = dlg.getBoundingClientRect()
      return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom
    }
    const onPointerDown = (e: PointerEvent) => {
      pressedOutside = outside(e)
    }
    const onPointerUp = (e: PointerEvent) => {
      if (!('closedBy' in HTMLDialogElement.prototype) && pressedOutside && outside(e)) {
        dlg.close()
      }
      pressedOutside = false
    }
    dlg.addEventListener('pointerdown', onPointerDown)
    dlg.addEventListener('pointerup', onPointerUp)
    return () => {
      dlg.removeEventListener('close', onCloseEv)
      dlg.removeEventListener('pointerdown', onPointerDown)
      dlg.removeEventListener('pointerup', onPointerUp)
    }
  }, [onClose])

  useEffect(() => {
    if (!open || !projectId) return
    void Promise.all([api.getTree(projectId), api.listScenarios(projectId)])
      .then(([tree, scs]) => {
        setApis(flattenAPINodes(tree.children || []))
        setScenarios(scs || [])
      })
      .catch(() => undefined)
  }, [open, projectId])

  const items = useMemo<CommandItem[]>(() => {
    const list: CommandItem[] = [
      {
        id: 'act-new-api',
        category: 'action',
        label: '新建临时请求',
        hint: '',
        run: () => {
          onNewScratchAPI()
          onClose()
        },
      },
      {
        id: 'act-mcp',
        category: 'action',
        label: 'MCP 接入配置',
        hint: '',
        badge: 'MCP',
        run: () => {
          onOpenMCPConfig()
          onClose()
        },
      },
      {
        id: 'act-nav-workbench',
        category: 'action',
        label: '工作台',
        hint: '',
        run: () => {
          onChangeTab('workbench')
          onClose()
        },
      },
      {
        id: 'act-nav-scenarios',
        category: 'action',
        label: '场景与数据',
        hint: '',
        run: () => {
          onChangeTab('scenarios')
          onClose()
        },
      },
      {
        id: 'act-nav-run',
        category: 'action',
        label: '实时压测',
        hint: '',
        run: () => {
          onChangeTab('run')
          onClose()
        },
      },
      {
        id: 'act-nav-agents',
        category: 'action',
        label: '发压节点',
        hint: '',
        run: () => {
          onChangeTab('agents')
          onClose()
        },
      },
      {
        id: 'act-nav-gov',
        category: 'action',
        label: '系统治理',
        hint: '',
        run: () => {
          onChangeTab('governance')
          onClose()
        },
      },
    ]

    for (const env of environments) {
      list.push({
        id: `env-${env.id}`,
        category: 'env',
        label: `切换环境：${env.name}`,
        hint: env.base_url || '',
        badge: env.id === activeEnvId ? '当前' : '环境',
        run: () => {
          onSelectEnv(env.id)
          onClose()
        },
      })
    }

    for (const sc of scenarios) {
      list.push({
        id: `scen-${sc.id}`,
        category: 'scenario',
        label: sc.name,
        hint: `${sc.steps?.length || 0} 步 / ${sc.load?.mode === 'rate' ? 'QPS' : 'VU'}`,
        badge: '场景',
        run: () => {
          onChangeTab('scenarios')
          onClose()
        },
      })
    }

    for (const a of apis) {
      list.push({
        id: `api-${a.id}`,
        category: 'api',
        label: a.name,
        hint: a.url ? `${a.path}  ${a.url}` : a.path,
        badge: a.method,
        run: () => {
          onOpenAPI(a.id)
          onClose()
        },
      })
    }

    const q = query.trim().toLowerCase()
    if (!q) return list.slice(0, 20)
    return list
      .filter(
        (it) =>
          it.label.toLowerCase().includes(q) ||
          it.hint.toLowerCase().includes(q) ||
          (it.badge || '').toLowerCase().includes(q)
      )
      .slice(0, 24)
  }, [apis, scenarios, environments, activeEnvId, query, onChangeTab, onClose, onNewScratchAPI, onOpenAPI, onOpenMCPConfig, onSelectEnv])

  useEffect(() => {
    setSelectedIdx(0)
  }, [query])

  const iconFor = (it: CommandItem) => {
    if (it.id === 'act-new-api') return <FilePlus className="w-4 h-4 text-sky-600 shrink-0" />
    if (it.id === 'act-mcp') return <Bot className="w-4 h-4 text-cyan-600 shrink-0" />
    if (it.id === 'act-nav-run') return <Play className="w-4 h-4 text-emerald-600 shrink-0" />
    if (it.id === 'act-nav-agents') return <Server className="w-4 h-4 text-indigo-600 shrink-0" />
    if (it.id === 'act-nav-gov') return <ShieldCheck className="w-4 h-4 text-amber-600 shrink-0" />
    if (it.category === 'env') return <Globe className="w-4 h-4 text-emerald-600 shrink-0" />
    if (it.category === 'scenario') return <Workflow className="w-4 h-4 text-purple-600 shrink-0" />
    if (it.category === 'api') return <Terminal className="w-4 h-4 text-sky-600 shrink-0" />
    return <Layers className="w-4 h-4 text-slate-500 shrink-0" />
  }

  return (
    <dialog
      ref={dialogRef}
      closedby="any"
      aria-labelledby="tg-cmd-title"
      className="tg-dialog w-[min(36rem,calc(100vw-2rem))] mt-[12vh] overflow-hidden"
    >
      <search className="px-4 py-3 border-b border-slate-200 flex items-center gap-2.5 bg-surface">
        <Search className="w-4 h-4 text-sky-600 shrink-0" />
        <h2 id="tg-cmd-title" className="sr-only">
          全局命令与接口搜索
        </h2>
        <input
          ref={inputRef}
          type="text"
          name="command_search"
          autoCorrect="off"
          autoCapitalize="none"
          spellCheck={false}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') {
              e.preventDefault()
              setSelectedIdx((i) => (items.length ? (i + 1) % items.length : 0))
            } else if (e.key === 'ArrowUp') {
              e.preventDefault()
              setSelectedIdx((i) => (items.length ? (i - 1 + items.length) % items.length : 0))
            } else if (e.key === 'Enter' && items[selectedIdx]) {
              e.preventDefault()
              items[selectedIdx].run()
            }
          }}
          placeholder="接口、场景、环境、命令"
          className="flex-1 min-w-0 text-sm text-slate-900 placeholder:text-slate-400 focus:outline-none"
        />
        <button
          type="button"
          onClick={onClose}
          aria-label="关闭命令面板"
          className="p-1 rounded text-slate-400 hover:text-slate-700 hover:bg-slate-100"
        >
          <X className="w-4 h-4" />
        </button>
      </search>

      <div className="max-h-[55vh] overflow-y-auto tg-scroller divide-y divide-slate-100 bg-surface">
        {items.length === 0 ? (
          <div className="py-10 text-center text-xs text-slate-400">无匹配结果</div>
        ) : (
          items.map((it, idx) => {
            const active = idx === selectedIdx
            return (
              <button
                key={it.id}
                type="button"
                onMouseEnter={() => setSelectedIdx(idx)}
                onClick={it.run}
                className={`w-full px-4 py-2.5 flex items-center gap-3 text-left text-xs transition-colors ${
                  active ? 'bg-sky-50 text-sky-950' : 'text-slate-700 hover:bg-slate-50'
                }`}
              >
                {iconFor(it)}
                <div className="min-w-0 flex-1">
                  <div className="font-medium text-slate-900 truncate">{it.label}</div>
                  {it.hint && <div className="text-[11px] text-slate-500 font-mono truncate">{it.hint}</div>}
                </div>
                {it.badge && (
                  <span className="px-2 py-0.5 rounded border border-slate-200 bg-slate-100 font-mono text-[10px] font-semibold text-slate-700 shrink-0">
                    {it.badge}
                  </span>
                )}
                {active && <CornerDownLeft className="w-3.5 h-3.5 text-sky-600 shrink-0" />}
              </button>
            )
          })
        )}
      </div>

      <div className="px-4 py-2 bg-slate-50 border-t border-slate-200 flex items-center justify-between text-[11px] text-slate-500 font-mono">
        <span className="flex items-center gap-1">
          <kbd className="tg-kbd">↑</kbd>
          <kbd className="tg-kbd">↓</kbd>
          <kbd className="tg-kbd">Enter</kbd>
        </span>
        <kbd className="tg-kbd">Esc</kbd>
      </div>
    </dialog>
  )
}
