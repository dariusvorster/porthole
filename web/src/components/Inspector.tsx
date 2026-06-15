import { useEffect, useState } from 'react'
import { DESTRUCTIVE, type Action, type ActionState } from '../api/actions'
import { isRunning, primaryIPv4 } from '../api/helpers'
import { MutationError, getContainer, setPolicy, type PolicyBody } from '../api/rest'
import type { Container, StatsSample, Supervision } from '../api/types'
import { useContainerLogs } from '../api/useContainerLogs'
import { formatUptime, shortId } from '../lib/format'
import { ExecView } from './ExecView'
import { LogsView } from './LogsView'
import { StatusDot, type StatusKind } from './StatusDot'

interface InspectorProps {
  container: Container | undefined
  sample: StatsSample | undefined
  action: ActionState | undefined
  supervision: Supervision | undefined
  onAction: (action: Action) => void
  onDismissError: () => void
  onClose: () => void
}

const ACTIONS: { label: string; action: Action }[] = [
  { label: 'Start', action: 'start' },
  { label: 'Stop', action: 'stop' },
  { label: 'Restart', action: 'restart' },
  { label: 'Kill', action: 'kill' },
  { label: 'Delete', action: 'delete' },
]

function stateKind(state: string): StatusKind {
  if (state === 'running') return 'running'
  if (state === 'stopped') return 'stopped'
  return 'other'
}

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[5.5rem_1fr] gap-2 py-1">
      <span className="text-neutral-500">{k}</span>
      <span className="min-w-0 break-words font-mono text-neutral-800 dark:text-neutral-200">
        {children}
      </span>
    </div>
  )
}

const dash = <span className="text-neutral-400">—</span>

