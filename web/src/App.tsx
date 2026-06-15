import { useCallback, useEffect, useRef, useState } from 'react'
import { ACTION_LABEL, type Action, type ActionState } from './api/actions'
import { isRunning } from './api/helpers'
import { applyFilter } from './lib/filter'
import {
  MutationError,
  deleteContainer,
  killContainer,
  restartContainer,
  startContainer,
  stopContainer,
} from './api/rest'
import { useNetworks } from './api/useNetworks'
import { useResources } from './api/useResources'
import { useStacks } from './api/useStacks'
import { useStream } from './api/useStream'
import { useSystemVersion } from './api/useSystemVersion'
import { BootstrapBanner } from './components/BootstrapBanner'
import { CreateForm } from './components/CreateForm'
import { EmptyState } from './components/EmptyState'
import { HostRail } from './components/HostRail'
import { Inspector } from './components/Inspector'
import { ListView } from './components/ListView'
import { ResourcesView } from './components/ResourcesView'
import { SettingsView } from './components/SettingsView'
import { StacksView } from './components/StacksView'
import { Toaster } from './components/Toaster'
import { TopBar, type View } from './components/TopBar'
import { TopologyView } from './components/TopologyView'

const RUNNERS: Record<Action, (id: string) => Promise<void>> = {
  start: startContainer,
  stop: stopContainer,
  restart: restartContainer,
  kill: killContainer,
  delete: (id) => deleteContainer(id),
}

export default function App() {
  const { containers, stats, supervision, diskUsage, apiServerRunning, stackEpoch, resourceEpoch } =
    useStream()
  const version = useSystemVersion(apiServerRunning)
  const networks = useNetworks(apiServerRunning, resourceEpoch)
  const { stacks, refresh: refreshStacks } = useStacks(apiServerRunning, stackEpoch)
  const { bundle: resources, refresh: refreshResources } = useResources(apiServerRunning, resourceEpoch)

  const [view, setView] = useState<View>('topology')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [actions, setActions] = useState<Map<string, ActionState>>(new Map())
  const [showCreate, setShowCreate] = useState(false)
  const [query, setQuery] = useState('')
  const filterRef = useRef<HTMLInputElement>(null)

  // ⌘K (or Ctrl-K) focuses the filter from anywhere — but NOT while a text input
  // or the exec terminal is capturing keys (it needs its own; FILT3).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key.toLowerCase() !== 'k') return
      const ae = document.activeElement as HTMLElement | null
      const capturing =
        ae !== filterRef.current &&
        (ae?.tagName === 'INPUT' ||
          ae?.tagName === 'TEXTAREA' ||
          !!ae?.isContentEditable ||
          !!ae?.closest?.('.xterm'))
      if (capturing) return
      e.preventDefault()
      filterRef.current?.focus()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const setAction = useCallback((id: string, state: ActionState | null) => {
    setActions((prev) => {
      const next = new Map(prev)
      if (state) next.set(id, state)
      else next.delete(id)
      return next
    })
  }, [])

  // Fire a mutation optimistically: show pending now, let the SSE stream deliver
  // the real state (202 → do nothing further), roll back to an error on failure.
  const runAction = useCallback(
    async (id: string, action: Action) => {
      setAction(id, { phase: 'pending', action, label: ACTION_LABEL[action] })
      try {
        await RUNNERS[action](id)
      } catch (e) {
        const error =
          e instanceof MutationError
            ? { kind: e.kind, message: e.message, raw: e.raw }
            : { kind: 'unknown', message: String(e) }
        setAction(id, { phase: 'error', action, error })
      }
    },
    [setAction],
  )

  // Reconcile pending against the authoritative stream: once the observed state
  // matches intent (or the container is gone for a delete), clear pending. The
  // stream always wins — optimistic state never overrides it.
  useEffect(() => {
    setActions((prev) => {
      let changed = false
      const next = new Map(prev)
      for (const [id, st] of prev) {
        if (st.phase !== 'pending') continue
        const c = containers.get(id)
        const settled =
          st.action === 'delete'
            ? !c
            : c !== undefined &&
              (st.action === 'start' || st.action === 'restart' ? isRunning(c) : !isRunning(c))
        if (settled) {
          next.delete(id)
          changed = true
        }
      }
      return changed ? next : prev
    })
  }, [containers])

  // Close the inspector if its container was removed (e.g. completed delete).
  useEffect(() => {
    if (selectedId && !containers.has(selectedId)) setSelectedId(null)
  }, [containers, selectedId])

  const list = [...containers.values()]
  const filtered = applyFilter(list, query)
  const noMatch = query.trim() !== '' && filtered.length === 0
  const running = list.filter(isRunning).length
  const stopped = list.filter((c) => c.status.state === 'stopped').length
  const other = list.length - running - stopped
  const diskUsed = diskUsage
    ? diskUsage.containers.sizeInBytes + diskUsage.images.sizeInBytes + diskUsage.volumes.sizeInBytes
    : 0

  const selected = selectedId ? containers.get(selectedId) : undefined
  const selectedSample = selectedId ? stats.get(selectedId) : undefined
  const selectedAction = selectedId ? actions.get(selectedId) : undefined
  const selectedSupervision = selectedId ? supervision.get(selectedId) : undefined

  return (
    <div className="flex h-full flex-col">
      <TopBar
        version={version}
        view={view}
        onViewChange={setView}
        onRun={apiServerRunning ? () => setShowCreate(true) : undefined}
        query={query}
        onQueryChange={setQuery}
        inputRef={filterRef}
      />

      {apiServerRunning ? (
        <div className="flex flex-1 overflow-hidden">
          <HostRail
            version={version}
            running={running}
            stopped={stopped}
            other={other}
            diskUsed={diskUsed}
          />
          <main className="flex-1 overflow-auto">
            {view === 'stacks' ? (
              <StacksView stacks={stacks} onChanged={refreshStacks} />
            ) : view === 'resources' ? (
              <ResourcesView bundle={resources} liveDiskUsage={diskUsage} onChanged={refreshResources} />
            ) : view === 'settings' ? (
              <SettingsView />
            ) : list.length === 0 ? (
              <EmptyState
                title="No containers yet"
                hint="Run one to get started — or import a stack."
                action={{ label: '+ Run your first container', onClick: () => setShowCreate(true) }}
              />
            ) : noMatch ? (
              <EmptyState
                title={`No containers match “${query.trim()}”`}
                hint="No name, image, or status matches your filter."
                action={{ label: 'Clear filter', onClick: () => setQuery('') }}
              />
            ) : view === 'topology' ? (
              <TopologyView
                containers={list}
                networks={networks}
                actions={actions}
                supervision={supervision}
                selectedId={selectedId}
                onSelect={setSelectedId}
                query={query}
              />
            ) : (
              <div className="p-4">
                <ListView containers={filtered} stats={stats} supervision={supervision} />
              </div>
            )}
          </main>
          {view !== 'stacks' && view !== 'resources' && view !== 'settings' && selectedId && (
            <Inspector
              container={selected}
              sample={selectedSample}
              action={selectedAction}
              supervision={selectedSupervision}
              onAction={(a) => runAction(selectedId, a)}
              onDismissError={() => setAction(selectedId, null)}
              onClose={() => setSelectedId(null)}
            />
          )}
        </div>
      ) : (
        <BootstrapBanner />
      )}

      {showCreate && (
        <CreateForm
          networks={networks}
          onClose={() => setShowCreate(false)}
          onCreated={() => setShowCreate(false)}
          onOpenRegistry={() => {
            setShowCreate(false)
            setView('settings')
          }}
        />
      )}

      <Toaster />
    </div>
  )
}
