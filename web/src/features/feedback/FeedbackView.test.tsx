import {cleanup, fireEvent, render, screen} from '@testing-library/react'
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {TooltipProvider} from '@/components/ui/tooltip'
import {FeedbackView} from './FeedbackView'
import {startOver, useFeedback} from './store'

vi.mock('@/lib/queries', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useFeedbackInfo: () => ({
    isError: false,
    isPending: false,
    data: {available: true, repo: 'owner/repo', max_turns: 3},
    refetch: () => Promise.resolve(),
  }),
}))

// The theme reads matchMedia at load, before any test runs.
const send = vi.hoisted(() => {
  window.matchMedia = ((media: string) => ({
    matches: false,
    media,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia
  return vi.fn()
})
vi.mock('./store', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  sendMessage: send,
}))

beforeEach(() => {
  startOver()
  send.mockClear()
})
afterEach(cleanup)

const show = () =>
  render(
    <TooltipProvider>
      <FeedbackView />
    </TooltipProvider>,
  )

// The feedback page is a chat: plain ↵ sends the message, ⇧↵ is a newline.
describe('FeedbackView composer', () => {
  it('sends on Enter', () => {
    show()
    useFeedback.setState({composer: 'The queue stalls'})
    fireEvent.keyDown(screen.getByLabelText('Message'), {key: 'Enter'})
    expect(send).toHaveBeenCalledOnce()
  })

  it('keeps Shift+Enter for a newline', () => {
    show()
    fireEvent.keyDown(screen.getByLabelText('Message'), {key: 'Enter', shiftKey: true})
    expect(send).not.toHaveBeenCalled()
  })
})
