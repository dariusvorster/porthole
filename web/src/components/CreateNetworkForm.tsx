import { useState } from 'react'
import { MutationError, createNetwork } from '../api/rest'
import type { Network } from '../api/types'
import { showToast } from '../lib/toast'

interface KV {
  key: string
  value: string
}

const field =
  'w-full rounded border-hairline border-neutral-300/70 bg-neutral-100/60 px-1.5 py-1 font-mono text-2xs dark:border-neutral-700/70 dark:bg-neutral-900/60'
const btn =
  'rounded border-hairline border-neutral-300/70 px-2 py-0.5 font-mono text-2xs hover:bg-neutral-50 disabled:opacity-40 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40'

// Lenient IPv4 CIDR check (e.g. 10.88.0.0/24). v6 isn't strictly validated.
const CIDR_RE = /^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/

/**
 * The ONE create-network form (Phase 11), used as a modal from two entry points:
 * the Resources network section and the container create-form's network dropdown.
 * Mirrors container's actual flags — name + Advanced (subnet/v6/internal/labels/
 * options). On success it calls onCreated with the returned Network (carrying the
 * auto-assigned subnet) so the caller can show/select it.
 */
export function CreateNetworkForm({
  onClose,
  onCreated,
}: {
  onClose: () => void
  onCreated: (net: Network) => void
}) {
  const [name, setName] = useState('')
  const [subnet, setSubnet] = useState('')
  const [subnetV6, setSubnetV6] = useState('')
  const [internal, setInternal] = useState(false)
  const [labels, setLabels] = useState<KV[]>([])
  const [options, setOptions] = useState<KV[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const toMap = (rows: KV[]): Record<string, string> | undefined => {
    const m: Record<string, string> = {}
    for (const r of rows) if (r.key.trim()) m[r.key.trim()] = r.value
    return Object.keys(m).length ? m : undefined
  }

  const onSubmit = async () => {
    if (!name.trim()) {
      setError('network name is required')
      return
    }
    if (subnet.trim() && !CIDR_RE.test(subnet.trim())) {
      setError(`invalid subnet "${subnet.trim()}" (e.g. 10.88.0.0/24)`)
      return
    }
    setBusy(true)
    setError(null)
    try {
      const net = await createNetwork({
        name: name.trim(),
        subnet: subnet.trim() || undefined,
        subnetV6: subnetV6.trim() || undefined,
        internal: internal || undefined,
        labels: toMap(labels),
        options: toMap(options),
      })
      showToast(`Created network ${net.configuration.name}`)
      onCreated(net)
      onClose()
    } catch (e) {
      setError(
        e instanceof MutationError
          ? e.kind === 'name_conflict'
            ? `A network named "${name.trim()}" already exists.`
            : e.message
          : String(e),
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-auto bg-black/40 p-6">
      <div className="w-full max-w-md space-y-3 rounded border-hairline border-neutral-300/70 bg-white p-4 dark:border-neutral-700/70 dark:bg-neutral-950">
        <div className="flex items-center justify-between">
          <h2 className="font-mono text-sm font-semibold">Create network</h2>
          <button type="button" onClick={onClose} className={btn} aria-label="close">
            ✕
          </button>
        </div>

        <div>
          <label className="mb-0.5 block font-mono text-2xs text-neutral-500">name *</label>
          <input value={name} onChange={(e) => setName(e.target.value)} aria-label="network name" className={field} />
        </div>

        <details>
          <summary className="cursor-pointer font-mono text-2xs text-neutral-500">advanced</summary>
          <div className="mt-2 space-y-2">
            <div className="grid grid-cols-2 gap-2">
              <div>
                <label className="mb-0.5 block font-mono text-2xs text-neutral-500">subnet (CIDR)</label>
                <input value={subnet} onChange={(e) => setSubnet(e.target.value)} placeholder="10.88.0.0/24" aria-label="subnet" className={field} />
              </div>
              <div>
                <label className="mb-0.5 block font-mono text-2xs text-neutral-500">subnet-v6</label>
                <input value={subnetV6} onChange={(e) => setSubnetV6(e.target.value)} placeholder="fd00::/64" aria-label="subnet-v6" className={field} />
              </div>
            </div>
            <label className="flex items-center gap-1.5 font-mono text-2xs text-neutral-600 dark:text-neutral-300">
              <input type="checkbox" checked={internal} onChange={(e) => setInternal(e.target.checked)} aria-label="internal" />
              --internal (host-only)
            </label>
            <KVRows title="labels" rows={labels} setRows={setLabels} />
            <KVRows title="options" rows={options} setRows={setOptions} />
          </div>
        </details>

        {error && <div className="font-mono text-2xs text-status-danger" data-testid="network-error">{error}</div>}

        <div className="flex items-center justify-end gap-2 border-t border-neutral-200/70 pt-2 dark:border-neutral-800/70">
          <button type="button" onClick={onClose} className={btn}>
            cancel
          </button>
          <button
            type="button"
            onClick={onSubmit}
            disabled={busy || !name.trim()}
            className="rounded border-hairline border-status-running/50 bg-status-running/10 px-3 py-0.5 font-mono text-2xs text-status-running hover:bg-status-running/20 disabled:opacity-40"
          >
            {busy ? 'creating…' : 'create'}
          </button>
        </div>
      </div>
    </div>
  )
}

function KVRows({ title, rows, setRows }: { title: string; rows: KV[]; setRows: (r: KV[]) => void }) {
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2">
        <span className="font-mono text-2xs text-neutral-500">{title}</span>
        <button type="button" onClick={() => setRows([...rows, { key: '', value: '' }])} className={btn} aria-label={`add ${title}`}>
          + add
        </button>
      </div>
      {rows.map((row, i) => (
        <div key={i} className="flex items-center gap-1">
          <input
            value={row.key}
            onChange={(e) => setRows(rows.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))}
            placeholder="KEY"
            aria-label={`${title} key`}
            className={field}
          />
          <span className="text-neutral-400">=</span>
          <input
            value={row.value}
            onChange={(e) => setRows(rows.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))}
            placeholder="value"
            aria-label={`${title} value`}
            className={field}
          />
          <button type="button" onClick={() => setRows(rows.filter((_, j) => j !== i))} className={btn} aria-label={`remove ${title}`}>
            ✕
          </button>
        </div>
      ))}
    </div>
  )
}
