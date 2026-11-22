import type { WSEvent } from './types'

type Listener = (ev: WSEvent) => void
type StatusListener = (connected: boolean) => void

class TidegateWS {
  private ws: WebSocket | null = null
  private listeners = new Set<Listener>()
  private statusListeners = new Set<StatusListener>()
  private subscribedRunId = ''
  private enabled = false
  private reconnectTimer: number | null = null
  private backoffMs = 800
  connected = false

  start() {
    if (this.enabled) return
    this.enabled = true
    this.connect()
  }

  stop() {
    this.enabled = false
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    if (this.ws) {
      this.ws.close()
      this.ws = null
    }
    this.setConnected(false)
  }

  subscribe(runId: string) {
    this.subscribedRunId = runId
    if (this.ws && this.ws.readyState === WebSocket.OPEN && runId) {
      this.ws.send(JSON.stringify({ type: 'subscribe', run_id: runId }))
    }
  }

  unsubscribe() {
    this.subscribedRunId = ''
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: 'subscribe', run_id: '' }))
    }
  }

  onEvent(fn: Listener): () => void {
    this.listeners.add(fn)
    return () => {
      this.listeners.delete(fn)
    }
  }

  onStatus(fn: StatusListener): () => void {
    this.statusListeners.add(fn)
    fn(this.connected)
    return () => {
      this.statusListeners.delete(fn)
    }
  }

  private setConnected(val: boolean) {
    if (this.connected === val) return
    this.connected = val
    for (const fn of this.statusListeners) {
      fn(val)
    }
  }

  private connect() {
    if (!this.enabled) return
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) {
      return
    }
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${window.location.host}/api/v1/ws`
    try {
      const sock = new WebSocket(url)
      this.ws = sock
      sock.onopen = () => {
        this.backoffMs = 800
        this.setConnected(true)
        if (this.subscribedRunId) {
          sock.send(JSON.stringify({ type: 'subscribe', run_id: this.subscribedRunId }))
        }
      }
      sock.onmessage = (msg) => {
        try {
          const ev = JSON.parse(String(msg.data)) as WSEvent
          for (const fn of this.listeners) {
            fn(ev)
          }
        } catch {
          // ignore malformed messages
        }
      }
      sock.onerror = () => {
        // close event triggers reconnect
      }
      sock.onclose = () => {
        if (this.ws === sock) {
          this.ws = null
        }
        this.setConnected(false)
        if (this.enabled) {
          const wait = this.backoffMs
          this.backoffMs = Math.min(this.backoffMs * 1.5, 6000)
          this.reconnectTimer = window.setTimeout(() => {
            this.reconnectTimer = null
            this.connect()
          }, wait)
        }
      }
    } catch {
      this.setConnected(false)
    }
  }
}

export const wsClient = new TidegateWS()
