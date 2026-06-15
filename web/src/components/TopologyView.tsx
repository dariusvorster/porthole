import type { ActionState } from '../api/actions'
import { isRunning, primaryIPv4 } from '../api/helpers'
import type { Container, Network, Supervision } from '../api/types'
import { applyFilter } from '../lib/filter'
import { shortId } from '../lib/format'
import { StatusDot, type StatusKind } from './StatusDot'
import { SupervisionBadges } from './SupervisionBadges'

interface TopologyViewProps {
  containers: Container[]
  networks: Network[]
  actions: Map<string, ActionState>
  supervision: Map<string, Supervision>
  selectedId: string | null
  onSelect: (id: string) => void
  /** Active filter query (FILT2). Keeps the graph coherent: networks with no
   *  matching member are hidden; matching members show within their network. */
  query: string
}

function stateKind(state: string): StatusKind {
  if (state === 'running') return 'running'
  if (state === 'stopped') return 'stopped'
  return 'other'
}

function byRunningFirst(a: Container, b: Container): number {
  const rank = (c: Container) => (c.status.state === 'running' ? 0 : 1)
  return rank(a) - rank(b) || a.id.localeCompare(b.id)
}

interface Route {
  container: string
  hostPort?: number
  containerPort?: number
  proto?: string
}

/** One selectable container node: name + dedicated IP. Stopped = dimmed, no IP.
 * While a mutation is pending it shows the optimistic label (e.g. "stopping…"). */
function Node({
  c,
  selected,
  pending,
  sup,
  onSelect,
}: {
  c: Container
  selected: boolean
  pending: string | null
  sup: Supervision | undefined
  onSelect: (id: string) => void
}) {
  const ip = primaryIPv4(c)
  const running = isRunning(c)
  return (
    <button
      type="button"
      onClick={() => onSelect(c.id)}
      className={[
        'flex w-full flex-col items-start gap-0.5 rounded border-hairline px-2 py-1.5 text-left transition',
        selected
          ? 'border-status-running/60 bg-status-running/5 ring-1 ring-status-running/40'
          : 'border-neutral-300/70 hover:bg-neutral-50 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40',
        running || pending ? '' : 'opacity-50',
      ].join(' ')}
    >
      <span className="flex w-full items-center gap-1.5 font-mono text-2xs font-medium" title={c.id}>
        <StatusDot kind={stateKind(c.status.state)} />
        {shortId(c.id)}
        <span className="ml-auto">
          <SupervisionBadges sup={sup} />
        </span>
      </span>
      {pending ? (
        <span className="font-mono text-2xs italic text-status-warn">{pending}</span>
      ) : (
        <span className="font-mono text-2xs text-neutral-500">{ip || '—'}</span>
      )}
    </button>
  )
}

const LABEL_STACK = 'porthole.stack'

/**
 * Member nodes inside a network card, framed by their stack (porthole.stack
 * label): each stack draws a labelled boundary around its services; standalone
 * containers render loose below. Each node shows its live IP via <Node>.
 */
function MemberNodes({
  members,
  actions,
  supervision,
  selectedId,
  onSelect,
}: {
  members: Container[]
  actions: Map<string, ActionState>
  supervision: Map<string, Supervision>
  selectedId: string | null
  onSelect: (id: string) => void
}) {
  if (members.length === 0) {
    return <div className="font-mono text-2xs text-neutral-400">no containers</div>
  }
  const stacks = new Map<string, Container[]>()
  const stackOrder: string[] = []
  const loose: Container[] = []
  for (const c of members) {
    const s = c.configuration.labels?.[LABEL_STACK]
    if (s) {
      if (!stacks.has(s)) {
        stacks.set(s, [])
        stackOrder.push(s)
      }
      stacks.get(s)!.push(c)
    } else {
      loose.push(c)
    }
  }
  stackOrder.sort((a, b) => a.localeCompare(b))

  const node = (c: Container) => {
    const st = actions.get(c.id)
    return (
      <Node
        key={c.id}
        c={c}
        selected={c.id === selectedId}
        pending={st?.phase === 'pending' ? st.label : null}
        sup={supervision.get(c.id)}
        onSelect={onSelect}
      />
    )
  }

  return (
    <div className="space-y-1.5">
      {stackOrder.map((name) => (
        <div
          key={name}
          className="rounded border-hairline border-status-running/30 bg-status-running/5 p-1"
          data-testid={`stack-frame-${name}`}
        >
          <div className="mb-1 px-0.5 font-mono text-2xs font-semibold text-neutral-500">▦ {name}</div>
          <div className="space-y-1.5">{stacks.get(name)!.map(node)}</div>
        </div>
      ))}
      {loose.map(node)}
    </div>
  )
}

