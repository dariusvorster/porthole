import { useEffect, useRef, useState } from 'react'
import { createContainer, getImages } from '../api/rest'
import type { CreateSpec, Image, Network } from '../api/types'
import { showToast } from '../lib/toast'

interface CreateFormProps {
  networks: Network[]
  onClose: () => void
  onCreated: (id: string) => void
  /** Open Settings → Registry (the create-error nudge target, REG4). */
  onOpenRegistry?: () => void
}

interface KV {
  key: string
  value: string
}
interface PortRow {
  host: string
  container: string
  proto: string
}
interface VolRow {
  source: string
  target: string
}

type ProgressState = { index: number; total: number; phase: string } | null

const field =
  'w-full rounded border-hairline border-neutral-300/70 bg-neutral-100/60 px-1.5 py-1 font-mono text-2xs dark:border-neutral-700/70 dark:bg-neutral-900/60'
const btn =
  'rounded border-hairline border-neutral-300/70 px-2 py-0.5 font-mono text-2xs hover:bg-neutral-50 disabled:opacity-40 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40'

/**
 * The "Run container" modal (Phase 5). Reuses the proven RunSpec via the create
 * endpoint; submit streams the pull/start phases (run auto-pulls + blocks) and
 * shows a phase stepper — never a frozen dialog. Recreate/restart/health write
 * supervision labels server-side.
 */
