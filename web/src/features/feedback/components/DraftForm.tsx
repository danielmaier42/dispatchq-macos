import {ExternalLink, Tag} from 'lucide-react'
import {type KeyboardEvent, useRef, useState} from 'react'
import {Badge} from '@/components/ui/badge'
import {Button} from '@/components/ui/button'
import {Field} from '@/components/ui/field'
import {Input, Textarea} from '@/components/ui/input'
import {Kbd} from '@/components/ui/kbd'
import {toast} from '@/components/ui/toaster'
import {errorMessage, post} from '@/lib/api'
import {openExternal} from '@/lib/native'
import {enterKey, modifierKey} from '@/lib/platform'
import type {FeedbackUrlResponse} from '@/lib/types'
import {httpsOnly} from '../feedback'
import {setDraft, setError, useFeedback} from '../store'

// DraftForm is the issue as it will be filed. The environment line and the
// labels are stated here rather than edited here: they travel in the
// prefilled page, where they can still be removed.
export function DraftForm({repo, env}: {repo: string; env: string}) {
  const {title, body, labels, error} = useFeedback()
  const [opening, setOpening] = useState(false)
  const [titleMissing, setTitleMissing] = useState(false)
  const titleRef = useRef<HTMLInputElement>(null)

  const openIssue = async () => {
    if (opening) return
    const t = title.trim()
    if (!t) {
      setTitleMissing(true)
      titleRef.current?.focus()
      return
    }
    setError('')
    setOpening(true)
    try {
      const r = await post<FeedbackUrlResponse>('/api/feedback/url', {title: t, body, labels})
      const u = httpsOnly(r.url)
      if (!u) throw new Error('the issue link could not be built')
      openExternal(u)
      toast.success('Opened GitHub', {description: 'Press "Create" there to file the issue.'})
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setOpening(false)
    }
  }

  const onKeyDown = (e: KeyboardEvent) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault()
      void openIssue()
    }
  }

  return (
    <div className="animate-in rounded-xl border border-border bg-surface shadow-sm" onKeyDown={onKeyDown}>
      <div className="flex flex-col gap-4 p-4">
        <Field label="Title" htmlFor="fb-title" error={titleMissing && 'The issue needs a title.'}>
          <Input
            id="fb-title"
            ref={titleRef}
            autoFocus
            value={title}
            placeholder="Short, concrete title"
            aria-invalid={titleMissing}
            onChange={(e) => {
              setTitleMissing(false)
              setDraft({title: e.target.value})
            }}
          />
        </Field>
        <Field label="Description" htmlFor="fb-body">
          <Textarea
            id="fb-body"
            rows={12}
            value={body}
            placeholder="What happens, and what you expected"
            onChange={(e) => setDraft({body: e.target.value})}
          />
        </Field>
        {(labels.length > 0 || env) && (
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-[12px] text-muted-foreground">
            {labels.length > 0 && (
              <span className="flex items-center gap-1.5">
                <Tag className="size-3.5" />
                Labels
                {labels.map((l) => (
                  <Badge key={l} tone="outline">
                    {l}
                  </Badge>
                ))}
              </span>
            )}
            {env && (
              <span>
                <span className="font-medium text-foreground/80">{env}</span> is appended at the end of the
                issue.
              </span>
            )}
          </div>
        )}
      </div>
      <div className="flex items-center gap-3 border-t border-border bg-surface-2/50 px-4 py-3">
        <p className="min-w-0 flex-1 text-[12px] text-muted-foreground">
          Nothing has been sent yet. This opens a prefilled new issue on{' '}
          <span className="font-mono text-[11.5px]">{repo || 'GitHub'}</span> in your browser; it is created
          only when you press <strong className="text-foreground/80">Create</strong> there, and you can still
          change or delete anything on that page.
        </p>
        <span className="hidden items-center gap-0.5 sm:flex">
          <Kbd>{modifierKey()}</Kbd>
          <Kbd>{enterKey()}</Kbd>
        </span>
        <Button variant="primary" loading={opening} onClick={() => void openIssue()}>
          {!opening && <ExternalLink />}
          Open on GitHub
        </Button>
      </div>
      {error && (
        <p className="border-t border-danger/20 bg-danger-soft px-4 py-2 text-[12.5px] text-danger">
          {error}
        </p>
      )}
    </div>
  )
}
