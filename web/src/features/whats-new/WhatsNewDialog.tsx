import {CheckCircle2, Palette, Search, Sparkles, Tag, Unlock} from 'lucide-react'
import {useEffect, useState, type ReactNode} from 'react'
import {Button} from '@/components/ui/button'
import {Checkbox} from '@/components/ui/checkbox'
import {confirm} from '@/components/ui/confirm'
import {Dialog, DialogBody, DialogContent, DialogFooter, DialogHeader} from '@/components/ui/dialog'
import {Meter} from '@/components/ui/progress'
import {toast} from '@/components/ui/toaster'
import {errorMessage, get, post} from '@/lib/api'
import {invalidate, keys} from '@/lib/queries'
import {keyCombo} from '@/lib/platform'
import type {RenameApplyRequest, RenameApplyResult, RenameCheck} from '@/lib/types'
import {useWhatsNew, useWhatsNewStatus, whatsNewKey} from './store'

// WhatsNewDialog introduces DispatchQ 2.0 once, on the first start after the
// update from ClaudeQ, and again whenever the command palette asks for it. From
// it the operator can have the prompt reviewer check the queue for tasks that
// still name ClaudeQ and apply the fixes they pick.
export function WhatsNewDialog() {
  const status = useWhatsNewStatus()
  const [closed, setClosed] = useState(false)
  const [check, setCheck] = useState<RenameCheck | null>(null)
  const [migrated, setMigrated] = useState(false)
  const reopened = useWhatsNew((s) => s.reopened)
  const open = (!!status.data?.show && !closed) || reopened

  // Closing with the check still running or its fixes not applied asks first:
  // a running check's results are lost with the dialog.
  const close = async () => {
    const pending =
      check?.state === 'running' ||
      (check?.state === 'done' && !migrated && check.findings.some((f) => f.revised_prompt))
    if (
      pending &&
      !(await confirm({
        title: 'Close without migrating?',
        description:
          'A task that still calls ClaudeQ.app runs through a link that a later release removes. The banner at the top offers the check again until it is done.',
        confirmLabel: 'Close',
        cancelLabel: 'Keep open',
      }))
    )
      return
    setClosed(true)
    useWhatsNew.setState({reopened: false, autoCheck: false})
    if (!status.data?.show) return
    try {
      await post('/api/whats-new/dismiss')
      // Closed, the dialog hands over to the banner if the check is still due.
      invalidate(whatsNewKey)
    } catch (e) {
      toast.error('Could not save that you have seen this', {description: errorMessage(e)})
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && void close()}>
      <DialogContent size="lg">
        <DialogHeader
          icon={<Sparkles />}
          title="ClaudeQ is now DispatchQ"
          description={`What’s new in DispatchQ ${status.data?.edition ?? ''}`}
        />
        <DialogBody className="space-y-4 text-[13px] leading-relaxed">
          <Item icon={<Tag />} title="A new name">
            ClaudeQ is now DispatchQ, with a new icon. Your tasks, run history and settings moved over as they
            were. The command line tool is now <code>dispatchq</code>, in{' '}
            <code>/Applications/DispatchQ.app/Contents/MacOS</code>. The old <code>claudeq</code> path still
            works through a link, but it is deprecated and goes away in a later release.
          </Item>
          <Item icon={<Palette />} title="A complete redesign">
            The app was rebuilt from the ground up: a Dashboard overview, a command palette ({keyCombo('K')}),
            shortcuts for every view ({keyCombo('1')} to {keyCombo('8')}), a slide-in task panel, a new log
            view and artifact viewer, and an accent colour that follows macOS or can be pinned.
          </Item>
          <Item icon={<Unlock />} title="Every beta feature is public">
            Codex, opencode, adding your own providers and provider pools are no longer behind the beta
            switch. They are available to everyone, and the switch is gone.
          </Item>
          <RenameCheckPanel check={check} setCheck={setCheck} onMigrated={() => setMigrated(true)} />
        </DialogBody>
        <DialogFooter>
          <Button variant="primary" onClick={() => void close()}>
            Got it
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Item({icon, title, children}: {icon: ReactNode; title: string; children: ReactNode}) {
  return (
    <div className="flex gap-3">
      <div className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-md bg-accent-soft text-accent-text [&_svg]:size-3.5">
        {icon}
      </div>
      <div>
        <div className="font-medium">{title}</div>
        <p className="text-muted-foreground">{children}</p>
      </div>
    </div>
  )
}

// fixable selects every finding that comes with a rewrite.
function fixable(c: RenameCheck) {
  return new Set(c.findings.filter((f) => f.revised_prompt).map((f) => f.task_id))
}

// RenameCheckPanel runs the check and lists what it found, each fix selected
// until the operator deselects it.
function RenameCheckPanel({
  check,
  setCheck,
  onMigrated,
}: {
  check: RenameCheck | null
  setCheck: (c: RenameCheck) => void
  onMigrated: () => void
}) {
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [applying, setApplying] = useState(false)

  const running = check?.state === 'running'
  const done = check?.state === 'done'

  // A check started earlier, or from the banner, is picked up where it is.
  useEffect(() => {
    let live = true
    void (async () => {
      try {
        const c = await get<RenameCheck>('/api/rename-check')
        if (!live) return
        // The banner asks for a fresh look: tasks may have changed since an
        // earlier check, so its result is not reused.
        if (c.state !== 'running' && useWhatsNew.getState().autoCheck) {
          useWhatsNew.setState({autoCheck: false})
          setCheck(await post<RenameCheck>('/api/rename-check'))
          return
        }
        setCheck(c)
        if (c.state === 'done') setSelected(fixable(c))
      } catch (e) {
        toast.error('Could not load the check', {description: errorMessage(e)})
      }
    })()
    return () => {
      live = false
    }
  }, [setCheck])

  // A finished check may have settled the banner.
  useEffect(() => {
    if (done) invalidate(whatsNewKey)
  }, [done])
  useEffect(() => {
    if (!running) return
    const t = setInterval(async () => {
      try {
        const c = await get<RenameCheck>('/api/rename-check')
        setCheck(c)
        if (c.state === 'done') setSelected(fixable(c))
      } catch {
        /* the next poll tries again */
      }
    }, 1000)
    return () => clearInterval(t)
  }, [running, setCheck])

  const start = async () => {
    try {
      setCheck(await post<RenameCheck>('/api/rename-check'))
    } catch (e) {
      toast.error('Could not start the check', {description: errorMessage(e)})
    }
  }

  const apply = async () => {
    setApplying(true)
    try {
      const r = await post<RenameApplyResult>('/api/rename-check/apply', {
        task_ids: [...selected],
      } satisfies RenameApplyRequest)
      invalidate(keys.tasks, keys.settings, whatsNewKey)
      onMigrated()
      setCheck(await get<RenameCheck>('/api/rename-check'))
      setSelected(new Set())
      if (r.changed?.length)
        toast(`${r.applied.length} migrated, ${r.changed.length} skipped`, {
          description: 'Skipped tasks were edited after the check. Check them again.',
        })
      else toast.success(`${r.applied.length} ${r.applied.length === 1 ? 'task' : 'tasks'} migrated`)
    } catch (e) {
      toast.error('Could not migrate the tasks', {description: errorMessage(e)})
    } finally {
      setApplying(false)
    }
  }

  const toggle = (id: string, on: boolean) =>
    setSelected((s) => {
      const n = new Set(s)
      if (on) n.add(id)
      else n.delete(id)
      return n
    })

  return (
    <div className="rounded-lg border border-border bg-surface-2/60 p-3.5">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="font-medium">Check your tasks for the new name</div>
          <p className="text-muted-foreground">
            A task that calls ClaudeQ.app or uses CLAUDEQ_ variables should move to the new names. The prompt
            reviewer checks every task that mentions ClaudeQ; you choose which fixes to apply.
          </p>
        </div>
        {!running && (
          <Button onClick={() => void start()}>
            <Search />
            {done ? 'Check again' : 'Check tasks'}
          </Button>
        )}
      </div>

      {check?.state === 'unavailable' && (
        <p className="mt-3 text-muted-foreground">
          No prompt reviewer is available. Pick a Claude Code or Codex provider under Settings › Assistant ›
          Prompt review, then check again from the banner at the top.
        </p>
      )}

      {running && (
        <div className="mt-3 space-y-1.5">
          <Meter value={check.checked} max={check.total} />
          <p className="text-[12px] text-muted-foreground">
            Checked {check.checked} of {check.total}
          </p>
        </div>
      )}

      {check?.state === 'done' && (
        <div className="mt-3 space-y-2">
          {check.findings.length === 0 ? (
            <p className="flex items-center gap-1.5 text-muted-foreground">
              <CheckCircle2 className="size-4 text-success" />
              {check.total === 0
                ? 'No task mentions ClaudeQ. Nothing to migrate.'
                : 'Nothing left to migrate.'}
            </p>
          ) : (
            <>
              <ul className="space-y-2">
                {check.findings.map((f) => (
                  <li key={f.task_id} className="flex items-start gap-2.5">
                    <Checkbox
                      className="mt-0.5"
                      checked={selected.has(f.task_id)}
                      disabled={!f.revised_prompt}
                      onCheckedChange={(v) => toggle(f.task_id, v === true)}
                      aria-label={f.name}
                    />
                    <div className="min-w-0">
                      <div className="font-medium">{f.name}</div>
                      <p className="text-[12px] text-muted-foreground">
                        {f.message}
                        {!f.revised_prompt && ' Edit this task yourself.'}
                      </p>
                    </div>
                  </li>
                ))}
              </ul>
              <div className="flex justify-end">
                <Button
                  variant="primary"
                  loading={applying}
                  disabled={selected.size === 0}
                  onClick={() => void apply()}
                >
                  Migrate {selected.size} {selected.size === 1 ? 'task' : 'tasks'}
                </Button>
              </div>
            </>
          )}
          {!!check.errors?.length && (
            <p className="text-[12px] text-danger">Could not check: {check.errors.join('; ')}</p>
          )}
        </div>
      )}
    </div>
  )
}
