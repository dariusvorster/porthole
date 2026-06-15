import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useEffect, useRef, useState } from 'react'

type Status = 'connecting' | 'open' | 'ended' | 'unavailable'

/**
 * Interactive terminal (xterm.js) over one WebSocket to the exec endpoint.
 *
 * NO auto-reconnect (spec §4 — inverse of the SSE streams): a closed WS ends the
 * session for good. On close we show "session ended" + a manual New-session
 * button; we never reopen automatically. A new session starts only on the
 * New-session click or by re-showing the tab (remount).
 *
 * Note `running` is NOT an effect dependency: it gates the INITIAL open, but a
 * container stopping mid-session must surface as "session ended" (driven by the
 * WS close), not tear the terminal down to "container not running".
 */
export function ExecView({ id, running }: { id: string; running: boolean }) {
  const host = useRef<HTMLDivElement>(null)
  const [draftCmd, setDraftCmd] = useState('/bin/sh')
  const [cmd, setCmd] = useState('/bin/sh')
  const [session, setSession] = useState(0)
  const [status, setStatus] = useState<Status>(running ? 'connecting' : 'unavailable')

  useEffect(() => {
    if (!host.current) return
    if (!running) {
      setStatus('unavailable')
      return
    }
    setStatus('connecting')

    const enc = new TextEncoder()
    const term = new Terminal({
      fontFamily: '"IBM Plex Mono", ui-monospace, monospace',
      fontSize: 12,
      cursorBlink: true,
      theme: { background: '#0a0a0a' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(host.current)
    try {
      fit.fit()
    } catch {
      /* host not laid out yet */
    }

    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const cols = term.cols || 80
    const rows = term.rows || 24
    const ws = new WebSocket(
      `${proto}://${location.host}/api/containers/${encodeURIComponent(id)}/exec` +
        `?cmd=${encodeURIComponent(cmd)}&cols=${cols}&rows=${rows}`,
    )
    ws.binaryType = 'arraybuffer'

    const sendResize = () => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
      }
    }
    let resizeT: number | undefined
    const debouncedFit = () => {
      window.clearTimeout(resizeT)
      resizeT = window.setTimeout(() => {
        try {
          fit.fit()
        } catch {
          /* ignore */
        }
        sendResize()
      }, 120)
    }

    const dataSub = term.onData((d) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(d))
    })

    ws.onopen = () => {
      setStatus('open')
      try {
        fit.fit()
      } catch {
        /* ignore */
      }
      sendResize() // explicit initial resize (skip the 0×0 window)
      term.focus()
    }
    ws.onmessage = (e) => {
      if (typeof e.data === 'string') {
        try {
          if (JSON.parse(e.data)?.type === 'exit') setStatus('ended')
        } catch {
          /* ignore non-JSON control */
        }
        return
      }
      term.write(new Uint8Array(e.data as ArrayBuffer))
    }
    ws.onclose = () => setStatus('ended') // NO auto-reconnect
    ws.onerror = () => {}

    const ro = new ResizeObserver(debouncedFit)
    ro.observe(host.current)
    window.addEventListener('resize', debouncedFit)

    return () => {
      window.removeEventListener('resize', debouncedFit)
      window.clearTimeout(resizeT)
      ro.disconnect()
      dataSub.dispose()
      ws.close()
      term.dispose()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `running` intentionally excluded (see doc)
  }, [id, cmd, session])

  if (status === 'unavailable') {
    return (
      <div className="flex-1 p-3 text-2xs text-neutral-400">
        container not running — exec unavailable
      </div>
    )
  }

  const newSession = () => setSession((s) => s + 1)

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-neutral-200/70 px-3 py-1.5 dark:border-neutral-800/70">
        <input
          value={draftCmd}
          onChange={(e) => setDraftCmd(e.target.value)}
          aria-label="command"
          className="w-32 rounded border-hairline border-neutral-300/70 bg-neutral-100/60 px-1.5 py-0.5 font-mono text-2xs dark:border-neutral-700/70 dark:bg-neutral-900/60"
        />
        <button
          type="button"
          onClick={() => {
            setCmd(draftCmd)
            newSession()
          }}
          className="rounded border-hairline border-neutral-300/70 px-2 py-0.5 text-2xs hover:bg-neutral-50 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40"
        >
          run
        </button>
        <span className="font-mono text-2xs text-neutral-400">{status}</span>
      </div>

      <div ref={host} className="min-h-0 flex-1 overflow-hidden bg-[#0a0a0a] p-1" />

      {status === 'ended' && (
        <div className="absolute inset-x-0 bottom-0 flex items-center justify-between gap-2 border-t border-neutral-200/70 bg-white/95 px-3 py-2 text-2xs dark:border-neutral-800/70 dark:bg-neutral-900/95">
          <span className="font-mono text-neutral-500">— session ended —</span>
          <button
            type="button"
            onClick={newSession}
            className="rounded border-hairline border-neutral-300/70 px-2 py-0.5 hover:bg-neutral-50 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40"
          >
            New session
          </button>
        </div>
      )}
    </div>
  )
}
