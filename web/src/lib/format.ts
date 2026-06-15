/**
 * Compact uptime from an RFC3339 startedDate, running containers only. Returns
 * "" when not running, when the date is unparseable, or when it's the Go
 * zero-value (0001-01-01) — never render a year-1 duration.
 */
export function formatUptime(startedDate: string, running: boolean): string {
  if (!running) return ''
  const t = Date.parse(startedDate)
  if (Number.isNaN(t) || new Date(t).getUTCFullYear() < 2000) return ''
  let s = Math.max(0, Math.floor((Date.now() - t) / 1000))
  const d = Math.floor(s / 86400)
  s -= d * 86400
  const h = Math.floor(s / 3600)
  s -= h * 3600
  const m = Math.floor(s / 60)
  s -= m * 60
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

/**
 * Short display id: named containers use their name (id == name), but unnamed
 * ones get a 40-char UUID (§9.4). Truncate to the first 12 chars so the dense
 * UI never shows a 40-char string; the full id stays available via `title`.
 */
export function shortId(id: string): string {
  return id.length > 12 ? `${id.slice(0, 12)}…` : id
}

/** Human-readable byte size (binary units). 0/negative -> "0 B". */
export function formatBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let value = n
  let i = 0
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024
    i++
  }
  const digits = i === 0 || value >= 10 ? 0 : 1
  return `${value.toFixed(digits)} ${units[i]}`
}
