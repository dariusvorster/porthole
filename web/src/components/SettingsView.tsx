import { useCallback, useEffect, useState } from 'react'
import { MutationError, getRegistry, registryLogin, registryLogout } from '../api/rest'
import type { RegistryAuth } from '../api/types'
import { EmptyState } from './EmptyState'
import { showToast } from '../lib/toast'

const TOKEN_PAGE = 'https://app.docker.com/settings/personal-access-tokens'

const field =
  'w-full rounded border-hairline border-neutral-300/70 bg-neutral-100/60 px-1.5 py-1 font-mono text-2xs dark:border-neutral-700/70 dark:bg-neutral-900/60'

/** registry-1.docker.io → "Docker Hub"; others shown as-is (host normalization). */
function displayHost(host: string): string {
  if (host === 'registry-1.docker.io' || host === 'docker.io') return 'Docker Hub'
  return host
}

/**
 * Settings → Registry (Phase 7). v1 of Settings contains ONLY this section. A
 * two-field login form (registry / username / "password or access token") with an
 * SSO hint, plus the list of current logins with a per-registry Log out. The token
 * is a masked input, posted in the body, never reflected or put in a URL — the
 * runtime owns the credential; Porthole never stores it.
 */
const KEYCHAIN_HINT_KEY = 'porthole.keychainHintDismissed'

export function SettingsView() {
  const [logins, setLogins] = useState<RegistryAuth[]>([])
  const [host, setHost] = useState('docker.io')
  const [username, setUsername] = useState('')
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Proactive keychain-stall hint (Phase 7b): shown once a login exists, until the
  // user dismisses it (persisted). Informational only — never blocks or nags.
  const [hintDismissed, setHintDismissed] = useState(() => {
    try {
      return localStorage.getItem(KEYCHAIN_HINT_KEY) === '1'
    } catch {
      return false
    }
  })
  const dismissHint = () => {
    setHintDismissed(true)
    try {
      localStorage.setItem(KEYCHAIN_HINT_KEY, '1')
    } catch {
      /* private mode — dismissal just won't persist */
    }
  }

  const reload = useCallback(() => {
    getRegistry()
      .then(setLogins)
      .catch(() => {
        /* transient; keep last good */
      })
  }, [])

  useEffect(() => {
    reload()
  }, [reload])

  const onLogin = async () => {
    if (!username.trim() || !token) return
    setBusy(true)
    setError(null)
    try {
      await registryLogin(host.trim() || 'docker.io', username.trim(), token)
      setToken('') // drop the secret from state immediately on success
      showToast(`Logged in to ${displayHost(host.trim() || 'docker.io')}`)
      reload()
    } catch (e) {
      // Friendly, scrubbed message (the server never echoes the token).
      setError(
        e instanceof MutationError
          ? 'Login failed — check your username and token. SSO/2FA accounts need an access token, not your password.'
          : String(e),
      )
    } finally {
      setBusy(false)
    }
  }

  const onLogout = async (h: string) => {
    setError(null)
    try {
      await registryLogout(h)
      showToast(`Logged out of ${displayHost(h)}`)
      reload()
    } catch (e) {
      setError(e instanceof MutationError ? e.message : String(e))
    }
  }

  return (
    <div className="mx-auto max-w-xl space-y-5 p-4">
      <h2 className="font-mono text-sm font-semibold">Settings</h2>

      <section className="space-y-3">
        <h3 className="font-mono text-2xs font-semibold uppercase tracking-wide text-neutral-500">
          registry
        </h3>

        {/* Login form */}
        <div className="space-y-2 rounded border-hairline border-neutral-300/70 p-3 dark:border-neutral-700/70">
          <div>
            <label className="mb-0.5 block font-mono text-2xs text-neutral-500">registry</label>
            <input
              value={host}
              onChange={(e) => setHost(e.target.value)}
              aria-label="registry"
              className={field}
            />
          </div>
          <div>
            <label className="mb-0.5 block font-mono text-2xs text-neutral-500">username</label>
            <input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              aria-label="username"
              className={field}
            />
          </div>
          <div>
            <label className="mb-0.5 block font-mono text-2xs text-neutral-500">
              password or access token
            </label>
            <input
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              autoComplete="off"
              aria-label="password or access token"
              className={field}
            />
            <p className="mt-1 font-mono text-2xs text-neutral-400">
              Using Google/SSO sign-in or two-factor auth?{' '}
              <a href={TOKEN_PAGE} target="_blank" rel="noreferrer" className="text-status-running underline">
                Generate an access token instead of your password →
              </a>
            </p>
          </div>
          {error && <div className="font-mono text-2xs text-status-danger" data-testid="login-error">{error}</div>}
          <button
            type="button"
            onClick={onLogin}
            disabled={busy || !username.trim() || !token}
            className="rounded border-hairline border-status-running/50 bg-status-running/10 px-3 py-0.5 font-mono text-2xs text-status-running hover:bg-status-running/20 disabled:opacity-40"
          >
            {busy ? 'logging in…' : 'Log in'}
          </button>
        </div>

        {/* Current logins */}
        <div>
          <h4 className="mb-1 font-mono text-2xs font-semibold uppercase tracking-wide text-neutral-500">
            logged in
          </h4>
          {logins.length > 0 && !hintDismissed && (
            <div
              className="mb-2 flex items-start gap-2 rounded border-hairline border-status-warn/40 bg-status-warn/5 px-2 py-1.5 font-sans text-2xs text-neutral-600 dark:text-neutral-300"
              data-testid="keychain-hint"
            >
              <span aria-hidden className="text-status-warn">
                ⚠
              </span>
              <span className="flex-1">
                First private pull needs a one-time keychain authorization. Run{' '}
                <code className="rounded bg-neutral-200/70 px-1 font-mono dark:bg-neutral-800/70">
                  container image pull &lt;your-image&gt;
                </code>{' '}
                in Terminal once and click <span className="font-semibold">Always Allow</span> — after that, Porthole
                pulls private images on its own.
              </span>
              <button
                type="button"
                onClick={dismissHint}
                aria-label="dismiss"
                className="font-mono text-neutral-400 hover:text-neutral-700 dark:hover:text-neutral-200"
              >
                ✕
              </button>
            </div>
          )}
          {logins.length === 0 ? (
            <EmptyState compact title="Not logged in to any registry" />
          ) : (
            <div className="space-y-1">
              {logins.map((l) => (
                <div
                  key={l.host}
                  className="flex items-center gap-2 rounded border-hairline border-status-running/30 bg-status-running/5 px-2 py-1 font-mono text-2xs"
                >
                  <span className="text-status-running">✓</span>
                  <span className="font-medium">{displayHost(l.host)}</span>
                  <span className="text-neutral-500">as {l.username}</span>
                  <button
                    type="button"
                    onClick={() => onLogout(l.host)}
                    className="ml-auto rounded border-hairline border-neutral-300/70 px-2 py-0.5 hover:bg-neutral-50 dark:border-neutral-700/70 dark:hover:bg-neutral-900/40"
                  >
                    Log out
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      </section>
    </div>
  )
}
