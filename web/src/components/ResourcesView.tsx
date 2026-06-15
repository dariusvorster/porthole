import { useState } from 'react'
import {
  MutationError,
  deleteImage,
  deleteVolume,
  pruneApply,
  prunePreview,
  pullImage,
} from '../api/rest'
import type { AnnotatedNetwork, AnnotatedVolume, DiskUsage, PrunePlan, ResourceBundle } from '../api/types'
import { formatBytes } from '../lib/format'
import { showToast } from '../lib/toast'
import { EmptyState } from './EmptyState'
import { StatusDot } from './StatusDot'

interface ResourcesViewProps {
  bundle: ResourceBundle | null
  /** Live df from the SSE stream — the summary cards use this (not the fetched
   * bundle.summary) so they can never disagree with the sidebar (PF1 sub-issue). */
  liveDiskUsage: DiskUsage | null
  onChanged: () => void
}

const btn =
  'rounded border-hairline border-neutral-300/70 px-2 py-0.5 font-mono text-2xs hover:bg-neutral-50 disabled:opacity-40 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40'
const dangerBtn =
  'rounded border-hairline border-status-danger/50 px-2 py-0.5 font-mono text-2xs text-status-danger hover:bg-status-danger/10'

type PruneState = { kind: string; all: boolean; plan: PrunePlan } | null

/**
 * Resources / Disk view (Phase 6): summary cards + images/volumes/networks lists
 * with reclaim. Prune is preview-then-apply. Image delete WARNS (runtime never
 * blocks); volume delete surfaces the typed in-use reason. Anonymous-orphan
 * volumes are flagged. `sizeInBytes` is never shown as disk-used.
 */
