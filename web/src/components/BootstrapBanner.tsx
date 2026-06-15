/**
 * Blocking bootstrap state (spec §6 P0): shown when apiServerRunning is false.
 * Replaces the whole content area so no stale data is visible. The "Start
 * services" button is intentionally disabled until the mutation endpoint exists
 * (Phase 1 / Part C).
 */
export function BootstrapBanner() {
  return (
    <div className="flex flex-1 items-center justify-center p-8">
      <div className="max-w-md rounded border-hairline border-status-warn/40 bg-status-warn/5 p-5 text-center">
        <div className="flex items-center justify-center gap-2 text-sm font-medium">
          <span className="inline-block h-2 w-2 rounded-full bg-status-warn" />
          container services are not running
        </div>
        <p className="mt-1.5 text-2xs text-neutral-500">
          Porthole can&apos;t reach the container apiserver. Start the services to continue.
        </p>
        <button
          type="button"
          disabled
          title="available in Phase 1"
          className="mt-4 cursor-not-allowed rounded border-hairline border-neutral-300/70 bg-neutral-100/60 px-3 py-1 text-2xs font-medium text-neutral-500 dark:border-neutral-700/70 dark:bg-neutral-900/60"
        >
          Start services
        </button>
      </div>
    </div>
  )
}
