import type { ActionErrorBody } from './actions'
import type {
  ApiError,
  Container,
  CreateEvent,
  CreateSpec,
  Image,
  Network,
  PrunePlan,
  PruneResult,
  RegistryAuth,
  ResourceBundle,
  StackDownResult,
  StackPlan,
  StackUpResult,
  StackView,
  ValidationReport,
  VersionEntry,
} from './types'

/** Thin JSON GET against the same-origin /api surface. */
async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path, { headers: { Accept: 'application/json' } })
  if (!res.ok) throw new Error(`${path} -> ${res.status}`)
  return (await res.json()) as T
}

export function getVersion(): Promise<VersionEntry[]> {
  return getJSON<VersionEntry[]>('/api/system/version')
}

export function getNetworks(): Promise<Network[]> {
  return getJSON<Network[]>('/api/networks')
}

/** Full inspect of one container — backs the inspector's "raw" disclosure. */
export function getContainer(id: string): Promise<Container> {
  return getJSON<Container>(`/api/containers/${encodeURIComponent(id)}`)
}

// --- mutations (Phase 1) ---------------------------------------------------

/** Thrown on a non-2xx mutation; carries the typed error envelope. */
export class MutationError extends Error {
  readonly kind: string
  readonly raw?: string
  constructor(body: ActionErrorBody) {
    super(body.message)
    this.name = 'MutationError'
    this.kind = body.kind
    this.raw = body.raw
  }
}

async function mutate(path: string, method: 'POST' | 'DELETE'): Promise<void> {
  const res = await fetch(path, { method })
  if (res.ok) return // 202 (and any 2xx) — new state arrives via SSE, no body
  let body: ApiError | undefined
  try {
    body = (await res.json()) as ApiError
  } catch {
    /* non-JSON error body */
  }
  throw new MutationError(
    body?.error ?? { kind: 'unknown', message: `request failed (${res.status})` },
  )
}

const id = (s: string) => encodeURIComponent(s)

export interface PolicyBody {
  restart: string
  health?: {
    type: string
    port: number
    path?: string
    interval?: number
  }
}

/** PUT a supervision policy. Throws MutationError on a non-2xx response. */
export async function setPolicy(c: string, body: PolicyBody): Promise<void> {
  const res = await fetch(`/api/containers/${id(c)}/policy`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (res.ok) return
  let err: ApiError | undefined
  try {
    err = (await res.json()) as ApiError
  } catch {
    /* non-JSON */
  }
  throw new MutationError(err?.error ?? { kind: 'unknown', message: `request failed (${res.status})` })
}

export const startContainer = (c: string) => mutate(`/api/containers/${id(c)}/start`, 'POST')
export const stopContainer = (c: string) => mutate(`/api/containers/${id(c)}/stop`, 'POST')
export const restartContainer = (c: string) => mutate(`/api/containers/${id(c)}/restart`, 'POST')
export const killContainer = (c: string) => mutate(`/api/containers/${id(c)}/kill`, 'POST')
export const deleteContainer = (c: string, force = false) =>
  mutate(`/api/containers/${id(c)}${force ? '?force=true' : ''}`, 'DELETE')

/**
 * The clean CLI semver from the version array, ignoring the apiserver's
 * free-form string (mirrors engine.CLIVersion). "" if absent.
 */
export function cliVersion(entries: VersionEntry[]): string {
  return entries.find((e) => e.appName === 'container')?.version ?? ''
}

// --- stacks (Phase 4) ------------------------------------------------------

/** POST JSON, returning the parsed result; throws MutationError on non-2xx. */
async function postJSON<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.ok) return (await res.json()) as T
  let err: ApiError | undefined
  try {
    err = (await res.json()) as ApiError
  } catch {
    /* non-JSON */
  }
  throw new MutationError(err?.error ?? { kind: 'unknown', message: `request failed (${res.status})` })
}

