import {ExternalLink} from 'lucide-react'
import {Row, RowGroup} from '@/components/layout/settings-row'
import {Section} from '@/components/layout/view-layout'
import {Alert} from '@/components/ui/alert'
import {Button} from '@/components/ui/button'
import {Switch} from '@/components/ui/switch'
import {toast} from '@/components/ui/toaster'
import {isAppWindow, openExternal} from '@/lib/native'
import {useHealth} from '@/lib/queries'
import type {Settings} from '@/lib/types'
import {commitSettings} from '../actions'
import {Channels} from '../components/Channels'
import {SaveMode} from '../components/parts'
import {usePlatform} from '@/lib/platform'

// macOS keeps the alert style (Banners vanish, Alerts wait for a click). Only
// the app window can open a System Settings URL; a browser tab says where to look.
function openNotificationSettings() {
  if (isAppWindow()) openExternal('x-apple.systempreferences:com.apple.Notifications-Settings.extension')
  else toast('Open System Settings → Notifications → DispatchQ')
}

export function NotificationsSection({value, stored}: {value: Settings; stored: Settings}) {
  const {isMac, machine} = usePlatform()
  const notify = useHealth().data?.notify_status
  const blocked = notify === 'denied' || notify === 'not_determined'

  return (
    <>
      {blocked && (
        <Alert
          tone="warning"
          className="mb-6"
          title="macOS is not showing DispatchQ's notifications"
          actions={<Button onClick={openNotificationSettings}>Open System Settings</Button>}
        >
          {notify === 'denied'
            ? 'Run outcomes and new artifacts are announced, but macOS hides them. Turn Allow notifications back on for DispatchQ.'
            : "DispatchQ hasn't been allowed to notify you yet, so run outcomes and new artifacts stay silent."}{' '}
          Pick Alerts while you are there and an alert stays on screen until you click it.
        </Alert>
      )}

      <Section title={`On ${machine}`} actions={<SaveMode mode="instant" />}>
        <RowGroup>
          <Row
            htmlFor="s-artifact-notify"
            label="Notify about new artifacts"
            description="Off: a published artifact still shows up under Artifacts, just without a notification"
            control={
              <Switch
                id="s-artifact-notify"
                checked={!value.artifact_notify_disabled}
                onCheckedChange={(on) => void commitSettings({artifact_notify_disabled: !on})}
              />
            }
          />
          {isMac && !blocked && (
            <Row
              label="Keep alerts on screen"
              description="macOS decides how long a notification lingers: pick Alerts, not Banners"
              control={
                <Button onClick={openNotificationSettings}>
                  System Settings
                  <ExternalLink className="size-3.5" />
                </Button>
              }
            />
          )}
        </RowGroup>
      </Section>

      <Channels value={value} stored={stored} />
    </>
  )
}
