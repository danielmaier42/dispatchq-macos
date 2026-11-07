import type {ReactNode} from 'react'
import {Row, RowGroup} from '@/components/layout/settings-row'
import {Section} from '@/components/layout/view-layout'
import {Badge} from '@/components/ui/badge'
import {Field} from '@/components/ui/field'
import {Input, Textarea} from '@/components/ui/input'
import {Switch} from '@/components/ui/switch'
import type {Settings} from '@/lib/types'
import type {FieldKey} from '../draft'
import {editDraft, useSettingsUi} from '../store'
import {SaveMode} from './parts'
import {SecretInput} from './SecretInput'

type Channel = 'pushover' | 'ntfy' | 'webhook'

// The push channels are forms: a channel switched on with half its fields is
// refused by the daemon (better here than at three in the morning), so the
// switch and the fields are saved together, with Save.
export function Channels({value, stored}: {value: Settings; stored: Settings}) {
  const errors = useSettingsUi((s) => s.errors)
  const err = (k: FieldKey) => errors[k]

  return (
    <>
      <ChannelCard
        channel="pushover"
        title="Pushover"
        description="Push run alerts to your phone"
        value={value}
        stored={stored}
      >
        <Field label="API token" htmlFor="s-po-token" error={err('pushover.token')}>
          <SecretInput
            key={stored.pushover.token}
            id="s-po-token"
            stored={stored.pushover.token}
            value={value.pushover.token}
            invalid={!!err('pushover.token')}
            onChange={(token) => editDraft({pushover: {token}})}
          />
        </Field>
        <Field label="User key" htmlFor="s-po-user" error={err('pushover.user_key')}>
          <SecretInput
            key={stored.pushover.user_key}
            id="s-po-user"
            stored={stored.pushover.user_key}
            value={value.pushover.user_key}
            invalid={!!err('pushover.user_key')}
            onChange={(user_key) => editDraft({pushover: {user_key}})}
          />
        </Field>
      </ChannelCard>

      <ChannelCard
        channel="ntfy"
        title="ntfy"
        description="Push to a topic on ntfy.sh or on your own server"
        value={value}
        stored={stored}
      >
        <Field label="Server" htmlFor="s-ntfy-server" hint="Empty means ntfy.sh" error={err('ntfy.server')}>
          <Input
            id="s-ntfy-server"
            value={value.ntfy.server}
            placeholder="https://ntfy.sh"
            spellCheck={false}
            aria-invalid={!!err('ntfy.server') || undefined}
            onChange={(e) => editDraft({ntfy: {server: e.target.value}})}
          />
        </Field>
        <Field
          label="Topic"
          htmlFor="s-ntfy-topic"
          hint="The name your phone is subscribed to"
          error={err('ntfy.topic')}
        >
          <Input
            id="s-ntfy-topic"
            value={value.ntfy.topic}
            spellCheck={false}
            aria-invalid={!!err('ntfy.topic') || undefined}
            onChange={(e) => editDraft({ntfy: {topic: e.target.value}})}
          />
        </Field>
        <Field label="Access token" htmlFor="s-ntfy-token" hint="Only for a protected topic">
          <SecretInput
            key={stored.ntfy.token}
            id="s-ntfy-token"
            stored={stored.ntfy.token}
            value={value.ntfy.token}
            placeholder="None"
            onChange={(token) => editDraft({ntfy: {token}})}
          />
        </Field>
      </ChannelCard>

      <ChannelCard
        channel="webhook"
        title="Webhook"
        description="Any JSON endpoint: Slack, Discord, Home Assistant, n8n"
        value={value}
        stored={stored}
      >
        <Field label="Endpoint" htmlFor="s-wh-url" error={err('webhook.url')}>
          <Input
            id="s-wh-url"
            value={value.webhook.url}
            placeholder="https://hooks.example.com/…"
            spellCheck={false}
            aria-invalid={!!err('webhook.url') || undefined}
            onChange={(e) => editDraft({webhook: {url: e.target.value}})}
          />
        </Field>
        <Field
          label="Body"
          htmlFor="s-wh-template"
          error={err('webhook.template')}
          hint={
            <>
              The JSON posted to the endpoint. <Code>{'{{title}}'}</Code>, <Code>{'{{message}}'}</Code> and{' '}
              <Code>{'{{url}}'}</Code> are filled in and escaped. Leave empty to send DispatchQ's own body;
              set it to what the service expects (Slack wants <Code>text</Code>, Discord <Code>content</Code>
              ).
            </>
          }
        >
          <Textarea
            id="s-wh-template"
            rows={3}
            spellCheck={false}
            value={value.webhook.template}
            placeholder='{"text":"{{title}}: {{message}}"}'
            aria-invalid={!!err('webhook.template') || undefined}
            onChange={(e) => editDraft({webhook: {template: e.target.value}})}
            className="font-mono text-[12.5px]"
          />
        </Field>
      </ChannelCard>
    </>
  )
}

function Code({children}: {children: ReactNode}) {
  return <code className="rounded bg-surface-3/80 px-1 font-mono text-[11.5px]">{children}</code>
}

function ChannelCard({
  channel,
  title,
  description,
  value,
  stored,
  children,
}: {
  channel: Channel
  title: string
  description: string
  value: Settings
  stored: Settings
  children: ReactNode
}) {
  const on = value[channel].enabled
  const dirty = JSON.stringify(value[channel]) !== JSON.stringify(stored[channel])
  const live = stored[channel].enabled
  return (
    <Section
      title={
        <span className="flex items-center gap-2">
          {title}
          {live && (
            <Badge tone="success" dot>
              On
            </Badge>
          )}
        </span>
      }
      actions={<SaveMode mode="draft" dirty={dirty} />}
    >
      <RowGroup>
        <Row
          htmlFor={`s-${channel}-enabled`}
          label={`Send to ${title}`}
          description={description}
          control={
            <Switch
              id={`s-${channel}-enabled`}
              checked={on}
              onCheckedChange={(enabled) => editDraft({[channel]: {enabled}})}
            />
          }
        />
        {on && <div className="grid gap-4 px-4 py-4">{children}</div>}
      </RowGroup>
    </Section>
  )
}
