export type Action = 'start' | 'stop' | 'restart' | 'kill' | 'delete'

/** Typed error surfaced from a failed mutation (mirrors the engine envelope). */
export interface ActionErrorBody {
  kind: string
  message: string
  raw?: string
}

/**
 * Per-container optimistic action state. `pending` is shown the instant a button
 * is clicked (the reconcile loop lags 6–8s); `error` survives a failed request
 * until dismissed or retried.
 */
export type ActionState =
  | { phase: 'pending'; action: Action; label: string }
  | { phase: 'error'; action: Action; error: ActionErrorBody }

export const ACTION_LABEL: Record<Action, string> = {
  start: 'starting…',
  stop: 'stopping…',
  restart: 'restarting…',
  kill: 'killing…',
  delete: 'deleting…',
}

/** Actions that require a confirm dialog (spec §8 gap 7). */
export const DESTRUCTIVE: Record<Action, boolean> = {
  start: false,
  stop: false,
  restart: false,
  kill: true,
  delete: true,
}
