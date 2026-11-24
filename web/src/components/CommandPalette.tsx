import { useEffect, useMemo, useRef, useState } from 'react'
import { Bot, CornerDownLeft, FilePlus, FolderKanban, Globe, Layers, Play, Search, Server, ShieldCheck, Terminal, Workflow, X } from 'lucide-react'
import { api } from '../lib/api'
import type { Environment, Scenario, TreeNode } from '../lib/types'
import { useNativeDialog } from '../lib/useNativeDialog'
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
  onOpenProjects?: () => void
  onOpenEnvs?: () => void
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

function HighlightText({ text, query }: { text: string; query: string }) {
  const q = query.trim()
  if (!q) return <>{text}</>
  const lower = text.toLowerCase()
  const ql = q.toLowerCase()
  const out: Array<{ k: number; s: string; hit: boolean }> = []
  let i = 0
  let k = 0
  while (i < text.length) {
    const at = lower.indexOf(ql, i)
    if (at < 0) {
      out.push({ k: k++, s: text.slice(i), hit: false })
      break
    }
    if (at > i) out.push({ k: k++, s: text.slice(i, at), hit: false })
    out.push({ k: k++, s: text.slice(at, at + q.length), hit: true })
    i = at + q.length
  }
  return (
    <>
      {out.map((p) =>
        p.hit ? (
          <mark key={p.k} className="rounded bg-sky-200/80 dark:bg-sky-400/30 px-0.5 text-fg">
            {p.s}
          </mark>
        ) : (
          <span key={p.k}>{p.s}</span>
        )
      )}
    </>
  )
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
  onOpenProjects,
  onOpenEnvs,
}: Props) {
  const dialogRef = useNativeDialog(open, onClose)
  const inputRef = useRef<HTMLInputElement | null>(null)
  const [query, setQuery] = useState('')
  const [selectedIdx, setSelectedIdx] = useState(0)
  const [apis, setApis] = useState<Array<{ id: string; name: string; method: string; url: string; path: string }>>([])
  const [scenarios, setScenarios] = useState<Scenario[]>([])

  useEffect(() => {
    if (!open) return
    setQuery('')
    setSelectedIdx(0)
    window.requestAnimationFrame(() => inputRef.current?.focus())
  }, [open])

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
        label: '系统治理 · 账号与权限管控',
        hint: '',
        run: () => {
          onChangeTab('governance')
          onClose()
        },
      },
      ...(onOpenProjects
        ? [
            {
              id: 'act-modal-projects',
              category: 'action' as const,
              label: '项目管理与新建',
              hint: '',
              run: () => {
                onClose()
                onOpenProjects()
              },
            },
          ]
        : []),
      ...(onOpenEnvs
        ? [
            {
              id: 'act-modal-envs',
              category: 'action' as const,
              label: '环境配置与变量',
              hint: '',
              run: () => {
                onClose()
                onOpenEnvs()
              },
            },
          ]
        : []),
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
  }, [apis, scenarios, environments, activeEnvId, query, onChangeTab, onClose, onNewScratchAPI, onOpenAPI, onOpenMCPConfig, onOpenProjects, onOpenEnvs, onSelectEnv])

  useEffect(() => {
    setSelectedIdx(0)
  }, [query])

  const iconFor = (it: CommandItem) => {
    if (it.id === 'act-new-api') return <FilePlus className="w-4 h-4 text-sky-600 shrink-0" />
    if (it.id === 'act-mcp') return <Bot className="w-4 h-4 text-cyan-600 shrink-0" />
    if (it.id === 'act-modal-projects') return <FolderKanban className="w-4 h-4 text-sky-600 shrink-0" />
    if (it.id === 'act-modal-envs') return <Layers className="w-4 h-4 text-emerald-600 shrink-0" />
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
      {...({ closedby: 'any' } as Record<string, string>)}
      aria-labelledby="tg-cmd-title"
      className="tg-dialog w-[min(36rem,calc(100vw-2rem))] mt-[12vh] overflow-hidden"
    >
      <search className="px-4 py-3 border-b border-line flex items-center gap-2.5">
        <Search className="w-4 h-4 text-tidal shrink-0" />
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
          className="flex-1 min-w-0 bg-transparent text-sm text-fg placeholder:text-fg-subtle focus:outline-none"
        />
        <button
          type="button"
          onClick={onClose}
          aria-label="关闭命令面板"
          className="p-1 rounded-lg text-fg-subtle hover:text-fg hover:bg-surface-hover"
        >
          <X className="w-4 h-4" />
        </button>
      </search>

      <div className="max-h-[55vh] overflow-y-auto tg-scroller divide-y divide-[var(--tg-border)]">
        {items.length === 0 ? (
          <div className="py-10 text-center text-xs text-fg-subtle">无匹配结果</div>
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
                  active ? 'bg-sky-500/15 text-fg' : 'text-fg-muted hover:bg-surface-hover'
                }`}
              >
                {iconFor(it)}
                <div className="min-w-0 flex-1">
                  <div className="font-semibold text-fg truncate">
                    <HighlightText text={it.label} query={query} />
                  </div>
                  {it.hint && (
                    <div className="text-[11px] text-fg-subtle font-mono truncate">
                      <HighlightText text={it.hint} query={query} />
                    </div>
                  )}
                </div>
                {it.badge && (
                  <span className="px-2 py-0.5 rounded-full border border-line bg-surface-subtle font-mono text-[10px] font-semibold text-fg-muted shrink-0">
                    {it.badge}
                  </span>
                )}
                {active && <CornerDownLeft className="w-3.5 h-3.5 text-tidal shrink-0" />}
              </button>
            )
          })
        )}
      </div>

      <div className="px-4 py-2 bg-surface-subtle border-t border-line flex items-center justify-between text-[11px] text-fg-subtle font-mono">
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
