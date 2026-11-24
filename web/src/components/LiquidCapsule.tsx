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
  const propsRef = useRef({ activeId, orientation })
  propsRef.current = { activeId, orientation }

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
    const { activeId: curId, orientation: curOrient } = propsRef.current
    if (!container || !pill || !curId) return

    const item = container.querySelector<HTMLElement>(`[data-liq-id="${CSS.escape(curId)}"]`)
    if (!item || item.offsetParent === null) {
      pill.style.opacity = '0'
      return
    }

    const cr = container.getBoundingClientRect()
    const ir = item.getBoundingClientRect()
    const tx = ir.left - cr.left + container.scrollLeft
    const ty = ir.top - cr.top + container.scrollTop
    const tw = Math.max(1, ir.width)
    const th = Math.max(1, ir.height)
    const st = stateRef.current

    const sameTarget =
      Math.abs(tx - st.tx) < 0.5 &&
      Math.abs(ty - st.ty) < 0.5 &&
      Math.abs(tw - st.tw) < 0.5 &&
      Math.abs(th - st.th) < 0.5

    st.tx = tx
    st.ty = ty
    st.tw = tw
    st.th = th
    pill.style.opacity = '1'

    if (sameTarget && (st.ready || st.raf)) {
      return
    }

    // Set base dimensions once per transition; animate solely via compositor `transform`.
    pill.style.width = `${tw.toFixed(1)}px`
    pill.style.height = `${th.toFixed(1)}px`

    if (instant || !st.ready || prefersReducedMotion()) {
      if (st.raf) {
        window.cancelAnimationFrame(st.raf)
        st.raf = 0
      }
      st.ready = true
      st.x = tx
      st.y = ty
      st.w = tw
      st.h = th
      st.vx = 0
      st.vy = 0
      pill.style.transform = `translate3d(${tx.toFixed(2)}px, ${ty.toFixed(2)}px, 0) scale(1, 1)`
      return
    }

    if (!st.raf) {
      const step = () => {
        const s = stateRef.current
        const orient = propsRef.current.orientation
        const dx = s.tx - s.x
        const dy = s.ty - s.y
        const dw = s.tw - s.w
        const dh = s.th - s.h

        s.vx = (s.vx + dx * 0.28) * 0.7
        s.vy = (s.vy + dy * 0.28) * 0.7
        s.x += s.vx
        s.y += s.vy
        s.w += dw * 0.3
        s.h += dh * 0.3

        const stretchX =
          orient === 'horizontal' ? Math.min(0.24, Math.abs(s.vx) * 0.012) : -Math.min(0.06, Math.abs(s.vy) * 0.004)
        const stretchY =
          orient === 'vertical' ? Math.min(0.26, Math.abs(s.vy) * 0.013) : -Math.min(0.08, Math.abs(s.vx) * 0.004)

        if (pillRef.current && s.tw > 0 && s.th > 0) {
          const sx = (s.w / s.tw) * (1 + stretchX)
          const sy = (s.h / s.th) * (1 + stretchY)
          const px = s.x - (s.w * stretchX) * 0.5
          const py = s.y - (s.h * stretchY) * 0.5
          pillRef.current.style.transform = `translate3d(${px.toFixed(2)}px, ${py.toFixed(2)}px, 0) scale(${sx.toFixed(3)}, ${sy.toFixed(3)})`
        }

        const settled =
          Math.abs(dx) < 0.35 &&
          Math.abs(dy) < 0.35 &&
          Math.abs(dw) < 0.35 &&
          Math.abs(dh) < 0.35 &&
          Math.abs(s.vx) < 0.18 &&
          Math.abs(s.vy) < 0.18

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
    void curOrient
  }

  useLayoutEffect(() => {
    measureAndKick(false)
    const t = window.requestAnimationFrame(() => measureAndKick(!stateRef.current.ready))
    return () => window.cancelAnimationFrame(t)
  }, [activeId, orientation])

  useEffect(() => {
    const container = containerRef.current
    if (!container || typeof ResizeObserver === 'undefined') return
    let mounted = false
    const ro = new ResizeObserver(() => {
      if (!mounted) {
        mounted = true
        return
      }
      measureAndKick(!stateRef.current.raf)
    })
    ro.observe(container)
    const activeEl = propsRef.current.activeId
      ? container.querySelector<HTMLElement>(`[data-liq-id="${CSS.escape(propsRef.current.activeId)}"]`)
      : null
    if (activeEl) ro.observe(activeEl)
    return () => {
      ro.disconnect()
      if (stateRef.current.raf) {
        window.cancelAnimationFrame(stateRef.current.raf)
        stateRef.current.raf = 0
      }
    }
  }, [containerRef, activeId])

  const toneCls =
    tone === 'dark'
      ? 'bg-sky-400/18 border border-sky-300/35 shadow-[0_6px_18px_-4px_rgb(56_189_248/0.35),inset_0_1px_0_rgb(255_255_255/0.18)]'
      : 'bg-white/90 dark:bg-sky-400/18 border border-sky-400/30 shadow-[0_6px_18px_-5px_rgb(2_132_199/0.24),inset_0_1px_0_var(--tg-specular)]'

  return (
    <div
      ref={pillRef}
      aria-hidden="true"
      className={`pointer-events-none absolute top-0 left-0 rounded-xl opacity-0 ${toneCls} ${className}`}
      style={{ transformOrigin: 'top left', willChange: 'transform' }}
    />
  )
}
