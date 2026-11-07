import {beforeEach, describe, expect, it, vi} from 'vitest'
import {useNav} from '@/app/nav'
import {useArtifactViewer} from '@/features/artifacts/store'
import type {AssistantReply} from '@/lib/types'

const api = vi.hoisted(() => ({
  post: vi.fn(),
  put: vi.fn(),
  get: vi.fn(),
  del: vi.fn(),
}))
vi.mock('@/lib/api', () => ({...api, errorMessage: (e: unknown) => String((e as Error).message ?? e)}))
// The real one shows a toast, which needs a browser this test does not have.
vi.mock('@/features/settings/actions', () => ({setPaused: vi.fn()}))

const {clearConversation, sendMessage, setComposer, useAssistant} = await import('./store')
const {confirmCard} = await import('./actions')

const reply = (r: Partial<AssistantReply>): AssistantReply => ({
  session_id: 's1',
  text: '',
  actions: [],
  ...r,
})

async function say(text: string) {
  setComposer(text)
  sendMessage()
  await vi.waitFor(() => expect(useAssistant.getState().busy).toBe(false))
}

beforeEach(() => {
  vi.clearAllMocks()
  clearConversation()
  useNav.setState({view: 'queue'})
  useArtifactViewer.setState({artifactId: null, sequence: null})
})

describe('assistant conversation', () => {
  it('sends the history and what is open, and keeps the session', async () => {
    api.post.mockResolvedValueOnce(reply({text: 'Hi.'}))
    await say('first')
    api.post.mockResolvedValueOnce(reply({text: 'Again.'}))
    await say('second')

    const [path, body] = api.post.mock.calls[1]
    expect(path).toBe('/api/assistant/turn')
    expect(body).toMatchObject({
      session_id: 's1',
      text: 'second',
      history: [
        {role: 'user', text: 'first'},
        {role: 'assistant', text: 'Hi.'},
      ],
      focus: {view: 'queue'},
    })
  })

  it('opens what the answer points at at once', async () => {
    api.post.mockResolvedValueOnce(
      reply({text: 'Opened.', actions: [{type: 'open_artifact', id: 'a1', label: 'Weekly report'}]}),
    )
    await say('show me the report')
    expect(useNav.getState().view).toBe('artifacts')
    expect(useArtifactViewer.getState().artifactId).toBe('a1')
    const last = useAssistant.getState().entries.at(-1)
    expect(last?.moved).toEqual(['Opened Weekly report'])
    expect(last?.cards).toEqual([])
  })

  it('turns changes into cards and replaces an older proposal', async () => {
    const add = (cron: string) => ({type: 'add_task' as const, label: 'Todos', task: {prompt: 'p', cron}})
    api.post.mockResolvedValueOnce(reply({actions: [add('0 7 * * 1-5')]}))
    await say('plan it')
    api.post.mockResolvedValueOnce(reply({actions: [add('0 8 * * 1-5'), {type: 'disable_task', id: 't2'}]}))
    await say('8 instead')

    const [first, second] = useAssistant.getState().entries.filter((e) => e.role === 'assistant')
    expect(first.cards?.[0].state).toBe('replaced')
    expect(second.cards?.map((c) => c.state)).toEqual(['pending', 'pending'])
  })

  it('reports a failed turn and starts the next one fresh', async () => {
    api.post.mockRejectedValueOnce(new Error('limit reached'))
    await say('hello')
    const st = useAssistant.getState()
    expect(st.entries.at(-1)).toMatchObject({role: 'error', text: 'limit reached'})
    expect(st.session).toBe('')
  })
})

