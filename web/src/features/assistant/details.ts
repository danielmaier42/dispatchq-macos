import {describeSchedule} from '@/features/queue/schedule'
import type {AssistantTaskDraft} from '@/lib/types'

// The permission labels on a proposal card. An unattended agent job with the
// provider's own rules has nobody to approve a prompt: a command that needs
// approval, `dispatchq notify` included, is refused unless the provider's allow
// rules already permit it. The card says so, so the operator can choose before
// a run fails instead of finding out from it.
export const PERMISSIONS_DEFAULT_LABEL = 'Provider rules: commands not already allowed are refused'
export const PERMISSIONS_SKIP_LABEL = 'May do anything'

// taskDetailRows lists what a task proposal sets: the whole job for a new one
// (full), just the changed fields for an update.
export function taskDetailRows(draft: AssistantTaskDraft, full: boolean): [string, string][] {
  const rows: [string, string][] = []
  if (!full && draft.name) rows.push(['Name', draft.name])
  if (full || draft.kind)
    rows.push(['Kind', draft.kind === 'script' ? 'Script, runs without a model' : 'Agent job'])
  if (draft.id) rows.push(['Id', draft.id])
  if (draft.trigger || draft.cron || draft.fixed_at)
    rows.push([
      'When',
      describeSchedule({trigger: draft.trigger ?? 'asap', cron: draft.cron, fixed_at: draft.fixed_at}),
    ])
  if (draft.working_dir) rows.push(['Folder', draft.working_dir])
  if (draft.group) rows.push(['Group', draft.group])
  if (draft.provider) rows.push(['Provider', draft.provider])
  if (draft.pool) rows.push(['Pool', draft.pool])
  if (draft.model) rows.push(['Model', draft.model])
  if (draft.reasoning_effort) rows.push(['Effort', draft.reasoning_effort])
  const permissions = permissionsRow(draft, full)
  if (permissions) rows.push(['Permissions', permissions])
  if (draft.depends_on?.length) rows.push(['Waits for', draft.depends_on.join(', ')])
  if (draft.clear?.length) rows.push(['Cleared', draft.clear.join(', ')])
  const switches: [keyof AssistantTaskDraft, string][] = [
    ['enabled', 'Enabled'],
    ['parallel', 'Parallel'],
    ['notify_on_result', 'Notify'],
    ['quiet_history', 'Quiet history'],
    ['backfill', 'Backfill'],
    ['include_results', 'Their results'],
  ]
  for (const [k, label] of switches)
    if (typeof draft[k] === 'boolean') rows.push([label, draft[k] ? 'On' : 'Off'])
  return rows
}

// permissionsRow is shown for every new agent job, and for an update only when
// it changes the permissions. A script job runs no model, so it has none.
function permissionsRow(draft: AssistantTaskDraft, full: boolean): string | undefined {
  if (draft.kind === 'script') return undefined
  if (draft.permissions === 'skip') return PERMISSIONS_SKIP_LABEL
  if (draft.permissions === 'default' || full) return PERMISSIONS_DEFAULT_LABEL
  return undefined
}
