import { useEffect, useState } from 'react'
import {
  MutationError,
  downStack,
  importStack,
  planStack,
  restartStack,
  setStackDiscovery,
  upStack,
  validateStack,
} from '../api/rest'
import type { StackActionKind, StackPlan, StackView, ValidationReport } from '../api/types'
import { showToast } from '../lib/toast'
import { EmptyState } from './EmptyState'
import { StatusDot, type StatusKind } from './StatusDot'

interface StacksViewProps {
  stacks: StackView[]
  onChanged: () => void
}

function statusKind(status: string): StatusKind {
  if (status === 'up') return 'running'
  if (status === 'degraded') return 'other'
  return 'stopped' // down | unknown
}

/** v1 applies create/start; recreate/orphan are detected-only (shown, not run). */
const SAFE: Record<StackActionKind, boolean> = {
  create: true,
  start: true,
  noop: true,
  recreate: false,
  orphan: false,
}

const ACTION_NOTE: Partial<Record<StackActionKind, string>> = {
  recreate: 'would recreate — not applied in this version',
  orphan: 'running but not in the file',
}

/**
 * Stacks console (Phase 4, v1): import + validate a compose subset, list stored
 * stacks with live status, and per-stack up/down/restart + a non-destructive
 * Plan/diff. Recreate/orphan are SHOWN, never applied (that's v2).
 */
export function StacksView({ stacks, onChanged }: StacksViewProps) {
  const [selected, setSelected] = useState<string | null>(null)
  const selectedStack = stacks.find((s) => s.name === selected) ?? null

  return (
    <div className="flex h-full min-h-0">
      <div className="w-80 shrink-0 space-y-3 overflow-auto border-r border-neutral-200/70 p-3 dark:border-neutral-800/70">
        <ImportPanel onImported={onChanged} />
        <StackList stacks={stacks} selected={selected} onSelect={setSelected} />
      </div>
      <div className="min-w-0 flex-1 overflow-auto p-3">
        {selectedStack ? (
          <StackDetail key={selectedStack.name} stack={selectedStack} onChanged={onChanged} />
        ) : (
          <div className="font-mono text-2xs text-neutral-400">select a stack</div>
        )}
      </div>
    </div>
  )
}

// --- import + validate -----------------------------------------------------

