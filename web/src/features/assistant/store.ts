import {create} from 'zustand'
import {useNav} from '@/app/nav'
import {useArtifactViewer} from '@/features/artifacts/store'
import {useRunLog} from '@/features/log/store'
import {errorMessage, post} from '@/lib/api'
import type {AssistantAction, AssistantMessage, AssistantReply, AssistantTurnRequest} from '@/lib/types'
import {isNavigation, runNavigation} from './actions'

/* The assistant: a chat docked to the right of the window that operates
   DispatchQ. The daemon asks the configured provider, looks things up on its
   own and answers with a short reply plus actions. Navigation happens as soon
   as the answer arrives; every change to the queue arrives as a card that does
   nothing until it is confirmed.

   The conversation lives in a store, so closing the panel or switching views
   keeps it. */

// A proposal's lifecycle on its card.
export type CardState = 'pending' | 'busy' | 'done' | 'dismissed' | 'replaced' | 'failed'

export interface Card {
  action: AssistantAction
  state: CardState
  error?: string
  // What a confirmed run_cli printed.
  output?: string
}

export interface ChatEntry {
  id: number
  role: 'user' | 'assistant' | 'error'
  text: string
  steps?: string[]
  // Where the answer took the app, in its own words ("Opened run …").
  moved?: string[]
  cards?: Card[]
}

interface AssistantState {
  open: boolean
  entries: ChatEntry[]
  session: string
  composer: string
  busy: boolean
}

const initial: AssistantState = {open: false, entries: [], session: '', composer: '', busy: false}

export const useAssistant = create<AssistantState>(() => initial)

let nextId = 1
// A turn still running when the conversation is cleared must not land in the
// new one.
let generation = 0

export const openAssistant = () => useAssistant.setState({open: true})
export const closeAssistant = () => useAssistant.setState({open: false})
export const toggleAssistant = () => useAssistant.setState((s) => ({open: !s.open}))
export const setComposer = (composer: string) => useAssistant.setState({composer})

export function clearConversation() {
  generation++
  useAssistant.setState((s) => ({...initial, open: s.open}))
}

// ask opens the panel and sends text straight away, for entry points that
// already know the question (the command palette).
// While an answer is still coming, the question waits in the composer next to
// whatever was typed there, rather than replacing it.
export function ask(text: string) {
  openAssistant()
  const {busy, composer} = useAssistant.getState()
  if (busy) {
    setComposer(composer.trim() ? `${composer.trim()}\n${text}` : text)
    return
  }
  setComposer(text)
  sendMessage()
}

const push = (e: Omit<ChatEntry, 'id'>) =>
  useAssistant.setState((s) => ({entries: [...s.entries, {...e, id: nextId++}]}))

export function sendMessage() {
  const s = useAssistant.getState()
  const text = s.composer.trim()
  if (s.busy || !text) return
  // What was said so far, for a daemon that has forgotten the session.
  const history: AssistantMessage[] = s.entries
    .filter((e) => e.role !== 'error' && e.text)
    .map((e) => ({role: e.role === 'user' ? 'user' : 'assistant', text: e.text}))
  push({role: 'user', text})
  useAssistant.setState({composer: '', busy: true})
  void runTurn({session_id: s.session, text, history, focus: focus()})
}

// focus is what the operator has in front of them right now.
function focus(): AssistantTurnRequest['focus'] {
  return {
    view: useNav.getState().view,
    run_id: useRunLog.getState().runId ?? undefined,
    artifact_id: useArtifactViewer.getState().artifactId ?? undefined,
  }
}

async function runTurn(req: AssistantTurnRequest) {
  const gen = generation
  try {
    const r = await post<AssistantReply>('/api/assistant/turn', req)
    if (gen !== generation) return
    const actions = r.actions ?? []
    const moved = actions.filter(isNavigation)
    const proposals = actions.filter((a) => !isNavigation(a))
    replaceOlderProposals(proposals)
    push({
      role: 'assistant',
      text: r.text,
      steps: r.steps,
      moved: moved.map(describeMove),
      cards: proposals.map((action) => ({action, state: 'pending'})),
    })
    useAssistant.setState({session: r.session_id || ''})
    for (const a of moved) void runNavigation(a)
  } catch (e) {
    if (gen !== generation) return
    push({role: 'error', text: errorMessage(e)})
    // The daemon forgets a session that failed; the next message starts a new
    // one and carries the history along.
    useAssistant.setState({session: ''})
  } finally {
    if (gen === generation) useAssistant.setState({busy: false})
  }
}

// A new proposal of the same kind for the same target supersedes one still
// waiting: "make it 8 instead of 7" must not leave two jobs to file.
function replaceOlderProposals(next: AssistantAction[]) {
  if (!next.length) return
  const key = (a: AssistantAction) =>
    a.type +
    '\0' +
    (a.type === 'add_task' ? '' : a.type === 'run_cli' ? (a.argv ?? []).slice(0, 2).join(' ') : (a.id ?? ''))
  const keys = new Set(next.map(key))
  useAssistant.setState((s) => ({
    entries: s.entries.map((e) =>
      e.cards?.some((c) => c.state === 'pending' && keys.has(key(c.action)))
        ? {
            ...e,
            cards: e.cards.map((c) =>
              c.state === 'pending' && keys.has(key(c.action)) ? {...c, state: 'replaced'} : c,
            ),
          }
        : e,
    ),
  }))
}

function describeMove(a: AssistantAction): string {
  switch (a.type) {
    case 'open_view':
      return `Opened ${a.view}`
    case 'open_run':
      return `Opened run ${a.label || a.id}`
    case 'open_artifact':
      return `Opened ${a.label || 'the artifact'}`
    case 'open_task':
      return `Opened ${a.label || 'the task'}`
    default:
      return ''
  }
}

export function setCard(entryId: number, index: number, patch: Partial<Card>) {
  useAssistant.setState((s) => ({
    entries: s.entries.map((e) =>
      e.id === entryId && e.cards
        ? {...e, cards: e.cards.map((c, i) => (i === index ? {...c, ...patch} : c))}
        : e,
    ),
  }))
}
