import { useState, type FormEvent } from 'react'
import { KeyRound, Lock, ShieldCheck, AlertTriangle } from 'lucide-react'
import { api, APIError } from '../lib/api'
import type { SetupStatus, User } from '../lib/types'

interface Props {
  status: SetupStatus
  onAuthenticated: (user: User) => void
}

export function AuthGate({ status, onAuthenticated }: Props) {
  const isSetup = status.setup_required
  const [code, setCode] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirmPwd, setConfirmPwd] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [insecureTransport, setInsecureTransport] = useState(false)

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setInsecureTransport(false)
    if (isSetup) {
      if (password.length < 10) {
        setError('管理员密码长度至少为 10 位')
        return
      }
      if (password !== confirmPwd) {
        setError('两次输入的密码不一致')
        return
      }
    }
    setSubmitting(true)
    try {
      const res = isSetup
        ? await api.setup({ code: code.trim(), username: username.trim(), password })
        : await api.login({ username: username.trim(), password })
      onAuthenticated(res.user)
    } catch (err) {
      if (err instanceof APIError) {
        setError(err.message)
        if (err.code === 'insecure_transport') {
          setInsecureTransport(true)
        }
      } else {
        setError('网络请求失败，请检查服务是否在线')
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-slate-100 p-4">
      <div className="w-full max-w-md bg-white border border-slate-200 rounded-xl shadow-sm overflow-hidden">
        <div className="bg-[#0B2A4A] text-white px-6 py-5 flex items-center gap-3">
          <img src="/favicon.svg" alt="Tidegate" className="w-10 h-10 rounded-lg" />
          <div>
            <h1 className="text-lg font-semibold tracking-tight">Tidegate 控制台</h1>
            <p className="text-xs text-sky-200">
              {isSetup ? '首次启动初始化管理员账号' : '请登录以管理接口、场景与分布式压测'}
            </p>
          </div>
        </div>

        <form onSubmit={handleSubmit} className="p-6 space-y-4">
          {error && (
            <div className="rounded-lg border border-rose-200 bg-rose-50 px-3.5 py-2.5 text-xs text-rose-800 space-y-1">
              <div className="flex items-center gap-1.5 font-medium">
                <AlertTriangle className="w-4 h-4 shrink-0 text-rose-600" />
                <span>{error}</span>
              </div>
              {insecureTransport && (
                <p className="text-rose-700 leading-relaxed pl-5">
                  非本机访问拒绝通过明文 HTTP 发送凭据：请启用 `--tls-self-signed` / `--tls-cert`，或仅在受信任内网使用
                  `--allow-insecure-remote`。
                </p>
              )}
            </div>
          )}

          {isSetup && (
            <div>
              <label className="block text-xs font-medium text-slate-700 mb-1">
                终端输出的一次性初始化码
              </label>
              <div className="relative">
                <KeyRound className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                <input
                  type="text"
                  required
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  placeholder="粘贴启动日志中的初始化码"
                  className="w-full pl-9 pr-3 py-2 text-sm font-mono border border-slate-300 rounded-lg focus:border-sky-500 focus:outline-none"
                />
              </div>
            </div>
          )}

          <div>
            <label className="block text-xs font-medium text-slate-700 mb-1">用户名</label>
            <div className="relative">
              <ShieldCheck className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
              <input
                type="text"
                required
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder={isSetup ? '例如 admin' : '输入用户名'}
                className="w-full pl-9 pr-3 py-2 text-sm border border-slate-300 rounded-lg focus:border-sky-500 focus:outline-none"
              />
            </div>
          </div>

          <div>
            <label className="block text-xs font-medium text-slate-700 mb-1">
              密码{isSetup ? '（至少 10 个字符）' : ''}
            </label>
            <div className="relative">
              <Lock className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
              <input
                type="password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="输入密码"
                className="w-full pl-9 pr-3 py-2 text-sm border border-slate-300 rounded-lg focus:border-sky-500 focus:outline-none"
              />
            </div>
          </div>

          {isSetup && (
            <div>
              <label className="block text-xs font-medium text-slate-700 mb-1">确认密码</label>
              <div className="relative">
                <Lock className="w-4 h-4 text-slate-400 absolute left-3 top-2.5" />
                <input
                  type="password"
                  required
                  value={confirmPwd}
                  onChange={(e) => setConfirmPwd(e.target.value)}
                  placeholder="再次输入密码"
                  className="w-full pl-9 pr-3 py-2 text-sm border border-slate-300 rounded-lg focus:border-sky-500 focus:outline-none"
                />
              </div>
            </div>
          )}

          <button
            type="submit"
            disabled={submitting}
            className="w-full py-2.5 rounded-lg bg-[#0B2A4A] hover:bg-[#123d69] text-white text-sm font-medium transition-colors disabled:opacity-50"
          >
            {submitting ? '正在提交...' : isSetup ? '完成初始化并进入系统' : '登录'}
          </button>

          <div className="text-[11px] text-slate-400 flex items-center justify-between pt-1">
            <span>版本：{status.version || 'dev'}</span>
            <span>传输保护：{status.tls ? 'HTTPS 已启用' : 'HTTP（仅本机直连安全）'}</span>
          </div>
        </form>
      </div>
    </div>
  )
}