/**
 * Topology-first home (spec §5.4): host (with published-port routes) → one card
 * per network (name + subnet) → member container nodes. Grouping uses declared
 * membership (configuration.networks[].network) so stopped containers — which
 * carry no runtime status.networks — still appear under their network.
 */
export function TopologyView({
  containers,
  networks,
  actions,
  supervision,
  selectedId,
  onSelect,
  query,
}: TopologyViewProps) {
  // Subnet lookup from the (REST) network list.
  const subnetByName = new Map(networks.map((n) => [n.configuration.name, n.status.ipv4Subnet]))

  // Filter: group only MATCHING containers by membership. A network with no
  // matching member simply gets no card (FILT2 — never a container without its
  // network, never a dangling edge). When the query is empty, applyFilter passes
  // everything AND we union the known networks so empty networks still render.
  const filtering = query.trim() !== ''
  const visible = applyFilter(containers, query)

  const groups = new Map<string, Container[]>()
  const order: string[] = []
  const ensure = (name: string) => {
    if (!groups.has(name)) {
      groups.set(name, [])
      order.push(name)
    }
    return groups.get(name)!
  }
  for (const c of visible) {
    const memberships = c.configuration.networks.length
      ? c.configuration.networks.map((n) => n.network)
      : ['(no network)']
    for (const net of memberships) ensure(net).push(c)
  }
  if (!filtering) for (const n of networks) ensure(n.configuration.name)
  order.sort((a, b) => a.localeCompare(b))

  // Published-port routes annotated on the host (matching containers only).
  const routes: Route[] = visible.flatMap((c) =>
    (c.configuration.publishedPorts ?? []).map((p) => ({
      container: c.id,
      hostPort: p.hostPort,
      containerPort: p.containerPort,
      proto: p.proto,
    })),
  )

  return (
    <div className="flex flex-col items-center p-4">
      {/* Host */}
      <div className="rounded border-hairline border-neutral-300/70 bg-white/60 px-3 py-2 dark:border-neutral-700/70 dark:bg-neutral-900/50">
        <div className="font-mono text-2xs font-semibold">host · this Mac</div>
        <div className="mt-1 space-y-0.5">
          {routes.length === 0 ? (
            <div className="font-mono text-2xs text-neutral-400">no published ports</div>
          ) : (
            routes.map((r, i) => (
              <div key={i} className="font-mono text-2xs text-neutral-600 dark:text-neutral-400">
                :{r.hostPort} → {r.container}:{r.containerPort}/{r.proto}
              </div>
            ))
          )}
        </div>
      </div>

      {/* Trunk connector */}
      <div className="h-6 w-px bg-neutral-300 dark:bg-neutral-700" />

      {/* Networks */}
      <div className="flex flex-wrap items-start justify-center gap-4">
        {order.map((name) => {
          const members = [...(groups.get(name) ?? [])].sort(byRunningFirst)
          return (
            <div
              key={name}
              className="min-w-[190px] rounded border-hairline border-neutral-300/70 bg-neutral-50/60 p-2 dark:border-neutral-700/70 dark:bg-neutral-900/30"
            >
              <div className="mb-2 flex items-baseline justify-between gap-3">
                <span className="font-mono text-2xs font-semibold">{name}</span>
                <span className="font-mono text-2xs text-neutral-500">
                  {subnetByName.get(name) ?? '—'}
                </span>
              </div>
              <MemberNodes
                members={members}
                actions={actions}
                supervision={supervision}
                selectedId={selectedId}
                onSelect={onSelect}
              />
            </div>
          )
        })}
      </div>
    </div>
  )
}
