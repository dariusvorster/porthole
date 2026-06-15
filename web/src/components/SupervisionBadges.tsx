import type { Supervision } from '../api/types'

const healthRing: Record<string, string> = {
  healthy: 'border-status-running',
  unhealthy: 'border-status-danger',
  starting: 'border-status-warn',
}

/**
 * Compact supervision badges for a node/row. The health indicator is a hollow
 * RING — deliberately distinct from the filled run-state dot so "running" and
 * "healthy" never read as the same thing.
 *
 * Intent made visible (spec §3.1): a crash mid-backoff shows an attention badge
 * (amber/red), while a container the user stopped (desiredState=stopped) keeps
 * the quiet supervised marker — its gray run-state dot says "I stopped that."
 *
 * The `↻` marker is the PERSISTENT supervised indicator (PF3): ANY container with
 * a restart policy other than `no` shows it the instant the policy is set — a bare
 * `↻` means "supervised, will auto-restart"; `↻N` adds the CUMULATIVE lifetime
 * restart count (restartTotal, persisted — it does NOT vanish on stabilization).
 * Unsupervised containers show no `↻` — that distinction is the point.
 */
export function SupervisionBadges({ sup }: { sup: Supervision | undefined }) {
  if (!sup) return null

  const supervised = sup.policy === 'always' || sup.policy === 'unless-stopped'
  const backoffActive = !!sup.backoffUntil && Date.parse(sup.backoffUntil) > Date.now()
  const badges: React.ReactNode[] = []

  if (sup.health) {
    badges.push(
      <span
        key="health"
        title={`health: ${sup.health.state}${sup.health.failures > 0 ? ` (${sup.health.failures} fails)` : ''}`}
        className={`inline-block h-2 w-2 rounded-full border-[1.5px] bg-transparent ${healthRing[sup.health.state] ?? 'border-neutral-400'}`}
      />,
    )
  }

  if (sup.gaveUp) {
    badges.push(
      <span key="gaveup" className="rounded bg-status-danger/10 px-1 text-[9px] font-medium text-status-danger" title="supervision gave up restarting">
        gave up
      </span>,
    )
  } else if (backoffActive) {
    badges.push(
      <span key="backoff" className="rounded bg-status-warn/10 px-1 text-[9px] font-medium text-status-warn" title="crashed — backing off before restart">
        ↻{sup.restartTotal} backoff
      </span>,
    )
  } else if (supervised) {
    const n = sup.restartTotal
    badges.push(
      <span
        key="supervised"
        className="rounded bg-neutral-200/70 px-1 text-[9px] font-medium text-neutral-500 dark:bg-neutral-800/70"
        title={n > 0 ? `supervised (restart: ${sup.policy}) · restarted ${n}×` : `supervised (restart: ${sup.policy})`}
      >
        ↻{n > 0 ? n : ''}
      </span>,
    )
  }

  if (badges.length === 0) return null
  return <span className="flex items-center gap-1">{badges}</span>
}
