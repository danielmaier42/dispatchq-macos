import {cleanup, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import type {HostInfo} from '@/lib/types'
import {SystemSection} from './SystemSection'

const host = vi.hoisted(() => ({data: undefined as HostInfo | undefined}))

vi.mock('@/lib/platform', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useHost: () => ({data: host.data}),
}))
vi.mock('../components/UninstallDialog', () => ({UninstallDialog: () => null}))

afterEach(cleanup)

const show = (h: HostInfo | undefined) => {
  host.data = h
  return render(<SystemSection />)
}

describe('SystemSection', () => {
  it('offers the macOS Installer where the daemon has it', () => {
    show({os: 'darwin', uninstall: true})
    expect(screen.getByText('Remove DispatchQ from this computer')).toBeTruthy()
    expect(screen.getByText(/macOS Installer/)).toBeTruthy()
    expect(screen.getByRole('button', {name: /Uninstall/})).toBeTruthy()
  })

  it('points Windows at Installed apps and offers no button', () => {
    const {container} = show({os: 'windows', uninstall: false})
    expect(screen.getByText('Remove DispatchQ from this computer')).toBeTruthy()
    expect(screen.getByText(/Settings > Apps > Installed apps/)).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
    expect(container.textContent).not.toMatch(/Mac/)
  })

  it('sends a build without an uninstaller to the README', () => {
    show({os: 'darwin', uninstall: false})
    expect(screen.getByText(/no uninstaller/)).toBeTruthy()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('says nothing about the system before the daemon answered', () => {
    const {container} = show(undefined)
    expect(container.textContent).not.toMatch(/macOS|Windows/)
    expect(screen.queryByRole('button')).toBeNull()
  })
})
