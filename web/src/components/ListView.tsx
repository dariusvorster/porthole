import { primaryIPv4 } from '../api/helpers'
import type { Container, StatsSample, Supervision } from '../api/types'
import { shortId } from '../lib/format'
import { StatusDot, type StatusKind } from './StatusDot'
import { SupervisionBadges } from './SupervisionBadges'

interface ListViewProps {
  containers: Container[]
  stats: Map<string, StatsSample>
  supervision: Map<string, Supervision>
}

function stateKind(state: string): StatusKind {
  if (state === 'running') return 'running'
  if (state === 'stopped') return 'stopped'
  return 'other'
}

function pct(n: number | undefined): string {
  return n == null ? '' : `${n.toFixed(1)}%`
}

// Running first, then alphabetical by id — stable so rows don't jump around.
function byRunningFirst(a: Container, b: Container): number {
  const rank = (c: Container) => (c.status.state === 'running' ? 0 : 1)
  return rank(a) - rank(b) || a.id.localeCompare(b.id)
}

const dash = <span className="text-neutral-400">—</span>

/**
 * Flat list view (spec §5.4 fallback / big-fleet escape hatch). A dense table
 * over the same store the topology uses. Rows key by id so stats ticks update
 * cells in place without re-mounting (no flicker).
 */
export function ListView({ containers, stats, supervision }: ListViewProps) {
  if (containers.length === 0) {
    return <div className="p-6 text-2xs text-neutral-400">no containers</div>
  }

  const rows = [...containers].sort(byRunningFirst)

  return (
    <table className="w-full border-collapse text-2xs">
      <thead>
        <tr className="border-b border-neutral-200/70 text-left text-[10px] uppercase tracking-wider text-neutral-400 dark:border-neutral-800/70">
          <th className="py-1.5 pr-3 font-medium">Name</th>
          <th className="py-1.5 pr-3 font-medium">Image</th>
          <th className="py-1.5 pr-3 font-medium">Status</th>
          <th className="py-1.5 pr-3 font-medium">IP</th>
          <th className="py-1.5 pr-3 text-right font-medium">CPU%</th>
          <th className="py-1.5 text-right font-medium">Mem%</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((c) => {
          const running = c.status.state === 'running'
          const sample = stats.get(c.id)
          const ip = primaryIPv4(c)
          const cpu = running ? pct(sample?.cpuPercent) : ''
          const mem = running ? pct(sample?.memoryPercent) : ''
          return (
            <tr
              key={c.id}
              className="border-b border-neutral-100 hover:bg-neutral-50 dark:border-neutral-900 dark:hover:bg-neutral-900/40"
            >
              <td
                className="py-1.5 pr-3 font-mono text-neutral-800 dark:text-neutral-200"
                title={c.id}
              >
                {shortId(c.id)}
              </td>
              <td className="py-1.5 pr-3 font-mono text-neutral-600 dark:text-neutral-400">
                <span className="block max-w-[32ch] truncate" title={c.configuration.image.reference}>
                  {c.configuration.image.reference}
                </span>
              </td>
              <td className="py-1.5 pr-3">
                <span className="flex items-center gap-1.5">
                  <StatusDot kind={stateKind(c.status.state)} />
                  <span className="text-neutral-700 dark:text-neutral-300">{c.status.state}</span>
                  <SupervisionBadges sup={supervision.get(c.id)} />
                </span>
              </td>
              <td className="py-1.5 pr-3 font-mono">{ip || dash}</td>
              <td className="py-1.5 pr-3 text-right font-mono tabular-nums">{cpu || dash}</td>
              <td className="py-1.5 text-right font-mono tabular-nums">{mem || dash}</td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}