/** Parse + report a compose document. No side effects. */
export function validateStack(name: string, compose: string): Promise<ValidationReport> {
  return postJSON<ValidationReport>('/api/stacks/validate', { name, compose })
}

/**
 * Import (store) a stack. The backend returns the ValidationReport on both
 * success (201) and invalid-compose (400); we surface `ok` so the UI can show
 * the rejections without treating an invalid file as a hard error.
 */
export async function importStack(
  name: string,
  compose: string,
): Promise<{ ok: boolean; report: ValidationReport }> {
  const res = await fetch('/api/stacks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ name, compose }),
  })
  const body = (await res.json().catch(() => null)) as ValidationReport | ApiError | null
  if (body && 'valid' in body) {
    return { ok: res.status === 201, report: body }
  }
  // Non-report error (e.g. missing name).
  throw new MutationError(
    (body as ApiError | null)?.error ?? { kind: 'unknown', message: `import failed (${res.status})` },
  )
}

export function listStacks(): Promise<StackView[]> {
  return getJSON<StackView[]>('/api/stacks')
}

export function getStack(name: string): Promise<StackView> {
  return getJSON<StackView>(`/api/stacks/${id(name)}`)
}

export function planStack(name: string): Promise<StackPlan> {
  return postJSON<StackPlan>(`/api/stacks/${id(name)}/plan`)
}

export function upStack(name: string): Promise<StackUpResult> {
  return postJSON<StackUpResult>(`/api/stacks/${id(name)}/up`)
}

export function downStack(name: string): Promise<StackDownResult> {
  return postJSON<StackDownResult>(`/api/stacks/${id(name)}/down`)
}

export function restartStack(name: string): Promise<StackUpResult> {
  return postJSON<StackUpResult>(`/api/stacks/${id(name)}/restart`)
}

export function deleteStack(name: string): Promise<void> {
  return mutate(`/api/stacks/${id(name)}`, 'DELETE')
}

// --- resources / disk (Phase 6) --------------------------------------------

export function getResources(): Promise<ResourceBundle> {
  return getJSON<ResourceBundle>('/api/resources')
}

const allQ = (all: boolean) => (all ? '&all=true' : '')

/** Dry-run: what a prune of `kind` would remove. No mutation. */
export function prunePreview(kind: string, all = false): Promise<PrunePlan> {
  return postJSON<PrunePlan>(`/api/prune/${kind}?preview=true${allQ(all)}`)
}

/** Apply the prune; returns the actual reclaimed figure. */
export function pruneApply(kind: string, all = false): Promise<PruneResult> {
  return postJSON<PruneResult>(`/api/prune/${kind}?apply=true${allQ(all)}`)
}

/** Delete a volume; rejects with MutationError(kind=volume_in_use) when mounted. */
export function deleteVolume(name: string): Promise<void> {
  return mutate(`/api/volumes/${id(name)}`, 'DELETE')
}

/** Delete an image (never blocked by the runtime — the UI warns beforehand). */
export function deleteImage(ref: string, force = false): Promise<void> {
  return mutate(`/api/images?ref=${id(ref)}${force ? '&force=true' : ''}`, 'DELETE')
}

export function tagImage(src: string, dst: string): Promise<void> {
  return postJSON<void>('/api/images/tag', { src, dst })
}

/** Standalone image pull, streaming the phase progress (reuses the create SSE). */
export function pullImage(ref: string, onEvent: (e: CreateEvent) => void): Promise<void> {
  return streamSSE('/api/images/pull', { ref }, onEvent)
}

// --- registry login (Phase 7) ----------------------------------------------

/** POST a JSON body, expect a 2xx with NO body (204). Throws MutationError on
 * non-2xx. Used for login/logout — never returns a body, so nothing to leak. */
async function postNoBody(path: string, body: unknown): Promise<void> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (res.ok) return
  let err: ApiError | undefined
  try {
    err = (await res.json()) as ApiError
  } catch {
    /* non-JSON */
  }
  throw new MutationError(err?.error ?? { kind: 'unknown', message: `request failed (${res.status})` })
}

