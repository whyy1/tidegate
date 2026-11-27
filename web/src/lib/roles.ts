import type { DefaultProjectAccess, ProjectAccessRole, UserRole } from './types'

export const GLOBAL_ROLE_META: Record<UserRole, { label: string; shortLabel: string; description: string; badgeClass: string }> = {
  admin: {
    label: '系统管理员',
    shortLabel: '管理员',
    description: '全站与用户治理权限',
    badgeClass: 'bg-rose-500/12 border-rose-400/40 text-rose-700',
  },
  member: {
    label: '协作编辑',
    shortLabel: '协作编辑',
    description: '读写接口与发起压测',
    badgeClass: 'bg-sky-500/12 border-sky-400/40 text-tidal',
  },
  viewer: {
    label: '全局只读',
    shortLabel: '只读',
    description: '仅查看与导出报告',
    badgeClass: 'bg-surface-subtle border-line-strong text-fg-muted',
  },
}

export const ROLE_BADGE_TONE: Record<UserRole | 'none', string> = {
  admin: GLOBAL_ROLE_META.admin.badgeClass,
  member: GLOBAL_ROLE_META.member.badgeClass,
  viewer: GLOBAL_ROLE_META.viewer.badgeClass,
  none: 'bg-rose-500/15 border-rose-400/45 text-rose-700',
}

export const PROJECT_ROLE_LABELS: Record<ProjectAccessRole, string> = {
  '': '继承全局',
  member: '读写执行',
  viewer: '只读',
  none: '禁止访问',
}

export function getGlobalRoleLabel(role: string, compact = false): string {
  const meta = GLOBAL_ROLE_META[role as UserRole]
  if (!meta) return role
  return compact ? meta.shortLabel : meta.label
}

export function getProjectRoleLabel(role: ProjectAccessRole | string, restricted = false): string {
  if (role === '') {
    return restricted ? '未授权' : PROJECT_ROLE_LABELS['']
  }
  return PROJECT_ROLE_LABELS[role as ProjectAccessRole] || role
}

/**
 * Mirrors internal/auth/acl.go ResolveProjectRole for instant client-side
 * preview before saving edits in GovernanceView or ProjectModal.
 */
export function computeEffectiveProjectAccess(
  globalRole: UserRole,
  defaultAccess: DefaultProjectAccess | undefined,
  restricted: boolean,
  overrideRole: ProjectAccessRole | undefined
): { effectiveRole: UserRole | ''; allowed: boolean; label: string } {
  if (globalRole === 'admin') {
    return { effectiveRole: 'admin', allowed: true, label: GLOBAL_ROLE_META.admin.shortLabel }
  }
  const ov = overrideRole || ''
  if (ov === 'none') {
    return { effectiveRole: '', allowed: false, label: '禁止访问' }
  }
  if (ov === 'member' || ov === 'viewer') {
    return {
      effectiveRole: ov,
      allowed: true,
      label: PROJECT_ROLE_LABELS[ov],
    }
  }
  if (restricted || defaultAccess === 'deny') {
    return { effectiveRole: '', allowed: false, label: '无访问权限' }
  }
  return {
    effectiveRole: globalRole,
    allowed: true,
    label: PROJECT_ROLE_LABELS[globalRole === 'member' ? 'member' : 'viewer'],
  }
}
