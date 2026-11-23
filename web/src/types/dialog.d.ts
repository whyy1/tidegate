import 'react'

declare module 'react' {
  interface HTMLAttributes<T> {
    /** Native Popover API attribute: https://html.spec.whatwg.org/#attr-popover */
    popover?: '' | 'auto' | 'manual' | 'hint'
  }

  interface DialogHTMLAttributes<T> extends HTMLAttributes<T> {
    /** Native light-dismiss control: https://html.spec.whatwg.org/#attr-dialog-closedby */
    closedby?: 'any' | 'closerequest' | 'none'
  }
}
