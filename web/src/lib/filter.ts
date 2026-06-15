import type { Container } from '../api/types'

/**
 * Pure, I/O-free filter predicate (FILT1) — the testable core, like the Stacks
 * planner / supervisor.decide. A container matches when the (trimmed, lowercased)
 * query is empty OR is a substring of its name (id), image reference, or status.
 *
 * v0.1 is plain case-insensitive substring over name/image/status — no structured
 * grammar (`status:running`), no fuzzy matching (those are v1.1).
 */
export function matchesFilter(c: Container, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (q === '') return true
  const name = c.id.toLowerCase()
  const image = (c.configuration.image.reference ?? '').toLowerCase()
  const status = (c.status.state ?? '').toLowerCase()
  return name.includes(q) || image.includes(q) || status.includes(q)
}

/** Filter a container list by the query. An empty/whitespace query passes all. */
export function applyFilter(containers: Container[], query: string): Container[] {
  if (query.trim() === '') return containers
  return containers.filter((c) => matchesFilter(c, query))
}
