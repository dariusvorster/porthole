import type {
  Container,
  ContainerEvent,
  DiskUsage,
  RemovedEvent,
  SnapshotEvent,
  StackEvent,
  StatsEvent,
  StatsSample,
  Supervision,
  SystemEvent,
} from './types'

/**
 * The normalized live store maintained from the single SSE stream. Lists key by
 * container id; stats are kept in a separate map so a stats tick never
 * re-creates container entries (the no-flicker guarantee).
 */
export interface StreamState {
  containers: Map<string, Container>
  stats: Map<string, StatsSample>
  supervision: Map<string, Supervision>
  diskUsage: DiskUsage | null
  apiServerRunning: boolean
  /** EventSource open/closed — distinct from apiServerRunning (the daemon). */
  connected: boolean
  /**
   * Bumped on each `stack` SSE event (after a stack mutation). The Stacks view
   * depends on it to refetch the stack list — a lightweight live nudge that
   * avoids threading per-stack member state through the store.
   */
  stackEpoch: number
  /** Bumped on each `resource` SSE event — the Resources view refetches on it. */
  resourceEpoch: number
}

/**
 * A reducible event. The SSE `name` maps 1:1 to the stream's named events;
 * `connected` is the only non-SSE member (a transport signal the hook injects).
 */
export type StreamEvent =
  | { name: 'snapshot'; data: SnapshotEvent }
  | { name: 'container.upserted'; data: ContainerEvent }
  | { name: 'container.removed'; data: RemovedEvent }
  | { name: 'stats'; data: StatsEvent }
  | { name: 'df'; data: DiskUsage }
  | { name: 'system'; data: SystemEvent }
  | { name: 'supervision'; data: Supervision }
  | { name: 'stack'; data: StackEvent }
  | { name: 'resource'; data: unknown }
  | { name: 'connected'; data: boolean }

export const initialState: StreamState = {
  containers: new Map(),
  stats: new Map(),
  supervision: new Map(),
  diskUsage: null,
  apiServerRunning: true,
  connected: false,
  stackEpoch: 0,
  resourceEpoch: 0,
}

/**
 * Pure store reducer. Exported (and unit-tested in reduce.test.ts) so the
 * snapshot-reseed and no-flicker guarantees can be verified deterministically,
 * independent of a live EventSource.
 */
export function reduce(state: StreamState, event: StreamEvent): StreamState {
  switch (event.name) {
    case 'snapshot': {
      // Authoritative reset of the store (also fired after reconnect): rebuild
      // the container map from scratch so anything absent from the snapshot is
      // gone — no ghosts.
      const containers = new Map<string, Container>()
      for (const c of event.data.containers) containers.set(c.id, c)
      return {
        ...state,
        containers,
        // Reseed supervision on (re)connect just like containers: the snapshot
        // carries no supervision data, so clear stale badges and let the
        // every-cycle `supervision` emits refill within a poll.
        supervision: new Map(),
        diskUsage: event.data.diskUsage,
        apiServerRunning: event.data.apiServerRunning,
      }
    }
    case 'container.upserted': {
      const containers = new Map(state.containers)
      containers.set(event.data.container.id, event.data.container)
      return { ...state, containers }
    }
    case 'container.removed': {
      const containers = new Map(state.containers)
      containers.delete(event.data.id)
      const stats = new Map(state.stats)
      stats.delete(event.data.id)
      const supervision = new Map(state.supervision)
      supervision.delete(event.data.id)
      return { ...state, containers, stats, supervision }
    }
    case 'stats': {
      // Copy ONLY the stats map; container entries keep referential identity.
      const stats = new Map(state.stats)
      for (const s of event.data.samples) stats.set(s.id, s)
      return { ...state, stats }
    }
    case 'supervision': {
      const supervision = new Map(state.supervision)
      supervision.set(event.data.id, event.data)
      return { ...state, supervision }
    }
    case 'stack':
      // A stack changed (up/down/restart). Bump the epoch so the Stacks view
      // refetches; the per-stack status/members come from that REST fetch.
      return { ...state, stackEpoch: state.stackEpoch + 1 }
    case 'resource':
      // A resource changed (prune/delete/pull). Bump so the Resources view refetches.
      return { ...state, resourceEpoch: state.resourceEpoch + 1 }
    case 'df':
      return { ...state, diskUsage: event.data }
    case 'system':
      return { ...state, apiServerRunning: event.data.apiServerRunning }
    case 'connected':
      return { ...state, connected: event.data }
  }
}
