// JSON shapes of the dispatchq daemon's HTTP API (internal/api).
//
// Conventions: Go time.Time arrives as an RFC 3339 string (ISOTime). A Go field
// tagged omitempty/omitzero is optional here (`?`), because the key is left out
// when the value is empty. Routes that answer 204 No Content have no response type.

/** RFC 3339 timestamp as produced by Go's time.Time JSON encoding. */
export type ISOTime = string

// Every non-2xx answer: writeErr in internal/api/api.go.
export interface ApiErrorBody {
  error: string
}

// ---------------------------------------------------------------------------
// Tasks (internal/task)
// ---------------------------------------------------------------------------

export type TaskTrigger = 'asap' | 'fixed' | 'cron'

// "agent" runs the prompt through a provider; "script" runs it as a shell script without a model.
export type TaskKind = 'agent' | 'script'

// "skip" bypasses the harness's permission prompts.
export type TaskPermissions = 'default' | 'skip'

/** task.Task as stored and returned. */
export interface Task {
  id: string
  name: string
  // Absent means "agent".
  kind?: TaskKind
  prompt: string
  working_dir: string
  // "" when the task is in no group.
  group: string
  trigger: TaskTrigger
  // Earliest start for trigger "fixed".
  fixed_at?: ISOTime
  cron?: string
  parallel: boolean
  enabled: boolean
  // Provider instance id; absent means the default provider.
  provider?: string
  // Pool id; mutually exclusive with provider.
  pool?: string
  backfill?: boolean
  model?: string
  reasoning_effort?: string
  permissions: TaskPermissions
  notify_on_result?: boolean
  depends_on?: string[]
  include_results?: boolean
  parent_run?: string
  workflow_id?: string
  // Set by the job that created this task; never editable.
  origin_id?: string
  origin_name?: string
  quiet_history?: boolean
}

/**
 * POST /api/tasks and PUT /api/tasks/{id} request body (decoded into task.Task).
 * Missing id is generated from the name; missing permissions becomes "default".
 * On PUT the id comes from the URL, and leaving out `group` keeps the stored group.
 */
export type TaskInput = Partial<Omit<Task, 'origin_id' | 'origin_name'>> &
  Pick<Task, 'prompt' | 'working_dir' | 'trigger'>

// schedule.Hold: why the scheduler did not start a due task, and since when.
export interface TaskHold {
  reason: string
  since: ISOTime
}

// GET /api/tasks -> Task[] with transient queue state (taskView).
export interface QueueTask extends Task {
  running: boolean
  // Next occurrence, cron tasks only.
  next_run?: ISOTime
  // Last actual start, cron tasks only.
  last_run?: ISOTime
  // The task's interrupted session waits for the rate limit to reopen.
  waiting_for_limit?: boolean
  // Why the task's provider (or pool) cannot run it right now.
  blocked_reason?: string
  // Names of the jobs this one still waits for.
  waiting_for?: string[]
  hold?: TaskHold
}

// POST /api/tasks/{id}/move query parameters (no body). `group` present, even "", moves into that group.
export interface TaskMoveQuery {
  to: number
  group?: string
}

// POST /api/tasks/import response (request body: raw .dispatchq bundle bytes).
export interface ImportResult {
  task: Task
  // Working dir named in the bundle that does not exist here; task.working_dir is "" then.
  missing_working_dir?: string
  // Harness the bundle was written for when no single local instance matches; task.provider is "" then.
  unresolved_provider?: string
}