export function CreateForm({ networks, onClose, onCreated, onOpenRegistry }: CreateFormProps) {
  const [images, setImages] = useState<Image[]>([])
  const [image, setImage] = useState('')
  const [name, setName] = useState('')
  const [command, setCommand] = useState('')
  const [restart, setRestart] = useState('')
  const [cpus, setCpus] = useState('')
  const [memory, setMemory] = useState('')
  const [network, setNetwork] = useState('')
  const [workdir, setWorkdir] = useState('')
  const [user, setUser] = useState('')
  const [ports, setPorts] = useState<PortRow[]>([])
  const [env, setEnv] = useState<KV[]>([])
  const [volumes, setVolumes] = useState<VolRow[]>([])
  const [labels, setLabels] = useState<KV[]>([])

  const [progress, setProgress] = useState<ProgressState>(null)
  const [lines, setLines] = useState<string[]>([]) // streaming pull/start status (hung-vs-slow visibility)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [errorKind, setErrorKind] = useState<string | null>(null)
  // The keychain-stall state (Phase 7b): distinct from `error` — hedged, with the
  // image ref so we can show the exact one-time `container image pull <ref>` fix.
  const [stalled, setStalled] = useState<{ image: string; message: string } | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  const logRef = useRef<HTMLDivElement>(null)

  // Abort any in-flight pull if the form unmounts (closes the SSE → backend kills
  // the child), so a closed dialog never leaves an orphaned pull running.
  useEffect(() => () => abortRef.current?.abort(), [])

  // Auto-scroll the status log to the latest line.
  useEffect(() => {
    const el = logRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [lines])

  useEffect(() => {
    getImages()
      .then(setImages)
      .catch(() => {
        /* picker is optional; typing a ref still works */
      })
  }, [])

  const validate = (): string | null => {
    if (!image.trim()) return 'image is required'
    for (const p of ports) {
      if (!p.host && !p.container) continue
      const h = Number(p.host)
      const c = Number(p.container)
      if (!Number.isInteger(h) || h < 1 || h > 65535 || !Number.isInteger(c) || c < 1 || c > 65535)
        return `invalid port mapping ${p.host}:${p.container}`
    }
    for (const v of volumes) {
      if ((v.source && !v.target) || (!v.source && v.target)) return 'volume needs both source and target'
    }
    if (memory && !/^\d+(\.\d+)?[kmg]?b?$/i.test(memory)) return `invalid memory "${memory}" (e.g. 512m)`
    return null
  }

  const buildSpec = (): CreateSpec => {
    const envMap: Record<string, string> = {}
    for (const e of env) if (e.key.trim()) envMap[e.key.trim()] = e.value
    const labelMap: Record<string, string> = {}
    for (const l of labels) if (l.key.trim()) labelMap[l.key.trim()] = l.value
    return {
      image: image.trim(),
      name: name.trim() || undefined,
      command: command.trim() || undefined,
      env: Object.keys(envMap).length ? envMap : undefined,
      labels: Object.keys(labelMap).length ? labelMap : undefined,
      ports: ports
        .filter((p) => p.host && p.container)
        .map((p) => ({ hostPort: Number(p.host), containerPort: Number(p.container), proto: p.proto || 'tcp' })),
      volumes: volumes.filter((v) => v.source && v.target).map((v) => ({ source: v.source, target: v.target })),
      restart: restart || undefined,
      cpus: cpus ? Number(cpus) : undefined,
      memory: memory.trim() || undefined,
      network: network || undefined,
      workdir: workdir.trim() || undefined,
      user: user.trim() || undefined,
    }
  }

  const onSubmit = async () => {
    const v = validate()
    if (v) {
      setError(v)
      return
    }
    setError(null)
    setErrorKind(null)
    setStalled(null)
    setLines([])
    setSubmitting(true)
    setProgress({ index: 0, total: 6, phase: 'starting…' })
    const ac = new AbortController()
    abortRef.current = ac
    await createContainer(
      buildSpec(),
      (e) => {
        if (e.kind === 'progress') {
          setProgress({ index: e.index, total: e.total, phase: e.phase || 'working…' })
          // Append the raw status line so a stall (lines stop) reads differently
          // from slow progress (lines keep flowing).
          const line = `[${e.index}/${e.total}] ${e.phase || ''}`.trimEnd()
          setLines((prev) => (prev[prev.length - 1] === line ? prev : [...prev, line].slice(-200)))
        } else if (e.kind === 'created') {
          showToast(`Created ${name.trim() || e.id}`)
          onCreated(e.id)
          onClose()
        } else if (e.kind === 'stalled') {
          // Keep the progress log visible — it shows the lines stopped at the
          // initial phase. The form returns to usable (submitting=false) so Retry
          // works after the user authorizes the keychain.
          setStalled({ image: e.image, message: e.message })
          setSubmitting(false)
        } else {
          setError(e.error.message || 'create failed')
          setErrorKind(e.error.kind)
          setSubmitting(false)
          setProgress(null)
        }
      },
      ac.signal,
    )
  }

  // Abort an in-flight pull (escape a hung/slow pull); otherwise close the form.
  const onCancel = () => {
    if (submitting) {
      abortRef.current?.abort()
      setSubmitting(false)
      setProgress(null)
      setError(null)
      return
    }
    onClose()
  }

  const isBind = (src: string) => src.startsWith('/') || src.startsWith('.')

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-auto bg-black/40 p-6">
      <div className="w-full max-w-2xl space-y-3 rounded border-hairline border-neutral-300/70 bg-white p-4 dark:border-neutral-700/70 dark:bg-neutral-950">
        <div className="flex items-center justify-between">
          <h2 className="font-mono text-sm font-semibold">Run container</h2>
          <button type="button" onClick={onClose} className={btn} aria-label="close">
            ✕
          </button>
        </div>

        {/* image + name */}
        <div className="grid grid-cols-2 gap-2">
          <div>
            <label className="mb-0.5 block font-mono text-2xs text-neutral-500">image *</label>
            <input
              value={image}
              onChange={(e) => setImage(e.target.value)}
              list="image-list"
              placeholder="docker.io/library/nginx"
              aria-label="image"
              className={field}
            />
            <datalist id="image-list">
              {images.map((im) => (
                <option key={im.reference} value={im.reference} />
              ))}
            </datalist>
          </div>
          <div>
            <label className="mb-0.5 block font-mono text-2xs text-neutral-500">name</label>
            <input value={name} onChange={(e) => setName(e.target.value)} aria-label="name" className={field} />
          </div>
        </div>

        <div>
          <label className="mb-0.5 block font-mono text-2xs text-neutral-500">command</label>
          <input
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            placeholder='e.g. sh -c "echo hi"'
            aria-label="command"
            className={field}
          />
        </div>

        {/* ports */}
        <Repeatable
          title="ports"
          rows={ports}
          onAdd={() => setPorts([...ports, { host: '', container: '', proto: 'tcp' }])}
          onRemove={(i) => setPorts(ports.filter((_, j) => j !== i))}
          render={(p, i) => (
            <div className="flex items-center gap-1">
              <input
                value={p.host}
                onChange={(e) => setPorts(upd(ports, i, { host: e.target.value }))}
                placeholder="host"
                aria-label="host port"
                className={field}
              />
              <span className="text-neutral-400">:</span>
              <input
                value={p.container}
                onChange={(e) => setPorts(upd(ports, i, { container: e.target.value }))}
                placeholder="container"
                aria-label="container port"
                className={field}
              />
              <select
                value={p.proto}
                onChange={(e) => setPorts(upd(ports, i, { proto: e.target.value }))}
                aria-label="proto"
                className={field}
              >
                <option value="tcp">tcp</option>
                <option value="udp">udp</option>
              </select>
            </div>
          )}
        />

        {/* environment */}
        <Repeatable
          title="environment"
          rows={env}
          onAdd={() => setEnv([...env, { key: '', value: '' }])}
          onRemove={(i) => setEnv(env.filter((_, j) => j !== i))}
          render={(e, i) => (
            <KVRow kv={e} onKey={(k) => setEnv(upd(env, i, { key: k }))} onVal={(v) => setEnv(upd(env, i, { value: v }))} />
          )}
        />

        {/* volumes */}
        <Repeatable
          title="volumes"
          rows={volumes}
          onAdd={() => setVolumes([...volumes, { source: '', target: '' }])}
          onRemove={(i) => setVolumes(volumes.filter((_, j) => j !== i))}
          render={(v, i) => (
            <div className="flex items-center gap-1">
              <input
                value={v.source}
                onChange={(e) => setVolumes(upd(volumes, i, { source: e.target.value }))}
                placeholder="name or /host/path"
                aria-label="volume source"
                className={field}
              />
              <span className="text-neutral-400">:</span>
              <input
                value={v.target}
                onChange={(e) => setVolumes(upd(volumes, i, { target: e.target.value }))}
                placeholder="/container/path"
                aria-label="volume target"
                className={field}
              />
              {v.source && (
                <span
                  className={`shrink-0 rounded px-1 font-mono text-2xs ${
                    isBind(v.source) ? 'bg-status-warn/15 text-status-warn' : 'bg-neutral-200/60 text-neutral-500 dark:bg-neutral-800/60'
                  }`}
                >
                  {isBind(v.source) ? 'host-path bind' : 'named'}
                </span>
              )}
            </div>
          )}
        />

        {/* labels */}
        <Repeatable
          title="labels"
          rows={labels}
          onAdd={() => setLabels([...labels, { key: '', value: '' }])}
          onRemove={(i) => setLabels(labels.filter((_, j) => j !== i))}
          render={(l, i) => (
            <KVRow
              kv={l}
              onKey={(k) => setLabels(upd(labels, i, { key: k }))}
              onVal={(v) => setLabels(upd(labels, i, { value: v }))}
            />
          )}
        />

        {/* restart + resources + network */}
        <div className="grid grid-cols-3 gap-2">
          <div>
            <label className="mb-0.5 block font-mono text-2xs text-neutral-500">restart</label>
            <select value={restart} onChange={(e) => setRestart(e.target.value)} aria-label="restart" className={field}>
              <option value="">no</option>
              <option value="always">always</option>
              <option value="unless-stopped">unless-stopped</option>
            </select>
          </div>
          <div>
            <label className="mb-0.5 block font-mono text-2xs text-neutral-500">network</label>
            <select value={network} onChange={(e) => setNetwork(e.target.value)} aria-label="network" className={field}>
              <option value="">default</option>
              {networks
                .filter((n) => n.configuration.name !== 'default')
                .map((n) => (
                  <option key={n.configuration.name} value={n.configuration.name}>
                    {n.configuration.name}
                  </option>
                ))}
            </select>
          </div>
          <div className="grid grid-cols-2 gap-1">
            <div>
              <label className="mb-0.5 block font-mono text-2xs text-neutral-500">cpus</label>
              <input value={cpus} onChange={(e) => setCpus(e.target.value)} placeholder="2" aria-label="cpus" className={field} />
            </div>
            <div>
              <label className="mb-0.5 block font-mono text-2xs text-neutral-500">memory</label>
              <input value={memory} onChange={(e) => setMemory(e.target.value)} placeholder="512m" aria-label="memory" className={field} />
            </div>
          </div>
        </div>

        {/* advanced */}
        <details>
          <summary className="cursor-pointer font-mono text-2xs text-neutral-500">advanced</summary>
          <div className="mt-2 grid grid-cols-2 gap-2">
            <div>
              <label className="mb-0.5 block font-mono text-2xs text-neutral-500">workdir</label>
              <input value={workdir} onChange={(e) => setWorkdir(e.target.value)} aria-label="workdir" className={field} />
            </div>
            <div>
              <label className="mb-0.5 block font-mono text-2xs text-neutral-500">user</label>
              <input value={user} onChange={(e) => setUser(e.target.value)} aria-label="user" className={field} />
            </div>
          </div>
        </details>

        {/* progress / error / actions */}
        {progress && (
          <div
            className="space-y-1 rounded border-hairline border-status-running/40 bg-status-running/5 px-2 py-1.5 font-mono text-2xs"
            data-testid="create-progress"
          >
            <div className="text-status-running">
              [{progress.index}/{progress.total}] {progress.phase}
            </div>
            {/* Streaming status: the raw lines as they arrive. If they stop, the
                pull is hung; if they keep coming, it's just slow. */}
            {lines.length > 0 && (
              <div
                ref={logRef}
                data-testid="create-progress-log"
                className="max-h-28 overflow-auto whitespace-pre-wrap break-all text-2xs leading-snug text-neutral-500 dark:text-neutral-400"
              >
                {lines.map((l, i) => (
                  <div key={i}>{l}</div>
                ))}
              </div>
            )}
          </div>
        )}
        {error && (
          <div className="space-y-1" data-testid="create-error">
            <div className="font-mono text-2xs text-status-danger">{error}</div>
            {errorKind === 'image_pull_failed' && onOpenRegistry && (
              <button
                type="button"
                onClick={onOpenRegistry}
                className="font-mono text-2xs text-status-running underline"
                data-testid="registry-nudge"
              >
                If this is a private image, log in to its registry first →
              </button>
            )}
          </div>
        )}
        {/* Keychain-stall panel (Phase 7b): hedged, amber (distinct from the red
            not-found error), with the EXACT one-time fix using the real ref. */}
        {stalled && (
          <div
            className="space-y-2 rounded border-hairline border-status-warn/50 bg-status-warn/5 px-2 py-2"
            data-testid="create-stalled"
          >
            <div className="font-mono text-2xs font-medium text-status-warn">
              {stalled.message || 'This pull appears stalled — likely waiting on a one-time keychain authorization.'}
            </div>
            <div className="font-sans text-2xs text-neutral-600 dark:text-neutral-300">
              Run this once in Terminal and click <span className="font-semibold">Always Allow</span> on the keychain
              prompt. After that, Porthole pulls private images on its own — then retry here.
            </div>
            <div className="flex items-center gap-1">
              <pre
                className="flex-1 overflow-auto rounded bg-neutral-800 px-2 py-1 font-mono text-2xs text-neutral-100 dark:bg-neutral-900"
                data-testid="stalled-command"
              >
                container image pull {stalled.image}
              </pre>
              <button
                type="button"
                onClick={() => {
                  navigator.clipboard?.writeText(`container image pull ${stalled.image}`).then(
                    () => showToast('Command copied'),
                    () => {
                      /* clipboard blocked — the block is still selectable */
                    },
                  )
                }}
                className={btn}
                aria-label="copy command"
              >
                copy
              </button>
            </div>
            <button
              type="button"
              onClick={onSubmit}
              className="rounded border-hairline border-status-running/50 bg-status-running/10 px-3 py-0.5 font-mono text-2xs text-status-running hover:bg-status-running/20"
              data-testid="stalled-retry"
            >
              Retry
            </button>
          </div>
        )}

        <div className="flex items-center justify-end gap-2 border-t border-neutral-200/70 pt-2 dark:border-neutral-800/70">
          <button type="button" onClick={onCancel} className={btn}>
            cancel
          </button>
          <button
            type="button"
            onClick={onSubmit}
            disabled={submitting || !image.trim()}
            className="rounded border-hairline border-status-running/50 bg-status-running/10 px-3 py-0.5 font-mono text-2xs text-status-running hover:bg-status-running/20 disabled:opacity-40"
          >
            {submitting ? 'running…' : 'run'}
          </button>
        </div>
      </div>
    </div>
  )
}

