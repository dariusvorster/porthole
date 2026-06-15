export type StatusKind = 'running' | 'stopped' | 'other'

const dotColor: Record<StatusKind, string> = {
  running: 'bg-status-running',
  stopped: 'bg-status-stopped',
  other: 'bg-status-warn',
}

/** Small status semaphore dot. Used inline next to a label/count. */
export function StatusDot({ kind }: { kind: StatusKind }) {
  return <span className={`inline-block h-1.5 w-1.5 rounded-full ${dotColor[kind]}`} />
}
