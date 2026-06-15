import type { RefObject } from 'react'

export type View = 'topology' | 'list' | 'stacks' | 'resources' | 'settings'

interface TopBarProps {
  version: string
  view: View
  onViewChange: (v: View) => void
  onRun?: () => void
  query: string
  onQueryChange: (q: string) => void
  inputRef?: RefObject<HTMLInputElement>
}

/**
 * Top chrome bar: Porthole mark, a (non-functional) ⌘K mono filter input, the
 * view toggle + Run button, and `container <version>`. Stays visible even when
 * the daemon is down.
 *
 * Responsive: the bar can WRAP (min-h, flex-wrap) and the filter is the thing
 * that yields — it's `flex-1 min-w-0` so it shrinks first, while the right group
 * (Run + tabs + version) is `shrink-0` so the navigation is never squeezed off
 * the right edge. At narrow widths the right group simply wraps to a second line;
 * every control stays reachable at any width.
 */
export function TopBar({ version, view, onViewChange, onRun, query, onQueryChange, inputRef }: TopBarProps) {
  return (
    <header className="flex min-h-11 shrink-0 flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-neutral-200/70 px-3 py-1.5 dark:border-neutral-800/70">
      <span className="shrink-0 font-mono text-sm font-semibold tracking-tight">porthole</span>

      <div className="relative min-w-0 flex-1 basis-40 max-w-sm">
        <span className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 font-mono text-2xs text-neutral-400">
          ⌘K
        </span>
        <input
          ref={inputRef}
          value={query}
          onChange={(e) => onQueryChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              onQueryChange('')
              e.currentTarget.blur()
            }
          }}
          placeholder="filter…"
          aria-label="filter containers"
          className="w-full rounded border-hairline border-neutral-300/70 bg-neutral-100/60 py-1 pl-9 pr-6 font-mono text-2xs text-neutral-700 placeholder:text-neutral-400 focus:outline-none focus:ring-1 focus:ring-neutral-300 dark:border-neutral-700/70 dark:bg-neutral-900/60 dark:text-neutral-200 dark:focus:ring-neutral-600"
        />
        {query && (
          <button
            type="button"
            onClick={() => {
              onQueryChange('')
              inputRef?.current?.focus()
            }}
            aria-label="clear filter"
            className="absolute right-1.5 top-1/2 -translate-y-1/2 font-mono text-2xs text-neutral-400 hover:text-neutral-700 dark:hover:text-neutral-200"
          >
            ✕
          </button>
        )}
      </div>

      <div className="ml-auto flex max-w-full shrink-0 flex-wrap items-center justify-end gap-x-3 gap-y-1">
        {onRun && (
          <button
            type="button"
            onClick={onRun}
            className="rounded border-hairline border-status-running/50 bg-status-running/10 px-2 py-0.5 font-mono text-2xs text-status-running hover:bg-status-running/20"
          >
            + Run container
          </button>
        )}
        <div
          role="tablist"
          aria-label="view"
          className="flex overflow-hidden rounded border-hairline border-neutral-300/70 dark:border-neutral-700/70"
        >
          {(['topology', 'list', 'stacks', 'resources', 'settings'] as const).map((v) => (
            <button
              key={v}
              type="button"
              role="tab"
              aria-selected={view === v}
              onClick={() => onViewChange(v)}
              className={[
                'px-2 py-0.5 font-mono text-2xs transition',
                view === v
                  ? 'bg-neutral-800 text-white dark:bg-neutral-200 dark:text-neutral-900'
                  : 'text-neutral-500 hover:text-neutral-800 dark:hover:text-neutral-200',
              ].join(' ')}
            >
              {v}
            </button>
          ))}
        </div>
        <span className="whitespace-nowrap font-mono text-2xs text-neutral-500">
          container {version || '—'}
        </span>
      </div>
    </header>
  )
}
