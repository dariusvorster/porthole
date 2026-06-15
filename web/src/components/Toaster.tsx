import { useEffect, useState } from 'react'
import { subscribeToasts } from '../lib/toast'

interface Toast {
  id: number
  msg: string
}

let nextId = 1

/**
 * Renders success toasts from the toast bus — one consistent component, bottom-
 * right, auto-dismiss. Mounted once in App; create / stack / resources all feed
 * it via showToast (PF4: unify the previously per-view Resources toast).
 */
export function Toaster() {
  const [toasts, setToasts] = useState<Toast[]>([])

  useEffect(
    () =>
      subscribeToasts((msg) => {
        const id = nextId++
        setToasts((ts) => [...ts, { id, msg }])
        window.setTimeout(() => setToasts((ts) => ts.filter((t) => t.id !== id)), 3500)
      }),
    [],
  )

  if (toasts.length === 0) return null
  return (
    <div className="fixed bottom-3 right-3 z-[60] flex flex-col gap-1.5">
      {toasts.map((t) => (
        <div
          key={t.id}
          data-testid="toast"
          className="rounded border-hairline border-status-running/40 bg-status-running/10 px-3 py-1.5 font-mono text-2xs text-status-running shadow-sm"
        >
          {t.msg}
        </div>
      ))}
    </div>
  )
}
