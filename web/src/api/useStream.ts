import { useEffect, useReducer } from 'react'
import { initialState, reduce } from './reduce'
import type { StreamState } from './reduce'

export type { StreamState } from './reduce'

/**
 * Opens ONE EventSource('/api/stream') and maintains the live store via the
 * pure `reduce` function. The browser's EventSource auto-reconnects on drop;
 * the next `snapshot` re-seeds the store, so no special reconnect handling is
 * needed here.
 */
export function useStream(): StreamState {
  const [state, dispatch] = useReducer(reduce, initialState)

  useEffect(() => {
    const es = new EventSource('/api/stream')

    es.onopen = () => dispatch({ name: 'connected', data: true })
    es.onerror = () => dispatch({ name: 'connected', data: false })

    es.addEventListener('snapshot', (e) =>
      dispatch({ name: 'snapshot', data: JSON.parse((e as MessageEvent).data) }),
    )
    es.addEventListener('container.upserted', (e) =>
      dispatch({ name: 'container.upserted', data: JSON.parse((e as MessageEvent).data) }),
    )
    es.addEventListener('container.removed', (e) =>
      dispatch({ name: 'container.removed', data: JSON.parse((e as MessageEvent).data) }),
    )
    es.addEventListener('stats', (e) =>
      dispatch({ name: 'stats', data: JSON.parse((e as MessageEvent).data) }),
    )
    es.addEventListener('df', (e) =>
      dispatch({ name: 'df', data: JSON.parse((e as MessageEvent).data) }),
    )
    es.addEventListener('system', (e) =>
      dispatch({ name: 'system', data: JSON.parse((e as MessageEvent).data) }),
    )
    es.addEventListener('supervision', (e) =>
      dispatch({ name: 'supervision', data: JSON.parse((e as MessageEvent).data) }),
    )
    es.addEventListener('stack', (e) =>
      dispatch({ name: 'stack', data: JSON.parse((e as MessageEvent).data) }),
    )
    es.addEventListener('resource', (e) =>
      dispatch({ name: 'resource', data: JSON.parse((e as MessageEvent).data) }),
    )

    return () => es.close()
  }, [])

  return state
}
