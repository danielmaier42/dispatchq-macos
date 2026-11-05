import {MoreHorizontal, Pencil, RefreshCw, Star, Trash2} from 'lucide-react'
import {useState, type ReactNode} from 'react'
import {ProviderLogoTile} from '@/components/domain/provider-logo'
import {Badge} from '@/components/ui/badge'
import {Button} from '@/components/ui/button'
import {Card} from '@/components/ui/card'
import {confirm} from '@/components/ui/confirm'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {Switch} from '@/components/ui/switch'
import {Tooltip} from '@/components/ui/tooltip'
import {fullTime, relTime} from '@/lib/format'
import {modelLabel} from '@/lib/providers'
import {useModels} from '@/lib/queries'
import type {Provider, ProviderLimits as Limits} from '@/lib/types'
import {cn} from '@/lib/utils'
import {checkProvider, makeDefault, removeProvider, setEnabled} from './actions'
import {ProviderEditDialog} from './ProviderEditDialog'
import {ProviderLimits} from './ProviderLimits'
import {hasLimitsRow, healthInfo} from './provider-logic'

// ProviderCard is the only place a provider's binary, configuration directory
// and default model live, so there is exactly one answer to "which CLI does
// DispatchQ run?". The card shows them; the Edit dialog changes them.
export function ProviderCard({
  provider: p,
  providers,
  limits,
}: {
  provider: Provider
  providers: Provider[]
  limits?: Limits
}) {
  const [editing, setEditing] = useState(false)
  const [checking, setChecking] = useState(false)
  const models = useModels(p.id).data
  const fbModels = useModels(p.fallback_provider).data
  const health = healthInfo(p.health.state)
  const reason = p.health.reason || p.health.detail
  const fallback = p.fallback_provider ? providers.find((x) => x.id === p.fallback_provider) : undefined
  const label = p.name || p.id

  const check = async () => {
    setChecking(true)
    await checkProvider(p)
    setChecking(false)
  }
  const remove = async () => {
    const ok = await confirm({
      title: `Remove the provider “${label}”?`,
      description: 'DispatchQ forgets its settings. The CLI, its account and its sessions stay untouched.',
      confirmLabel: 'Remove',
      cancelLabel: 'Keep it',
      destructive: true,
    })
    if (ok) await removeProvider(p)
  }

  return (
    <Card className="group/card overflow-hidden">
      <div className="flex items-start gap-3 px-4 pt-3.5 pb-3">
        <ProviderLogoTile kind={p.kind} className={cn('mt-0.5', !p.enabled && 'opacity-50')} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <h3 className={cn('text-[13.5px] font-semibold', !p.enabled && 'text-muted-foreground')}>
              {label}
            </h3>
            {p.default && (
              <Tooltip content="Tasks that name no provider run here">
                <Badge tone="accent">
                  <Star /> Default
                </Badge>
              </Tooltip>
            )}
          </div>
          <div className="mt-1.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[12.5px]">
            <Tooltip content={p.health.checked_at ? 'Checked ' + fullTime(p.health.checked_at) : undefined}>
              <Badge tone={health.tone} dot>
                {health.label}
              </Badge>
            </Tooltip>
            {reason && <span className="min-w-0 text-muted-foreground">{reason}</span>}
            {!reason && p.health.checked_at && (
              <span className="text-subtle-foreground">checked {relTime(p.health.checked_at)}</span>
            )}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button variant="ghost" size="sm" onClick={check} loading={checking}>
            {!checking && <RefreshCw />}
            Check
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setEditing(true)}>
            <Pencil />
            Edit
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon-sm" aria-label={`More actions for ${label}`}>
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent>
              <DropdownMenuItem disabled={p.default || !p.enabled} onSelect={() => void makeDefault(p)}>
                <Star />
                {p.default
                  ? 'Already the default'
                  : p.enabled
                    ? 'Make default'
                    : 'Make default (switched off)'}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem destructive disabled={p.default} onSelect={() => void remove()}>
                <Trash2 />
                {p.default ? 'Remove (choose another default first)' : 'Remove…'}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <Tooltip content={p.enabled ? 'Switched on: takes tasks' : 'Switched off: takes no tasks'}>
            <span className="ml-1.5 inline-flex">
              <Switch
                checked={p.enabled}
                onCheckedChange={(v) => void setEnabled(p, v)}
                aria-label={`${label} switched on`}
              />
            </span>
          </Tooltip>
        </div>
      </div>

      <dl
        className={cn(
          'grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] gap-x-4 gap-y-1.5 border-t border-border bg-surface-2/40 px-4 py-3 text-[12.5px]',
          !p.enabled && 'opacity-70',
        )}
      >
        <Detail label="Id">
          <span className="font-mono">{p.id}</span>
        </Detail>
        <Detail label="Binary">
          {p.binary_path ? (
            <span className="font-mono">{p.binary_path}</span>
          ) : (
            <span className="text-muted-foreground">
              Auto-detect
              {(p.health.binary || p.detected) && (
                <>
                  {' '}
                  · <span className="font-mono">{p.health.binary || p.detected}</span>
                </>
              )}
            </span>
          )}
        </Detail>
        <Detail label="Configuration">
          {p.config_dir ? (
            <span className="font-mono">{p.config_dir}</span>
          ) : (
            <span className="text-muted-foreground">
              CLI default
              {p.default_config_dir && (
                <>
                  {' '}
                  · <span className="font-mono">{p.default_config_dir}</span>
                </>
              )}
            </span>
          )}
        </Detail>
        {p.env?.length > 0 && (
          <Detail label="Environment">
            <span className="font-mono">{p.env.join(', ')}</span>
          </Detail>
        )}
        <Detail label="Default model">
          {p.default_model ? (
            modelLabel(models, p.default_model)
          ) : (
            <span className="text-muted-foreground">Provider default</span>
          )}
        </Detail>
        <Detail label="At the limit">
          {!p.fallback_provider ? (
            <span className="text-muted-foreground">Wait for the limit</span>
          ) : fallback ? (
            <>
              Run on {fallback.name || fallback.id}
              {!fallback.enabled && <span className="text-muted-foreground"> (switched off)</span>}
              <span className="text-muted-foreground">
                {' · '}
                {p.fallback_model ? modelLabel(fbModels, p.fallback_model) : 'model decided by DispatchQ'}
              </span>
            </>
          ) : (
            <Tooltip content="The provider this one falls back to is no longer configured. Saving keeps it until you choose another.">
              <span className="text-warning">{p.fallback_provider} (unavailable)</span>
            </Tooltip>
          )}
        </Detail>
      </dl>

      {hasLimitsRow(limits) && (
        <div className="border-t border-border px-4 py-3">
          <ProviderLimits limits={limits} />
        </div>
      )}

      <ProviderEditDialog provider={p} open={editing} onOpenChange={setEditing} />
    </Card>
  )
}

function Detail({label, children}: {label: string; children: ReactNode}) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 [overflow-wrap:anywhere]">{children}</dd>
    </>
  )
}
