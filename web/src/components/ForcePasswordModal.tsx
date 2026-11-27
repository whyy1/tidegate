import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { Dices, Eye, EyeOff, KeyRound, LogOut, ShieldAlert } from 'lucide-react'
import { api, APIError } from '../lib/api'
import { getGlobalRoleLabel } from '../lib/roles'
import type { User } from '../lib/types'
import { useNativeDialog } from '../lib/useNativeDialog'

const MIN_PASSWORD = 10

function randomStrongPassword(length = 16): string {
  const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789-_.!@#%'
  const limit = 256 - (256 % alphabet.length)
  const out: string[] = []
  const buf = new Uint8Array(length * 2)
  while (out.length < length) {
    crypto.getRandomValues(buf)
    for (const b of buf) {
      if (b < limit) out.push(alphabet[b % alphabet.length])
      if (out.length === length) break
    }
  }
  return out.join('')
}

interface Props {
  open: boolean
  user: User
  onCompleted: () => Promise<void>
  onLogout: () => Promise<void>
  onToast: (kind: 'success' | 'error' | 'info', msg: string) => void
}

const inputCls =
  'w-full px-3 py-2 border border-line-strong rounded-[10px] bg-surface text-fg font-mono focus:border-sky-500 focus:outline-none'

export function ForcePasswordModal({ open, user, onCompleted, onLogout, onToast }: Props) {
  const titleId = useId()
  const ref = useNativeDialog(open, () => {
    // Non-dismissible while must_change_password is true.
  })
  const curRef = useRef<HTMLInputElement>(null)
  const [curPwd, setCurPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')
  const [confirmPwd, setConfirmPwd] = useState('')
  const [showNew, setShowNew] = useState(false)
  const [busy, setBusy] = useState(false)
  const [errMsg, setErrMsg] = useState('')

  useEffect(() => {
    if (!open) return
    setCurPwd('')
    setNewPwd('')
    setConfirmPwd('')
    setShowNew(false)
    setErrMsg('')
    curRef.current?.focus()
  }, [open])

  // Prevent Esc from closing the modal on engines without <dialog closedby="none">.
  useEffect(() => {
    const dlg = ref.current
    if (!dlg) return
    const onCancel = (e: Event) => {
      if (open) e.preventDefault()
    }
    dlg.addEventListener('cancel', onCancel)
    return () => dlg.removeEventListener('cancel', onCancel)
  }, [open, ref])

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!curPwd) {
      setErrMsg('请输入当前临时密码')
      return
    }
    if (newPwd.length < MIN_PASSWORD) {
      setErrMsg(`新密码至少需要 ${MIN_PASSWORD} 个字符`)
      return
    }
    if (newPwd !== confirmPwd) {
      setErrMsg('两次输入的新密码不一致')
      return
    }
    setBusy(true)
    setErrMsg('')
    try {
      await api.changePassword({ current_password: curPwd, new_password: newPwd })
      onToast('success', '密码已更新')
      await onCompleted()
    } catch (err) {
      setErrMsg(err instanceof APIError ? err.message : '修改密码失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <dialog
      ref={ref}
      {...({ closedby: 'none' } as Record<string, string>)}
      aria-labelledby={titleId}
      className="tg-dialog w-[min(28rem,calc(100vw-2rem))] max-h-[88dvh]"
    >
      {open && (
        <form onSubmit={handleSubmit} className="flex flex-col min-h-0 flex-1 text-xs">
          <header className="tg-dialog-header">
            <h1 id={titleId} className="text-sm font-semibold text-fg flex items-center gap-2">
              <ShieldAlert className="w-4 h-4 text-amber-500 shrink-0" />
              首次登录请修改密码
            </h1>
            <span className="px-2 py-0.5 rounded-full border border-line bg-surface text-[11px] font-mono text-fg-muted">
              {user.username} · {getGlobalRoleLabel(user.role, true)}
            </span>
          </header>

          <div className="flex-1 min-h-0 overflow-y-auto tg-scroller p-5 space-y-3.5">
            {errMsg && (
              <div role="alert" className="px-3 py-2 rounded-[10px] border border-rose-400/50 bg-rose-500/10 text-rose-700">
                {errMsg}
              </div>
            )}

            <input type="text" name="username" autoComplete="username" value={user.username} readOnly className="sr-only" tabIndex={-1} aria-hidden="true" />

            <div>
              <label htmlFor="force-cur-pwd" className="block font-medium text-fg-muted mb-1">
                当前临时密码
              </label>
              <input
                ref={curRef}
                id="force-cur-pwd"
                name="current-password"
                type="password"
                autoFocus
                required
                autoComplete="current-password"
                value={curPwd}
                onChange={(e) => setCurPwd(e.target.value)}
                placeholder="输入管理员提供的临时密码"
                className={inputCls}
              />
            </div>

            <div>
              <label htmlFor="force-new-pwd" className="block font-medium text-fg-muted mb-1">
                新密码（至少 {MIN_PASSWORD} 位）
              </label>
              <div className="flex items-stretch rounded-[10px] border border-line-strong bg-surface focus-within:border-sky-500">
                <input
                  id="force-new-pwd"
                  name="new-password"
                  type={showNew ? 'text' : 'password'}
                  required
                  minLength={MIN_PASSWORD}
                  autoComplete="new-password"
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  value={newPwd}
                  onChange={(e) => setNewPwd(e.target.value)}
                  className="flex-1 min-w-0 px-3 py-2 bg-transparent text-fg font-mono outline-none"
                />
                <button
                  type="button"
                  onClick={() => setShowNew((s) => !s)}
                  aria-pressed={showNew}
                  aria-label={showNew ? '隐藏新密码' : '显示新密码'}
                  className="px-2.5 text-fg-subtle hover:text-fg border-l border-line"
                >
                  {showNew ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                </button>
                <button
                  type="button"
                  onClick={() => {
                    const p = randomStrongPassword()
                    setNewPwd(p)
                    setConfirmPwd(p)
                    setShowNew(true)
                  }}
                  aria-label="生成随机强密码"
                  title="生成随机强密码"
                  className="px-2.5 text-fg-subtle hover:text-tidal border-l border-line"
                >
                  <Dices className="w-3.5 h-3.5" />
                </button>
              </div>
            </div>

            <div>
              <label htmlFor="force-confirm-pwd" className="block font-medium text-fg-muted mb-1">
                确认新密码
              </label>
              <input
                id="force-confirm-pwd"
                name="confirm-password"
                type={showNew ? 'text' : 'password'}
                required
                minLength={MIN_PASSWORD}
                autoComplete="new-password"
                autoCorrect="off"
                autoCapitalize="none"
                spellCheck={false}
                value={confirmPwd}
                onChange={(e) => setConfirmPwd(e.target.value)}
                aria-invalid={confirmPwd.length > 0 && confirmPwd !== newPwd}
                className={`${inputCls} ${
                  confirmPwd.length > 0 && confirmPwd !== newPwd ? '!border-rose-400' : ''
                }`}
              />
            </div>
          </div>

          <footer className="tg-dialog-footer justify-between">
            <button type="button" onClick={() => void onLogout()} className="tg-btn-danger">
              <LogOut className="w-3.5 h-3.5" /> 退出登录
            </button>
            <button type="submit" disabled={busy} className="tg-btn-primary">
              <KeyRound className="w-3.5 h-3.5" /> {busy ? '提交中…' : '确认修改密码'}
            </button>
          </footer>
        </form>
      )}
    </dialog>
  )
}
