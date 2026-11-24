import { useEffect, type RefObject } from 'react'

export function prefersReducedMotion(): boolean {
  return typeof window !== 'undefined' && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
}

const TILT_BLOCK_SELECTOR =
  'input, textarea, select, [contenteditable="true"], .uplot, [role="separator"], pre, code'

/**
 * Tracks cursor position across all frosted cards inside `containerRef` so:
 * - Cursor spotlights illuminate hovered cards (`.tg-glass-card::before` / `.tg-accent-card::before`)
 * - Adjacent borders glow smoothly before entering (`.tg-glass-card::after` / `.tg-accent-card::after`)
 * - Precision `.tg-tilt-card` surfaces tilt subtly only while directly hovered outside form/chart controls.
 */
export function useGlassStagePhysics(containerRef: RefObject<HTMLElement | null>) {
  useEffect(() => {
    const root = containerRef.current
    if (!root) return

    let rafId = 0
    let lastX = -9999
    let lastY = -9999
    let activeTarget: EventTarget | null = null
    let cards: HTMLElement[] = Array.from(root.querySelectorAll<HTMLElement>('.tg-glass-card, .tg-accent-card'))
    const litCards = new WeakSet<HTMLElement>()

    const mo =
      typeof MutationObserver !== 'undefined'
        ? new MutationObserver(() => {
            cards = Array.from(root.querySelectorAll<HTMLElement>('.tg-glass-card, .tg-accent-card'))
          })
        : null
    mo?.observe(root, { childList: true, subtree: true, attributes: true, attributeFilter: ['class'] })

    const clearCards = () => {
      cards.forEach((el) => {
        if (litCards.has(el)) {
          el.style.setProperty('--mx', '-420px')
          el.style.setProperty('--my', '-420px')
          litCards.delete(el)
        }
        if (el.classList.contains('tg-tilt-card') && el.style.transform) {
          el.style.transform = ''
        }
      })
    }

    const flush = () => {
      rafId = 0
      const reduced = prefersReducedMotion()
      const blockTilt =
        activeTarget instanceof Element && Boolean(activeTarget.closest(TILT_BLOCK_SELECTOR))

      for (const el of cards) {
        if (el.offsetParent === null) continue
        const r = el.getBoundingClientRect()
        const x = lastX - r.left
        const y = lastY - r.top
        const near = x >= -240 && x <= r.width + 240 && y >= -240 && y <= r.height + 240
        if (near) {
          litCards.add(el)
          el.style.setProperty('--mx', `${x.toFixed(1)}px`)
          el.style.setProperty('--my', `${y.toFixed(1)}px`)
        } else if (litCards.has(el)) {
          litCards.delete(el)
          el.style.setProperty('--mx', '-420px')
          el.style.setProperty('--my', '-420px')
        }

        if (el.classList.contains('tg-tilt-card')) {
          const inside = x >= 0 && x <= r.width && y >= 0 && y <= r.height
          if (!reduced && inside && !blockTilt && r.width > 40 && r.height > 40) {
            const nx = (x / r.width - 0.5) * 2
            const ny = (y / r.height - 0.5) * 2
            const maxTilt = r.width > 560 ? 1.4 : 2.3
            const rx = (-ny * maxTilt).toFixed(2)
            const ry = (nx * maxTilt).toFixed(2)
            el.style.transform = `perspective(1100px) rotateX(${rx}deg) rotateY(${ry}deg)`
          } else if (el.style.transform) {
            el.style.transform = ''
          }
        }
      }
    }

    const onMove = (e: PointerEvent) => {
      if (e.pointerType === 'touch' || e.buttons !== 0) return
      lastX = e.clientX
      lastY = e.clientY
      activeTarget = e.target
      if (!rafId) rafId = window.requestAnimationFrame(flush)
    }

    const onLeave = () => {
      if (rafId) {
        window.cancelAnimationFrame(rafId)
        rafId = 0
      }
      clearCards()
    }

    root.addEventListener('pointermove', onMove, { passive: true })
    root.addEventListener('pointerleave', onLeave)
    return () => {
      mo?.disconnect()
      if (rafId) window.cancelAnimationFrame(rafId)
      root.removeEventListener('pointermove', onMove)
      root.removeEventListener('pointerleave', onLeave)
      clearCards()
    }
  }, [containerRef])
}

/**
 * Animates a newly mounted/resized element from a source DOMRect using transform + opacity only.
 */
export function flipFromRect(el: HTMLElement | null, fromRect: DOMRect | null) {
  if (!el || !fromRect || prefersReducedMotion() || typeof el.animate !== 'function') return
  const toRect = el.getBoundingClientRect()
  if (toRect.width < 10 || toRect.height < 10) return
  const dx = fromRect.left - toRect.left
  const dy = fromRect.top - toRect.top
  const sx = Math.max(0.2, Math.min(1.6, fromRect.width / toRect.width))
  const sy = Math.max(0.2, Math.min(1.6, fromRect.height / toRect.height))
  el.style.transformOrigin = 'top left'
  const anim = el.animate(
    [
      { transform: `translate(${dx}px, ${dy}px) scale(${sx}, ${sy})`, opacity: 0.72 },
      { transform: 'translate(0px, 0px) scale(1, 1)', opacity: 1 },
    ],
    { duration: 300, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' }
  )
  anim.onfinish = () => {
    el.style.transformOrigin = ''
  }
}
