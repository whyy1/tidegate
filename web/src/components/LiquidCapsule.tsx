import { useEffect, useLayoutEffect, useRef, type RefObject } from 'react'
import { prefersReducedMotion } from '../lib/glassPhysics'

interface Props {
  containerRef: RefObject<HTMLElement | null>
  activeId: string
  orientation?: 'horizontal' | 'vertical'
  tone?: 'light' | 'dark'
  className?: string
}

/**
 * Spring-physics glass highlight capsule that tracks the active `[data-liq-id]` element
 * inside `containerRef`, elongating gently along its travel axis before rebounding cleanly.
 */
export function LiquidCapsule({
  containerRef,
  activeId,
  orientation = 'horizontal',
  tone = 'light',
  className = '',
}: Props) {
  const pillRef = useRef<HTMLDivElement | null>(null)
  const stateRef = useRef({
    ready: false,
    x: 0,
    y: 0,
    w: 0,
    h: 0,
    tx: 0,
    ty: 0,
    tw: 0,
    th: 0,
    vx: 0,
    vy: 0,
    raf: 0,
  })

  const measureAndKick = (instant = false) => {
    const container = containerRef.current
    const pill = pillRef.current
    if (!container || !pill || !activeId) return

    const item = container.querySelector<HTMLElement>(`[data-liq-id="${CSS.escape(activeId)}"]`)
    if (!item || item.offsetParent === null) {
      pill.style.opacity = '0'
      return
    }

    const cr = container.getBoundingClientRect()
    const ir = item.getBoundingClientRect()
    const tx = ir.left - cr.left + container.scrollLeft
    const ty = ir.top - cr.top + container.scrollTop
    const tw = ir.width
    const th = ir.height
    const st = stateRef.current

    st.tx = tx
    st.ty = ty
    st.tw = tw
    st.th = th
    pill.style.opacity = '1'

    if (instant || !st.ready || prefersReducedMotion()) {
      st.ready = true
      st.x = tx
      st.y = ty
      st.w = tw
      st.h = th
      st.vx = 0
      st.vy = 0
      pill.style.width = `${tw.toFixed(1)}px`
      pill.style.height = `${th.toFixed(1)}px`
      pill.style.transform = `translate3d(${tx.toFixed(2)}px, ${ty.toFixed(2)}px, 0) scale(1, 1)`
      return
    }

    if (!st.raf) {
      const step = () => {
        const s = stateRef.current
        const dx = s.tx - s.x
        const dy = s.ty - s.y
        const dw = s.tw - s.w
        const dh = s.th - s.h

        s.vx = (s.vx + dx * 0.24) * 0.72
        s.vy = (s.vy + dy * 0.24) * 0.72
        s.x += s.vx
        s.y += s.vy
        s.w += dw * 0.26
        s.h += dh * 0.26

        const stretchX =
          orientation === 'horizontal' ? Math.min(0.28, Math.abs(s.vx) * 0.013) : -Math.min(0.08, Math.abs(s.vy) * 0.005)
        const stretchY =
          orientation === 'vertical' ? Math.min(0.30, Math.abs(s.vy) * 0.015) : -Math.min(0.11, Math.abs(s.vx) * 0.005)

        if (pillRef.current) {
          pillRef.current.style.width = `${s.w.toFixed(1)}px`
          pillRef.current.style.height = `${s.h.toFixed(1)}px`
          pillRef.current.style.transform = `translate3d(${s.x.toFixed(2)}px, ${s.y.toFixed(2)}px, 0) scale(${(1 + stretchX).toFixed(3)}, ${(1 + stretchY).toFixed(3)})`
        }

        const settled =
          Math.abs(dx) < 0.3 &&
          Math.abs(dy) < 0.3 &&
          Math.abs(dw) < 0.3 &&
          Math.abs(dh) < 0.3 &&
          Math.abs(s.vx) < 0.15 &&
          Math.abs(s.vy) < 0.15

        if (!settled) {
          s.raf = window.requestAnimationFrame(step)
        } else {
          s.raf = 0
          s.x = s.tx
          s.y = s.ty
          s.w = s.tw
          s.h = s.th
          s.vx = 0
          s.vy = 0
          if (pillRef.current) {
            pillRef.current.style.width = `${s.tw.toFixed(1)}px`
            pillRef.current.style.height = `${s.th.toFixed(1)}px`
            pillRef.current.style.transform = `translate3d(${s.tx.toFixed(2)}px, ${s.ty.toFixed(2)}px, 0) scale(1, 1)`
          }
        }
      }
      st.raf = window.requestAnimationFrame(step)
    }
  }

  useLayoutEffect(() => {
    measureAndKick(false)
    // Re-run once fonts/children settle on initial mount
    const t = window.requestAnimationFrame(() => measureAndKick(!stateRef.current.ready))
    return () => window.cancelAnimationFrame(t)
  })

  useEffect(() => {
    const container = containerRef.current
    if (!container || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => measureAndKick(true))
    ro.observe(container)
    return () => {
      ro.disconnect()
      if (stateRef.current.raf) {
        window.cancelAnimationFrame(stateRef.current.raf)
        stateRef.current.raf = 0
      }
    }
  }, [containerRef])

  const toneCls =
    tone === 'dark'
      ? 'bg-sky-400/18 border border-sky-300/35 shadow-[0_6px_18px_-4px_rgb(56_189_248/0.35),inset_0_1px_0_rgb(255_255_255/0.18)]'
      : 'bg-white/85 dark:bg-sky-400/16 border border-sky-400/30 shadow-[0_6px_18px_-5px_rgb(2_132_199/0.24),inset_0_1px_0_var(--tg-specular)] backdrop-blur-md'

  return (
    <div
      ref={pillRef}
      aria-hidden="true"
      className={`pointer-events-none absolute top-0 left-0 rounded-xl opacity-0 ${toneCls} ${className}`}
      style={{ transformOrigin: 'center center', willChange: 'transform, width, height' }}
    />
  )
}
