import {QueryClient, QueryClientProvider} from '@tanstack/react-query'
import {cleanup, render} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {Sidebar} from '@/app/Sidebar'
import {TooltipProvider} from '@/components/ui/tooltip'
import {SaveBar} from '@/features/settings/components/SaveBar'

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia
})

vi.mock('@/lib/queries', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useHealth: () => ({isError: false}),
  useSettings: () => ({data: undefined}),
  useUpdate: () => ({data: undefined}),
}))
vi.mock('@/app/badges', () => ({useBadges: () => ({})}))

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const show = (platform: string) => {
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <TooltipProvider>
        <Sidebar />
      </TooltipProvider>
    </QueryClientProvider>,
  ).container
}

describe('Sidebar shortcut hints', () => {
  it('show Control combos and no Command symbol on Windows', () => {
    const text = show('Win32').textContent ?? ''
    expect(text).toContain('Ctrl+J')
    expect(text).toContain('Ctrl+K')
    expect(text).toContain('Ctrl+1')
    expect(text).not.toContain('⌘')
  })

  it('keep the Command symbol on a Mac', () => {
    const text = show('MacIntel').textContent ?? ''
    expect(text).toContain('⌘J')
    expect(text).toContain('⌘K')
    expect(text).toContain('⌘1')
    expect(text).not.toContain('Ctrl')
  })
})

describe('SaveBar shortcut hint', () => {
  it('names the save key of this keyboard', () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const {container} = render(<SaveBar sections={new Set(['general'])} />)
    expect(container.textContent).toContain('Ctrl+S')
    expect(container.textContent).not.toContain('⌘')
  })
})