export function ResourcesView({ bundle, liveDiskUsage, onChanged }: ResourcesViewProps) {
  const [prune, setPrune] = useState<PruneState>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmImg, setConfirmImg] = useState<{ ref: string; warn: string } | null>(null)
  const [pullRef, setPullRef] = useState('')
  const [pullPhase, setPullPhase] = useState<string | null>(null)

  if (!bundle) {
    return <div className="p-4 font-mono text-2xs text-neutral-400">loading resources…</div>
  }
  // Summary cards use the LIVE df (same source as the sidebar), falling back to
  // the fetched bundle summary only until the first stream tick.
  const df = liveDiskUsage ?? bundle.summary

  const startPrune = async (kind: string, all = false) => {
    setError(null)
    try {
      setPrune({ kind, all, plan: await prunePreview(kind, all) })
    } catch (e) {
      setError(String(e))
    }
  }
  const applyPrune = async () => {
    if (!prune) return
    setBusy(true)
    setError(null)
    try {
      const r = await pruneApply(prune.kind, prune.all)
      showToast(`Reclaimed ${r.reclaimed}`)
      setPrune(null)
      onChanged()
    } catch (e) {
      setError(e instanceof MutationError ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const removeVolume = async (name: string) => {
    setError(null)
    try {
      await deleteVolume(name)
      onChanged()
    } catch (e) {
      // The runtime's raw "delete failed" line is unhelpful; give the typed
      // in-use case a friendly message (the kind is authoritative).
      if (e instanceof MutationError && e.kind === 'volume_in_use') {
        setError(`volume "${name}" is in use by a container — stop and remove it first`)
      } else {
        setError(e instanceof MutationError ? e.message : String(e))
      }
    }
  }
  const removeImage = async (ref: string) => {
    setError(null)
    setConfirmImg(null)
    try {
      await deleteImage(ref)
      onChanged()
    } catch (e) {
      setError(e instanceof MutationError ? e.message : String(e))
    }
  }
  const doPull = async () => {
    const ref = pullRef.trim()
    if (!ref) return
    setError(null)
    setPullPhase('starting…')
    await pullImage(ref, (e) => {
      if (e.kind === 'progress') setPullPhase(e.phase || 'working…')
      else if (e.kind === 'created') {
        setPullPhase(null)
        setPullRef('')
        showToast(`Pulled ${ref}`)
        onChanged()
      } else if (e.kind === 'stalled') {
        // Only the create path runs the keychain-stall watchdog today; handle it
        // defensively so a future watchdog here surfaces rather than vanishes.
        setError(e.message)
        setPullPhase(null)
      } else {
        setError(e.error.message)
        setPullPhase(null)
      }
    })
  }

  return (
    <div className="space-y-4 p-4">
      {/* summary cards */}
      <div className="grid grid-cols-4 gap-2">
        <Card title="containers" cat={df.containers} onPrune={() => startPrune('containers')} />
        <Card title="images" cat={df.images} onPrune={() => startPrune('images', false)} />
        <Card title="volumes" cat={df.volumes} onPrune={() => startPrune('volumes')} />
        <div className="rounded border-hairline border-neutral-300/70 p-2 dark:border-neutral-700/70">
          <div className="font-mono text-2xs font-semibold text-neutral-500">networks</div>
          <div className="mt-1 font-mono text-2xs">{bundle.networks.length} total</div>
          <button type="button" onClick={() => startPrune('networks')} className={`${btn} mt-1`}>
            prune unused
          </button>
        </div>
      </div>

      {error && <div className="font-mono text-2xs text-status-danger" data-testid="resource-error">{error}</div>}

      {/* prune preview */}
      {prune && (
        <div className="rounded border-hairline border-status-warn/50 bg-status-warn/5 p-2" data-testid="prune-preview">
          <div className="mb-1 font-mono text-2xs font-semibold text-status-warn">
            prune {prune.kind}
            {prune.all ? ' (all unreferenced)' : ''} — will remove {prune.plan.items?.length ?? 0}:
          </div>
          <div className="max-h-32 space-y-0.5 overflow-auto">
            {(prune.plan.items ?? []).map((it) => (
              <div key={it} className="font-mono text-2xs text-neutral-600 dark:text-neutral-400">
                {it}
              </div>
            ))}
            {(prune.plan.items?.length ?? 0) === 0 && (
              <div className="font-mono text-2xs text-neutral-400">nothing to reclaim</div>
            )}
          </div>
          <div className="mt-2 flex gap-2">
            <button
              type="button"
              onClick={applyPrune}
              disabled={busy || (prune.plan.items?.length ?? 0) === 0}
              className={dangerBtn}
            >
              {busy ? 'pruning…' : 'apply'}
            </button>
            <button type="button" onClick={() => setPrune(null)} className={btn}>
              cancel
            </button>
          </div>
        </div>
      )}

      {/* images */}
      <Section title="images" prune={<button type="button" onClick={() => startPrune('images', true)} className={btn}>prune all unused</button>}>
        <div className="mb-2 flex items-center gap-2">
          <input
            value={pullRef}
            onChange={(e) => setPullRef(e.target.value)}
            placeholder="docker.io/library/redis — pull image"
            aria-label="pull image"
            className="w-72 rounded border-hairline border-neutral-300/70 bg-neutral-100/60 px-1.5 py-0.5 font-mono text-2xs dark:border-neutral-700/70 dark:bg-neutral-900/60"
          />
          <button type="button" onClick={doPull} disabled={!pullRef.trim() || pullPhase !== null} className={btn}>
            pull
          </button>
          {pullPhase && <span className="font-mono text-2xs text-status-running" data-testid="pull-phase">{pullPhase}</span>}
        </div>
        {bundle.images.map((im) => {
          const inUse = (im.inUseByRunning ?? []).length > 0
          return (
            <Row key={im.reference} mono={im.reference} meta={formatBytes(im.size)}>
              {inUse && <Badge kind="warn">in use · {(im.inUseByRunning ?? []).join(', ')}</Badge>}
              <button
                type="button"
                onClick={() =>
                  inUse
                    ? setConfirmImg({ ref: im.reference, warn: `in use by running ${(im.inUseByRunning ?? []).join(', ')} — deleting may break it` })
                    : removeImage(im.reference)
                }
                className={dangerBtn}
              >
                delete
              </button>
            </Row>
          )
        })}
      </Section>

      {/* image-delete advisory confirm */}
      {confirmImg && (
        <div className="rounded border-hairline border-status-warn/50 bg-status-warn/5 p-2 font-mono text-2xs" data-testid="image-warn">
          <span className="text-status-warn">{confirmImg.warn}</span>
          <div className="mt-1 flex gap-2">
            <button type="button" onClick={() => removeImage(confirmImg.ref)} className={dangerBtn}>
              delete anyway
            </button>
            <button type="button" onClick={() => setConfirmImg(null)} className={btn}>
              cancel
            </button>
          </div>
        </div>
      )}

      {/* volumes */}
      <Section title="volumes" prune={<button type="button" onClick={() => startPrune('volumes')} className={btn}>prune unused</button>}>
        {bundle.volumes.map((v) => (
          <VolumeRow key={v.name} v={v} onDelete={() => removeVolume(v.name)} />
        ))}
        {bundle.volumes.length === 0 && <EmptyState compact title="No volumes" />}
      </Section>

      {/* networks */}
      <Section title="networks" prune={<button type="button" onClick={() => startPrune('networks')} className={btn}>prune unused</button>}>
        {bundle.networks.map((n) => (
          <NetworkRow key={n.configuration.name} n={n} />
        ))}
      </Section>
    </div>
  )
}

function Card({ title, cat, onPrune }: { title: string; cat: { sizeInBytes: number; reclaimable: number; active: number; total: number }; onPrune: () => void }) {
  return (
    <div className="rounded border-hairline border-neutral-300/70 p-2 dark:border-neutral-700/70">
      <div className="font-mono text-2xs font-semibold text-neutral-500">{title}</div>
      <div className="mt-1 font-mono text-2xs">{cat.active}/{cat.total} · {formatBytes(cat.sizeInBytes)}</div>
      <button type="button" onClick={onPrune} className={`${btn} mt-1`}>
        reclaim {formatBytes(cat.reclaimable)}
      </button>
    </div>
  )
}

function Section({ title, prune, children }: { title: string; prune: React.ReactNode; children: React.ReactNode }) {
  return (
    <section>
      <div className="mb-1 flex items-center gap-2">
        <h3 className="font-mono text-2xs font-semibold uppercase tracking-wide text-neutral-500">{title}</h3>
        {prune}
      </div>
      <div className="space-y-1">{children}</div>
    </section>
  )
}

function Row({ mono, meta, children }: { mono: string; meta?: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2 rounded border-hairline border-neutral-300/70 px-2 py-1 dark:border-neutral-700/70">
      <span className="font-mono text-2xs">{mono}</span>
      {meta && <span className="font-mono text-2xs text-neutral-500">{meta}</span>}
      <span className="ml-auto flex items-center gap-2">{children}</span>
    </div>
  )
}

function Badge({ kind, children }: { kind: 'warn' | 'neutral'; children: React.ReactNode }) {
  const cls =
    kind === 'warn'
      ? 'bg-status-warn/15 text-status-warn'
      : 'bg-neutral-200/60 text-neutral-500 dark:bg-neutral-800/60'
  return <span className={`rounded px-1 font-mono text-2xs ${cls}`}>{children}</span>
}

function VolumeRow({ v, onDelete }: { v: AnnotatedVolume; onDelete: () => void }) {
  return (
    <div className="flex items-center gap-2 rounded border-hairline border-neutral-300/70 px-2 py-1 dark:border-neutral-700/70">
      <StatusDot kind={v.inUse ? 'running' : 'stopped'} />
      <span className="font-mono text-2xs">{v.name}</span>
      <span className="ml-auto flex items-center gap-2">
        {v.anonymous && <Badge kind="warn">anonymous · unreferenced · safe to reclaim</Badge>}
        {v.inUse && <Badge kind="neutral">in use · {(v.usedBy ?? []).join(', ')}</Badge>}
        <button type="button" onClick={onDelete} className={dangerBtn}>
          delete
        </button>
      </span>
    </div>
  )
}

function NetworkRow({ n }: { n: AnnotatedNetwork }) {
  return (
    <div className="flex items-center gap-2 rounded border-hairline border-neutral-300/70 px-2 py-1 dark:border-neutral-700/70">
      <span className="font-mono text-2xs">{n.configuration.name}</span>
      <span className="font-mono text-2xs text-neutral-500">{n.status.ipv4Subnet || '—'}</span>
      <span className="font-mono text-2xs text-neutral-400">{n.memberCount} members</span>
      <span className="ml-auto">
        {n.protected ? (
          <Badge kind="neutral">builtin · protected</Badge>
        ) : (
          <span className="font-mono text-2xs text-neutral-400">—</span>
        )}
      </span>
    </div>
  )
}