function upd<T>(rows: T[], i: number, patch: Partial<T>): T[] {
  return rows.map((r, j) => (j === i ? { ...r, ...patch } : r))
}

function KVRow({ kv, onKey, onVal }: { kv: KV; onKey: (k: string) => void; onVal: (v: string) => void }) {
  return (
    <div className="flex items-center gap-1">
      <input value={kv.key} onChange={(e) => onKey(e.target.value)} placeholder="KEY" aria-label="key" className={field} />
      <span className="text-neutral-400">=</span>
      <input value={kv.value} onChange={(e) => onVal(e.target.value)} placeholder="value" aria-label="value" className={field} />
    </div>
  )
}

function Repeatable<T>({
  title,
  rows,
  onAdd,
  onRemove,
  render,
}: {
  title: string
  rows: T[]
  onAdd: () => void
  onRemove: (i: number) => void
  render: (row: T, i: number) => React.ReactNode
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2">
        <span className="font-mono text-2xs text-neutral-500">{title}</span>
        <button type="button" onClick={onAdd} className={btn} aria-label={`add ${title}`}>
          + add
        </button>
      </div>
      {rows.map((row, i) => (
        <div key={i} className="flex items-center gap-1">
          <div className="flex-1">{render(row, i)}</div>
          <button type="button" onClick={() => onRemove(i)} className={btn} aria-label={`remove ${title}`}>
            ✕
          </button>
        </div>
      ))}
    </div>
  )
}
