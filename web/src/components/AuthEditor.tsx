import type { AuthConfig, AuthType } from '../lib/types'

interface Props {
  value?: AuthConfig | null
  onChange: (next: AuthConfig) => void
  allowInherit?: boolean
}

export function AuthEditor({ value, onChange, allowInherit = true }: Props) {
  const cur: AuthConfig = value && value.type ? value : { type: allowInherit ? 'inherit' : 'none' }
  const setType = (t: AuthType) => onChange({ ...cur, type: t })

  return (
    <div className="space-y-3 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-slate-500 font-medium">鉴权类型：</span>
        {(
          [
            ...(allowInherit ? [{ id: 'inherit' as const, label: '继承父目录' }] : []),
            { id: 'none' as const, label: '无鉴权' },
            { id: 'bearer' as const, label: 'Bearer Token' },
            { id: 'basic' as const, label: 'Basic Auth' },
            { id: 'api_key' as const, label: 'API Key' },
            { id: 'oauth2' as const, label: 'OAuth 2.0' },
          ] as const
        ).map((opt) => (
          <button
            key={opt.id}
            type="button"
            onClick={() => setType(opt.id)}
            className={`px-2.5 py-1 rounded-md border text-xs font-medium transition-colors ${
              cur.type === opt.id
                ? 'bg-sky-50 border-sky-400 text-sky-700'
                : 'bg-white border-slate-200 text-slate-600 hover:bg-slate-50'
            }`}
          >
            {opt.label}
          </button>
        ))}
      </div>

      {cur.type === 'inherit' && (
        <p className="text-slate-500">将自动沿所在目录树向上查找最近一个非继承鉴权配置（可通过“生效预览”查看来源）。</p>
      )}
      {cur.type === 'none' && <p className="text-slate-500">显式关闭鉴权并停止向上继承父目录凭证。</p>}

      {cur.type === 'bearer' && (
        <div className="grid grid-cols-1 max-w-lg gap-1.5">
          <label className="text-slate-600 font-medium">Token（支持 `{'{{变量}}'}`）</label>
          <input
            type="text"
            value={cur.token || ''}
            placeholder="例如 {{token}}"
            onChange={(e) => onChange({ ...cur, token: e.target.value })}
            className="px-2.5 py-1.5 border border-slate-300 rounded-md font-mono text-xs focus:border-sky-500 focus:outline-none"
          />
        </div>
      )}

      {cur.type === 'basic' && (
        <div className="grid grid-cols-2 max-w-lg gap-3">
          <div>
            <label className="block text-slate-600 font-medium mb-1">用户名</label>
            <input
              type="text"
              value={cur.username || ''}
              onChange={(e) => onChange({ ...cur, username: e.target.value })}
              className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono text-xs focus:border-sky-500 focus:outline-none"
            />
          </div>
          <div>
            <label className="block text-slate-600 font-medium mb-1">密码</label>
            <input
              type="password"
              value={cur.password || ''}
              onChange={(e) => onChange({ ...cur, password: e.target.value })}
              className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono text-xs focus:border-sky-500 focus:outline-none"
            />
          </div>
        </div>
      )}

      {cur.type === 'api_key' && (
        <div className="grid grid-cols-3 max-w-xl gap-3">
          <div>
            <label className="block text-slate-600 font-medium mb-1">参数位置</label>
            <select
              value={cur.in || 'header'}
              onChange={(e) => onChange({ ...cur, in: e.target.value as 'header' | 'query' })}
              className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md text-xs bg-white focus:border-sky-500 focus:outline-none"
            >
              <option value="header">请求头 (Header)</option>
              <option value="query">查询参数 (Query)</option>
            </select>
          </div>
          <div>
            <label className="block text-slate-600 font-medium mb-1">参数名</label>
            <input
              type="text"
              value={cur.key || ''}
              placeholder="X-API-Key"
              onChange={(e) => onChange({ ...cur, key: e.target.value })}
              className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono text-xs focus:border-sky-500 focus:outline-none"
            />
          </div>
          <div>
            <label className="block text-slate-600 font-medium mb-1">密钥值</label>
            <input
              type="text"
              value={cur.value || ''}
              placeholder="{{api_key}}"
              onChange={(e) => onChange({ ...cur, value: e.target.value })}
              className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono text-xs focus:border-sky-500 focus:outline-none"
            />
          </div>
        </div>
      )}

      {cur.type === 'oauth2' && (
        <div className="space-y-3 max-w-2xl">
          <div className="text-[11px] text-slate-500 bg-sky-50/70 border border-sky-200 rounded-md px-3 py-2">
            支持 OAuth 2.0 客户端凭证模式（Client Credentials），调试和压测时会自动获取并按过期时间缓存/续期 Access Token。
          </div>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div className="md:col-span-2">
              <label className="block text-slate-600 font-medium mb-1">Token 获取地址 (token_url)</label>
              <input
                type="text"
                value={cur.token_url || ''}
                placeholder="https://auth.example.com/oauth/token"
                onChange={(e) => onChange({ ...cur, token_url: e.target.value })}
                className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono text-xs focus:border-sky-500 focus:outline-none"
              />
            </div>
            <div>
              <label className="block text-slate-600 font-medium mb-1">Client ID</label>
              <input
                type="text"
                value={cur.client_id || ''}
                placeholder="{{oauth_client_id}}"
                onChange={(e) => onChange({ ...cur, client_id: e.target.value })}
                className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono text-xs focus:border-sky-500 focus:outline-none"
              />
            </div>
            <div>
              <label className="block text-slate-600 font-medium mb-1">Client Secret</label>
              <input
                type="password"
                value={cur.client_secret || ''}
                placeholder="{{oauth_client_secret}}"
                onChange={(e) => onChange({ ...cur, client_secret: e.target.value })}
                className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono text-xs focus:border-sky-500 focus:outline-none"
              />
            </div>
            <div>
              <label className="block text-slate-600 font-medium mb-1">Scope（可选）</label>
              <input
                type="text"
                value={cur.scope || ''}
                placeholder="read write"
                onChange={(e) => onChange({ ...cur, scope: e.target.value })}
                className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md font-mono text-xs focus:border-sky-500 focus:outline-none"
              />
            </div>
            <div>
              <label className="block text-slate-600 font-medium mb-1">客户端认证方式</label>
              <select
                value={cur.client_auth || 'basic'}
                onChange={(e) => onChange({ ...cur, client_auth: e.target.value as 'basic' | 'body' })}
                className="w-full px-2.5 py-1.5 border border-slate-300 rounded-md text-xs bg-white focus:border-sky-500 focus:outline-none"
              >
                <option value="basic">Authorization Basic 请求头（默认）</option>
                <option value="body">表单 Body 参数传输</option>
              </select>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
