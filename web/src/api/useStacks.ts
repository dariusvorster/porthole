import { useCallback, useEffect, useState } from 'react'
import { listStacks } from './rest'
import type { StackView } from './types'

/**
 * Fetches the stored-stack list (each carries live status + members, computed
 * server-side at fetch time). Refetches when:
 *   - `enabled` (apiServerRunning) flips up,
 *   - `epoch` changes — the stream bumps it on every `stack` SSE event, so a
 *     mutation's result lands without polling,
 *   - `refresh()` is called — used after import/delete, which emit no SSE event.
 */
export function useStacks(
  enabled: boolean,
  epoch: number,
): { stacks: StackView[]; refresh: () => void } {
  const [stacks, setStacks] = useState<StackView[]>([])
  const [tick, setTick] = useState(0)
  const refresh = useCallback(() => setTick((t) => t + 1), [])

  useEffect(() => {
    if (!enabled) {
      setStacks([])
      return
    }
    let cancelled = false
    listStacks()
      .then((s) => {
        if (!cancelled) setStacks(s ?? [])
      })
      .catch(() => {
        /* transient; keep the last good list */
      })
    return () => {
      cancelled = true
    }
  }, [enabled, epoch, tick])

  return { stacks, refresh }
}
