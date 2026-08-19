import type { ReviewEvent } from './adminApi'

type AdminEventListener = (event: ReviewEvent) => void

const listeners = new Set<AdminEventListener>()
let source: EventSource | null = null

const ensureSource = () => {
  if (source || typeof EventSource === 'undefined') return
  source = new EventSource('/api/v1/admin/events', { withCredentials: true })
  source.addEventListener('review', message => {
    try {
      const event = JSON.parse((message as MessageEvent<string>).data) as ReviewEvent
      listeners.forEach(listener => listener(event))
    } catch {
      // Ignore malformed events; EventSource keeps the connection alive.
    }
  })
  source.onerror = () => {
    // Native EventSource reconnects automatically with the same credentials.
  }
}

export const subscribeAdminEvents = (listener: AdminEventListener) => {
  listeners.add(listener)
  ensureSource()
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0 && source) {
      source.close()
      source = null
    }
  }
}
