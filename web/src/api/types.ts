// TypeScript mirror of the Go wire types. The Go source of truth is `types.go`
// (engine domain models) and `hub.go` (reconcile SSE payloads). The API is the
// contract — keep these in lockstep with the backend, do not infer extras.

// ---------------------------------------------------------------------------
// Containers
// ---------------------------------------------------------------------------

export interface ImageDescriptor {
  digest: string
  mediaType: string
  size: number
}

export interface ImageRef {
  reference: string
  descriptor?: ImageDescriptor
}

export interface Resources {
  cpus: number
  cpuOverhead: number
  memoryInBytes: number
}

/** A container's declared membership of a network (name only, never the IP). */
export interface NetworkAttach {
  network: string
  options?: { hostname?: string; mtu?: number }
}

/**
 * Confirmed against real `container inspect` of a `-p 8080:80` container:
 * {"containerPort":80,"count":1,"hostAddress":"0.0.0.0","hostPort":8080,"proto":"tcp"}
 * Still render defensively — fields are omitempty on the wire.
 */
export interface PublishedPort {
  hostAddress?: string
  hostPort?: number
  containerPort?: number
  proto?: string
  count?: number
}

export interface ContainerConfig {
  id: string
  creationDate: string
  image: ImageRef
  networks: NetworkAttach[]
  resources: Resources
  labels: Record<string, string>
  publishedPorts: PublishedPort[]
  stopSignal?: string
}

/**
 * Runtime network assignment. The dedicated per-container IPv4 lives here as a
 * CIDR string and ONLY exists while the container is running.
 */
export interface NetworkStatus {
  network: string
  hostname: string
  ipv4Address: string // CIDR, e.g. "192.168.64.2/24"
  ipv4Gateway: string
  ipv6Address: string
  macAddress: string
  mtu: number
}

export interface ContainerStatus {
  state: string // observed "running" | "stopped"; treat as an open set
  startedDate: string
  networks: NetworkStatus[] // POPULATED ONLY WHEN RUNNING — [] when stopped
}

export interface Container {
  id: string
  configuration: ContainerConfig
  status: ContainerStatus
}

// ---------------------------------------------------------------------------
// Networks
// ---------------------------------------------------------------------------

export interface NetworkConfig {
  name: string
  creationDate: string
  mode: string
  plugin: string
  labels: Record<string, string>
  options: Record<string, string>
}

export interface NetworkRuntime {
  ipv4Gateway: string
  ipv4Subnet: string // "192.168.64.0/24"
  ipv6Subnet: string
}

export interface Network {
  id: string
  configuration: NetworkConfig
  status: NetworkRuntime
}

// ---------------------------------------------------------------------------
// System: version, disk usage, stats
// ---------------------------------------------------------------------------

export interface VersionEntry {
  appName: string
  buildType: string
  commit: string
  version: string
}

export interface DiskCategory {
  active: number
  total: number
  sizeInBytes: number
  reclaimable: number
}

export interface DiskUsage {
  containers: DiskCategory
  images: DiskCategory
  volumes: DiskCategory
}

/**
 * REST `GET /stats` — raw cumulative counters. cpuUsageUsec is CUMULATIVE CPU
 * time, not a percentage. The UI reads CPU% from the SSE `StatsSample` instead,
 * where the server has already computed it. This type exists for completeness.
 */
export interface Stats {
  id: string
  cpuUsageUsec: number
  memoryUsageBytes: number
  memoryLimitBytes: number
  blockReadBytes: number
  blockWriteBytes: number
  networkRxBytes: number
  networkTxBytes: number
  numProcesses: number
}

// ---------------------------------------------------------------------------
// SSE payloads — `GET /api/stream`, one EventSource. Event name -> data shape.
// ---------------------------------------------------------------------------

/** First event on connect; the authoritative reset of all state. */
export interface SnapshotEvent {
  containers: Container[]
  diskUsage: DiskUsage
  apiServerRunning: boolean
}

export interface ContainerEvent {
  container: Container
}

export interface RemovedEvent {
  id: string
}

/** stats SSE sample — cpuPercent is computed server-side (≠ REST `Stats`). */
export interface StatsSample {
  id: string
  cpuPercent: number
  memoryPercent: number
  memoryUsageBytes: number
  numProcesses: number
}

export interface StatsEvent {
  samples: StatsSample[]
}

/** Edge-triggered when the apiserver goes up or down. */
export interface SystemEvent {
  apiServerRunning: boolean
  detail?: string
}

// --- supervision (Phase 3) -------------------------------------------------

export type HealthStateName = 'starting' | 'healthy' | 'unhealthy'

export interface SupervisionHealth {
  state: HealthStateName
  lastProbe?: string
  failures: number
}

/** The configured probe (so the inspector health form can pre-fill). */
export interface SupervisionHealthConfig {
  type: string
  port: number
  path?: string
  interval?: number
}

export type RestartPolicyName = '' | 'no' | 'always' | 'unless-stopped'

/** `supervision` SSE payload — per-container restart policy + health state. */
export interface Supervision {
  id: string
  policy: RestartPolicyName
  desiredState: 'running' | 'stopped'
  restartCount: number // transient backoff-attempt counter (resets on stabilization)
  restartTotal: number // cumulative lifetime supervision restarts (persisted — the badge uses this)
  backoffUntil?: string
  gaveUp: boolean
  health?: SupervisionHealth
  healthConfig?: SupervisionHealthConfig
}

// ---------------------------------------------------------------------------
// Stacks (Phase 4) — REST shapes + the `stack` SSE event. Mirrors the Go
// stacks package (model.go / plan.go / service.go).
// ---------------------------------------------------------------------------