function ImportPanel({ onImported }: { onImported: () => void }) {
  const [name, setName] = useState('')
  const [compose, setCompose] = useState('')
  const [report, setReport] = useState<ValidationReport | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const onValidate = async () => {
    setBusy(true)
    setError(null)
    try {
      setReport(await validateStack(name, compose))
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy(false)
    }
  }

  const onImport = async () => {
    setBusy(true)
    setError(null)
    try {
      const { ok, report } = await importStack(name, compose)
      setReport(report)
      if (ok) {
        setName('')
        setCompose('')
        setReport(null)
        onImported()
      }
    } catch (e) {
      setError(e instanceof MutationError ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const onFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0]
    if (!f) return
    setCompose(await f.text())
    if (!name) setName(f.name.replace(/\.(ya?ml)$/i, ''))
  }

  return (
    <section className="space-y-2">
      <h2 className="font-mono text-2xs font-semibold uppercase tracking-wide text-neutral-500">
        import stack
      </h2>
      <input
        value={name}
        onChange={(e) => setName(e.target.value)}
        aria-label="stack name"
        placeholder="name"
        className="w-full rounded border-hairline border-neutral-300/70 bg-neutral-100/60 px-1.5 py-1 font-mono text-2xs dark:border-neutral-700/70 dark:bg-neutral-900/60"
      />
      <textarea
        value={compose}
        onChange={(e) => setCompose(e.target.value)}
        aria-label="compose yaml"
        placeholder={'services:\n  web:\n    image: docker.io/library/nginx'}
        spellCheck={false}
        className="h-40 w-full resize-y rounded border-hairline border-neutral-300/70 bg-neutral-100/60 p-1.5 font-mono text-2xs dark:border-neutral-700/70 dark:bg-neutral-900/60"
      />
      <div className="flex items-center gap-2">
        <label className="cursor-pointer rounded border-hairline border-neutral-300/70 px-2 py-0.5 font-mono text-2xs hover:bg-neutral-50 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40">
          file…
          <input type="file" accept=".yml,.yaml" onChange={onFile} className="hidden" />
        </label>
        <button
          type="button"
          onClick={onValidate}
          disabled={busy || !compose}
          className="rounded border-hairline border-neutral-300/70 px-2 py-0.5 font-mono text-2xs hover:bg-neutral-50 disabled:opacity-40 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40"
        >
          validate
        </button>
        <button
          type="button"
          onClick={onImport}
          disabled={busy || !compose || !name}
          className="rounded border-hairline border-status-running/50 bg-status-running/10 px-2 py-0.5 font-mono text-2xs text-status-running hover:bg-status-running/20 disabled:opacity-40"
        >
          import
        </button>
      </div>
      {error && <div className="font-mono text-2xs text-status-danger">{error}</div>}
      {report && <ReportView report={report} />}
    </section>
  )
}

function ReportView({ report }: { report: ValidationReport }) {
  return (
    <div
      className={[
        'space-y-1 rounded border-hairline p-2 font-mono text-2xs',
        report.valid
          ? 'border-status-running/40 bg-status-running/5'
          : 'border-status-danger/40 bg-status-danger/5',
      ].join(' ')}
      data-testid="validation-report"
    >
      <div className={report.valid ? 'text-status-running' : 'text-status-danger'}>
        {report.valid ? '✓ valid' : '✗ invalid'}
      </div>
      {report.rejected?.map((r) => (
        <div key={r.path} className="text-status-danger">
          rejected <span className="font-semibold">{r.path}</span> — {r.reason}
        </div>
      ))}
      {report.errors?.map((e, i) => (
        <div key={i} className="text-status-danger">
          {e}
        </div>
      ))}
      {report.warnings?.map((wn, i) => (
        <div key={i} className="text-status-warn">
          {wn}
        </div>
      ))}
      {report.notes?.map((n, i) => (
        <div key={i} className="text-neutral-500">
          {n}
        </div>
      ))}
    </div>
  )
}

// --- stored stack list -----------------------------------------------------

function StackList({
  stacks,
  selected,
  onSelect,
}: {
  stacks: StackView[]
  selected: string | null
  onSelect: (name: string) => void
}) {
  return (
    <section className="space-y-1">
      <h2 className="font-mono text-2xs font-semibold uppercase tracking-wide text-neutral-500">
        stacks
      </h2>
      {stacks.length === 0 ? (
        <EmptyState compact title="No stacks yet" hint="Import a compose file above to start." />
      ) : (
        stacks.map((s) => (
          <button
            key={s.name}
            type="button"
            onClick={() => onSelect(s.name)}
            className={[
              'flex w-full items-center gap-1.5 rounded border-hairline px-2 py-1 text-left font-mono text-2xs transition',
              s.name === selected
                ? 'border-status-running/60 bg-status-running/5'
                : 'border-neutral-300/70 hover:bg-neutral-50 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40',
            ].join(' ')}
          >
            <StatusDot kind={statusKind(s.status)} />
            <span className="font-medium">{s.name}</span>
            <span className="ml-auto text-neutral-500">{s.status}</span>
          </button>
        ))
      )}
    </section>
  )
}

// --- per-stack detail ------------------------------------------------------

function StackDetail({ stack, onChanged }: { stack: StackView; onChanged: () => void }) {
  const [plan, setPlan] = useState<StackPlan | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirmDown, setConfirmDown] = useState(false)

  // Clear transient state when the selected stack changes.
  useEffect(() => {
    setPlan(null)
    setError(null)
    setConfirmDown(false)
  }, [stack.name])

  const run = async (label: string, fn: () => Promise<unknown>, okMsg?: string) => {
    setBusy(label)
    setError(null)
    try {
      await fn()
      if (okMsg) showToast(okMsg)
      onChanged()
    } catch (e) {
      setError(e instanceof MutationError ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const onPlan = () =>
    run('plan', async () => {
      setPlan(await planStack(stack.name))
    })

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <StatusDot kind={statusKind(stack.status)} />
        <span className="font-mono text-sm font-semibold">{stack.name}</span>
        <span className="font-mono text-2xs text-neutral-500">{stack.status}</span>
        {!stack.valid && (
          <span className="rounded bg-status-danger/10 px-1.5 py-0.5 font-mono text-2xs text-status-danger">
            stored file invalid
          </span>
        )}
        {stack.discovery && (
          <span
            className="rounded bg-status-running/10 px-1.5 py-0.5 font-mono text-2xs text-status-running"
            title="Members resolve each other by service name via injected /etc/hosts"
            data-testid="discovery-indicator"
          >
            discovery on
          </span>
        )}
      </div>

      {/* Actions */}
      <div className="flex flex-wrap items-center gap-2">
        <ActionButton
          label="up"
          busy={busy === 'up'}
          onClick={() => run('up', () => upStack(stack.name), `Stack ${stack.name} up`)}
        />
        <ActionButton
          label="restart"
          busy={busy === 'restart'}
          onClick={() => run('restart', () => restartStack(stack.name), `Stack ${stack.name} restarted`)}
        />
        {confirmDown ? (
          <span className="flex items-center gap-1.5 rounded border-hairline border-status-warn/50 bg-status-warn/5 px-2 py-0.5 font-mono text-2xs">
            <span className="text-status-warn">down keeps named volumes — confirm?</span>
            <button
              type="button"
              onClick={() => {
                setConfirmDown(false)
                run('down', () => downStack(stack.name), `Stack ${stack.name} down`)
              }}
              className="rounded border-hairline border-status-danger/50 px-1.5 text-status-danger hover:bg-status-danger/10"
            >
              yes, down
            </button>
            <button
              type="button"
              onClick={() => setConfirmDown(false)}
              className="rounded border-hairline border-neutral-300/70 px-1.5 hover:bg-neutral-50 dark:border-neutral-700/70"
            >
              cancel
            </button>
          </span>
        ) : (
          <ActionButton label="down" busy={busy === 'down'} onClick={() => setConfirmDown(true)} />
        )}
        <button
          type="button"
          onClick={onPlan}
          disabled={busy === 'plan'}
          className="rounded border-hairline border-neutral-300/70 px-2 py-0.5 font-mono text-2xs hover:bg-neutral-50 disabled:opacity-40 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40"
        >
          plan
        </button>
        <button
          type="button"
          onClick={() =>
            run(
              'discovery',
              () => setStackDiscovery(stack.name, !stack.discovery),
              `Service discovery ${stack.discovery ? 'off' : 'on'} for ${stack.name}`,
            )
          }
          disabled={busy === 'discovery'}
          title="Toggle name-based resolution between this stack's services"
          data-testid="discovery-toggle"
          className={[
            'ml-auto rounded border-hairline px-2 py-0.5 font-mono text-2xs disabled:opacity-40',
            stack.discovery
              ? 'border-status-running/50 bg-status-running/10 text-status-running hover:bg-status-running/20'
              : 'border-neutral-300/70 hover:bg-neutral-50 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40',
          ].join(' ')}
        >
          {busy === 'discovery' ? 'saving…' : stack.discovery ? 'discovery: on' : 'discovery: off'}
        </button>
      </div>
      {stack.discovery && (
        <p className="font-mono text-2xs text-neutral-500">
          Services reach each other by name — e.g. <code>http://{(stack.services ?? [])[0] ?? 'api'}</code>. Porthole
          keeps each member's <code>/etc/hosts</code> updated as IPs change on restart.
        </p>
      )}

      {error && <div className="font-mono text-2xs text-status-danger">{error}</div>}

      {/* Members */}
      <section>
        <h3 className="mb-1 font-mono text-2xs font-semibold uppercase tracking-wide text-neutral-500">
          members
        </h3>
        {!stack.members || stack.members.length === 0 ? (
          <div className="font-mono text-2xs text-neutral-400">no running members</div>
        ) : (
          <div className="space-y-1">
            {stack.members.map((m) => (
              <div
                key={m.service}
                className="flex items-center gap-2 rounded border-hairline border-neutral-300/70 px-2 py-1 font-mono text-2xs dark:border-neutral-700/70"
              >
                <StatusDot kind={m.state === 'running' ? 'running' : 'stopped'} />
                <span className="font-medium">{m.service}</span>
                <span className="text-neutral-500">{m.image}</span>
                <span className="ml-auto text-neutral-500">{m.ip || '—'}</span>
              </div>
            ))}
          </div>
        )}
      </section>

      {/* Plan / diff */}
      {plan && <PlanView plan={plan} />}
    </div>
  )
}

function ActionButton({ label, busy, onClick }: { label: string; busy: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={busy}
      className="rounded border-hairline border-neutral-300/70 px-2 py-0.5 font-mono text-2xs hover:bg-neutral-50 disabled:opacity-40 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40"
    >
      {busy ? `${label}…` : label}
    </button>
  )
}

function PlanView({ plan }: { plan: StackPlan }) {
  const actions = plan.actions ?? []
  return (
    <section data-testid="plan-view">
      <h3 className="mb-1 font-mono text-2xs font-semibold uppercase tracking-wide text-neutral-500">
        plan (diff)
      </h3>
      {actions.length === 0 ? (
        <div className="font-mono text-2xs text-neutral-400">no changes</div>
      ) : (
        <div className="space-y-1">
          {actions.map((a) => {
            const safe = SAFE[a.action]
            return (
              <div
                key={a.service}
                className={[
                  'rounded border-hairline px-2 py-1 font-mono text-2xs',
                  safe
                    ? 'border-neutral-300/70 dark:border-neutral-700/70'
                    : 'border-status-warn/50 bg-status-warn/5',
                ].join(' ')}
              >
                <div className="flex items-center gap-2">
                  <span className="font-medium">{a.service}</span>
                  <span className={safe ? 'text-neutral-600 dark:text-neutral-400' : 'text-status-warn'}>
                    {a.action}
                  </span>
                  {ACTION_NOTE[a.action] && (
                    <span className="ml-auto italic text-status-warn">{ACTION_NOTE[a.action]}</span>
                  )}
                </div>
                {a.diff?.map((d, i) => (
                  <div key={i} className="pl-3 text-neutral-500">
                    {d}
                  </div>
                ))}
              </div>
            )
          })}
        </div>
      )}
    </section>
  )
}