export function Inspector({
  container,
  sample,
  action,
  supervision,
  onAction,
  onDismissError,
  onClose,
}: InspectorProps) {
  const [rawOpen, setRawOpen] = useState(false)
  const [raw, setRaw] = useState('')
  const [rawErr, setRawErr] = useState('')
  const [confirm, setConfirm] = useState<Action | null>(null)
  const [tab, setTab] = useState<'details' | 'logs' | 'exec'>('details')
  const id = container?.id

  // Logs stream is open only while the Logs tab is shown for a container; closing
  // the tab / changing selection (id) tears it down so the backend reaps the child.
  const logs = useContainerLogs(id ?? null, tab === 'logs' && !!container)

  useEffect(() => {
    if (!rawOpen || !id) return
    let cancelled = false
    setRaw('')
    setRawErr('')
    getContainer(id)
      .then((c) => !cancelled && setRaw(JSON.stringify(c, null, 2)))
      .catch((e) => !cancelled && setRawErr(String(e)))
    return () => {
      cancelled = true
    }
  }, [rawOpen, id])

  const header = (
    <div className="flex items-center justify-between border-b border-neutral-200/70 px-3 py-2 dark:border-neutral-800/70">
      <span className="font-mono text-2xs font-semibold" title={id}>
        {id ? shortId(id) : 'inspector'}
      </span>
      <button
        type="button"
        onClick={onClose}
        aria-label="close inspector"
        className="font-mono text-2xs text-neutral-400 hover:text-neutral-700 dark:hover:text-neutral-200"
      >
        ✕
      </button>
    </div>
  )

  if (!container) {
    return (
      <aside className="flex w-80 shrink-0 flex-col border-l border-neutral-200/70 dark:border-neutral-800/70">
        {header}
        <div className="p-3 text-2xs text-neutral-400">
          {id ? `container ${id} not found` : 'select a container'}
        </div>
      </aside>
    )
  }

  const running = isRunning(container)
  const pending = action?.phase === 'pending'
  const ip = primaryIPv4(container)
  const nets = container.configuration.networks.map((n) => n.network)
  const ports = container.configuration.publishedPorts ?? []
  const labels = Object.entries(container.configuration.labels ?? {})
  const uptime = formatUptime(container.status.startedDate, running)
  const cpu = running && sample ? `${sample.cpuPercent.toFixed(1)}%` : ''
  const mem = running && sample ? `${sample.memoryPercent.toFixed(1)}%` : ''

  // Enabled per current state (spec): running → stop/restart/kill/delete;
  // stopped → start/delete. Everything is disabled while a mutation is pending.
  const can: Record<Action, boolean> = {
    start: !running && !pending,
    stop: running && !pending,
    restart: running && !pending,
    kill: running && !pending,
    delete: !pending,
  }

  const clickAction = (a: Action) => {
    if (DESTRUCTIVE[a]) setConfirm(a)
    else onAction(a)
  }

  return (
    <aside className="relative flex w-80 shrink-0 flex-col border-l border-neutral-200/70 dark:border-neutral-800/70">
      {header}

      <div className="flex shrink-0 gap-1 border-b border-neutral-200/70 px-3 py-1.5 dark:border-neutral-800/70">
        {(['details', 'logs', 'exec'] as const).map((t) => (
          <button
            key={t}
            type="button"
            onClick={() => setTab(t)}
            className={[
              'rounded px-2 py-0.5 font-mono text-2xs transition',
              tab === t
                ? 'bg-neutral-800 text-white dark:bg-neutral-200 dark:text-neutral-900'
                : 'text-neutral-500 hover:text-neutral-800 dark:hover:text-neutral-200',
            ].join(' ')}
          >
            {t}
          </button>
        ))}
      </div>

      {tab === 'logs' ? (
        <LogsView
          logs={logs}
          supervised={supervision?.policy === 'always' || supervision?.policy === 'unless-stopped'}
        />
      ) : tab === 'exec' ? (
        <ExecView key={container.id} id={container.id} running={running} />
      ) : (
      <div className="flex-1 overflow-auto p-3 text-2xs">
        <Row k="status">
          {pending ? (
            <span className="flex items-center gap-1.5 text-status-warn">
              <span className="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-status-warn" />
              {action.label}
            </span>
          ) : (
            <span className="flex items-center gap-1.5">
              <StatusDot kind={stateKind(container.status.state)} />
              {container.status.state}
            </span>
          )}
        </Row>
        <Row k="image">{container.configuration.image.reference}</Row>
        <Row k="ip">{ip || dash}</Row>
        <Row k="network">{nets.length ? nets.join(', ') : dash}</Row>
        <Row k="ports">
          {ports.length === 0
            ? dash
            : ports.map((p, i) => (
                <div key={i}>
                  :{p.hostPort} → {p.containerPort}/{p.proto}
                </div>
              ))}
        </Row>
        <Row k="cpu">{cpu || dash}</Row>
        <Row k="mem">{mem || dash}</Row>
        <Row k="uptime">{uptime || dash}</Row>
        <Row k="labels">
          {labels.length === 0
            ? dash
            : labels.map(([k, v]) => (
                <div key={k}>
                  {k}={v}
                </div>
              ))}
        </Row>

        {/* Typed error from a failed action (rolled back). */}
        {action?.phase === 'error' && (
          <div className="mt-3 rounded border-hairline border-status-danger/40 bg-status-danger/5 p-2">
            <div className="flex items-start justify-between gap-2">
              <span className="font-mono text-2xs text-status-danger">
                {action.action} failed — {action.error.kind}: {action.error.message}
              </span>
              <button
                type="button"
                onClick={onDismissError}
                aria-label="dismiss error"
                className="font-mono text-2xs text-neutral-400 hover:text-neutral-700 dark:hover:text-neutral-200"
              >
                ✕
              </button>
            </div>
            {action.error.raw && (
              <details className="mt-1">
                <summary className="cursor-pointer font-mono text-2xs text-neutral-500">details</summary>
                <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap font-mono text-[10px] text-neutral-500">
                  {action.error.raw}
                </pre>
              </details>
            )}
          </div>
        )}

        {/* Lifecycle actions — now live (C2). */}
        <div className="mt-3 flex flex-wrap gap-1">
          {ACTIONS.map(({ label, action: a }) => (
            <button
              key={a}
              type="button"
              disabled={!can[a]}
              onClick={() => clickAction(a)}
              className="rounded border-hairline border-neutral-300/70 px-2 py-0.5 text-2xs text-neutral-700 enabled:hover:bg-neutral-50 disabled:cursor-not-allowed disabled:text-neutral-400 dark:border-neutral-700/70 dark:text-neutral-300 dark:enabled:hover:bg-neutral-900/40"
            >
              {label}
            </button>
          ))}
        </div>

        {/* Supervision policy (Phase 3). */}
        <PolicySection key={container.id} container={container} supervision={supervision} onAction={onAction} />

        {/* Raw inspect JSON. */}
        <div className="mt-3">
          <button
            type="button"
            onClick={() => setRawOpen((o) => !o)}
            className="font-mono text-2xs text-neutral-500 hover:text-neutral-800 dark:hover:text-neutral-200"
          >
            {rawOpen ? '▾' : '▸'} raw
          </button>
          {rawOpen && (
            <pre className="mt-1 max-h-80 overflow-auto rounded bg-neutral-100 p-2 font-mono text-[10px] leading-snug dark:bg-neutral-900">
              {rawErr || raw || 'loading…'}
            </pre>
          )}
        </div>
      </div>
      )}

      {confirm && (
        <ConfirmDialog
          action={confirm}
          container={container}
          onCancel={() => setConfirm(null)}
          onConfirm={() => {
            setConfirm(null)
            onAction(confirm)
          }}
        />
      )}
    </aside>
  )
}

