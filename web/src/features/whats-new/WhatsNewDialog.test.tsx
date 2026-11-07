import {QueryClient, QueryClientProvider} from '@tanstack/react-query'
import {act, cleanup, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {WhatsNewDialog} from './WhatsNewDialog'

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia
})

vi.mock('./store', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useWhatsNewStatus: () => ({data: {show: true, edition: '2.0'}}),
}))
vi.mock('@/lib/api', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  get: vi.fn().mockResolvedValue({state: 'idle', findings: []}),
}))

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('upgrade history keyboard guidance', () => {
  for (const [platform, palette, first, last] of [
    ['Win32', 'Ctrl+K', 'Ctrl+1', 'Ctrl+8'],
    ['MacIntel', '⌘K', '⌘1', '⌘8'],
  ]) {
    it(`uses the ${platform} keyboard in the real dialog`, async () => {
      vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
      await act(async () => {
        render(
          <QueryClientProvider client={new QueryClient()}>
            <WhatsNewDialog />
          </QueryClientProvider>,
        )
      })
      const text = screen.getByText(/The app was rebuilt from the ground up/).textContent
      expect(text).toContain(`command palette (${palette})`)
      expect(text).toContain(`every view (${first} to ${last})`)
      if (platform === 'Win32') expect(text).not.toContain('⌘')
    })
  }
})
