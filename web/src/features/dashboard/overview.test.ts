import {describe, expect, it} from 'vitest'
import type {QueueTask, Run} from '@/lib/types'
import {cardOrder, recentFailures, upNext} from './overview'

const now = Date.parse('2026-10-19T12:00:00Z')

function task(id: string, over: Partial<QueueTask> = {}): QueueTask {
  return {
    id,
    name: id,
    prompt: '',
    working_dir: '',
    group: '',
    trigger: 'asap',
    parallel: false,
    enabled: true,
    permissions: 'default',
    running: false,
    ...over,
  }
}

function run(id: string, status: Run['status'], startedAgoH: number): Run {
  return {
    run_id: id,
    task_id: id,
    task_name: id,
    started_at: new Date(now - startedAgoH * 3600_000).toISOString(),
    status,
    exit_code: 0,
    log_path: '',
    unread: false,
  }
}

describe('overview', () => {
  it('upNext puts due tasks first in queue order, then by start time, without quiet-history tasks', () => {
    const tasks = [
      task('cron-late', {trigger: 'cron', next_run: '2026-10-20T03:00:00Z'}),
      task('asap-b'),
      task('cron-soon', {trigger: 'cron', next_run: '2026-10-19T13:00:00Z'}),
      task('fixed-past', {trigger: 'fixed', fixed_at: '2026-10-19T10:00:00Z'}),
      task('off', {enabled: false}),
      task('busy', {running: true}),
      task('cron-none', {trigger: 'cron'}),
      task('quiet', {trigger: 'cron', next_run: '2026-10-19T12:05:00Z', quiet_history: true}),
    ]
    expect(upNext(tasks, now).map((n) => n.task.id)).toEqual([
      'asap-b',
      'fixed-past',
      'cron-soon',
      'cron-late',
    ])
  })
  it('recentFailures keeps failed runs of the last day, newest first', () => {
    const runs = [
      run('old', 'failed', 30),
      run('a', 'failed', 5),
      run('ok', 'success', 1),
      run('b', 'auth_error', 2),
    ]
    expect(recentFailures(runs, now).map((r) => r.run_id)).toEqual(['b', 'a'])
  })
  it('cardOrder leads with failures only while there are any', () => {
    expect(cardOrder(2)[0]).toBe('failed')
    expect(cardOrder(0)).toEqual(['next', 'running', 'week', 'failed'])
  })
})
