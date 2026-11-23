import type { AuthConfig, AuthType } from '../lib/types'

interface Props {
  value?: AuthConfig | null
  onChange: (next: AuthConfig) => void
  allowInherit?: boolean
}

const inputCls =
  'w-full px-2.5 py-1.5 border border-line-strong rounded-md bg-surface text-fg font-mono text-xs focus:border-sky-500 focus:outline-none'
const selectCls =
  'w-full px-2.5 py-1.5 border border-line-strong rounded-md bg-surface text-fg text-xs focus:border-sky-500 focus:outline-none'
const labelCls = 'block text-fg-muted font-medium mb-1'
const idProps = { autoComplete: 'off', autoCorrect: 'off', autoCapitalize: 'none', spellCheck: false } as const

export function AuthEditor({ value, onChange, allowInherit = true }: Props) {
  const cur: AuthConfig = value && value.type ? value : { type: allowInherit ? 'inherit' : 'none' }
  const setType = (t: AuthType) => onChange({ ...cur, type: t })

  return (
    <div className="space-y-3 text-xs">
      <div role="group" aria-label="鉴权类型" className="flex flex-wrap items-center gap-2">
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
            aria-pressed={cur.type === opt.id}
            onClick={() => setType(opt.id)}
            className={`px-2.5 py-1 rounded-md border text-xs font-medium transition-colors ${
              cur.type === opt.id
                ? 'bg-sky-50 border-sky-400 text-sky-700'
                : 'bg-surface border-line text-fg-muted hover:bg-surface-hover hover:text-fg'
            }`}
          >
            {opt.label}
          </button>
        ))}
      </div>

      {cur.type === 'bearer' && (
        <label className="block max-w-lg">
          <span className={labelCls}>Token</span>
          <input
            type="text"
            name="auth-token"
            {...idProps}
            value={cur.token || ''}
            placeholder="例如 {{token}}"
            onChange={(e) => onChange({ ...cur, token: e.target.value })}
            className={inputCls}
          />
        </label>
      )}

      {cur.type === 'basic' && (
        <div className="grid grid-cols-2 max-w-lg gap-3">
          <label className="block">
            <span className={labelCls}>用户名</span>
            <input
              type="text"
              name="auth-username"
              {...idProps}
              value={cur.username || ''}
              onChange={(e) => onChange({ ...cur, username: e.target.value })}
              className={inputCls}
            />
          </label>
          <label className="block">
            <span className={labelCls}>密码</span>
            <input
              type="password"
              name="auth-password"
              autoComplete="new-password"
              value={cur.password || ''}
              onChange={(e) => onChange({ ...cur, password: e.target.value })}
              className={inputCls}
            />
          </label>
        </div>
      )}

      {cur.type === 'api_key' && (
        <div className="grid grid-cols-3 max-w-xl gap-3">
          <label className="block">
            <span className={labelCls}>参数位置</span>
            <select
              name="auth-api-key-in"
              value={cur.in || 'header'}
              onChange={(e) => onChange({ ...cur, in: e.target.value as 'header' | 'query' })}
              className={selectCls}
            >
              <option value="header">请求头 (Header)</option>
              <option value="query">查询参数 (Query)</option>
            </select>
          </label>
          <label className="block">
            <span className={labelCls}>参数名</span>
            <input
              type="text"
              name="auth-api-key-name"
              {...idProps}
              value={cur.key || ''}
              placeholder="X-API-Key"
              onChange={(e) => onChange({ ...cur, key: e.target.value })}
              className={inputCls}
            />
          </label>
          <label className="block">
            <span className={labelCls}>密钥值</span>
            <input
              type="text"
              name="auth-api-key-value"
              {...idProps}
              value={cur.value || ''}
              placeholder="{{api_key}}"
              onChange={(e) => onChange({ ...cur, value: e.target.value })}
              className={inputCls}
            />
          </label>
        </div>
      )}

      {cur.type === 'oauth2' && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3 max-w-2xl">
          <label className="block md:col-span-2">
            <span className={labelCls}>Token URL（客户端凭证模式）</span>
            <input
              type="text"
              name="auth-oauth-token-url"
              {...idProps}
              value={cur.token_url || ''}
              placeholder="https://auth.example.com/oauth/token"
              onChange={(e) => onChange({ ...cur, token_url: e.target.value })}
              className={inputCls}
            />
          </label>
          <label className="block">
            <span className={labelCls}>Client ID</span>
            <input
              type="text"
              name="auth-oauth-client-id"
              {...idProps}
              value={cur.client_id || ''}
              placeholder="{{oauth_client_id}}"
              onChange={(e) => onChange({ ...cur, client_id: e.target.value })}
              className={inputCls}
            />
          </label>
          <label className="block">
            <span className={labelCls}>Client Secret</span>
            <input
              type="password"
              name="auth-oauth-client-secret"
              autoComplete="new-password"
              value={cur.client_secret || ''}
              placeholder="{{oauth_client_secret}}"
              onChange={(e) => onChange({ ...cur, client_secret: e.target.value })}
              className={inputCls}
            />
          </label>
          <label className="block">
            <span className={labelCls}>Scope</span>
            <input
              type="text"
              name="auth-oauth-scope"
              {...idProps}
              value={cur.scope || ''}
              placeholder="read write"
              onChange={(e) => onChange({ ...cur, scope: e.target.value })}
              className={inputCls}
            />
          </label>
          <label className="block">
            <span className={labelCls}>客户端认证方式</span>
            <select
              name="auth-oauth-client-auth"
              value={cur.client_auth || 'basic'}
              onChange={(e) => onChange({ ...cur, client_auth: e.target.value as 'basic' | 'body' })}
              className={selectCls}
            >
              <option value="basic">Basic 请求头</option>
              <option value="body">表单 Body</option>
            </select>
          </label>
        </div>
      )}
    </div>
  )
}