export function getRegistry(): Promise<RegistryAuth[]> {
  return getJSON<RegistryAuth[]>('/api/registry')
}

/** Log in to a registry. The token goes in the POST body (never a URL/query) and
 * is never returned; the runtime stores it. Rejects MutationError on failure. */
export function registryLogin(host: string, username: string, token: string): Promise<void> {
  return postNoBody('/api/registry/login', { host, username, token })
}

export function registryLogout(host: string): Promise<void> {
  return postNoBody('/api/registry/logout', { host })
}

// --- create / run container (Phase 5) --------------------------------------

export function getImages(): Promise<Image[]> {
  return getJSON<Image[]>('/api/images')
}

/**
 * Create + run a container, streaming pull/start progress. Run AUTO-PULLS and
 * blocks, so the endpoint responds with an SSE stream (not a 202). EventSource
 * is GET-only, so we POST and read the body stream with fetch, parsing SSE
 * frames manually. Each event is delivered to `onEvent`; resolves when the
 * stream ends (after a terminal created/error).
 */
export function createContainer(
  spec: CreateSpec,
  onEvent: (e: CreateEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  return streamSSE('/api/containers', spec, onEvent, signal)
}

/**
 * POST a JSON body and consume an SSE progress stream (run/pull auto-pull +
 * block, so these respond with a stream, not a 202). EventSource is GET-only, so
 * we POST and read the body with fetch, parsing SSE frames manually. Shared by
 * create and standalone image pull.
 */
async function streamSSE(
  path: string,
  body: unknown,
  onEvent: (e: CreateEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  let res: Response
  try {
    res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
      signal,
    })
  } catch (e) {
    if (signal?.aborted) return // user cancelled — silent, no error event
    onEvent({ kind: 'error', error: { kind: 'unknown', message: `request failed: ${String(e)}` } })
    return
  }
  // Pre-stream failures (400/403/422/503) return a JSON error envelope, not SSE.
  if (!res.ok || !res.body) {
    let env: ApiError | undefined
    try {
      env = (await res.json()) as ApiError
    } catch {
      /* non-JSON */
    }
    onEvent({
      kind: 'error',
      error: env?.error ?? { kind: 'unknown', message: `request failed (${res.status})` },
    })
    return
  }
  const reader = res.body.getReader()
  const dec = new TextDecoder()
  let buf = ''
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      let sep: number
      while ((sep = buf.indexOf('\n\n')) >= 0) {
        parseCreateFrame(buf.slice(0, sep), onEvent)
        buf = buf.slice(sep + 2)
      }
    }
  } catch (e) {
    // A dropped/aborted stream is the only non-terminal exit. If the user
    // cancelled, stay silent; otherwise surface it so the UI isn't left hanging.
    if (signal?.aborted) return
    onEvent({ kind: 'error', error: { kind: 'unknown', message: `stream interrupted: ${String(e)}` } })
  }
}

function parseCreateFrame(frame: string, onEvent: (e: CreateEvent) => void): void {
  let event = ''
  let data = ''
  for (const line of frame.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) data += line.slice(5).trim()
  }
  if (!event) return
  let d: Record<string, unknown> = {}
  try {
    d = data ? JSON.parse(data) : {}
  } catch {
    return
  }
  if (event === 'progress') {
    onEvent({ kind: 'progress', index: Number(d.index), total: Number(d.total), phase: String(d.phase ?? '') })
  } else if (event === 'created') {
    onEvent({ kind: 'created', id: String(d.id ?? '') })
  } else if (event === 'pull_stalled') {
    onEvent({ kind: 'stalled', image: String(d.image ?? ''), message: String(d.message ?? '') })
  } else if (event === 'error') {
    onEvent({
      kind: 'error',
      error: { kind: String(d.kind ?? 'unknown'), message: String(d.message ?? ''), raw: d.raw as string | undefined },
    })
  }
}