export interface RejectedKey {
  path: string
  reason: string
}

/** Result of parsing a compose file — shown BEFORE import, never silently dropped. */
export interface ValidationReport {
  valid: boolean
  rejected: RejectedKey[] | null
  errors: string[] | null
  warnings: string[] | null
  notes: string[] | null
}

/** One container belonging to a stack (IP present only while running). */
export interface StackMember {
  service: string
  id: string
  state: string
  ip?: string
  image: string
}

/** A stored stack plus live status + members — the GET shape. */
export interface StackView {
  name: string
  createdAt: string
  updatedAt: string
  status: string // up | degraded | down | unknown
  valid: boolean
  discovery: boolean // service-discovery opt-in (Phase 8): members resolve peers by name
  services: string[] | null
  members: StackMember[] | null
}

/** Plan action kinds. recreate/orphan are DETECTED-ONLY in v1 (never applied). */
export type StackActionKind = 'create' | 'start' | 'noop' | 'recreate' | 'orphan'

export interface StackServiceAction {
  service: string
  action: StackActionKind
  containerId?: string
  diff?: string[]
}

export interface StackPlan {
  stack: string
  actions: StackServiceAction[] | null
}

export interface StackFailure {
  service: string
  action: string
  error: string
}

export interface StackUpResult {
  stack: string
  plan: StackPlan
  applied: StackServiceAction[] | null
  status: string
  failures?: StackFailure[]
}

export interface StackDownResult {
  stack: string
  removed: string[] | null
  failures?: StackFailure[]
}

/** `stack` SSE payload — broadcast after a stack mutation (live status nudge). */
export interface StackEvent {
  stack: string
  status: string
  members: StackMember[] | null
}

// ---------------------------------------------------------------------------
// Create / Run container (Phase 5)
// ---------------------------------------------------------------------------

/** A locally-present image — `GET /api/images` (the create picker). */
export interface Image {
  reference: string
  digest: string
  size: number
  created: string
}

export interface CreatePort {
  hostPort: number
  containerPort: number
  proto: string
}

export interface CreateVolume {
  source: string // named volume or host path (bind)
  target: string
}

export interface CreateHealth {
  type: string // http | tcp
  port: number
  path?: string
  interval?: number
}

/** The create form body — maps to a single-container RunSpec server-side. */
export interface CreateSpec {
  image: string
  name?: string
  command?: string
  env?: Record<string, string>
  envFile?: string[]
  ports?: CreatePort[]
  volumes?: CreateVolume[]
  labels?: Record<string, string>
  restart?: string // '' | no | always | unless-stopped
  health?: CreateHealth | null
  cpus?: number
  memory?: string // e.g. "512m"
  network?: string
  workdir?: string
  user?: string
  // Richer create flags (Phase 9).
  init?: boolean
  readOnly?: boolean
  entrypoint?: string
  capAdd?: string[]
  capDrop?: string[]
  tmpfs?: string[]
  shmSize?: string
}

/** Events from the create SSE stream (run auto-pulls + blocks). */
export type CreateEvent =
  | { kind: 'progress'; index: number; total: number; phase: string }
  | { kind: 'created'; id: string }
  // pull_stalled (Phase 7b): the watchdog cancelled a pull stuck at the initial
  // phase — almost always a one-time macOS keychain authorization the headless
  // daemon can't surface. Distinct from `error`: hedged, with the image ref so the
  // UI can build the exact `container image pull <ref>` fix command.
  | { kind: 'stalled'; image: string; message: string }
  | { kind: 'error'; error: { kind: string; message: string; raw?: string } }

// ---------------------------------------------------------------------------
// Resources / Disk (Phase 6) — annotated lists + prune. Mirrors the Go
// `resources` package + engine.Volume / engine.PruneResult.
// ---------------------------------------------------------------------------

export interface Volume {
  name: string
  driver: string
  format: string
  sizeInBytes: number // ALLOCATED/sparse — NOT usage; never render as disk-used
  source: string
  created: string
  labels: Record<string, string>
}

export interface AnnotatedImage extends Image {
  inUseByRunning: string[] | null // advisory only — image delete is never blocked
}

export interface AnnotatedVolume extends Volume {
  inUse: boolean // any container, running OR stopped (enforced)
  usedBy: string[] | null
  anonymous: boolean // UUID-shaped name + zero references (the disk-leak case)
}

export interface AnnotatedNetwork extends Network {
  protected: boolean // builtin role label — non-removable
  members: string[] | null
  memberCount: number
}

export interface ResourceBundle {
  summary: DiskUsage
  images: AnnotatedImage[]
  volumes: AnnotatedVolume[]
  networks: AnnotatedNetwork[]
}

/** Dry-run prune preview — exactly what a prune would remove (before applying). */
export interface PrunePlan {
  kind: string
  items: string[] | null
}

/** Applied prune result — `reclaimed` is the runtime's display figure (verbatim). */
export interface PruneResult {
  reclaimed: string
  removed: string[] | null
}

// ---------------------------------------------------------------------------
// Registry login (Phase 7) — `GET /api/registry`. Host + username only; the
// credential lives in the runtime's store, never here.
// ---------------------------------------------------------------------------

export interface RegistryAuth {
  host: string
  username: string
  created: string
}

// ---------------------------------------------------------------------------
// Error envelope — `{ error: { kind, message, raw? } }`
// ---------------------------------------------------------------------------

export type ErrorKind =
  | 'daemon_down'
  | 'not_found'
  | 'name_conflict'
  | 'not_running'
  | 'unknown_option'
  | 'unknown'

export interface ApiError {
  error: { kind: ErrorKind; message: string; raw?: string }
}
