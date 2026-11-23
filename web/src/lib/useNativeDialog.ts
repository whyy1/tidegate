import { useEffect, useRef } from 'react'

/**
 * Drive a native <dialog closedby="any"> from React state.
 * - open=true → showModal(); open=false → close()
 * - Esc / light-dismiss / form[method=dialog] all route through the native "close" event → onClose()
 * - Fallback light-dismiss for engines without `closedby` (pointer pressed AND released outside the box).
 *
 * Render: <dialog ref={ref} closedby="any" className="tg-dialog ..."> … </dialog>
 * Keep the <dialog> mounted; render heavy content conditionally on `open` if needed.
 */
export function useNativeDialog<T extends HTMLDialogElement = HTMLDialogElement>(open: boolean, onClose: () => void) {
  const ref = useRef<T | null>(null)
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose

  useEffect(() => {
    const dlg = ref.current
    if (!dlg) return
    if (open && !dlg.open) {
      try {
        dlg.showModal()
      } catch {
        dlg.setAttribute('open', '')
      }
    } else if (!open && dlg.open) {
      dlg.close()
    }
  }, [open])

  useEffect(() => {
    const dlg = ref.current
    if (!dlg) return
    const onCloseEv = () => onCloseRef.current()
    const supportsClosedBy = typeof HTMLDialogElement !== 'undefined' && 'closedBy' in HTMLDialogElement.prototype
    let pressedOutside = false
    const outside = (e: PointerEvent) => {
      if (e.target !== dlg) return false
      const r = dlg.getBoundingClientRect()
      return e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom
    }
    const onDown = (e: PointerEvent) => {
      pressedOutside = outside(e)
    }
    const onUp = (e: PointerEvent) => {
      if (!supportsClosedBy && pressedOutside && outside(e)) dlg.close()
      pressedOutside = false
    }
    dlg.addEventListener('close', onCloseEv)
    dlg.addEventListener('pointerdown', onDown)
    dlg.addEventListener('pointerup', onUp)
    return () => {
      dlg.removeEventListener('close', onCloseEv)
      dlg.removeEventListener('pointerdown', onDown)
      dlg.removeEventListener('pointerup', onUp)
    }
  }, [])

  return ref
}
