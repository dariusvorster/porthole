import { describe, expect, it } from 'vitest'
import { initialState, reduce } from './reduce'
import type { Container, DiskUsage, StatsSample } from './types'

function container(id: string, state = 'running'): Container {
  return {
    id,
    configuration: {
      id,
      creationDate: '',
      image: { reference: `img/${id}` },
      networks: [],
      resources: { cpus: 1, cpuOverhead: 0, memoryInBytes: 0 },
      labels: {},
      publishedPorts: [],
    },
    status: { state, startedDate: '', networks: [] },
  }
}

function sample(id: string, cpuPercent: number): StatsSample {
  return { id, cpuPercent, memoryPercent: 1, memoryUsageBytes: 100, numProcesses: 1 }
}

function disk(active: number): DiskUsage {
  const cat = { active, total: active, sizeInBytes: 0, reclaimable: 0 }
  return { containers: cat, images: cat, volumes: cat }
}

const keys = (s: { containers: Map<string, Container> }) => [...s.containers.keys()].sort()

describe('reduce — SSE store integrity', () => {
  it('replays snapshot/upsert/stats/remove/reseed/system with no ghosts and no flicker', () => {
    let s = initialState

    // a. snapshot [A,B] -> store has A,B
    s = reduce(s, {
      name: 'snapshot',
      data: { containers: [container('A'), container('B')], diskUsage: disk(2), apiServerRunning: true },
    })
    expect(keys(s)).toEqual(['A', 'B'])

    // b. container.upserted C -> store has A,B,C
    s = reduce(s, { name: 'container.upserted', data: { container: container('C') } })
    expect(keys(s)).toEqual(['A', 'B', 'C'])

    // c. stats sample for A -> A's stats present; B and C container objects keep
    //    referential identity (a stats update must NOT replace container entries).
    const bBefore = s.containers.get('B')!
    const cBefore = s.containers.get('C')!
    s = reduce(s, { name: 'stats', data: { samples: [sample('A', 12.5)] } })
    expect(s.stats.get('A')?.cpuPercent).toBe(12.5)
    expect(s.containers.get('B')).toBe(bBefore) // SAME object ref = no flicker
    expect(s.containers.get('C')).toBe(cBefore)

    // d. container.removed A -> store has B,C (and A's stats are cleared too)
    s = reduce(s, { name: 'container.removed', data: { id: 'A' } })
    expect(keys(s)).toEqual(['B', 'C'])
    expect(s.stats.has('A')).toBe(false)

    // e. SECOND snapshot [B] only -> store has ONLY B; A and C gone; diskUsage
    //    and apiServerRunning reset from it. (reconnect-reseed guarantee.)
    s = reduce(s, {
      name: 'snapshot',
      data: { containers: [container('B')], diskUsage: disk(1), apiServerRunning: true },
    })
    expect(keys(s)).toEqual(['B'])
    expect(s.containers.has('A')).toBe(false)
    expect(s.containers.has('C')).toBe(false)
    expect(s.diskUsage?.containers.active).toBe(1)
    expect(s.apiServerRunning).toBe(true)

    // f. system {apiServerRunning:false} -> flag flips, container map untouched.
    const mapBefore = s.containers
    s = reduce(s, { name: 'system', data: { apiServerRunning: false } })
    expect(s.apiServerRunning).toBe(false)
    expect(s.containers).toBe(mapBefore)
  })
})
