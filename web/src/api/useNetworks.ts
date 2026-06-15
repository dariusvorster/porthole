import { useEffect, useState } from 'react'
import { getNetworks } from './rest'
import type { Network } from './types'

/**
 * Fetches the network list from REST. Networks aren't part of the SSE stream
 * (the reconcile loop only streams containers/stats/df/system), and they change
 * rarely, so we fetch when `enabled` (apiServerRunning), refetch when it flips
 * back up, AND when `epoch` changes — the `resource` SSE nudge after a create /
 * stack up-down (PF1), so a stack's new network appears live, not only via
 * member grouping. Container membership stays live via the store.
 */
export function useNetworks(enabled: boolean, epoch = 0): Network[] {
  const [networks, setNetworks] = useState<Network[]>([])

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    getNetworks()
      .then((n) => {
        if (!cancelled) setNetworks(n)
      })
      .catch(() => {
        /* transient; cards still derive from live container membership */
      })
    return () => {
      cancelled = true
    }
  }, [enabled, epoch])

  return networks
}
