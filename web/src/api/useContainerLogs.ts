import { useEffect, useRef, useState } from 'react'

export type LogLineKind = 'history' | 'log' | 'dropped' | 'terminal'

export interface LogLine {
  kind: LogLineKind
  text: string
}

export interface LogsState {
  lines: LogLine[]
  terminal: boolean // a terminal `stopped` event was received
  paused: boolean
  setPaused: (p: boolean) => void
}

// Cap the rendered buffer so a long-running follow can't grow the DOM unbounded.
const MAX_LINES = 5000

/**
 * Streams a container's logs from the dedicated SSE endpoint while `active`.
 *
 * Two distinct reconnect cases, keyed on whether a terminal event was seen:
 *  - TERMINAL (`stopped`): the container stopped — we MUST es.close() and NOT
 *    reopen, or the browser's auto-reconnect loops history→stopped forever. We
 *    only reopen when id/active changes (tab-show or selection-change).
 *  - DROPPED connection (no terminal): let EventSource auto-reconnect; the next
 *    `history` batch RESETS the view (clear + re-render) — we accept the dupe.
 */
export function useContainerLogs(id: string | null, active: boolean): LogsState {
  const [lines, setLines] = useState<LogLine[]>([])
  const [terminal, setTerminal] = useState(false)
  const [paused, setPaused] = useState(false)

  const pausedRef = useRef(false)
  const pendingRef = useRef<LogLine[]>([]) // buffered while paused

  // Flush buffered lines on resume.
  useEffect(() => {
    pausedRef.current = paused
    if (!paused && pendingRef.current.length > 0) {
      const buffered = pendingRef.current
      pendingRef.current = []
      setLines((prev) => cap([...prev, ...buffered]))
    }
  }, [paused])

  useEffect(() => {
    if (!active || !id) return

    // Fresh open for this id: reset everything.
    setLines([])
    setTerminal(false)
    pendingRef.current = []

    const es = new EventSource(`/api/containers/${encodeURIComponent(id)}/logs`)

    const append = (newLines: LogLine[]) => {
      if (newLines.length === 0) return
      if (pausedRef.current) {
        pendingRef.current.push(...newLines)
        return
      }
      setLines((prev) => cap([...prev, ...newLines]))
    }

    es.addEventListener('history', (e) => {
      const data = JSON.parse((e as MessageEvent).data) as { lines?: string[] }
      // history RESETS the view — both on initial open and after a dropped
      // connection auto-reconnects.
      pendingRef.current = []
      setLines((data.lines ?? []).map((t) => ({ kind: 'history', text: t })))
    })

    es.addEventListener('log', (e) => {
      const data = JSON.parse((e as MessageEvent).data) as { lines?: string[] }
      append((data.lines ?? []).map((t) => ({ kind: 'log', text: t })))
    })

    es.addEventListener('dropped', (e) => {
      const data = JSON.parse((e as MessageEvent).data) as { count?: number }
      append([{ kind: 'dropped', text: `— ${data.count ?? 0} lines dropped —` }])
    })

    es.addEventListener('stopped', (e) => {
      const data = JSON.parse((e as MessageEvent).data) as { reason?: string }
      append([{ kind: 'terminal', text: `— ${data.reason ?? 'container stopped'} —` }])
      setTerminal(true)
      es.close() // CRITICAL: stop the browser auto-reconnect loop on a terminal
    })

    // On a real drop (not terminal) the browser auto-reconnects and the next
    // `history` resets the view — nothing to do here.
    es.onerror = () => {}

    return () => es.close()
  }, [id, active])

  return { lines, terminal, paused, setPaused }
}

function cap(lines: LogLine[]): LogLine[] {
  return lines.length > MAX_LINES ? lines.slice(lines.length - MAX_LINES) : lines
}
