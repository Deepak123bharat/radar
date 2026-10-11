import { clsx } from 'clsx'
import { AlertTriangle, Bug, RefreshCw } from 'lucide-react'
import { timelineDatabaseError } from '../../api/client'

const SECONDARY_BUTTON =
  'flex items-center gap-2 px-3 py-1.5 text-sm bg-theme-elevated border border-theme-border-light rounded-lg hover:bg-theme-hover transition-colors'

// TimelineLoadError is the full-screen state when the timeline has nothing to
// show. An unreachable PostgreSQL database gets its own copy: the operator has
// to know it is the database, not the cluster, and that waiting is enough once
// it is fixed.
export function TimelineLoadError({
  error,
  onRetry,
  retrying = false,
  onOpenDiagnostics,
  className,
}: {
  error?: Error | null
  onRetry: () => void
  /** A request is in flight: the error stays up, the button shows the attempt. */
  retrying?: boolean
  onOpenDiagnostics?: () => void
  className?: string
}) {
  const databaseError = timelineDatabaseError(error)
  const detail = error?.message?.trim()

  return (
    <div className={clsx('flex flex-col items-center justify-center text-theme-text-tertiary gap-3 px-6', className)}>
      <AlertTriangle className="w-10 h-10 text-amber-400/70" />
      {databaseError !== null ? (
        <>
          <p className="text-base text-theme-text-secondary">Timeline history is unavailable</p>
          <p className="max-w-md text-center text-sm">
            Radar can't reach its PostgreSQL database. It keeps retrying, and this page recovers on its own
            once the database is back. Changes made until then won't appear in the timeline.
          </p>
          {databaseError && (
            <div className="w-full max-w-md bg-theme-elevated border border-theme-border rounded-md p-3 overflow-auto max-h-32">
              <code className="text-xs text-theme-text-tertiary font-mono whitespace-pre-wrap break-words">
                {databaseError}
              </code>
            </div>
          )}
        </>
      ) : (
        <>
          <p className="text-base">Failed to load timeline data</p>
          {detail && <p className="max-w-md text-center text-sm">{detail}</p>}
        </>
      )}
      <div className="flex items-center gap-2">
        <button type="button" onClick={onRetry} disabled={retrying} className={clsx(SECONDARY_BUTTON, 'disabled:opacity-60 disabled:cursor-default')}>
          <RefreshCw className={clsx('w-3.5 h-3.5', retrying && 'animate-spin')} />
          Try again
        </button>
        {databaseError !== null && onOpenDiagnostics && (
          <button type="button" onClick={onOpenDiagnostics} className={SECONDARY_BUTTON}>
            <Bug className="w-3.5 h-3.5" />
            Open diagnostics
          </button>
        )}
      </div>
    </div>
  )
}
