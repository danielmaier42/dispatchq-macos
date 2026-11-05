import {Workflow} from 'lucide-react'
import {relTime} from '@/lib/format'
import type {WorkflowRun} from '../runs'
import {RunRow} from './RunRow'

// One workflow's runs boxed together, oldest first; a join sits indented under
// the runs it waited for.
export function WorkflowGroup({
  runs,
  showProvider,
  limitedUntil,
}: {
  runs: WorkflowRun[]
  showProvider: boolean
  limitedUntil?: string
}) {
  const last = runs[runs.length - 1].run
  return (
    <div className="bg-surface-2/50">
      <div className="flex items-center gap-2 px-3 pt-2 pb-0.5 text-[11.5px] text-subtle-foreground">
        <span className="w-2" />
        <Workflow className="size-3.5" />
        <span className="font-semibold tracking-wide uppercase">Workflow</span>
        <span>
          {runs.length} runs · {relTime(last.started_at)}
        </span>
      </div>
      {runs.map((w) => (
        <RunRow
          key={w.run.run_id}
          run={w.run}
          depth={w.depth}
          showProvider={showProvider}
          limitedUntil={limitedUntil}
        />
      ))}
    </div>
  )
}
