import {Row, RowGroup} from '@/components/layout/settings-row'
import {Section} from '@/components/layout/view-layout'
import {ModelSelect} from '@/components/domain/model-select'
import {Select, type SelectOption} from '@/components/ui/select'
import {Switch} from '@/components/ui/switch'
import {defaultProvider, providerName, useProviderList} from '@/lib/providers'
import {invalidate, keys} from '@/lib/queries'
import type {Settings} from '@/lib/types'
import {commitSettings} from '../actions'
import {SaveMode} from '../components/parts'

// asideProviderOptions lists the providers that can do a job DispatchQ runs on
// its own behalf (prompt review, the assistant). Only a harness that
// can answer such a question is offered: one that cannot would leave the
// feature silently unavailable. A stored provider that is gone keeps its place,
// marked, so nothing quietly moves the job somewhere else. "Default" is marked
// the same way when the default provider is one that cannot do the job.
function useAsideProviderOptions(current: string): SelectOption[] {
  const providers = useProviderList()
  const offered = providers.filter((p) => p.enabled && p.asides)
  const dflt = defaultProvider(providers)
  const dfltLabel = !dflt
    ? 'Default provider'
    : offered.includes(dflt)
      ? `Default (${providerName(dflt)})`
      : `Default (${providerName(dflt)}, unavailable)`
  const opts: SelectOption[] = [
    {value: '', label: dfltLabel},
    ...offered.map((p) => ({value: p.id, label: providerName(p)})),
  ]
  if (current && !offered.some((p) => p.id === current))
    opts.push({value: current, label: `${current} (unavailable)`})
  return opts
}

export function AssistantSection({value}: {value: Settings}) {
  const reviewOn = !value.prompt_review_disabled
  const reviewProviders = useAsideProviderOptions(value.prompt_review_provider)
  const assistantProviders = useAsideProviderOptions(value.assistant_provider)

  return (
    <>
      <Section title="Prompt review" actions={<SaveMode mode="instant" />}>
        <RowGroup>
          <Row
            htmlFor="s-review"
            label="Check prompts with Claude"
            description="Before a task is added, imported or edited, Claude reads its prompt against this computer: paths that aren't here, a file to be written into a folder that doesn't exist, guidelines it points at. It offers a rewrite under the prompt box, and reviews the custom system prompt the same way."
            control={
              <Switch
                id="s-review"
                checked={reviewOn}
                onCheckedChange={(on) => void commitSettings({prompt_review_disabled: !on})}
              />
            }
          />
          <Row
            htmlFor="s-review-provider"
            label="Review provider"
            description="Reviewing costs allowance too, so it can run on an account other than the one your tasks use"
            control={
              <Select
                id="s-review-provider"
                className="w-56"
                disabled={!reviewOn}
                value={value.prompt_review_provider}
                options={reviewProviders}
                // A model name means nothing to another harness, so it goes too.
                onValueChange={(v) =>
                  void commitSettings({prompt_review_provider: v, prompt_review_model: ''})
                }
              />
            }
          />
          <Row
            htmlFor="s-review-model"
            label="Review model"
            description="Runs on every prompt change, so a fast model earns its keep"
            control={
              <ModelSelect
                id="s-review-model"
                className="w-56"
                disabled={!reviewOn}
                provider={value.prompt_review_provider}
                value={value.prompt_review_model}
                emptyLabel="The provider's default model"
                onValueChange={(v) => void commitSettings({prompt_review_model: v})}
              />
            }
          />
        </RowGroup>
      </Section>

      <Section title="Assistant" actions={<SaveMode mode="instant" />}>
        <RowGroup>
          <Row
            htmlFor="s-assistant-provider"
            label="Assistant provider"
            description="Answers in the Ask DispatchQ chat and drafts the GitHub issue on the Feedback page. It sees DispatchQ's queue, runs and artifacts, never your projects, and every change it proposes waits for your OK."
            control={
              <Select
                id="s-assistant-provider"
                className="w-56"
                value={value.assistant_provider}
                options={assistantProviders}
                onValueChange={(v) => {
                  void commitSettings({assistant_provider: v, assistant_model: ''}).then(() =>
                    invalidate(keys.assistant, keys.feedback),
                  )
                }}
              />
            }
          />
          <Row
            htmlFor="s-assistant-model"
            label="Assistant model"
            description="DispatchQ's choice is a mid-size model for the chat and a small one for feedback"
            control={
              <ModelSelect
                id="s-assistant-model"
                className="w-56"
                provider={value.assistant_provider}
                value={value.assistant_model}
                emptyLabel="DispatchQ's choice"
                onValueChange={(v) => void commitSettings({assistant_model: v})}
              />
            }
          />
        </RowGroup>
      </Section>
    </>
  )
}
