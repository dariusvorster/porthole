// A tiny module-level toast bus so any feature (create, stacks, resources) emits
// success toasts through ONE component (the <Toaster>), with consistent style /
// position / duration — no prop-drilling, no per-view toast (PF4).

type Listener = (msg: string) => void

let listeners: Listener[] = []

/** Show a success toast. Safe to call from anywhere. */
export function showToast(msg: string): void {
  for (const l of listeners) l(msg)
}

/** Subscribe to toasts; returns an unsubscribe fn (for the Toaster's effect). */
export function subscribeToasts(cb: Listener): () => void {
  listeners.push(cb)
  return () => {
    listeners = listeners.filter((l) => l !== cb)
  }
}
