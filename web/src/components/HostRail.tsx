import { formatBytes } from '../lib/format'
import { StatusDot, type StatusKind } from './StatusDot'

interface HostRailProps {
  version: string
  running: number
  stopped: number
  other: number
  diskUsed: number
}

function Section({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <section className="mb-4 last:mb-0">
      <div className="mb-1.5 text-[10px] uppercase tracking-wider text-neutral-400">{label}</div>
      <div className="space-y-1">{children}</div>
    </section>
  )
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex items-baseline justify-between gap-2">
      <span className="text-neutral-500">{k}</span>
      <span className="tabular-nums text-neutral-800 dark:text-neutral-200">{v}</span>
    </div>
  )
}

function CountRow({ kind, label, n }: { kind: StatusKind; label: string; n: number }) {
  return (
    <div className="flex items-center justify-between gap-2">
      <span className="flex items-center gap-1.5 text-neutral-500">
        <StatusDot kind={kind} />
        {label}
      </span>
      <span className="tabular-nums text-neutral-800 dark:text-neutral-200">{n}</span>
    </div>
  )
}

/**
 * Persistent left host-telemetry rail (spec §5.4): runtime version, live
 * container counts by state, and disk used. Mono + compact.
 */
export function HostRail({ version, running, stopped, other, diskUsed }: HostRailProps) {
  return (
    <aside className="w-44 shrink-0 overflow-y-auto border-r border-neutral-200/70 p-3 font-mono text-2xs dark:border-neutral-800/70">
      <Section label="runtime">
        <Row k="version" v={version || '—'} />
      </Section>
      <Section label="containers">
        <CountRow kind="running" label="running" n={running} />
        <CountRow kind="stopped" label="stopped" n={stopped} />
        <CountRow kind="other" label="other" n={other} />
      </Section>
      <Section label="disk">
        <Row k="used" v={formatBytes(diskUsed)} />
      </Section>
    </aside>
  )
}
