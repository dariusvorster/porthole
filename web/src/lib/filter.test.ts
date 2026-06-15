import { describe, expect, it } from 'vitest'
import type { Container } from '../api/types'
import { applyFilter, matchesFilter } from './filter'

function mk(id: string, image: string, state: string): Container {
  return {
    id,
    configuration: {
      id,
      creationDate: '',
      image: { reference: image },
      networks: [],
      resources: { cpus: 0, cpuOverhead: 0, memoryInBytes: 0 },
      labels: {},
      publishedPorts: [],
    },
    status: { state, startedDate: '', networks: [] },
  } as Container
}

const web = mk('web', 'docker.io/library/nginx:latest', 'running')
const api = mk('api', 'docker.io/library/node:20-alpine', 'stopped')
const all = [web, api]

describe('matchesFilter', () => {
  it('empty query passes everything', () => {
    expect(matchesFilter(web, '')).toBe(true)
  })
  it('all-whitespace query is treated as empty', () => {
    expect(matchesFilter(api, '   ')).toBe(true)
  })
  it('matches on name (id)', () => {
    expect(matchesFilter(web, 'web')).toBe(true)
    expect(matchesFilter(api, 'web')).toBe(false)
  })
  it('matches on image ref', () => {
    expect(matchesFilter(web, 'nginx')).toBe(true)
    expect(matchesFilter(api, 'nginx')).toBe(false)
  })
  it('matches on status', () => {
    expect(matchesFilter(web, 'running')).toBe(true)
    expect(matchesFilter(web, 'stopped')).toBe(false)
    expect(matchesFilter(api, 'stopped')).toBe(true)
  })
  it('is case-insensitive', () => {
    expect(matchesFilter(web, 'NGINX')).toBe(true)
    expect(matchesFilter(web, 'Running')).toBe(true)
  })
  it('no match returns false', () => {
    expect(matchesFilter(web, 'zzz')).toBe(false)
  })
})

describe('applyFilter', () => {
  it('empty query returns all', () => {
    expect(applyFilter(all, '')).toEqual(all)
    expect(applyFilter(all, '  ')).toEqual(all)
  })
  it('narrows by name', () => {
    expect(applyFilter(all, 'web')).toEqual([web])
  })
  it('narrows by status', () => {
    expect(applyFilter(all, 'running')).toEqual([web])
    expect(applyFilter(all, 'stopped')).toEqual([api])
  })
  it('narrows by image', () => {
    expect(applyFilter(all, 'node')).toEqual([api])
  })
  it('no match returns empty', () => {
    expect(applyFilter(all, 'zzz')).toEqual([])
  })
})