describe('confirming a card', () => {
  it('files a new task with exactly the drafted fields', async () => {
    api.post.mockResolvedValueOnce(
      reply({
        actions: [{type: 'add_task', task: {name: 'N', prompt: 'p', working_dir: '/x', trigger: 'asap'}}],
      }),
    )
    await say('plan')
    const entry = useAssistant.getState().entries.at(-1)!
    api.post.mockResolvedValueOnce({})
    await confirmCard(entry.id, 0, entry.cards![0])
    expect(api.post).toHaveBeenLastCalledWith('/api/tasks', {
      name: 'N',
      prompt: 'p',
      working_dir: '/x',
      trigger: 'asap',
      permissions: 'default',
    })
    expect(useAssistant.getState().entries.at(-1)?.cards?.[0].state).toBe('done')
  })

  it('applies an update over the task as it is now', async () => {
    api.post.mockResolvedValueOnce(
      reply({actions: [{type: 'update_task', id: 't1', task: {cron: '0 8 * * *'}}]}),
    )
    await say('later')
    const entry = useAssistant.getState().entries.at(-1)!
    api.get.mockResolvedValueOnce([{id: 't1', name: 'T', prompt: 'p', cron: '0 7 * * *', trigger: 'cron'}])
    api.put.mockResolvedValueOnce({})
    await confirmCard(entry.id, 0, entry.cards![0])
    expect(api.put).toHaveBeenCalledWith(
      '/api/tasks/t1',
      expect.objectContaining({name: 'T', prompt: 'p', cron: '0 8 * * *'}),
    )
  })

  it('takes a task off its pool when the update names a provider', async () => {
    api.post.mockResolvedValueOnce(
      reply({actions: [{type: 'update_task', id: 't1', task: {provider: 'codex'}}]}),
    )
    await say('run it on codex')
    const entry = useAssistant.getState().entries.at(-1)!
    api.get.mockResolvedValueOnce([{id: 't1', name: 'T', prompt: 'p', pool: 'night', trigger: 'asap'}])
    api.put.mockResolvedValueOnce({})
    await confirmCard(entry.id, 0, entry.cards![0])
    const body = api.put.mock.calls[0][1]
    expect(body).toMatchObject({provider: 'codex'})
    expect(body).not.toHaveProperty('pool')
  })

  it('keeps the error on the card when the daemon refuses', async () => {
    api.post.mockResolvedValueOnce(reply({actions: [{type: 'delete_task', id: 'gone'}]}))
    await say('delete it')
    const entry = useAssistant.getState().entries.at(-1)!
    api.del.mockRejectedValueOnce(new Error('no such task'))
    await confirmCard(entry.id, 0, entry.cards![0])
    expect(useAssistant.getState().entries.at(-1)?.cards?.[0]).toMatchObject({
      state: 'failed',
      error: 'no such task',
    })
  })
})

describe('full CLI parity on cards', () => {
  it('runs a confirmed dispatchq command through the daemon and keeps its output', async () => {
    const argv = ['provider', 'disable', 'codex']
    api.post.mockResolvedValueOnce(
      reply({actions: [{type: 'run_cli', argv, command: 'dispatchq provider disable codex'}]}),
    )
    await say('disable codex')
    const entry = useAssistant.getState().entries.at(-1)!
    expect(api.post).toHaveBeenCalledTimes(1) // nothing runs before it is confirmed
    api.post.mockResolvedValueOnce({output: 'provider "codex" disabled'})
    await confirmCard(entry.id, 0, entry.cards![0])
    expect(api.post).toHaveBeenLastCalledWith('/api/assistant/cli', {argv})
    expect(useAssistant.getState().entries.at(-1)?.cards?.[0]).toMatchObject({
      state: 'done',
      output: 'provider "codex" disabled',
    })
  })

  it('files a script job with every drafted field, switches set to false included', async () => {
    const task = {
      name: 'Minute test',
      kind: 'script' as const,
      prompt: `"$DISPATCHQ_BIN" notify --title 'test' --message "test $(date '+%Y-%m-%d %H:%M:%S')"`,
      working_dir: '/tmp',
      trigger: 'cron' as const,
      cron: '* * * * *',
      notify_on_result: false,
      quiet_history: true,
    }
    api.post.mockResolvedValueOnce(reply({actions: [{type: 'add_task', task}]}))
    await say('every minute')
    const entry = useAssistant.getState().entries.at(-1)!
    api.post.mockResolvedValueOnce({})
    await confirmCard(entry.id, 0, entry.cards![0])
    expect(api.post).toHaveBeenLastCalledWith('/api/tasks', {...task, permissions: 'default'})
  })

  it('turns a job into a script the way dispatchq edit does', async () => {
    api.post.mockResolvedValueOnce(
      reply({actions: [{type: 'update_task', id: 't1', task: {kind: 'script', prompt: 'echo hi'}}]}),
    )
    await say('make it a script')
    const entry = useAssistant.getState().entries.at(-1)!
    api.get.mockResolvedValueOnce([
      {
        id: 't1',
        name: 'T',
        prompt: 'p',
        trigger: 'asap',
        provider: 'claude',
        model: 'opus',
        reasoning_effort: 'high',
        permissions: 'skip',
        backfill: true,
      },
    ])
    api.put.mockResolvedValueOnce({})
    await confirmCard(entry.id, 0, entry.cards![0])
    const body = api.put.mock.calls[0][1]
    expect(body).toMatchObject({kind: 'script', prompt: 'echo hi', permissions: 'default', backfill: false})
    for (const k of ['provider', 'pool', 'model', 'reasoning_effort']) expect(body).not.toHaveProperty(k)
  })
})
