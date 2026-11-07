import {Layers} from 'lucide-react'
import {useState, type FormEvent} from 'react'
import {Alert} from '@/components/ui/alert'
import {Button} from '@/components/ui/button'
import {Checkbox} from '@/components/ui/checkbox'
import {
  Dialog,
  DialogBody,
  DialogClose,
  DialogContent,
  DialogFooter,
  DialogHeader,
} from '@/components/ui/dialog'
import {Field} from '@/components/ui/field'
import {Input} from '@/components/ui/input'
import {Label} from '@/components/ui/label'
import {toast} from '@/components/ui/toaster'
import {errorMessage, post, put} from '@/lib/api'
import {executionLabel, useProviderList} from '@/lib/providers'
import {invalidate, keys, useLimits} from '@/lib/queries'
import type {Pool, PoolInput} from '@/lib/types'
import {cn} from '@/lib/utils'
import {formatWeight, membersBody, poolKind, weightPlaceholder} from './pool-logic'

// PoolDialog adds a pool (pool undefined) or edits one: its name and which
// providers are in it with what weight.
export function PoolDialog({
  pool,
  open,
  onOpenChange,
}: {
  pool?: Pool
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        {open && <PoolForm pool={pool} onDone={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function PoolForm({pool, onDone}: {pool?: Pool; onDone: () => void}) {
  const providers = useProviderList()
  const limits = useLimits().data
  const [id, setId] = useState('')
  const [name, setName] = useState(pool?.name ?? '')
  const [checked, setChecked] = useState(() => new Set(pool?.members.map((m) => m.provider)))
  const [weights, setWeights] = useState<Record<string, string>>(() =>
    Object.fromEntries(
      (pool?.members ?? []).map((m) => [m.provider, m.weight ? formatWeight(m.weight) : '']),
    ),
  )
  const [idError, setIdError] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  // A member whose provider has since gone keeps its row, marked, so saving
  // does not quietly drop it; unticking it is a deliberate choice.
  const gone = (pool?.members ?? [])
    .map((m) => m.provider)
    .filter((pid) => !providers.some((p) => p.id === pid))
  const order = [...providers.map((p) => p.id), ...gone]
  const kind = poolKind([...checked], providers)
  const capacity = (pid: string) => limits?.find((l) => l.id === pid)?.limits.capacity

  const toggle = (pid: string, on: boolean) =>
    setChecked((s) => {
      const next = new Set(s)
      if (on) next.add(pid)
      else next.delete(pid)
      return next
    })

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const newId = id.trim()
    if (!pool && !newId) {
      setIdError('Give the pool an id; tasks name it by that.')
      return
    }
    const body: PoolInput = {
      name: name.trim() || pool?.id || newId,
      members: membersBody(order, checked, weights),
    }
    setSaving(true)
    setError('')
    try {
      if (pool) await put('/api/pools/' + encodeURIComponent(pool.id), body)
      else await post('/api/pools', {...body, id: newId})
      await invalidate(keys.pools)
      toast.success(pool ? 'Pool saved' : 'Pool added')
      onDone()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form onSubmit={submit} className="flex min-h-0 flex-col">
      <DialogHeader
        icon={<Layers />}
        title={pool ? `Edit ${pool.name || pool.id}` : 'Add pool'}
        description={
          pool ? (
            <>
              <span className="font-mono">{pool.id}</span> is how tasks name it and cannot change.
            </>
          ) : (
            'Several providers of one type that a task can be given as a whole.'
          )
        }
      />
      <DialogBody className="flex flex-col gap-4">
        <div className={cn('grid gap-3', !pool && 'grid-cols-2')}>
          {!pool && (
            <Field label="Id" htmlFor="pool-id" error={idError} hint="How tasks name it. Fixed once added.">
              <Input
                id="pool-id"
                className="font-mono text-[12.5px]"
                value={id}
                placeholder="claude-pool"
                aria-invalid={!!idError}
                onChange={(e) => {
                  setId(e.target.value)
                  setIdError('')
                }}
                spellCheck={false}
                autoFocus
              />
            </Field>
          )}
          <Field label="Name" htmlFor="pool-name" hint="Shown in the app and in run messages">
            <Input
              id="pool-name"
              value={name}
              placeholder={pool?.id || id.trim() || 'Claude accounts'}
              onChange={(e) => setName(e.target.value)}
              autoFocus={!!pool}
            />
          </Field>
        </div>

        <div className="flex flex-col gap-1.5">
          <Label>Members</Label>
          <p className="text-[12px] text-muted-foreground">
            Providers of one type only. Weight is how large a plan is next to the others'; empty takes what
            the provider reports (Max 20x is 20, Max 5x is 5, Pro and anything unknown is 1).
          </p>
          <div className="mt-1 divide-y divide-border overflow-hidden rounded-lg border border-border">
            {order.map((pid) => {
              const p = providers.find((x) => x.id === pid)
              const on = checked.has(pid)
              const otherKind = !!p && !!kind && p.kind !== kind && !on
              return (
                <div key={pid} className={cn('flex items-center gap-3 px-3 py-2', otherKind && 'opacity-50')}>
                  <Checkbox
                    id={`pool-m-${pid}`}
                    checked={on}
                    disabled={otherKind}
                    onCheckedChange={(v) => toggle(pid, v === true)}
                  />
                  <label htmlFor={`pool-m-${pid}`} className="min-w-0 flex-1 text-[13px]">
                    <span className="font-medium">{p ? p.name || p.id : pid}</span>
                    <span className="ml-2 text-[12px] text-muted-foreground">
                      {p
                        ? (p.type_name || p.kind) +
                          (executionLabel(p.execution) ? ` · ${executionLabel(p.execution)}` : '') +
                          (otherKind ? ' · another type' : '') +
                          (p.enabled ? '' : ' · switched off')
                        : 'unavailable: no longer configured'}
                    </span>
                  </label>
                  <Input
                    type="number"
                    min={0}
                    step="any"
                    aria-label={`Weight of ${p?.name || pid}`}
                    className="h-7 w-28 text-right tabular-nums"
                    value={weights[pid] ?? ''}
                    placeholder={weightPlaceholder(capacity(pid))}
                    disabled={!on}
                    onChange={(e) => setWeights((w) => ({...w, [pid]: e.target.value}))}
                  />
                </div>
              )
            })}
            {order.length === 0 && (
              <div className="px-3 py-3 text-[12.5px] text-muted-foreground">No providers configured.</div>
            )}
          </div>
        </div>
        {error && (
          <Alert tone="danger" title={pool ? 'Not saved' : 'Not added'}>
            {error}
          </Alert>
        )}
      </DialogBody>
      <DialogFooter>
        <DialogClose asChild>
          <Button type="button" variant="ghost">
            Cancel
          </Button>
        </DialogClose>
        <Button type="submit" variant="primary" loading={saving}>
          {pool ? 'Save' : 'Add pool'}
        </Button>
      </DialogFooter>
    </form>
  )
}