// POST /api/tasks/{id}/export, POST /api/fs/choose, POST /api/update/download and POST /api/update/reinstall response (200; 204 when the dialog was cancelled).
export interface PathResult {
  path: string
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

// GET /api/groups -> Group[] (queue order).
export interface Group {
  name: string
  count: number
  collapsed: boolean
}

// POST /api/groups/collapse request.
export interface GroupCollapseRequest {
  name: string
  collapsed: boolean
}

// POST /api/groups/move request. `before` is the group to sit in front of ("" = ungrouped section); omit or null to move to the end.
export interface GroupMoveRequest {
  name: string
  before?: string | null
}

// POST /api/groups/rename request. Renaming onto an existing name merges the groups.
export interface GroupRenameRequest {
  name: string
  to: string
}

// ---------------------------------------------------------------------------
// Runs (internal/store/run.go)
// ---------------------------------------------------------------------------

export type RunStatus = 'running' | 'success' | 'failed' | 'rate_limited_waiting' | 'auth_error' | 'canceled'

// store.RunProvider: the provider a run actually used. kind "script" for a script job.
export interface RunProvider {
  id?: string
  kind?: ProviderKind | 'script'
  name?: string
  model?: string
  reasoning_effort?: string
  access_mode?: AccessMode
  pool?: string
}

/** store.Run plus the view flags (runView). */
export interface Run {
  run_id: string
  task_id: string
  task_name: string
  started_at: ISOTime
  finished_at?: ISOTime
  status: RunStatus
  session_id?: string
  exit_code: number
  log_path: string
  error?: string
  // Task snapshot at run time. In GET /api/runs its prompt is "" (stripped); GET /api/runs/{id} sends it in full.
  task?: Task
  provider?: RunProvider
  // Shadowed by runView and never filled by the handlers, so in practice always absent; read the log instead.
  final_output?: string
  final_output_truncated?: boolean
  workflow_id?: string
  parent_run_id?: string
  resume_at?: ISOTime
  cost_usd?: number
  input_tokens?: number
  output_tokens?: number
  num_turns?: number
  duration_ms?: number
  unread: boolean
  // A rate-limited run whose session the daemon will still resume.
  resume_pending?: boolean
}

// GET /api/runs/{id} response: same shape as a list entry, with the task prompt included.
export type RunDetail = Run

// ---------------------------------------------------------------------------
// Artifacts and notifications
// ---------------------------------------------------------------------------

// GET /api/artifacts -> Artifact[] (newest first). Content: GET /api/artifacts/{id}/content[?download=1] (raw file).
export interface Artifact {
  id: string
  title: string
  description?: string
  file_name: string
  // Path below the store's artifacts directory.
  rel_path: string
  size: number
  content_type: string
  task_id?: string
  task_name?: string
  run_id?: string
  group?: string
  origin_id?: string
  origin_name?: string
  published_at: ISOTime
  unread: boolean
}

export type NotificationKind = 'success' | 'failure' | 'artifact' | 'provider' | 'task'

// GET /api/notifications -> AppNotification[] (newest first). store.InboxEntry; its own `read` flag is always cleared in favour of `unread`.
export interface AppNotification {
  id: string
  kind: NotificationKind
  title: string
  message: string
  artifact_id?: string
  run_id?: string
  url?: string
  task_id?: string
  task_name?: string
  sent_at: ISOTime
  unread: boolean
}

// ---------------------------------------------------------------------------
// Providers (internal/provider)
// ---------------------------------------------------------------------------

// Adapter kinds registered in this build ("claude-code" = store.DefaultProviderKind).
export type ProviderKind = 'claude-code' | 'codex' | 'opencode'

export type AccessMode = 'provider-default' | 'read-only' | 'workspace-write' | 'full-access'

export type HealthState =
  'ready' | 'not_installed' | 'not_authenticated' | 'invalid_configuration' | 'disabled' | 'check_failed'

// provider.Health: readiness verdict for one instance.
export interface ProviderHealth {
  state: HealthState
  reason?: string
  binary?: string
  detail?: string
  checked_at: ISOTime
}

// provider.Instance: a configured provider (no credentials).
export interface ProviderInstance {
  id: string
  kind: ProviderKind
  name: string
  // "" means auto-detect.
  binary_path: string
  // "" means the CLI's default config dir.
  config_dir: string
  default_model: string
  // Instance that takes this one's tasks while its allowance is used up.
  fallback_provider: string
  fallback_model: string
  enabled: boolean
}

/**
 * GET /api/providers -> Provider[]; POST /api/providers (201), PUT /api/providers/{id}
 * and POST /api/providers/{id}/check -> Provider (freshly probed).
 */
export interface Provider extends ProviderInstance {
  health: ProviderHealth
  default: boolean
  // Binary dispatchq would use when binary_path is empty.
  detected?: string
  // Harness accepts a reasoning-effort setting.
  reasoning_effort: boolean
  // Harness can run prompt review / the assistant / feedback drafting.
  asides: boolean
  // Display name of the harness, e.g. "Claude".
  type_name: string
  default_config_dir?: string
  // Names of the environment variables the harness is started with. The values are
  // secrets and never come back from the daemon.
  env: string[]
}

// One variable in ProviderInput.env. A missing value keeps the one stored under the name.
export interface ProviderEnvInput {
  name: string
  value?: string
}

// POST /api/providers and PUT /api/providers/{id} request (providerInput). On PUT the id comes from the URL and kind cannot change.
export interface ProviderInput {
  id?: string
  kind?: ProviderKind
  name?: string
  binary_path?: string
  config_dir?: string
  default_model?: string
  // "" clears it.
  fallback_provider?: string
  fallback_model?: string
  // Omit to keep (PUT) or default to true (POST).
  enabled?: boolean | null
  // The complete list; a name left out is removed. Omit to leave the variables alone.
  env?: ProviderEnvInput[]
}

// GET /api/providers/kinds -> ProviderKindInfo[].
export interface ProviderKindInfo {
  kind: ProviderKind
  name: string
  default_config_dir?: string
}

// GET /api/models[?provider=<id>] -> Model[] (default provider when no id).
export interface Model {
  id: string
  label: string
}

// ---------------------------------------------------------------------------
// Limits and pools
// ---------------------------------------------------------------------------

export type LimitsState = 'ok' | 'unavailable' | 'unsupported' | 'disabled'

// provider.LimitWindow: one allowance window.
export interface LimitWindow {
  // Stable id, e.g. "five_hour", "week".
  id: string
  label: string
  // 0-100.
  used_percent: number
  resets_at?: ISOTime
}

// provider.Limits: one provider's allowance.
export interface ProviderLimits {
  state: LimitsState
  windows?: LimitWindow[]
  reason?: string
  plan?: string
  capacity?: number
  // The account spends a prepaid API credit balance, which cannot be read.
  api_key?: boolean
  // When the reading was taken.
  updated_at?: ISOTime
  checked_at: ISOTime
}

/**
 * provider.MemberScore: where a provider stands in a pool / for backfill.
 * tier: 0 room in both windows, 1 no reading yet, 2 nearly out, 3 cannot take work.
 */
export interface PoolRanking {
  provider: string
  name: string
  tier: 0 | 1 | 2 | 3
  urgency: number
  score: number
  weight: number
  // Percent of the long window left.
  week_free: number
  resets_at?: ISOTime
  running: number
  note: string
}

// GET /api/limits[?fresh=1] -> LimitsEntry[] (configuration order).
export interface LimitsEntry {
  id: string
  name: string
  type_name: string
  default: boolean
  limits: ProviderLimits
  // Absent for a disabled provider.
  urgency?: PoolRanking
  // Urgency is above the backfill threshold.
  backfill: boolean
  // The provider's rate-limit gate while it is closed.
  blocked?: ProviderBlock
}

// provider.Block: an account block behind a pause, as opposed to a used-up
// allowance window that resets on its own.
export type BlockReason = 'credit_exhausted' | 'org_disallowed'

export interface ProviderBlock {
  until: ISOTime
  reason?: BlockReason
}

export type LimitsResponse = LimitsEntry[]

export interface PoolMember {
  // Provider instance id.
  provider: string
  weight: number
}

// GET /api/pools -> Pool[]; GET /api/pools/{id}, POST /api/pools (201), PUT /api/pools/{id} -> Pool.
export interface Pool {
  id: string
  name: string
  members: PoolMember[]
  // Members in the order a run started now would try them, best first.
  ranking: PoolRanking[]
}

// POST /api/pools and PUT /api/pools/{id} request (poolInput). id is ignored on PUT.
export interface PoolInput {
  id?: string
  name?: string
  members: PoolMember[]
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

export interface PushoverSettings {
  enabled: boolean
  token: string
  user_key: string
}

export interface NtfySettings {
  enabled: boolean
  // "" means ntfy.sh.
  server: string
  topic: string
  token: string
}

export interface WebhookSettings {
  enabled: boolean
  url: string
  // "" means dispatchq's default JSON body.
  template: string
}

// "" follows macOS.
export type Appearance = '' | 'light' | 'dark'

/**
 * GET /api/settings and PUT /api/settings (request and response): store.Settings.
 * On PUT, paused, default_provider and backfill_urgency are ignored and kept as stored.
 */
export interface Settings {
  default_provider: string
  heartbeat_minutes: number
  pushover: PushoverSettings
  ntfy: NtfySettings
  webhook: WebhookSettings
  idle_timeout_minutes: number
  max_run_history: number
  system_prompt: string
  // 0 means the built-in default threshold.
  backfill_urgency: number
  paused: boolean
  prompt_review_disabled: boolean
  artifact_notify_disabled: boolean
  prompt_review_provider: string
  prompt_review_model: string
  // Provider and model of the assistant chat and the feedback page; "" = default provider / DispatchQ's choice.
  assistant_provider: string
  assistant_model: string
  default_working_dir: string
  appearance: Appearance
  // "#rrggbb" pins the accent colour; "" follows macOS.
  accent: string
}

// POST /api/pause request and response.
export interface PauseState {
  paused: boolean
}

// POST /api/backfill request and response.
export interface BackfillState {
  urgency: number
}

// ---------------------------------------------------------------------------
// Cron, review, health, stats
// ---------------------------------------------------------------------------

// GET /api/cron/check?expr=<expr> response (always 200).
export interface CronCheck {
  valid: boolean
  error?: string
  // Next 3 occurrences.
  next?: ISOTime[]
}

export type PromptReviewKind = 'task' | 'system' | 'script'

// POST /api/review/prompt request. Unknown kind is treated as "task".
export interface PromptReviewRequest {
  kind: PromptReviewKind
  prompt: string
  working_dir: string
}

// POST /api/review/prompt response (200; 204 when superseded by a newer review).
export interface PromptReviewResponse {
  // false: review off or impossible, hide the banner.
  enabled: boolean
  ok: boolean
  message?: string
  revised_prompt?: string
}

// GET /api/review/context response.
export interface ReviewContext {
  enabled: boolean
  // "<provider id>/<model>"; changes when the reviewer changes.
  reviewer?: string
}

export type NotifyStatus =
  '' | 'authorized' | 'denied' | 'not_determined' | 'provisional' | 'unknown' | 'unsupported'

export interface LimitedProvider {
  name: string
  until: ISOTime
  // Label of the provider taking this one's tasks meanwhile.
  fallback?: string
  // Absent for an ordinary rate limit.
  reason?: BlockReason
}

// GET /api/health response.
export interface Health {
  // "" when scheduled wake is healthy.
  wake_error: string
  // "" when unknown.
  notify_status: NotifyStatus
  // "" when the global rate-limit gate is open.
  limited_until: ISOTime | ''
  // Sorted by until.
  limited_providers: LimitedProvider[]
}

export interface StatsBucket {
  runs: number
  success: number
  failed: number
  cost_usd: number
  input_tokens: number
  output_tokens: number
  duration_ms: number
}

export interface StatsDay {
  // YYYY-MM-DD, local.
  date: string
  runs: number
  cost_usd: number
  tokens: number
}

export interface StatsProvider extends StatsBucket {
  // "" groups runs from before the provider was recorded.
  id: string
  name: string
  // The provider's runs per model, busiest first.
  models?: StatsModel[]
}

export interface StatsModel extends StatsBucket {
  // As the run recorded it, alias or full id; "" means the harness default.
  model: string
}

// GET /api/stats?days= query: a number of calendar days (1..366) or 'all'.
// Without it the window is 14 days.
export type StatsRange = number | 'all'

// GET /api/stats response.
export interface Stats {
  // Everything recorded, whatever the window.
  totals: StatsBucket
  // Rolling 7×24h, whatever the window.
  last_7d: StatsBucket
  // The chosen window's totals.
  period: StatsBucket
  // Calendar days the window covers, today included. For all: from the first
  // recorded run's day to today.
  days: number
  all?: boolean
  // One entry per day of the window, oldest first, cut to the newest 366.
  per_day: StatsDay[]
  // The window's split per provider and model, busiest first; absent when its
  // runs came from one provider and one model.
  by_provider?: StatsProvider[]
}

// ---------------------------------------------------------------------------
// Updates
// ---------------------------------------------------------------------------

// GET /api/update, POST /api/update/check, POST /api/update/dismiss response.
export interface UpdateStatus {
  current: string
  // false for a dev build.
  supported: boolean
  available: boolean
  // "" if unknown.
  latest: string
  tag?: string
  // Notes of every release newer than current.
  notes?: string
  skipped_count?: number
  html_url?: string
  all_releases_url?: string
  has_installer: boolean
  published_at?: ISOTime
  dismissed?: string
  // Version of the newest DispatchQ.app on disk.
  installed?: string
  // A newer build is installed than the one running; relaunch instead of downloading.
  restart_required?: boolean
  // Dev build only: the latest release can replace this build.
  reinstall?: boolean
  // Dev build only: the oldest release that can run on this build's data.
  data_floor?: string
  checking: boolean
  downloading: boolean
  last_checked?: ISOTime
  error?: string
}

// POST /api/update/dismiss request.
export interface UpdateDismissRequest {
  version: string
}

// POST /api/update/relaunch response (202).
export interface RelaunchResult {
  version: string
  // Path of the installed DispatchQ.app.
  app: string
}

// ---------------------------------------------------------------------------
// Assistant
// ---------------------------------------------------------------------------

// GET /api/assistant response.
export interface AssistantInfo {
  available: boolean
  reason?: string
  // Label of the provider instance that answers.
  provider?: string
}

// assistant.Message: one line of the conversation, replayed when the daemon forgot the session.
export interface AssistantMessage {
  role: 'user' | 'assistant'
  text: string
}

// POST /api/assistant/turn request. Empty session_id starts a conversation.
export interface AssistantTurnRequest {
  session_id: string
  text: string
  history: AssistantMessage[]
  // What the operator has open, so "this run" resolves.
  focus: {view?: string; run_id?: string; artifact_id?: string}
}

// assistant.TaskDraft: a job as the assistant wrote it; on update_task only the changed fields.
export interface AssistantTaskDraft {
  name?: string
  kind?: TaskKind
  prompt?: string
  working_dir?: string
  trigger?: TaskTrigger
  fixed_at?: ISOTime
  cron?: string
  group?: string
  provider?: string
  model?: string
  permissions?: TaskPermissions
}

// open_* happen at once; every other type is a proposal the operator confirms.
export type AssistantActionType =
  | 'open_view'
  | 'open_run'
  | 'open_artifact'
  | 'open_task'
  | 'add_task'
  | 'update_task'
  | 'run_now'
  | 'enable_task'
  | 'disable_task'
  | 'delete_task'
  | 'cancel_run'
  | 'pause_all'
  | 'resume_all'

// assistant.Action.
export interface AssistantAction {
  type: AssistantActionType
  view?: string
  id?: string
  task?: AssistantTaskDraft
  // What id refers to (task name, artifact title, run).
  label?: string
  // Why the proposal cannot be confirmed as it stands.
  problem?: string
}

// POST /api/assistant/turn response (assistant.Reply).
export interface AssistantReply {
  session_id: string
  text: string
  actions: AssistantAction[]
  // What it looked up on the way.
  steps?: string[]
}

// ---------------------------------------------------------------------------
// Feedback
// ---------------------------------------------------------------------------

// GET /api/feedback response.
export interface FeedbackInfo {
  available: boolean
  reason?: string
  // "owner/name".
  repo: string
  app_version: string
  os_version?: string
  max_turns: number
}

// POST /api/feedback/turn request. Empty session_id starts a conversation.
export interface FeedbackTurnRequest {
  session_id: string
  text: string
}

// POST /api/feedback/turn response (feedback.Draft). "ask": question is set; "ready": title/body are set.
export interface FeedbackDraft {
  session_id: string
  status: 'ask' | 'ready'
  question?: string
  title?: string
  body?: string
  labels?: string[]
  // No further question possible.
  final: boolean
}

// POST /api/feedback/url request.
export interface FeedbackUrlRequest {
  title: string
  body: string
  labels: string[]
}

// POST /api/feedback/url response: prefilled GitHub new-issue URL.
export interface FeedbackUrlResponse {
  url: string
}

// POST /api/uninstall request: must be sent as Content-Type application/json; the body itself is ignored.
export type UninstallRequest = Record<string, never>

// Route index
//
// Method  Path                                   Request                      Response
// GET     /api/tasks                             -                            QueueTask[]
// POST    /api/tasks                             TaskInput                    Task (201)
// POST    /api/tasks/import                      raw .dispatchq bytes           ImportResult
// POST    /api/tasks/{id}/export                 -                            PathResult | 204 cancelled
// PUT     /api/tasks/{id}                        TaskInput                    Task
// DELETE  /api/tasks/{id}                        -                            204
// POST    /api/tasks/{id}/enable                 -                            204
// POST    /api/tasks/{id}/disable                -                            204
// POST    /api/tasks/{id}/move?to=&group=        TaskMoveQuery (query)        204
// POST    /api/tasks/{id}/run-now                -                            202
// GET     /api/groups                            -                            Group[]
// POST    /api/groups/collapse                   GroupCollapseRequest         204
// POST    /api/groups/move                       GroupMoveRequest             204
// POST    /api/groups/rename                     GroupRenameRequest           204
// GET     /api/runs                              -                            Run[] (newest first, prompts stripped)
// GET     /api/runs/{id}                         -                            RunDetail
// POST    /api/runs/read-all                     -                            204
// POST    /api/runs/{id}/read                    -                            204
// POST    /api/runs/{id}/cancel                  -                            204
// POST    /api/runs/{id}/continue                -                            204
// GET     /api/runs/{id}/log                     -                            text/plain
// GET     /api/artifacts                         -                            Artifact[]
// POST    /api/artifacts/read-all                -                            204
// POST    /api/artifacts/{id}/read               -                            204
// DELETE  /api/artifacts/{id}                    -                            204
// GET     /api/artifacts/{id}/content?download=  -                            raw file
// GET     /api/notifications                     -                            AppNotification[]
// POST    /api/notifications/read-all            -                            204
// POST    /api/notifications/{id}/read           -                            204
// GET     /api/providers                         -                            Provider[]
// GET     /api/providers/kinds                   -                            ProviderKindInfo[]
// GET     /api/limits?fresh=1                    -                            LimitsResponse
// GET     /api/pools                             -                            Pool[]
// GET     /api/pools/{id}                        -                            Pool
// POST    /api/pools                             PoolInput                    Pool (201)
// PUT     /api/pools/{id}                        PoolInput                    Pool
// DELETE  /api/pools/{id}                        -                            204
// POST    /api/providers                         ProviderInput                Provider (201)
// PUT     /api/providers/{id}                    ProviderInput                Provider
// DELETE  /api/providers/{id}                    -                            204
// POST    /api/providers/{id}/check              -                            Provider
// POST    /api/providers/{id}/enable             -                            204
// POST    /api/providers/{id}/disable            -                            204
// POST    /api/providers/{id}/default            -                            204
// GET     /api/settings                          -                            Settings
// PUT     /api/settings                          Settings                     Settings
// POST    /api/pause                             PauseState                   PauseState
// POST    /api/backfill                          BackfillState                BackfillState
// GET     /api/models?provider=                  -                            Model[]
// GET     /api/cron/check?expr=                  -                            CronCheck
// POST    /api/review/prompt                     PromptReviewRequest          PromptReviewResponse | 204 superseded
// GET     /api/review/context                    -                            ReviewContext
// POST    /api/fs/choose?path=                   -                            PathResult | 204 cancelled
// POST    /api/fs/warm                           -                            202 | 204
// GET     /api/stats?days=<n|all>                -                            Stats
// GET     /api/health                            -                            Health
// GET     /api/ping                              -                            204
// GET     /api/update                            -                            UpdateStatus
// POST    /api/update/check                      -                            UpdateStatus
// POST    /api/update/dismiss                    UpdateDismissRequest         UpdateStatus
// POST    /api/update/download                   -                            PathResult
// POST    /api/update/relaunch                   -                            RelaunchResult (202)
// POST    /api/update/reinstall                  -                            PathResult
// POST    /api/uninstall                         UninstallRequest             204
// GET     /api/assistant                         -                            AssistantInfo
// POST    /api/assistant/turn                    AssistantTurnRequest         AssistantReply
// GET     /api/feedback                          -                            FeedbackInfo
// POST    /api/feedback/turn                     FeedbackTurnRequest          FeedbackDraft
// POST    /api/feedback/url                      FeedbackUrlRequest           FeedbackUrlResponse
// Any error                                                                   ApiErrorBody

export interface WhatsNewStatus {
  edition: string
  show: boolean
  // Some task or the system prompt still names ClaudeQ and was never checked.
  rename_pending: boolean
}

export interface RenameFinding {
  task_id: string
  name: string
  message: string
  revised_prompt?: string
}

export interface RenameCheck {
  state: 'idle' | 'running' | 'done' | 'unavailable'
  total: number
  checked: number
  skipped: number
  findings: RenameFinding[]
  errors?: string[]
}

export interface RenameApplyRequest {
  task_ids: string[]
}

export interface RenameApplyResult {
  applied: string[]
  changed?: string[]
}
