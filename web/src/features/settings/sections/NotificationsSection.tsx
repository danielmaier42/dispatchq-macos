import {ExternalLink} from 'lucide-react'
import {Row, RowGroup} from '@/components/layout/settings-row'
import {Section} from '@/components/layout/view-layout'
import {Alert} from '@/components/ui/alert'
import {Button} from '@/components/ui/button'
import {Switch} from '@/components/ui/switch'
import {useHost} from '@/lib/platform'
import {useHealth} from '@/lib/queries'
import {openNotificationSettings} from '@/features/status/notification-settings'
import type {Settings} from '@/lib/types'
import {notifyCopy} from '@/features/status/notify-copy'
import {commitSettings} from '../actions'
import {Channels} from '../components/Channels'
import {SaveMode} from '../components/parts'

export function NotificationsSection({value, stored}: {value: Settings; stored: Settings}) {
  const notify = useHealth().data?.notify_status
  const blocked = notify === 'denied' || notify === 'not_determined'
  const host = useHost().data
  const copy = notifyCopy(host)
  const openSettings = () => void openNotificationSettings(host)

  return (
    <>
      {blocked && (
        <Alert
          tone="warning"
          className="mb-6"
          title={copy.blockedTitle(notify)}
          actions={<Button onClick={openSettings}>Open {copy.settingsLabel}</Button>}
        >
          {copy.mac ? (
            <>
              {notify === 'denied'
                ? 'Run outcomes and new artifacts are announced, but macOS hides them. Turn Allow notifications back on for DispatchQ.'
                : "DispatchQ hasn't been allowed to notify you yet, so run outcomes and new artifacts stay silent."}{' '}
              Pick Alerts while you are there and an alert stays on screen until you click it.
            </>
          ) : (
            copy.blockedBody(notify)
          )}
        </Alert>
      )}

      <Section title="On this computer" actions={<SaveMode mode="instant" />}>
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
          {!blocked && (
            <Row
              label={copy.lingerLabel}
              description={copy.lingerDescription}
              control={
                <Button onClick={openSettings}>
                  {copy.settingsLabel}
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
