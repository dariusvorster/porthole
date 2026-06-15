import { useEffect, useRef, useState } from 'react'
import type { LogLine, LogsState } from '../api/useContainerLogs'

const lineClass: Record<LogLine['kind'], string> = {
  history: 'text-neutral-600 dark:text-neutral-400',
  log: 'text-neutral-800 dark:text-neutral-200',
  dropped: 'italic text-status-warn',
  terminal: 'italic text-neutral-500',
}

/**
 * The Logs pane: a dense mono scroll area (NOT xterm.js — output is plain text,
 * §7.5), with scroll-lock + jump-to-latest and a pause/resume toggle.
 */
export function LogsView({ logs, supervised = false }: { logs: LogsState; supervised?: boolean }) {
  const { lines, terminal, paused, setPaused } = logs
  const scrollRef = useRef<HTMLDivElement>(null)
  const pinnedRef = useRef(true) // auto-scroll while pinned to bottom
  const [showJump, setShowJump] = useState(false)

  // Auto-scroll to bottom on new lines, but only while pinned.
  useEffect(() => {
    const el = scrollRef.current
    if (el && pinnedRef.current) el.scrollTop = el.scrollHeight
  }, [lines])

  const onScroll = () => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24
    pinnedRef.current = atBottom
    setShowJump(!atBottom)
  }

  const jumpToLatest = () => {
    const el = scrollRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
    pinnedRef.current = true
    setShowJump(false)
  }

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-neutral-200/70 px-3 py-1.5 dark:border-neutral-800/70">
        <button
          type="button"
          disabled={terminal}
          onClick={() => setPaused(!paused)}
          className="rounded border-hairline border-neutral-300/70 px-2 py-0.5 text-2xs enabled:hover:bg-neutral-50 disabled:cursor-not-allowed disabled:text-neutral-400 dark:border-neutral-700/70 dark:enabled:hover:bg-neutral-900/40"
        >
          {paused ? 'Resume' : 'Pause'}
        </button>
        <span className="font-mono text-2xs text-neutral-400">
          {terminal
            ? supervised
              ? 'ended · may restart under its policy'
              : 'ended'
            : paused
              ? 'paused'
              : 'following'}
        </span>
      </div>

      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="min-h-0 flex-1 overflow-auto bg-neutral-50 p-2 font-mono text-[11px] leading-snug dark:bg-neutral-950"
      >
        {lines.length === 0 && !terminal ? (
          <div className="text-2xs text-neutral-400">connecting…</div>
        ) : (
          lines.map((l, i) => (
            <div key={i} className={`whitespace-pre-wrap break-all ${lineClass[l.kind]}`}>
              {l.text}
            </div>
          ))
        )}
      </div>

      {showJump && (
        <button
          type="button"
          onClick={jumpToLatest}
          className="absolute bottom-3 right-3 rounded border-hairline border-neutral-300/70 bg-white/90 px-2 py-0.5 text-2xs shadow dark:border-neutral-700/70 dark:bg-neutral-900/90"
        >
          ↓ jump to latest
        </button>
      )}
    </div>
  )
}
