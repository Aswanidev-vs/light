import { Events } from '@wailsio/runtime'

// Subscribe to a backend event, normalizing the payload regardless of whether
// the runtime wraps it as { data } or passes it directly.
export function listen(name: string, handler: (payload: any) => void) {
  Events.On(name, (e: any) => {
    const payload = e && typeof e === 'object' && 'data' in e ? e.data : e
    handler(payload)
  })
}
