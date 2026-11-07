import {Trash2} from 'lucide-react'
import {useState} from 'react'
import {Row, RowGroup} from '@/components/layout/settings-row'
import {Section} from '@/components/layout/view-layout'
import {Button} from '@/components/ui/button'
import {isMacHost, useHost} from '@/lib/platform'
import {UninstallDialog} from '../components/UninstallDialog'

const LABEL = 'Remove DispatchQ from this computer'

// SystemSection offers the uninstaller only where the daemon has one (the
// macOS Installer). Windows removes apps from its own settings, so the row
// says where; any other build points at the README.
export function SystemSection() {
  const [uninstall, setUninstall] = useState(false)
  const host = useHost().data

  let row = null
  if (host?.uninstall && isMacHost(host)) {
    row = (
      <Row
        label={LABEL}
        description="Opens the uninstaller in the macOS Installer. It deletes the app, stops its background service and removes the wake permission. Your tasks and history stay unless you select them under Customize."
        control={
          <Button
            variant="danger-ghost"
            className="border border-danger/30"
            onClick={() => setUninstall(true)}
          >
            <Trash2 />
            Uninstall…
          </Button>
        }
      />
    )
  } else if (host?.os === 'windows') {
    row = (
      <Row
        label={LABEL}
        description="Open Settings > Apps > Installed apps, choose DispatchQ and Uninstall, or use Uninstall DispatchQ in the Start menu. It stops the background service and removes the sign-in entry, the shortcuts and the PATH entry. Your tasks and history stay."
      />
    )
  } else if (host) {
    row = (
      <Row
        label={LABEL}
        description="This build has no uninstaller. The README describes how to remove DispatchQ."
      />
    )
  }

  return (
    <>
      <Section title="Danger zone">
        <RowGroup className="border-danger/30">{row}</RowGroup>
      </Section>

      {host?.uninstall && <UninstallDialog open={uninstall} onOpenChange={setUninstall} />}
    </>
  )
}
