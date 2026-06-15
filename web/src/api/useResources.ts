import { useCallback, useEffect, useState } from 'react'
import { getResources } from './rest'
import type { ResourceBundle } from './types'

/**
 * Fetches the annotated resource bundle (images/volumes/networks + df summary).
 * Refetches on enable, on `epoch` (the `resource` SSE nudge after a mutation),
 * and on `refresh()`. Same pattern as useStacks.
 */
export function useResources(
  enabled: boolean,
  epoch: number,
): { bundle: ResourceBundle | null; refresh: () => void } {
  const [bundle, setBundle] = useState<ResourceBundle | null>(null)
  const [tick, setTick] = useState(0)
  const refresh = useCallback(() => setTick((t) => t + 1), [])

  useEffect(() => {
    if (!enabled) {
      setBundle(null)
      return
    }
    let cancelled = false
    getResources()
      .then((b) => {
        if (!cancelled) setBundle(b)
      })
      .catch(() => {
        /* transient; keep the last good bundle */
      })
    return () => {
      cancelled = true
    }
  }, [enabled, epoch, tick])

  return { bundle, refresh }
}
