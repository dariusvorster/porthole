/**
 * One shared empty-state shape used across every view (PF4) so "nothing here yet"
 * reads the same everywhere: a title, an optional one-line hint, and an optional
 * primary action. `compact` is for an empty section inside a populated view (e.g.
 * the volumes section) vs the full-view centered variant.
 */
export function EmptyState({
  title,
  hint,
  action,
  compact = false,
}: {
  title: string
  hint?: string
  action?: { label: string; onClick: () => void }
  compact?: boolean
}) {
  return (
    <div
      className={`flex flex-col items-center justify-center gap-2 text-center ${
        compact ? 'py-6' : 'h-full p-8'
      }`}
    >
      <div className="font-mono text-2xs font-medium text-neutral-500">{title}</div>
      {hint && <div className="font-mono text-2xs text-neutral-400">{hint}</div>}
      {action && (
        <button
          type="button"
          onClick={action.onClick}
          className="rounded border-hairline border-status-running/50 bg-status-running/10 px-3 py-1 font-mono text-xs text-status-running hover:bg-status-running/20"
        >
          {action.label}
        </button>
      )}
    </div>
  )
}
