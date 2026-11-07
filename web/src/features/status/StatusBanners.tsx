import {useNow} from '@/hooks/use-now'
import {BellOff, Check, Copy, MoonStar, PauseCircle, RefreshCw, Unplug} from 'lucide-react'
import {useEffect, useState} from 'react'
import {Button} from '@/components/ui/button'
import {toast} from '@/components/ui/toaster'
import {Tooltip} from '@/components/ui/tooltip'
import {errorMessage} from '@/lib/api'
import {relTime} from '@/lib/format'
import {useHost} from '@/lib/platform'
import {invalidate, keys, useHealth} from '@/lib/queries'
import {RenameBar} from '@/features/whats-new/RenameBar'
import {useWhatsNewStatus} from '@/features/whats-new/store'
import type {LimitedProvider, NotifyStatus} from '@/lib/types'
import {notifyCopy} from './notify-copy'
import {
  activeLimit,
  limitCoverage,
  limitHeading,
  notificationsBlocked,
  splitLimits,
  SUDOERS_COMMAND,
  wakeNeedsSudoers,
} from './health'
import {openNotificationSettings} from './notification-settings'
import {StatusBar} from './StatusBar'

// The banners that speak for the machine rather than for a view: the daemon
// out of reach, a failed wake, notifications that go nowhere, the rate-limit
// gate. They sit above whichever view is open, so the shell mounts them once.
export function StatusBanners() {
  const health = useHealth()
  const h = health.data
  const whatsNew = useWhatsNewStatus().data

  if (health.isError)
    return (
      <DisconnectedBar
        error={health.error}
        retrying={health.isFetching}
        onRetry={() => void health.refetch()}
      />
    )
  if (!h) return null
  return (
    <div className="shrink-0">
      {h.wake_error && <WakeBar error={h.wake_error} />}
      {notificationsBlocked(h.notify_status) && <NotifyBar status={h.notify_status} />}
      {h.limited_until && <LimitBars until={h.limited_until} providers={h.limited_providers ?? []} />}
      {whatsNew?.rename_pending && !whatsNew.show && <RenameBar />}
    </div>
  )
}

// While the daemon is unreachable the other banners would only repeat stale
// news, so this one stands alone until health answers again.
function DisconnectedBar({
  error,
  retrying,
  onRetry,
}: {
  error: unknown
  retrying: boolean
  onRetry: () => void
}) {
  return (
    <StatusBar
      tone="danger"
      icon={<Unplug />}
      title="DispatchQ daemon not reachable"
      actions={
        <Button size="sm" variant="ghost" loading={retrying} onClick={onRetry}>
          {!retrying && <RefreshCw />}
          Retry
        </Button>
      }
    >
      What you see is what was last loaded, and changes cannot be saved until it is back. Checking again every
      30 s.
      <span className="ml-1.5 text-muted-foreground">{errorMessage(error)}</span>
    </StatusBar>
  )
}

// Surfaced so timed runs do not silently miss while the Mac sleeps.
function WakeBar({error}: {error: string}) {
  if (!wakeNeedsSudoers(error)) {
    return (
      <StatusBar tone="warning" icon={<MoonStar />} title="Scheduled wake failed">
        {error}
      </StatusBar>
    )
  }
  return (
    <StatusBar
      tone="warning"
      icon={<MoonStar />}
      title="Scheduled wake isn't set up"
      extra={<CommandToCopy command={SUDOERS_COMMAND} />}
    >
      Timed tasks may not run while your Mac is asleep. Allow DispatchQ to schedule wake-ups by running this
      once in Terminal; it takes effect immediately.
    </StatusBar>
  )
}

function CommandToCopy({command}: {command: string}) {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), 1600)
    return () => clearTimeout(t)
  }, [copied])

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command)
      setCopied(true)
    } catch {
      toast.error('Could not copy', {description: 'Select the command and copy it by hand.'})
    }
  }

  return (
    <div className="flex max-w-3xl items-center gap-1 rounded-md border border-border bg-surface py-0.5 pr-0.5 pl-2.5 shadow-xs">
      <code className="min-w-0 flex-1 truncate font-mono text-[11.5px] select-all">{command}</code>
      <Tooltip content={copied ? 'Copied' : 'Copy command'}>
        <Button size="icon-sm" variant="ghost" aria-label="Copy command" onClick={() => void copy()}>
          {copied ? <Check className="text-success" /> : <Copy />}
        </Button>
      </Tooltip>
    </div>
  )
}

