import type { SupervisionHealthConfig } from '../api/types'

/**
 * The ONE health-probe control + its draft<->API mapping, shared by the inspector
 * (PolicySection) and the create form so there's a single health model with two
 * entry points (Phase 9). The probe shape is HTTP/TCP + port + path + interval —
 * the same `porthole.health.*` the supervisor adopts.
 */
export interface HealthDraft {
  type: 'none' | 'http' | 'tcp'
  port: string
  path: string
  interval: string
}

/** API health body shape (matches PolicyBody.health / CreateSpec.health). */
export interface HealthBody {
  type: string
  port: number
  path?: string
  interval?: number
}

export function emptyHealth(): HealthDraft {
  return { type: 'none', port: '', path: '/', interval: '' }
}

/** Pre-fill a draft from the probe carried on the supervision stream. */
export function healthFromConfig(hc?: SupervisionHealthConfig): HealthDraft {
  const t = hc?.type === 'http' || hc?.type === 'tcp' ? hc.type : 'none'
  return {
    type: t,
    port: hc?.port ? String(hc.port) : '',
    path: hc?.path ?? '/',
    interval: hc?.interval ? String(hc.interval) : '',
  }
}

/** Map a draft to the API body, or undefined when the probe is disabled. */
export function healthToBody(h: HealthDraft): HealthBody | undefined {
  if (h.type === 'none') return undefined
  return {
    type: h.type,
    port: Number(h.port) || 0,
    path: h.type === 'http' ? h.path || undefined : undefined,
    interval: Number(h.interval) || undefined,
  }
}

/** Inline validation; null when valid (or disabled). */
export function validateHealth(h: HealthDraft): string | null {
  if (h.type === 'none') return null
  const p = Number(h.port)
  if (!Number.isInteger(p) || p < 1 || p > 65535) return 'health port must be 1–65535'
  if (h.interval) {
    const iv = Number(h.interval)
    if (!Number.isInteger(iv) || iv < 1) return 'health interval must be a positive number of seconds'
  }
  return null
}

/** Controlled health fields. `inputClass` lets each host match its surroundings. */
export function HealthFields({
  value,
  onChange,
  inputClass,
}: {
  value: HealthDraft
  onChange: (h: HealthDraft) => void
  inputClass: string
}) {
  const set = (patch: Partial<HealthDraft>) => onChange({ ...value, ...patch })
  return (
    <>
      <label className="flex items-center justify-between gap-2">
        <span className="text-neutral-500">health</span>
        <select
          value={value.type}
          onChange={(e) => set({ type: e.target.value as HealthDraft['type'] })}
          aria-label="health type"
          className={inputClass}
        >
          <option value="none">none</option>
          <option value="http">http</option>
          <option value="tcp">tcp</option>
        </select>
      </label>
      {value.type !== 'none' && (
        <>
          <label className="flex items-center justify-between gap-2">
            <span className="text-neutral-500">port</span>
            <input
              value={value.port}
              onChange={(e) => set({ port: e.target.value })}
              inputMode="numeric"
              placeholder="80"
              aria-label="health port"
              className={inputClass}
            />
          </label>
          {value.type === 'http' && (
            <label className="flex items-center justify-between gap-2">
              <span className="text-neutral-500">path</span>
              <input
                value={value.path}
                onChange={(e) => set({ path: e.target.value })}
                placeholder="/"
                aria-label="health path"
                className={inputClass}
              />
            </label>
          )}
          <label className="flex items-center justify-between gap-2">
            <span className="text-neutral-500">interval s</span>
            <input
              value={value.interval}
              onChange={(e) => set({ interval: e.target.value })}
              inputMode="numeric"
              placeholder="30"
              aria-label="health interval"
              className={inputClass}
            />
          </label>
        </>
      )}
    </>
  )
}