function ConfirmDialog({
  action,
  container,
  onCancel,
  onConfirm,
}: {
  action: Action
  container: Container
  onCancel: () => void
  onConfirm: () => void
}) {
  const state = container.status.state
  // The runtime refuses to delete a running container (invalidState). Gate it in
  // the UI rather than firing a bare delete that 409s — guide the user to stop
  // first (spec §8 gap 7). We never silently force-delete.
  const deleteBlocked = action === 'delete' && isRunning(container)

  let body: React.ReactNode
  if (deleteBlocked) {
    body = (
      <>
        Container <span className="font-mono">{container.id}</span> is running. Stop it before
        deleting. (Anonymous volumes are <strong>not</strong> automatically removed by the runtime
        when you delete it.)
      </>
    )
  } else if (action === 'delete') {
    body = (
      <>
        Delete container <span className="font-mono">{container.id}</span> (currently{' '}
        <span className="font-mono">{state}</span>)? Anonymous volumes are <strong>not</strong>{' '}
        automatically removed by the runtime.
      </>
    )
  } else {
    body = (
      <>
        Kill container <span className="font-mono">{container.id}</span> (currently{' '}
        <span className="font-mono">{state}</span>)? This sends SIGKILL immediately.
      </>
    )
  }

  return (
    <div className="absolute inset-0 z-10 flex items-center justify-center bg-black/30 p-3">
      <div className="w-full rounded border-hairline border-neutral-300 bg-white p-3 shadow-lg dark:border-neutral-700 dark:bg-neutral-900">
        <div className="text-2xs leading-relaxed text-neutral-800 dark:text-neutral-200">{body}</div>
        <div className="mt-3 flex justify-end gap-2">
          <button
            type="button"
            onClick={onCancel}
            className="rounded border-hairline border-neutral-300/70 px-2 py-0.5 text-2xs text-neutral-700 hover:bg-neutral-50 dark:border-neutral-700/70 dark:text-neutral-300 dark:hover:bg-neutral-800"
          >
            {deleteBlocked ? 'OK' : 'Cancel'}
          </button>
          {!deleteBlocked && (
            <button
              type="button"
              onClick={onConfirm}
              className="rounded border-hairline border-status-danger/50 bg-status-danger/10 px-2 py-0.5 text-2xs font-medium text-status-danger hover:bg-status-danger/20"
            >
              {action === 'delete' ? 'Delete' : 'Kill'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

// useTick forces a re-render every second while `active` (for the backoff
// countdown). Returns the current epoch ms at render time.
function useTick(active: boolean): number {
  const [, bump] = useState(0)
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => bump((n) => n + 1), 1000)
    return () => clearInterval(t)
  }, [active])
  return Date.now()
}

const RESTART_OPTIONS = [
  { value: 'no', label: 'No' },
  { value: 'always', label: 'Always' },
  { value: 'unless-stopped', label: 'Unless-stopped' },
]

const inputCls =
  'max-w-[10rem] rounded border-hairline border-neutral-300/70 bg-neutral-100/60 px-1.5 py-0.5 font-mono text-2xs dark:border-neutral-700/70 dark:bg-neutral-900/60'

/**
 * Supervision policy controls + live status. The restart select round-trips via
 * the supervision stream (`policy`); on-failure is intentionally not offered.
 * Health config is write-only here (the stream carries health *state*, not the
 * probe config).
 */
function PolicySection({
  container,
  supervision,
  onAction,
}: {
  container: Container
  supervision: Supervision | undefined
  onAction: (a: Action) => void
}) {
  const current = supervision?.policy ? supervision.policy : 'no'
  const hc = supervision?.healthConfig
  const hcType = hc?.type === 'http' || hc?.type === 'tcp' ? hc.type : 'none'
  const [restart, setRestart] = useState<string>(current)
  const [htype, setHtype] = useState<'none' | 'http' | 'tcp'>(hcType)
  const [port, setPort] = useState(hc?.port ? String(hc.port) : '')
  const [path, setPath] = useState(hc?.path ?? '/')
  const [interval, setIntervalSecs] = useState(hc?.interval ? String(hc.interval) : '')
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState('')
  const [saved, setSaved] = useState(false)

  // Sync the restart select with the authoritative stream value (e.g. after a
  // reload, once the supervision event arrives).
  useEffect(() => {
    setRestart(current)
  }, [current])

  // Pre-fill the health form from the configured probe carried on the event,
  // so it round-trips across reload (not just the restart policy).
  useEffect(() => {
    setHtype(hcType)
    setPort(hc?.port ? String(hc.port) : '')
    setPath(hc?.path ?? '/')
    setIntervalSecs(hc?.interval ? String(hc.interval) : '')
  }, [hcType, hc?.port, hc?.path, hc?.interval])

  const save = async () => {
    setSaving(true)
    setErr('')
    setSaved(false)
    try {
      const body: PolicyBody = { restart }
      if (htype !== 'none') {
        body.health = {
          type: htype,
          port: Number(port) || 0,
          path: path || undefined,
          interval: Number(interval) || undefined,
        }
      }
      await setPolicy(container.id, body)
      setSaved(true)
    } catch (e) {
      setErr(e instanceof MutationError ? `${e.kind}: ${e.message}` : String(e))
    } finally {
      setSaving(false)
    }
  }

  const backoffMs = supervision?.backoffUntil ? Date.parse(supervision.backoffUntil) : 0
  const now = useTick(backoffMs > Date.now())
  const backoffSecs = backoffMs > now ? Math.ceil((backoffMs - now) / 1000) : 0

  return (
    <div className="mt-3 border-t border-neutral-200/70 pt-3 dark:border-neutral-800/70">
      <div className="mb-1.5 text-[10px] uppercase tracking-wider text-neutral-400">policy</div>

      <div className="space-y-1.5 text-2xs">
        <label className="flex items-center justify-between gap-2">
          <span className="text-neutral-500">restart</span>
          <select value={restart} onChange={(e) => setRestart(e.target.value)} className={inputCls}>
            {RESTART_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </label>

        <label className="flex items-center justify-between gap-2">
          <span className="text-neutral-500">health</span>
          <select
            value={htype}
            onChange={(e) => setHtype(e.target.value as 'none' | 'http' | 'tcp')}
            className={inputCls}
          >
            <option value="none">none</option>
            <option value="http">http</option>
            <option value="tcp">tcp</option>
          </select>
        </label>
        {htype !== 'none' && (
          <>
            <label className="flex items-center justify-between gap-2">
              <span className="text-neutral-500">port</span>
              <input value={port} onChange={(e) => setPort(e.target.value)} inputMode="numeric" placeholder="80" className={inputCls} />
            </label>
            {htype === 'http' && (
              <label className="flex items-center justify-between gap-2">
                <span className="text-neutral-500">path</span>
                <input value={path} onChange={(e) => setPath(e.target.value)} placeholder="/" className={inputCls} />
              </label>
            )}
            <label className="flex items-center justify-between gap-2">
              <span className="text-neutral-500">interval s</span>
              <input value={interval} onChange={(e) => setIntervalSecs(e.target.value)} inputMode="numeric" placeholder="30" className={inputCls} />
            </label>
          </>
        )}

        <div className="flex items-center gap-2 pt-1">
          <button
            type="button"
            disabled={saving}
            onClick={save}
            className="rounded border-hairline border-neutral-300/70 px-2 py-0.5 text-2xs enabled:hover:bg-neutral-50 disabled:cursor-not-allowed disabled:text-neutral-400 dark:border-neutral-700/70 dark:enabled:hover:bg-neutral-900/40"
          >
            {saving ? 'saving…' : 'Save policy'}
          </button>
          {saved && <span className="text-status-running">saved</span>}
          {err && <span className="text-status-danger">{err}</span>}
        </div>
      </div>

      {supervision && (
        <div className="mt-2 space-y-1 text-2xs">
          {supervision.health && (
            <div className="flex items-center justify-between gap-2">
              <span className="text-neutral-500">health state</span>
              <span className="font-mono">
                {supervision.health.state}
                {supervision.health.failures > 0 ? ` (${supervision.health.failures} fails)` : ''}
              </span>
            </div>
          )}
          {supervision.restartCount > 0 && (
            <div className="flex items-center justify-between gap-2">
              <span className="text-neutral-500">restarts</span>
              <span className="font-mono tabular-nums">{supervision.restartCount}</span>
            </div>
          )}
          {backoffSecs > 0 && (
            <div className="flex items-center justify-between gap-2">
              <span className="text-neutral-500">backoff</span>
              <span className="font-mono tabular-nums text-status-warn">retry in {backoffSecs}s</span>
            </div>
          )}
          {supervision.gaveUp && (
            <div className="flex items-center justify-between gap-2 rounded border-hairline border-status-danger/40 bg-status-danger/5 px-1.5 py-1">
              <span className="font-mono text-status-danger">gave up restarting</span>
              <button
                type="button"
                onClick={() => onAction('start')}
                className="rounded border-hairline border-neutral-300/70 px-2 py-0.5 hover:bg-neutral-50 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40"
              >
                retry
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