function NotifyBar({status}: {status: NotifyStatus}) {
  const host = useHost().data
  const copy = notifyCopy(host)
  return (
    <StatusBar
      tone="warning"
      icon={<BellOff />}
      title={copy.blockedTitle(status)}
      actions={
        <Button size="sm" variant="secondary" onClick={() => void openNotificationSettings(host)}>
          Open {copy.settingsLabel}
        </Button>
      }
    >
      {copy.mac ? (
        <>
          {status === 'denied' ? (
            <>
              Run outcomes and new artifacts are being announced, but macOS is set to hide them. Turn{' '}
              <strong>Allow notifications</strong> back on for DispatchQ.
            </>
          ) : (
            <>
              DispatchQ hasn't been allowed to notify you yet, so run outcomes and new artifacts stay silent.
            </>
          )}{' '}
          Pick <strong>Alerts</strong> while you're there and an alert stays on screen until you click it.
        </>
      ) : (
        copy.blockedBody(status)
      )}
    </StatusBar>
  )
}

// A used-up credit balance gets no bar, just as a used-up week gets none: the
// Allowance strip says it on the provider's line, and the scheduler passes the
// provider over meanwhile. Only an ordinary rate limit is announced here.
function LimitBars({until, providers}: {until: string; providers: LimitedProvider[]}) {
  const now = useNow(15_000)

  // Ask again the moment the gate should reopen instead of waiting for the
  // next 30 s poll; the views showing resume times read the same answer.
  useEffect(() => {
    const ms = new Date(until).getTime() - Date.now()
    if (!(ms > 0)) return
    const t = setTimeout(() => void invalidate(keys.health, keys.tasks, keys.runs), ms + 500)
    return () => clearTimeout(t)
  }, [until])

  const split = splitLimits(providers, until)
  return split.until ? <LimitBar until={split.until} providers={split.rateLimited} now={now} /> : null
}

// The queue standing still on a rate limit is normal, not a fault: say what it
// is waiting for, and until when, so a paused queue is never mistaken for a
// stuck one.
function LimitBar({until, providers, now}: {until: string; providers: LimitedProvider[]; now: number}) {
  const at = activeLimit(until, now)
  if (!at) return null
  const coverage = limitCoverage(providers)
  const when = (
    <>
      <strong className="text-foreground">{at.toLocaleString()}</strong> ({relTime(at, now)})
    </>
  )
  return (
    <StatusBar
      tone="info"
      icon={<PauseCircle />}
      title={limitHeading(providers)}
      extra={providers.length > 1 && <ProviderTimes providers={providers} now={now} />}
    >
      {coverage === 'none' && <>No task starts before {when}. </>}
      {coverage === 'all' && (
        <>Tasks keep running on the fallback provider. The blocked one takes them back from {when}. </>
      )}
      {coverage === 'some' && <>Tasks with a fallback keep running; the rest start again from {when}. </>}
      {/* A run a fallback took over is not continued afterwards: it was
          started again from the beginning over there. */}
      {coverage === 'all' ? (
        <>The run the limit interrupted is not resumed: the fallback did the work from the start.</>
      ) : (
        <>
          A run the limit interrupted is marked <strong>rescheduled</strong> in the Log and continues its
          Claude session then, or drop it there with <strong>Cancel resume</strong>.
        </>
      )}
    </StatusBar>
  )
}

// With several providers blocked, each reopens on its own clock.
function ProviderTimes({providers, now}: {providers: LimitedProvider[]; now: number}) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {providers.map((p) => {
        const at = activeLimit(p.until, now)
        return (
          <Tooltip key={p.name} content={at ? at.toLocaleString() : 'Reopens on the next check'}>
            <span
              tabIndex={0}
              className="inline-flex items-center gap-1.5 rounded-full border border-border bg-surface px-2 py-0.5 text-[11.5px] shadow-xs"
            >
              <span className="font-medium">{p.name}</span>
              {p.fallback && <span className="text-muted-foreground">→ {p.fallback}</span>}
              <span className="text-muted-foreground">· {at ? relTime(at, now) : 'reopening'}</span>
            </span>
          </Tooltip>
        )
      })}
    </div>
  )
}
