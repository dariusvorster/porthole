import { useEffect, useState } from 'react'
import { cliVersion, getVersion } from './rest'

/**
 * Fetches the `container` CLI version from REST. Only attempts while `enabled`
 * (apiServerRunning) is true — the endpoint isn't meaningful when the daemon is
 * down — and refetches when it flips back up.
 */
export function useSystemVersion(enabled: boolean): string {
  const [version, setVersion] = useState('')

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    getVersion()
      .then((entries) => {
        if (!cancelled) setVersion(cliVersion(entries))
      })
      .catch(() => {
        /* transient; the stream's `system` event governs the blocking state */
      })
    return () => {
      cancelled = true
    }
  }, [enabled])

  return version
}
