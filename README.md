<div align="center">
  <img src="docs/branding/dispatchq/dispatchq-logo.svg" width="96" height="96" alt="DispatchQ">
  <h1>DispatchQ</h1>
  <p>A queue for unattended coding-agents that runs on your Mac</p>
  <p><a href="https://www.dispatchq.de/">Website</a></p>
</div>

<!-- TODO: screenshot of the Queue view goes here (docs/queue.png, light and dark). -->

DispatchQ takes the work you would otherwise babysit in a terminal and runs it
while you are away, most usefully overnight, once your usage allowance has
reset. A task is a prompt plus the folder it runs in, with a time it may start.
A background daemon runs it headless through
[Claude Code](https://claude.com/claude-code),
[Codex](https://learn.chatgpt.com/docs/developer-commands?surface=cli) or
[opencode](https://opencode.ai), and in the morning the result is waiting: the
full transcript in the **Log**, any file the run produced in **Artifacts**,
and a notification on your phone if you asked for one.

It began as a way to spend the nightly Claude Code allowance, and the rate
limit is still where it does most of its thinking: a run that hits the limit is
paused, and the same session continues when the window reopens, or another
provider takes the task over. Around that, the queue has grown into a small
workflow engine:

- **Several agents, one queue.** Any number of providers and accounts, each with
  its own allowance. A task names the one it runs on, or is asked of several at
  once, with a further task that joins the answers.
- **Tasks that make tasks.** A run can queue follow-up work, publish a report
  or an export, and send you a message, all through the bundled `dispatchq` CLI.
  A prompt can say *"if you find something worth fixing, queue it instead of
  doing it now."*
- **Built for 3 a.m.** Every run is told it is unattended. Prompts are checked
  against this Mac before they are queued. Hung runs are killed, orphaned runs
  recovered, and the Mac is woken for timed work and held awake while it runs.
- **Nothing leaves the machine.** Tasks, settings and history are plain files
  under `~/Library/Application Support/dispatchq`, and the daemon listens on
  loopback only. The agent CLIs talk to their own services; DispatchQ itself
  contacts nothing but GitHub's release API for update checks, and
  [feedback](#sending-feedback) is filed by you, in your browser.

## What you can do with it

- **Queue the big refactor at lunch**, let it run after midnight on the fresh
  allowance, and review the branch over coffee.
- **Run a watcher every fifteen minutes** that checks a feed, a mailbox or a
  build. Write it as a *script job* and it costs no usage at all, keeps running
  while the allowance is gone, and queues a real agent job the moment it finds
  something. Quiet history keeps its successes out of the record; a real change
  reaches your phone as a push notification.
- **Ask Claude and Codex the same question**, with a third task that waits for
  both and merges the answers into one report.
- **Publish a nightly PDF or HTML report** to Artifacts and open it on your
  phone straight from the notification.
- **Turn a backlog into tasks**: one run reads the list and queues one task per
  item, so each gets its own session, log and outcome.
- **Keep a list of nice-to-have work** for allowance that would otherwise
  expire: a *backfill* task runs only while a provider is so far behind on its
  week that part of it would go unused, and waits otherwise.
- **Hand a task to a colleague** as a `.dispatchq` file. They import it, adjust
  the folder, and get the same job on their Mac.
- **Just ask.** "Why did last night's run fail?", "show me the newest report",
  "every weekday at 7, summarise the open TODOs in this repo": the
  [assistant](#asking-dispatchq) answers in a sentence, opens what you asked for,
  and drafts the job for you to confirm.

## Contents

**Getting started**

- [How it works](#how-it-works)
- [Install](#install)
- [Coming from ClaudeQ](#coming-from-claudeq)
- [Using it](#using-it)
- [The app](#the-app)
- [Settings](#settings)

**Doing more with it**

- [Providers](#providers)
- [Provider pools](#provider-pools)
- [Command-line interface](#command-line-interface)
- [From another tool or agent](#from-another-tool-or-agent)
- [What every run is told](#what-every-run-is-told)
- [Letting a task queue follow-up work](#letting-a-task-queue-follow-up-work)
- [Asking several providers at once](#asking-several-providers-at-once)
- [Letting a task publish artifacts](#letting-a-task-publish-artifacts)
- [Letting a task send a notification](#letting-a-task-send-a-notification)
- [Script jobs](#script-jobs)
- [Quiet history for frequent jobs](#quiet-history-for-frequent-jobs)
- [Backfill tasks](#backfill-tasks)
- [Sharing tasks as files](#sharing-tasks-as-files)
- [The prompt review](#the-prompt-review)
- [Asking DispatchQ](#asking-dispatchq)
- [Sending feedback](#sending-feedback)

**Reference**

- [Features](#features)
- [How scheduling and the limit gate behave](#how-scheduling-and-the-limit-gate-behave)
- [Data on disk](#data-on-disk)
- [Uninstall](#uninstall)
- [Build from source](#build-from-source)
- [Architecture](#architecture)
- [Requirements](#requirements)
- [License](#license)

## How it works

DispatchQ is two pieces that share a file-based store:

- **A background daemon (`dispatchqd`)** installed as a per-user launchd
  LaunchAgent. It starts at login, restarts if it exits, and does all the work:
  scheduling, running tasks through the provider's CLI, tracking the rate
  limit, planning wake-ups, pruning history, sending notifications, and serving
  the dashboard on `127.0.0.1`. Exactly one daemon owns a data directory: a
  second one started against the same store says so and exits, so a queue is
  never scheduled twice.
- **A native app window (`dispatchqapp`)** that wraps that dashboard in a macOS
  WKWebView window. The app is just the UI — closing it never stops scheduling,
  and the daemon keeps running with the window shut.

The nightly cycle looks like this:

1. You queue tasks during the day. Each task is a **prompt** plus the **folder**
   it runs in (its repo/context), with an optional trigger time. A task can also
   be a **script job**, where that text is a program instead of a prompt — see
   [Script jobs](#script-jobs).
2. The daemon watches the queue and starts due tasks headless, through the CLI
   of the provider each task names (Claude Code by default), one at a time
   unless a task is marked parallel. A script job is started the same way, but
   as a program: no provider, no model, no allowance spent.
3. If a run hits the **rate limit**, DispatchQ pauses the whole queue and, once the
   limit clears, resumes the *same* Claude session — no work is lost. The pause is
   visible while it lasts: a banner names the time the queue continues, the run is
   marked **rescheduled** in the Log with its resume time, and you can drop that
   resume there so the task does not start again.
4. To run a timed task past a scheduled sleep, it wakes the Mac with `pmset`, and
   holds the Mac awake (`caffeinate`) while a run is in flight so a task never
   freezes mid-run.
5. Results — success, failure, rate-limit wait, or auth problem — land in the
   **Log** with the full run output, optionally with a notification.

## Install

1. Download the latest `dispatchq-<version>.pkg` from the
   [Releases](https://github.com/danielmaier42/dispatchq-macos/releases) page.
2. Open it and follow the installer.

The package installs **DispatchQ** to `/Applications`, sets up a per-user
LaunchAgent so the daemon starts at login, and opens **DispatchQ** when it is
done, so you can start adding tasks right away. Installing over an existing
version works the same way: the installer closes the open DispatchQ window first
(the daemon and any running task are not interrupted) and reopens the new
version at the end, so an update takes effect without a manual restart.

The installer verifies the hand-over instead of assuming it: it waits until the
daemon that answers on `127.0.0.1:10765` reports the version it just installed,
retries once (dropping a stale LaunchAgent that still points at an old copy of
the app), and reports the install as *failed* if the new daemon never takes
over — rather than finishing green while the machine keeps running the old
version. It resolves the logged-in user through macOS SystemConfiguration,
the console device and console login records, validating that the user's GUI
session exists. If user detection fails while either daemon is running, the
install fails instead of silently skipping the update. Before stopping
processes it unloads both the current LaunchAgent
and the old ClaudeQ agent, so neither can restart an outdated daemon during
the update. It replaces stale agent files, re-enables the DispatchQ service
and explicitly starts it. Remaining daemons are stopped only for the logged-in
user. An install cannot succeed while that user's old ClaudeQ daemon remains
running, or if the bundled daemon cannot report its version. It also lists any
other `DispatchQ.app` copies it finds.

You can update directly from ClaudeQ 1.x to the latest DispatchQ package;
installing 2.0.0 first is not required. If installation fails, open **Window →
Installer Log** in macOS Installer, select **Show All Logs** and look for the
`dispatchq:` messages. Closing the app window alone does not stop its daemon.

> The package is not notarized, so on first launch macOS may warn that it is from
> an unidentified developer. Right-click **DispatchQ → Open**, then confirm — or
> allow it under **System Settings → Privacy & Security**.

You'll also see two normal macOS prompts by design: **Allow notifications?** on
first launch, and **allow access to your Documents?** the first time a task's
folder is in a protected location (Documents, Desktop, Downloads). Allow both so
unattended runs aren't blocked.

macOS, not DispatchQ, decides how long a notification stays on screen. DispatchQ asks
for the **Alerts** style, which waits until you click it — but if macOS already
knows the app (or overrides it), set it under **System Settings → Notifications →
DispatchQ → Alerts**. Settings has a button that opens that pane directly.

To run tasks past a scheduled sleep, DispatchQ schedules wakes with `pmset`, which
needs one sudoers entry (the daemon prints the exact line on install, and the
dashboard shows it if a wake ever fails):

```sh
echo "$USER ALL=(root) NOPASSWD: /usr/bin/pmset" | sudo tee /etc/sudoers.d/dispatchq
```

## Coming from ClaudeQ

DispatchQ was called **ClaudeQ** up to version 1.x. Version 2.0 renames the
app, the binaries and everything they write, and moves an existing
installation over on its own. Update as usual, from the update banner or by
opening the new `dispatchq-<version>.pkg`; the GitHub repository keeps its old
name for now, so updates keep arriving from the same place.

What the update does:

| ClaudeQ | DispatchQ | How it moves |
|---------|-----------|--------------|
| `/Applications/ClaudeQ.app` | `/Applications/DispatchQ.app` | The installer puts the new app in place and moves the old one to your Bin once DispatchQ runs (only a bundle whose id says it is ClaudeQ). It creates the Bin folder if needed and preserves existing items there. In its place it leaves a verified link to `DispatchQ.app`. Installed with nobody logged in, the old app stays; delete it yourself. |
| `claudeq`, `claudeqd`, `claudeqapp` | `dispatchq`, `dispatchqd`, `dispatchqapp` | Inside the new app. The app also carries `claudeq` as a link to `dispatchq`, so a script that calls `/Applications/ClaudeQ.app/Contents/MacOS/claudeq` keeps working. That path is deprecated: a later release removes both links, so move scripts to the new path (the check below finds them). |
| LaunchAgent `de.maierdaniel.claudeq` | `de.maierdaniel.dispatchq` | The old agent is booted out, its daemon stopped and its plist removed before the new one starts, so the queue never runs twice. Installed with nobody logged in, DispatchQ does this the first time you open it. |
| `~/Library/Application Support/claudeq` | `~/Library/Application Support/dispatchq` | Moved on the first start, tasks, history, logs and artifacts included. The old path stays as a link to the new folder, so log paths in the history and scripts that read the old folder keep working. |
| `/etc/sudoers.d/claudeq` | `/etc/sudoers.d/dispatchq` | Replaced. |
| `CLAUDEQ_HOME`, `CLAUDEQ_BIN`, `CLAUDEQ_RUN_ID`, … | `DISPATCHQ_HOME`, `DISPATCHQ_BIN`, `DISPATCHQ_RUN_ID`, … | Every run gets both names for now. `CLAUDEQ_HOME` set in your shell is still honoured when `DISPATCHQ_HOME` is not set. |
| `.claudeq` task files | `.dispatchq` | Both import. |
| Webhook default body `"source":"claudeq"` | `"source":"dispatchq"` | Changes with the update. An automation that filters on the old value needs the new one; a webhook with its own template is unaffected. |

With a logged-in user, installation fails if it cannot retire the old app or
if either compatibility link does not resolve to DispatchQ. A different app
or an unrelated link at `ClaudeQ.app` is preserved and reported as an error,
so the installer never replaces something it cannot identify as ClaudeQ.

Your tasks themselves are not rewritten behind your back. The first time
DispatchQ 2.0 opens, a **What's new** dialog introduces the new name, the
redesigned app and the beta features that are now available to everyone. It
has a **Check tasks** button: the [prompt review](#the-prompt-review) looks at
every task whose prompt or script mentions ClaudeQ, and at the custom system
prompt if it does (a task that never does is skipped and costs nothing) and lists the ones that still call `ClaudeQ.app`, read a `CLAUDEQ_`
variable or name the old data folder, each with what it would change. Every
fix with a rewrite is selected; untick the ones you want to keep as they are
and click **Migrate**. A task you edited after the check is left alone. A task
that calls the old CLI still runs through the compatibility link, but the check
reports it anyway, because the link goes away in a later release. Closing the dialog while the check runs
or before migrating asks first.

The dialog opens by itself once; a fresh installation that never had ClaudeQ
skips it. Until the check has run with nothing left to fix, a banner above every
view says that some tasks still mention ClaudeQ; its **Check tasks** button
reopens the dialog and starts the check, and its close button hides the banner
for good. The command palette (⌘K) reopens the dialog too, under **What's new
in DispatchQ**. The check needs a prompt reviewer that can answer (Claude Code
or Codex, see [the prompt review](#the-prompt-review)).

Two things do not carry over, because macOS keys them to the app's bundle id:
the notification permission (macOS asks again) and the window's view
preferences such as the artifact grid or list layout. Appearance and accent
colour are stored by the daemon and do carry over.

## Using it

1. **Ask DispatchQ** (**⌘J**) and describe the job in plain words; the assistant
   drafts it and you confirm it on its card (see [Asking DispatchQ](#asking-dispatchq)).
   Or open the task form with the **+** next to it (**⌘N**) — give it a prompt, choose the **job type** (*Agent*, the
   default, or *Script* — see [Script jobs](#script-jobs)), pick the working folder (prefilled from
   **Settings → General → Default working directory**, if you set one), and choose a trigger
   (as-soon-as-possible, earliest start, or cron; a cron schedule is validated as
   you type, with a preview of its next three runs). Optionally pick the provider
   it runs on (only ones that can actually run are offered), override the model
   or reasoning effort or permissions, or enable *parallel* / *notify on result*. The folder dialog
   starts at the folder currently set for the task; if that folder no longer
   exists it opens at the nearest existing parent, and at your home folder when
   nothing is set — so a task whose folder was deleted or renamed can always be
   pointed somewhere new.
2. Leave it queued. The daemon runs it at the scheduled time (or overnight when
   the allowance resets).
3. Check the **Log** for the outcome, open a run to read the full log, or replay
   it. A finished one-shot task leaves the queue but stays in history; recurring
   tasks remain queued for their next occurrence.
4. The **Dashboard** shows how much of each provider's allowance is left;
   **Usage** shows what DispatchQ consumed over the last 14 days.

## Features

The complete list, one line each. Every item links to the section that
explains it.

**Scheduling**

- **Three triggers per task**: *as soon as possible*, *earliest start* at a
  fixed date and time, or a 5-field *cron* schedule that is validated as you
  type, with a preview of its next three runs. See
  [How scheduling and the limit gate behave](#how-scheduling-and-the-limit-gate-behave).
- **Priority is list order**, top first. Reorder in the app or with
  `dispatchq move`.
- **One at a time by default.** A task marked *parallel* runs alongside other
  parallel tasks.
- **Pause everything** with one switch in Settings: a run in flight finishes,
  nothing new starts, and the Mac is not woken.

**Providers**

- **Three harnesses, one queue**: Claude Code, Codex and opencode.
  Same queue, same history, same limit handling. See [Providers](#providers).
- **Any number of accounts.** Two providers of the same kind with separate
  configuration directories are two accounts with two allowances.
- **How much is left, at a glance.** The Dashboard shows each account's
  5-hour and weekly window with its reset time, read without spending any of
  it. See [How much is left](#how-much-is-left).
- **Checked before the night.** A provider whose CLI is missing or logged out
  blocks its tasks with the reason instead of failing them; they start by
  themselves once it works again. See [Is it ready?](#is-it-ready).
- **Rate limit per provider.** A run that hits the limit is paused and its
  session resumed when the limit clears; only that provider's tasks wait, and
  the pause is visible as *rescheduled* with the time it continues. See
  [When the limit is reached](#when-the-limit-is-reached).
- **A fallback for the limit.** A provider can name another one to take its
  tasks while its allowance is used up, so the night carries on.
- **Pools spread the work.** Put two subscriptions in a pool and give the task
  the pool: every run goes to the account whose weekly allowance would
  otherwise expire unused soonest, weighted by plan size. See
  [Provider pools](#provider-pools).
- **Auth errors are their own outcome**, notified, never silently retried.

**Tasks that do more**

- **Two kinds of job.** An *agent job* sends its prompt to a model; a *script
  job* runs it as a program — deterministic, free, and never held up by a rate
  limit. See [Script jobs](#script-jobs).
- **Per-task overrides** for provider, model, reasoning effort, permission
  handling, notification and quiet history, layered over global defaults.
- **Built-in run framing.** Every run is told it is headless and unattended,
  so it finishes or hands on instead of waiting for someone. See
  [What every run is told](#what-every-run-is-told).
- **Custom system prompt** appended to every run after the built-in one.
- **Prompt review.** Before a task is queued, Claude reads the prompt against
  this Mac and names what would go wrong at 3 a.m.; one click applies the
  rewrite. See [The prompt review](#the-prompt-review).
- **Self-queueing.** A run schedules follow-up tasks with `dispatchq queue`. See
  [Letting a task queue follow-up work](#letting-a-task-queue-follow-up-work).
- **Fan-out and join.** One job per provider, and a job that waits for all of
  them and combines the results. See
  [Asking several providers at once](#asking-several-providers-at-once).
- **Artifacts.** A run publishes a file with `dispatchq publish`; it lands in the
  Artifacts view with an in-app viewer for HTML, PDF, images, Markdown and text. See
  [Letting a task publish artifacts](#letting-a-task-publish-artifacts).
- **Own notifications.** A run sends a message with `dispatchq notify`,
  optionally with a link that opens on click. See
  [Letting a task send a notification](#letting-a-task-send-a-notification).
- **Quiet history** for frequent watchers: successful runs leave no record,
  failures do. See [Quiet history for frequent jobs](#quiet-history-for-frequent-jobs).
- **Backfill tasks** soak up allowance that would otherwise expire: they run
  only while a provider's urgency is above a threshold you set on the
  Dashboard. See [Backfill tasks](#backfill-tasks).
- **Share a task** as a `.dispatchq` file; the importer adjusts folder and prompt
  before it is queued. See [Sharing tasks as files](#sharing-tasks-as-files).

**Unattended by design**

- **Hung runs are killed** after a configurable idle timeout, whole process
  group included. Orphaned runs are recovered after a crash or power loss.
- **Wake and stay awake.** `pmset` wakes the Mac at each task's time plus an
  hourly heartbeat, `caffeinate` holds it through a run, and a run frozen
  across a long sleep is not mistaken for a hung one. A broken wake setup is
  shown in the dashboard.
- **File-access prompts up front.** Task folders are read at login and on
  edit, so macOS asks for Documents access while you are present.
- **Bounded history.** Old runs are pruned to a configurable count.

**Seeing what happened**

- **Notifications** natively on macOS and, optionally, to
  [Pushover](https://pushover.net), an [ntfy](https://ntfy.sh) topic, or any
  incoming webhook (Slack, Discord, Home Assistant, n8n) with a JSON body you
  template. Failures always notify, successes only if the task opts in. Every
  notification also lands in the app's **Notifications** view, and clicking the
  macOS banner marks it read there and opens what it is about — the artifact,
  the run's log, the link.
- **Full history** of every run with its complete log as a chat transcript or
  raw output, and replay.
- **Continue in Chat…** opens Terminal in the task's folder and resumes the
  run's session interactively, with the harness and account that ran it (a run
  a rate-limit fallback took over reopens on the fallback's account). On a
  running task the same button takes it over: the unattended run is stopped,
  and its chat reopens in Terminal so you can write to it.
- **Usage**: tokens, runs and API-equivalent cost per day over the last 14
  days.

**Platform**

- **Scriptable.** The bundled `dispatchq` CLI does everything the window does,
  with `--json` output for tools and agents. See
  [Command-line interface](#command-line-interface).
- **Native macOS**: its own window, Dock icon and menu bar. Light or dark and
  the accent colour follow macOS live, and either can be pinned in Settings →
  Appearance.
- **Keyboard first**: a command palette (**⌘K**) for every view, action, task
  and recent run, **⌘1**–**⌘8** for the views, **⌘F** to search a list.
- **Automatic updates** from GitHub releases, one click to install. A version
  whose daemon never took over is flagged with a **Finish update** button. A
  development build can **Reinstall** the latest release, as long as that
  release still reads the data the build left behind.
- **An assistant that operates DispatchQ.** Ask about runs and artifacts, have
  it open them, or describe a job and confirm the draft. See
  [Asking DispatchQ](#asking-dispatchq).
- **Feedback that writes itself.** Describe a bug or a wish, Claude drafts the
  GitHub issue, and you file it yourself. See [Sending feedback](#sending-feedback).
- **Local and private.** Human-readable TOML/JSON under your Library folder;
  the API is loopback-only.

## The app

The dashboard (and the native window that wraps it) has eight views. Six sit at
the top of the sidebar; **Settings** and **Feedback** ([below](#sending-feedback))
sit at the bottom, out of the way of the work. Above them are **Ask DispatchQ**
(**⌘J**, the [assistant](#asking-dispatchq)), a **+** that opens the task form
directly (**⌘N**), and the **command palette** (**⌘K**): type to jump to a view, add or import a task,
pause or resume all runs, switch the appearance, open a task (or run it now)
or a recent run's log. **⌘1** to **⌘8** switch views in sidebar order. Tasks
are added and edited in a panel that slides in from the right, so the list stays
visible. Its sections run What (name, group, job type), When, Prompt or
Script, Where, How it runs, so the schedule is in view before the long prompt;
**Group** picks an existing queue group or starts one by typing a name (an edit
changes the group only when this field is changed). A one-line summary above
the footer says what saving will do ("Runs every weekday at 07:00 on Claude
Code · Opus"). **Recurring** starts from `0 3 * * *`, nightly at 03:00, which is
also the first preset. **⌘↵** saves, and closing with unsaved changes asks
first. The window opens on the Dashboard:

- **Dashboard** — what needs attention at a glance. On top, the **allowance
  strip**: one line per enabled provider with its logo, what is left of each
  window (for Claude the 5-hour and the weekly window, for Codex the windows its
  account has) and when each resets; a bar turns orange at 25% left and red at
  10%. At the right end of each line sits the provider's urgency on a small
  gauge with the [backfill threshold](#backfill-tasks) as a tick. In a narrow
  window each allowance window moves to a line of its own under the
  provider's name. The label shows
  only the urgency value, without a running-task count; it turns
  green while the provider is above it. It is read without spending any usage
  (see [How much is left](#how-much-is-left)); the strip says how old the
  reading is and **Read now** asks again. An account on an API key has no plan
  limits and says so plainly. Right under it, the **Pool** card puts every
  [pool](#provider-pools) on one line, its members in the order a run started
  now would take them. Below that, cards in the order they need action:
  runs that **failed in the last 24 hours** (first while there are any), what
  is **up next** (due tasks first, with the reason a task is held; tasks with
  quiet history are left out), what is
  **running now** (and runs a rate limit paused, with their resume time), and
  the **last 7 days** (runs, success rate, tokens and cost). Each entry opens
  its log or its task; *+N more* opens the Log filtered to failures, or the
  Queue.
- **Queue** — the pending tasks in priority order. Add, edit, delete, enable/pause,
  reorder, or **run now** (a manual test run, independent of the trigger). Each
  row carries its task's name, its schedule in words (*Every day at 03:00*,
  *Mondays at 06:30*, *Yearly on 1 Jan at 00:00*) and when it is next due (a
  date once it is more than a week away), what it runs on (the provider and the
  model that provider will use for it), and how its last run ended — a dot and
  *last: failed 1 h ago*; clicking it opens that run's log. **⌘F** searches the
  tasks by name, group, provider or model, schedule and label; groups without a
  match are hidden, and dragging is off while a search is active. **Run now**
  keeps you on the Queue; its toast offers **Open log** (from the command
  palette it opens the log straight away). Enabling or pausing a task with its
  switch takes effect without a toast. **Run now** and the edit button
  appear when the pointer is on a row; the **…** menu holds the rest, including
  **Move up / Move down** and **Move to group**, so everything dragging does
  also works from the keyboard. The **All / Active** switch in the toolbar hides
  the paused tasks; moving a task up then puts it above the next visible one,
  skipping the hidden tasks in between. Tasks can be filed into **groups**: grab a row by its handle and drag
  it onto a group's header to move it there, between two rows to reorder it, or
  onto **Drop here to start a new group** to make a group on the spot — it is
  named as it is created. A group header folds its section shut and says how
  many tasks are inside; whether it is open or folded is remembered across
  restarts. Dragging a header moves the whole section, so the groups can be put
  in the order the work happens in, and the pencil on a header renames the group
  — typing the name of a group that already exists merges the two, after asking. Dragging the last task out of a group removes the group, since a
  group is nothing but the tasks that name it. While the global pause switch is on, a yellow banner sits above the
  list (with a **Resume runs** button) and **Run now** is disabled on every
  row. A running one-shot task moves to
  Log; a recurring task stays here with a *running* badge; hovering its
  cron expression shows the next occurrence and when it last ran. Underneath
  each task sits a badge for every option it has switched on: *script* (it runs
  as a program, without a model), *parallel*, *granted* (orange, the task skips
  permission prompts), *notifies* (blue), and *silent* (quiet history). A script
  job has no provider or model to name; its *script* badge says so. A task the rate limit interrupted carries a
  *rescheduled* badge (orange) whose tooltip names when its interrupted session
  continues, and a task whose provider cannot run it carries a red *blocked*
  badge naming what is wrong. A task that is due but did not start carries an
  orange *waiting* badge with how long it has been due (*waiting 12 min*, counted
  from the daemon's start if it restarted meanwhile); its
  tooltip gives the scheduler's reason, such as the pause, a task that runs
  alone, or an account out of allowance until a given time. A wait of five
  minutes or more is also written to `dispatchqd.out.log` with its reason, once,
  so it can still be explained after the badge is gone. **Export** in a row's **…** menu saves the task as a `.dispatchq`
  file via the native save panel, and **Import…** in the toolbar opens such a
  file in the task sheet for review. Whenever a prompt is written or changed,
  Claude checks it against this Mac and shows what it found in a purple banner
  under the prompt box, with **Apply** to take the rewrite; just opening a sheet
  again shows the earlier finding and costs nothing. A [script job](#script-jobs)
  is reviewed as the program it is, and the sheet hides the provider, model and
  permission settings it cannot have.
  See [The prompt review](#the-prompt-review).
- **Log** — every run, newest first. A failed or canceled run shows its error
  under the task name (the whole text on hover).
  Open a run to see the live/finished log as a chat view or raw output, along
  with the prompt. The chat view pairs every tool call with its result and folds
  a run of tool calls into one line, renders the assistant's text with its
  headings, lists and code blocks, follows a live run as it writes (scrolling up
  stops that), and has copy buttons for messages, code and the whole log;
  the artifacts the run published sit at the top and open in the viewer, and
  **Edit task** opens the task as it is now in the queue (off once it was
  deleted). A script run shows its script and **Output | Raw** instead; a running task can be stopped from there with **Cancel task**
  (its process is terminated and the run is recorded as `canceled`); a run the
  rate limit paused shows as **rescheduled** with the time it continues, and
  **Cancel resume** — on the row and in its log view — drops that plan: the
  interrupted session is discarded, the run is recorded as `canceled`, and a
  one-shot task leaves the queue instead of starting again (a recurring task
  keeps its schedule and starts fresh at its next occurrence); a finished
  run offers **Continue in Chat…**, and so does a running one in its log view,
  where it takes the run over: after a confirmation the unattended process is
  stopped, the run is recorded as `canceled` ("taken over in Terminal"), and
  only once it is gone does the session reopen. **Continue in Chat…** opens Terminal in the task's folder
  and resumes the run's session interactively — with the harness that owns it
  (`claude --resume`, `codex resume` for a Codex run, `opencode --session`
  for an opencode run) and the same permission
  mode the run had (a skip-permissions task resumes with
  `--dangerously-skip-permissions`);
  the button needs the session to still exist — Claude Code prunes old sessions
  after ~30 days; **Show more** loads older runs 25 at a time; and replay a task. Above the list sits the
  same filter bar as in Artifacts: a **search field** (⌘F jumps to it, Escape
  clears it) where every word has to occur somewhere in the run — task, folder,
  harness, model, group, outcome or error —, **All | Running | Failed**
  (Running shows only the runs executing right now, Failed the failed ones and
  auth errors), and **Filter**, which
  holds the queue **group**, the **task**, the **provider**, the **outcome**
  (success, failed, rate limited, …) and the start date (Any time, Today,
  7 days, 30 days, or a From/To day). Its badge counts what is set; each active
  filter also shows as a chip next to it, × removes that one, and **Reset** in
  the popover clears them all. Runs of a *quiet history* task
  appear here only if they did not succeed.
- **Artifacts** — files your tasks published, newest first.
  The list is grouped by day (Today, Yesterday, then the date), one card per
  artifact with an icon and colour for its file type (the switch next to
  **Notify me** turns the cards into a compact list; the choice is kept for
  this window): its title, its
  description, the file name and size, a type tag, who made it, and when. "Who made it" is the parent job when
  another job created the publisher (a watcher that filed a review job), else
  the publisher itself; hovering it spells out the whole chain, clicking it
  opens the producing run's log (while that run is still in history). Clicking a card opens the
  artifact in a full-window viewer; **←** and **→** step to the previous and
  next artifact of the list, like Quick Look: HTML, PDF, images, and text are previewed
  inline, anything else (archives, binaries) shows a short note instead of a
  preview. A web link clicked inside an HTML artifact opens in your default
  browser, since the preview cannot load outside pages; links to a spot within
  the page still scroll there. Markdown files (`.md`) are shown rendered, with
  headings, tables and checklists; their web links open in your default
  browser too, and images show only their alt text because the preview loads
  nothing from outside. The viewer names the file and its size under the title and offers
  **Open externally**, which hands the file to your
  browser to open or save, and — when the run that published the artifact can
  still be resumed — **Continue in Chat…**, the same interactive resume as in
  a run's log. Opening an artifact either way also marks the notification about
  it read. Deleting one (which removes the stored copy) is on its card while the
  pointer is there. **Notify me** in the toolbar is the same switch as
  *Settings → Notifications → Notify about new artifacts*: off, new artifacts
  arrive here quietly, without a notification.
  Above the list sits a filter bar. It starts with a **search field** (⌘F
  jumps to it, Escape clears it): every word you type has to occur somewhere
  in the artifact — title, description, file name, task, parent, group or
  file type — so "adr 0050" finds the ADR-0050 review and "seo pdf" the PDF
  of the SEO job. Next to it, as in the Log, **Filter** holds the publish date,
  the queue group and the parent job, with each active one shown as a removable
  chip; the list shows 25 artifacts at a time with a footer holding the count
  and **Show more**. The group narrows the list to one queue group, and the parent to the
  artifacts one top-level job produced, itself or through the jobs it created
  (see [where an artifact comes from](#where-an-artifact-comes-from)). The
  parent menu lists only jobs at the root of a chain, never the one-off jobs a
  watcher filed.
  Clicking the notification of a newly published artifact opens it in the
  viewer over whatever view is showing.
- **Notifications** — every notification DispatchQ sent, newest first, with an
  unread badge. It is the one view with a read state; the Log and Artifacts
  carry no badges, since what matters in them arrives here as well: a run's outcome, a new artifact, a provider that stopped or
  resumed running tasks, and whatever a task said with `dispatchq notify`. Each
  row shows the notification as it was shown on screen — a glyph for its kind,
  the title, the message — and when it was sent; the list is grouped by day
  and grows with **Show more** like Artifacts; **↑** and **↓** move between rows. A row does what clicking the macOS notification
  does: it turns read, and what it is about opens — the artifact in the viewer,
  the run's log, a task's link in the browser. A message does not repeat the
  task's name, which stands next to the title. The other way round, clicking a
  macOS notification marks it read here before it opens its target; a
  notification with no target (a provider's state) lands in this view. Opening
  an artifact or a run's log from its own view also reads the notification
  about it, and **Mark all read** in the toolbar does what it says. Above the
  list: a **search field** (⌘F; every word has to occur in the title, message,
  task or kind), **All | Unread**, and **Filter** with the **kind** (run
  succeeded, run failed, new artifact, provider, from a task), shown as a
  removable chip once set. The daemon keeps the last
  200 notifications; older ones make room. Notifications macOS is set to hide
  still arrive here, so nothing is lost while the banner is off.
- **Usage** — tokens, runs and cost for the period chosen in the header: 7, 14
  (the default), 30 or 90 days, or All. A bar chart per day (per week beyond 45
  days) with a **Tokens | Runs | Cost** switch; hovering a bar (or the arrow
  keys) shows all three for it. Above it the period's totals with the all-time
  figures beside them, below it one row per provider with its runs, outcomes
  and runtime in that period, and under each provider one row per model that
  ran (as the harness reported it, so an alias and its full id share a row;
  else as the task named it, *Harness default* when neither is known). The rows
  follow the chart's switch: the bar, the figure on the right and the order
  show the chosen measure. A provider's bar is its share of the period, a
  model's bar its share of that provider's. Tokens count fresh input and
  output: Codex's cached input is left out, as Claude Code reports cache reads
  apart, so providers compare fairly (Codex runs recorded by older versions
  still include it).
- **Settings** — global defaults and integrations (below). The custom system
  prompt gets the same review banner as a task prompt. A red badge here means
  an update is available.

The views keep themselves current: every few seconds the window re-reads the
queue, the log, the artifacts, the notifications, the usage and the limits the daemon last read,
including the views that are not open, so a view you click shows the present state. Coming
back to the window re-reads them at once. **View → Refresh** (**⌘R**) in the
menu bar re-reads every view and the providers' limits on demand; it leaves an
unsaved Settings form alone.

The dashboard is also reachable in a normal browser at
`http://127.0.0.1:10765` while the daemon is running.

## Settings

Settings lists its sections on the left — **General**, **Appearance**,
**Execution**, **Notifications**, **Providers**, **Pools**, **Assistant**,
**System** and **About**; **↑**/**↓** step through them. An available update puts a red dot on
**About**, which shows the app's claim, "A queue for unattended coding-agents that
runs on your Mac", and is where the update banner and button live.

Switches, menus and pickers apply the moment they change. Typed text (the
custom system prompt and the notification channels) waits for the bar that
appears at the bottom — *Unsaved changes · Discard · Save*, or **⌘S** — and every
group says which of the two it does (*Applies instantly* or *Needs Save*). A section with unsaved text carries a dot
in the list, and leaving Settings keeps the draft for the session.

| Section | Group | Setting | What it does |
|---------|-------|---------|--------------|
| **General** | Custom system prompt | Custom system prompt | Extra instructions appended to every run after the built-in prompt. Reviewed like a task prompt. |
| | Default working directory | Choose folder… | The folder a new task's working directory starts at. Still editable per task; **Clear** leaves the field blank. |
| **Appearance** | Appearance | System / Light / Dark | *System* follows macOS; *Light* or *Dark* keeps the window and the dashboard that way whatever the Mac is doing. Stored as `appearance` in `config.toml`, so a browser tab shows the same scheme. |
| | Accent colour | System, a preset, or your own | *System* follows the accent colour set in macOS, live. A preset or the colour picker pins another one for buttons, selections and highlights; text on it switches between white and black so it stays readable. Stored as `accent` (`#rrggbb`, empty = follow macOS). |
| **Execution** | Global pause | Pause all runs | Global stop switch: nothing starts while it is on, not even *Run now*; a run already in flight keeps going. |
| | Runs | Stop a run with no output for | Idle-timeout watchdog: kills a hung run (default 30 min; a working run keeps streaming and is unaffected; Off disables it). |
| | | Keep run history | How many runs (and their logs) to retain before pruning (default 500; Unlimited keeps everything). |
| | Scheduler | Check for due tasks every | How often the daemon wakes to look for work (15 min – 6 h; also the wake safety-net interval). |
| **Notifications** | On this Mac | Keep alerts on screen | Opens System Settings → Notifications, where DispatchQ's alert style lives: *Banners* disappear on their own, *Alerts* stay until you click them. A warning sits on top while macOS hides DispatchQ's notifications. |
| | | Notify about new artifacts | On by default; the same switch sits as **Notify me** in the Artifacts toolbar. Off: a published artifact still lands in Artifacts, but raises no notification on any channel and no entry in Notifications. Switching it back on does not announce what was published in the meantime. |
| | Pushover | Send to Pushover | Toggle plus API token and user key for phone push. A stored secret shows as *set*, with **Replace** and **Remove**; it is never shown again. |
| | ntfy | Send to ntfy | Toggle, server (empty = ntfy.sh), topic, and an optional access token for a protected topic. |
| | Webhook | Post to a webhook | Toggle, endpoint URL, and a JSON body template using `{{title}}`, `{{message}}`, `{{url}}` (empty = DispatchQ's own body). |
| **Providers** | Backfill threshold | Slider | Below the providers: the urgency above which [backfill tasks](#backfill-tasks) run, with each provider's urgency next to it. Applies on release. |
| | One card per provider | Its settings | Name and type, a health dot with **Check**, its current limits (the same bars as the Dashboard, when the harness reports any), the binary and configuration directory in use, the default model, and the provider that takes over when the limit is reached (and the model it uses). The switch turns it on or off at once; **Edit** opens a dialog that saves that provider alone; **Make default** and **Remove** sit in its **…** menu. See [Providers](#providers). |
| | | **Add provider** | Asks for an id, a type, a name and optionally a configuration directory. |
| **Pools** | One card per pool | Its settings | Name, members and their weights, which member the next run goes to and why. **Edit** opens a dialog with, per provider of the pool's type, a switch for membership and a weight (empty = the plan size the provider reports, e.g. *auto · 20×*). **Remove** is refused while a task uses the pool. **Add pool** asks for an id, a name and the members. See [Provider pools](#provider-pools). |
| **Assistant** | Prompt review | Check prompts with Claude | Whether Claude reviews a prompt against this Mac before the task is queued (on by default). |
| | | Review provider / Review model | Which provider answers the review, and with which model. Only providers that can hold an *aside* are offered; *Default* follows the global default provider, and the model falls back to the reviewing provider's own default. |
| | Assistant | Assistant provider / Assistant model | Which provider answers in [Ask DispatchQ](#asking-dispatchq) and drafts the GitHub issue on the [Feedback](#sending-feedback) page, and with which model. Left at *DispatchQ's choice*, on Claude Code the chat uses a mid-size model (Sonnet) and feedback a small one (Haiku), rather than whatever you picked for real tasks; other harnesses use their own default. Only providers that can hold an *aside* are offered. |
| **System** | Danger zone | Remove DispatchQ from this Mac | **Uninstall…** asks you to type *uninstall*, then opens the uninstaller in the macOS Installer. Your tasks, history and settings are kept unless you select them under **Customize**. See [Uninstall](#uninstall). |
| **About** | Software updates | Version / Check for updates | Current version, when the last check ran, and a manual **Check for updates** (`dispatchq update check` does the same from a terminal; the running app shows the result within a minute); an available update shows its notes with **Download & install**, **Dismiss** and **All releases**. On a development build, **Reinstall** downloads the latest release and opens its installer; it stays disabled, with the reason, while that release is older than the oldest one that can read this build's data (2.0.0 for now, since 1.x cannot run on data moved by the DispatchQ rename). |

## Providers

A **provider** is one configured agent harness — the CLI a task is actually run
by. Every installation has one, `claude`, which is the
[Claude Code](https://claude.com/claude-code) CLI; an existing configuration is
migrated into it automatically, keeping the binary path and default model it
already used.

Three harnesses are supported:

| Type | CLI | Notes |
|------|-----|-------|
| Claude (`claude-code`) | [Claude Code](https://claude.com/claude-code) | Passes a task's reasoning effort through Claude Code's `--effort` flag. Reports token counts and cost. Its only authority settings are "ask" and "skip every prompt". |
| Codex (`codex`) | [Codex](https://learn.chatgpt.com/docs/developer-commands?surface=cli) | Takes a reasoning effort and a real sandbox mode, so read-only and workspace-write actually mean something. Tasks may use a working folder that is not itself a Git repository. Reports tokens but no cost — DispatchQ never invents one. |
| opencode (`opencode`) | [opencode](https://opencode.ai) | Runs whatever model opencode is configured for, including local ones (it was brought up against LM Studio). Takes a reasoning effort as opencode's *variant*. Permission handling is all or nothing: the CLI's own prompts, or none. Reports tokens and cost as opencode states them. It cannot answer DispatchQ's own questions yet, so the prompt review and the feedback assistant are not offered on it, and its errors do not tell a rate limit from any other failure, so a limited run fails instead of pausing. |

You can configure as many instances as you like, including two of the same kind:
give each its own configuration directory and they are two accounts, with their
own sessions and their own rate limit. A limit on one does not hold up the
other.

A provider has:

| Field | What it is |
|-------|------------|
| Id | The stable name a task selects it by (`claude`). Fixed once created. |
| Type | Which harness it runs — Claude, Codex or opencode. Fixed once created. The app says *type* and names the harness; the adapter kind it maps to (`claude-code`) is what the file stores. |
| Name | The label shown in the app and in run messages. |
| Binary | Absolute path to the CLI. The background daemon can't see your shell `PATH`, so a full path is safest; empty auto-detects and the card offers what it found. |
| Configuration directory | Where that CLI keeps its account and sessions. Empty uses the CLI's own (`~/.claude`, `~/.codex`, `~/.config/opencode`), which the field shows as its placeholder. Two providers with separate directories are two separate accounts. |

Both paths must be absolute; a leading `~` is expanded and stored resolved, so
the file says what is actually used.
| Default model | Used for tasks on this provider that name no model of their own. |
| When the limit is reached | Another provider that takes this one's tasks while its allowance is used up. Empty means they wait for the window to reopen, which is what DispatchQ did before. |
| Model there | The model those substituted runs are given. Empty (*Decided by DispatchQ*) keeps the task's own model between two accounts of the same CLI and takes the substitute's default model otherwise. Only shown once a fallback is chosen, and cleared with it. |
| Enabled | Off keeps the provider and its tasks, but runs nothing on it. |

One provider is the **default**: tasks that name none run on it. It cannot be
switched off or removed while it holds that role — make another one the default
first — because most tasks name no provider and would all stop at once.

**Settings → Providers manages all of this.** The providers are listed under
their harness (Claude, Codex, opencode), each group headed by its logo; the
default provider always comes first. Each provider gets its own card,
headed by its name, with an on/off switch that applies at once and
**Edit**, which opens a dialog for the name, binary, configuration directory,
default model and fallback and saves that provider alone. *Check*, *Make
default* and *Remove* are actions and take effect at once. Below them sits **Add provider**, which opens a dialog asking for the two things
that cannot be changed afterwards — the id and the type — plus an optional
configuration directory, which is what makes the new one a second account.

`dispatchq provider …` does exactly the same things, and both refuse the same
changes for the same reasons — removing an instance a task still names, for
instance, which names the tasks that have to change first.

### What Codex and opencode cannot tell DispatchQ yet

Both harnesses run tasks like Claude Code does, with two limits worth knowing.

Codex: what a real exhausted ChatGPT allowance looks like in its output has not
been observed yet. A rate-limited Codex run is paused and resumed on a fixed
delay rather than at the time the provider names, because in the controlled
test it named none.

opencode: its error reporting is the weakest of the three. Running it locally,
every failure came back in the same generic shape, so DispatchQ cannot tell an
exhausted allowance or a logged-out account from any other error. A failed
opencode run is therefore recorded as failed, not paused and resumed, and the
readiness check can only confirm that the binary runs.

Editing a task changes its provider only when you say so. Every other edit — the
prompt, the folder, the schedule — goes through whatever state the current
provider is in, since that edit may well be how you are fixing it.

### Is it ready?

Before a task starts, DispatchQ checks that its provider can actually run it. The
check costs no model usage — it looks at the binary, asks for its version, and
asks the CLI whether anyone is logged in — and it reports one of:

| Status | Meaning |
|--------|---------|
| Ready | Jobs can start. |
| Not installed | The CLI was not found, or the configured path cannot be run. |
| Not logged in | The CLI is there, but no account is signed in (`claude auth login`). |
| Invalid configuration | The provider's own settings cannot be used — an unreachable configuration directory, say. |
| Switched off | You disabled the provider. |
| Could not be checked | The check itself did not reach a verdict, so DispatchQ does not assume either way. |

A fresh installation therefore shows the real state of Claude Code rather than
assuming it is there because it is the default provider.

What follows from an unready provider:

- **New tasks are refused** — in the app and from the CLI — with the reason. A
  job that is known in advance to fail is not worth filing. *Could not be
  checked* is the exception: "I could not ask" is not "it does not work", so the
  task is filed and simply waits, rather than a running job losing the follow-up
  it just queued because one probe timed out.
- **Tasks that already exist stay queued.** They are not started, they take no
  concurrency slot, and nothing about their schedule advances: a one-shot task
  is not marked done, a cron task keeps its next occurrence. The Queue row shows
  a red *blocked* badge with the reason.
- **When the provider works again, they start by themselves** on the next check.
  Nothing has to be re-queued.
- **You are told once.** A provider that breaks raises one notification, and one
  more when it recovers — not one per scheduler tick, and not again after a
  daemon restart.

A rate limit is not a provider problem: that pauses the run and resumes it, as
it always did — and it pauses only the provider that hit it. The other providers
keep working, because the allowance belongs to one account.

### When the limit is reached

A provider can name a **fallback**: the provider its tasks run on while its own
allowance is used up. Without one — the default — those tasks simply wait, as
before.

- It applies to the rate limit and to nothing else. A provider that is missing,
  logged out or switched off is still never answered by running the work
  somewhere else; those tasks stay queued and say why.
- The chain is followed as far as it reaches. A provider in it that cannot take
  the work right now — rate-limited itself, switched off, not installed, not
  logged in — is stepped over and its own fallback is asked. When nothing in the
  chain can work, the tasks wait: the queue is honest about being stuck.
- **The interrupted session does not travel.** A session belongs to the account
  that issued it, so the substitute starts the task again from the beginning and
  says so in the run's log, which also names the provider it came from. Once
  that run finishes the paused session is dropped rather than resumed later:
  the work has been done, and doing it twice is worse than losing a
  conversation. The paused run stays in the Log as the record of what
  happened.
- **The model can be chosen with the fallback** ("Model there"). Left open, the
  task's own model travels only between two accounts of the same harness, and a
  fallback of a different type runs its own default model instead of a name it
  would not understand.
- A fallback must name another configured provider, and the chain may not close
  into a circle; removing a provider that is somebody's fallback is refused by
  name, exactly like removing one a task still uses.
- The waiting banner names both: *Claude → Second account*, and says the queue
  keeps running rather than that nothing starts. With several providers blocked
  it says which part of the work carries on.

Set it on the provider's block in **Settings → Providers** ("When the limit is
reached", plus "Model there"), or with
`dispatchq provider edit ID --fallback OTHER --fallback-model MODEL`; `none`
clears either flag.

### How much is left

The Dashboard and each provider's block in **Settings → Providers** show the
account's allowance windows: how much of each is used and when it resets.
Reading them costs no tokens and none of the allowance.

| Harness | Where the figures come from | Windows shown |
|---------|-----------------------------|---------------|
| Claude | The usage endpoint Claude Code's own `/usage` screen reads, called with the login the CLI stored for that provider's configuration directory | 5 hours, week |
| Codex | The CLI's own `codex app-server`, asked `account/rateLimits/read` with the provider's `CODEX_HOME` | whichever windows the account reports, one or two (5 hours, week) |
| opencode | nothing; it runs whatever backend it is pointed at | none, shown as *No limit* |

The daemon reads them **when the Dashboard opens**, on **View → Refresh**
(**⌘R**), **every 15 minutes** on its own, and **right after every run** on that
provider. A request that comes within 30 seconds of the last reading reuses it,
so switching views does not hammer the provider. The window's regular polling
only shows what was last read; it never asks the provider itself.

When a reading fails, the last good figures stay up with their age and the
reason underneath, for example an expired Claude login: DispatchQ never renews a
login itself (that would rotate the CLI's token and log it out), so the figures
come back with the next run on that account or after `claude auth login`. The
Claude endpoint is not a documented API; if Anthropic changes it, the Dashboard
says the limits cannot be read and nothing else is affected.

The same figures are on the command line, read right then rather than from
the daemon's memory, so the command works with the daemon stopped too:

```sh
dispatchq provider limits                 # every provider, one line per window
dispatchq provider limits claude --json   # one provider, in the shape the API serves
```

```
ID           WINDOW   USED  RESETS                 URGENCY       NOTE
claude       5 hours  10%   Sat 01:19 (in 4h 36m)  0.3
claude       Week     85%   Mon 02:59 (in 2d 6h)
codex        Week     20%   Tue 09:00 (in 3d 2h)   1.8 backfill
opencode     -        -     -                      -             No limit
```

The **urgency** column is the one the Dashboard shows (see
[Backfill tasks](#backfill-tasks)); *backfill* marks a provider above the
threshold. Read from the CLI, it does not know about runs in flight, which
share the daemon's figure. A running job can ask before it hands work to
another account: `used_percent` per window, `state` (`ok`, `unavailable`,
`unsupported`, `disabled`), `urgency` and `backfill` are in the JSON.

### Credentials

DispatchQ stores no passwords, tokens or API keys. It stores the *path* of a
CLI's configuration directory; the CLI owns what is inside it. Readiness checks
read only whether a login exists, never whose it is, and neither the daemon log
nor the API ever carries account details. The one exception to "never reads
the login" is the Claude limits reading above: the daemon reads the access
token Claude Code stored (from the macOS keychain, through the same `security`
tool the CLI uses, so no keychain prompt appears) and sends it to Anthropic
only, the same place the CLI sends it. It is held for that one request and is
never written, logged or served over the API.

## Provider pools

A **pool** is a set of providers of one type, two Claude subscriptions for
example, that a task can be given instead of a single provider. DispatchQ
decides at every start which member runs it, so the allowances are used up
before they reset instead of one account running dry while the other's
expires unused.

**How a member is chosen**, at the moment a run starts:

1. Members that cannot take work right now are out: switched off, not ready,
   or waiting out a rate limit.
2. Members whose 5-hour window is at 90% or more, or whose week is used up, go
   to the back. They are still tried if nothing else can run.
3. Among the rest, the highest **urgency × weight** wins. Urgency is the
   provider's own, the figure the Dashboard shows next to it:

   `urgency = free share of the week × hours in a week ÷ hours until the week resets ÷ (1 + runs already on it)`

   1 means spending evenly from now on uses up exactly what is left; 2 means it
   would take twice that pace, so half of what is left is at risk of expiring
   unused. 10% left that resets in 4 hours (urgency 4.2) beats 60% left for
   another five days (0.84). The **weight** is
   the plan's size: DispatchQ reads it from the Claude login (Max 20x = 20, a
   Team seat on Max 5x = 5, Pro = 1), so 10% of a large plan counts for more
   than 10% of a small one. Codex does not say how its plans compare, so a
   Codex member counts as 1 unless you set its weight.
4. Runs already going on a member share its urgency. Two pool tasks that start
   in the same tick land on two accounts rather than both on the first.
5. A member with no limit reading yet ranks after the ones with a reading.

**Sessions.** A pool task that hit a rate limit continues its session on the
same member as soon as that member is open again, because the session only
exists there. While that member is still limited, the task starts over on
another member instead of waiting, which is the point of the pool. The run's
log says so.

**Seeing the choice.** Every run of a pool task opens its log with the choice
and the reasons, for example *Pool Claude chose Claude Max: Claude Max (14% of
week left, resets in 2 d); Claude Team (100% of 5 hours used)*. The Log names
the pool next to the provider. The Dashboard and `dispatchq pool show` list the
members in the order the next run would take them, with each one's weight
and the reason for its place (the commands add the urgency); `dispatchq pool next` names the member the
next run goes to. Both commands ask the running daemon, so they show exactly
what the Dashboard shows, rate-limit pauses included. With no daemon running
they read the members' limits themselves and say so; a member waiting out a
rate limit is then not recognised as such.

**Rules.**
- Every member must be of one type, so the model a task names means the same
  on each. The model list in the task sheet is the first member's.
- A pool has its own id, which no provider may share. Tasks choose it in the
  same Provider field as a provider (listed under *Pools*), or with
  `--pool ID` on `add`, `edit` and `queue`. A task names a provider or a pool,
  never both; naming one clears the other.
- A member's own rate-limit fallback is not used inside a pool: the pool is
  the fallback.
- A pool is offered and a task on it is accepted as long as at least one member
  can run. The queue marks the task blocked only when none can.
- A provider cannot be removed while it is a member, and a pool cannot be
  removed while a task uses it.
- Jobs a pool task queues inherit the pool, not the member the parent landed
  on. A task exported as a `.dispatchq` file leaves its pool behind, like its
  provider.

```sh
dispatchq pool add claude-pool --name Claude --member claude --member claude-team
dispatchq add --id nightly --prompt "…" --dir ~/code/app --pool claude-pool
dispatchq pool next claude-pool
dispatchq pool show claude-pool
```

## Command-line interface

Everything the app does is also available on the command line. You can list the
queue, read and change a task's prompt and settings, edit the global settings,
trigger a test run, and read run history. This section is written to stand on its
own, so another tool or agent needs nothing else to drive DispatchQ. See
[From another tool or agent](#from-another-tool-or-agent).

### Where the binaries are

Both binaries ship **inside the app bundle**, and neither is on your `PATH`:

```
/Applications/DispatchQ.app/Contents/MacOS/dispatchq     # the control CLI
/Applications/DispatchQ.app/Contents/MacOS/dispatchqd    # the daemon
```

Call them by full path:

```sh
/Applications/DispatchQ.app/Contents/MacOS/dispatchq list
```

…or put the directory on your `PATH` once:

```sh
export PATH="/Applications/DispatchQ.app/Contents/MacOS:$PATH"
```

The rest of this section writes them as plain `dispatchq` and `dispatchqd`. Both work
on the store under `~/Library/Application Support/dispatchq` directly (override it
with `DISPATCHQ_HOME`), so they work whether or not the app window is open. The
running daemon re-reads the store on its next tick, so a change never needs a
restart.

### `dispatchqd` — the daemon

```
dispatchqd run [--interval 5s] [--no-wake] [--addr 127.0.0.1:10765]
dispatchqd install      # install & start the LaunchAgent (autostart at login)
dispatchqd uninstall    # stop & remove the LaunchAgent
dispatchqd --version
```

The installer runs `install` for you; you rarely need these directly.

### `dispatchq` — the control CLI

```
dispatchq list [--json]                          # show the queue
dispatchq show   ID [--json]                     # one task in full, prompt included
dispatchq add    --id ID --prompt P --dir DIR [--name N] [--group G]
               [--kind agent|script]           # script = a program, no model
               [--trigger asap|fixed|cron] [--at RFC3339] [--cron EXPR]
               [--provider ID] [--model M] [--reasoning-effort E] [--parallel]
               [--skip-permissions] [--notify] [--quiet-history]
               [--backfill]                    # run only on allowance that would go unused
dispatchq edit   ID                              # open the whole task in $EDITOR
dispatchq edit   ID [--name N] [--prompt P | --prompt-file PATH] [--dir DIR]
               [--group G]                     # "" takes it out of its group
               [--kind agent|script]           # switching to script drops the model settings
               [--trigger asap|fixed|cron] [--at RFC3339] [--cron EXPR]
               [--provider ID] [--model M] [--reasoning-effort E]
               [--parallel=BOOL] [--enabled=BOOL]
               [--skip-permissions=BOOL] [--notify=BOOL] [--quiet-history=BOOL]
               [--backfill=BOOL]
dispatchq queue  --prompt P [--at RFC3339 | --in DUR | --cron EXPR] [--dir DIR] [--name N]
               [--group G] [--kind agent|script]   # a queued job is an agent job unless told
               [--provider ID] [--model M] [--reasoning-effort E]
               [--parallel=BOOL] [--skip-permissions=BOOL]
               [--notify=BOOL] [--quiet-history=BOOL] [--backfill=BOOL]
               [--depends-on JOBID]...            # wait for these jobs to finish
               [--include-results]                # and put their answers in the prompt
                                                  # (a script reads them on stdin)
               [--json]                           # print the new job, id included
dispatchq publish --file PATH [--title T] [--description D]   # publish a file as an artifact
dispatchq artifacts list [--json] [--origin ID] [--since TS]   # published artifacts, oldest first; TS is RFC3339 or a duration like 24h
dispatchq notify --title T --message M [--url U]  # send a notification, no artifact
dispatchq export ID [--out PATH] [--force]       # write the task to a .dispatchq file
dispatchq import PATH [--id ID] [--provider ID] [--model NAME]   # add the task from a .dispatchq file
dispatchq rm ID
dispatchq enable ID | dispatchq disable ID
dispatchq move   ID INDEX                        # 0 = highest priority
dispatchq run-now ID                             # run once, now, for testing
dispatchq status [--all]                         # recent runs; unread marked *
dispatchq status [--all] --json                  # the same runs as JSON: origin job, error, log path
dispatchq read RUNID | dispatchq read-all
dispatchq provider list [--json]                 # the harnesses tasks run on
dispatchq provider show ID [--json]
dispatchq provider check ID [--json]             # probe it now
dispatchq provider limits [ID] [--json]          # 5-hour/weekly windows, used and reset, and the urgency
dispatchq pool list [--json]                     # provider pools
dispatchq pool next ID [--json]                  # the member the next run goes to, with its urgency and the ranking
dispatchq pool show ID [--json]                  # members ranked as a run started now would take them
dispatchq pool add  ID --member PROVIDER[=WEIGHT]... [--name N]
dispatchq pool edit ID [--name N] [--member PROVIDER[=WEIGHT]]...   # --member replaces the list
dispatchq pool rm ID
dispatchq provider add  ID --kind claude-code|codex [--name N] [--path PATH]
                      [--config-dir PATH] [--default-model MODEL]
                      [--fallback ID] [--fallback-model MODEL]
dispatchq provider edit ID [--name N] [--path PATH]
                      [--config-dir PATH] [--default-model MODEL]
                      [--fallback ID|none]      # who takes over at the limit
                      [--fallback-model M|none] # what they run it with
dispatchq provider enable ID | dispatchq provider disable ID
dispatchq provider default ID                    # run tasks that name none on it
dispatchq provider rm ID
dispatchq settings [--json] [--default-provider ID]
                 [--heartbeat-minutes N]
                 [--idle-timeout-minutes N] [--max-run-history N]
                 [--system-prompt S | --system-prompt-file PATH]
                 [--paused=BOOL]                # pause/resume every run
                 [--backfill-urgency X]         # backfill tasks run above this urgency (0.5-5)
                 [--artifact-notify=BOOL]       # notify when a task publishes an artifact
                 [--pushover=BOOL] [--pushover-token T] [--pushover-user U]
                 [--ntfy=BOOL] [--ntfy-server S] [--ntfy-topic T] [--ntfy-token T]
                 [--webhook=BOOL] [--webhook-url U] [--webhook-template J]
dispatchq update check [--json]                  # check for a newer release now; the app shows its update prompt
dispatchq uninstall [--purge] [--yes]            # run the uninstaller in the terminal (see Uninstall)
dispatchq --version
```

### Looking at the queue

`dispatchq list` prints one line per task in priority order, index 0 first:

```
#  ID              NAME            KIND    TRIGGER  WHEN         PARALLEL  ENABLED
0  nightly-sweep   Nightly sweep   agent   cron     0 3 * * *    false     true
1  needs-review    Needs review    script  cron     */5 * * * *  true      true
```

Prompts are often pages long, so that table leaves them out, and a name longer
than 40 characters is cut with an ellipsis so the columns stay readable.
`dispatchq show ID` prints every setting of one task, its full name and then its
complete prompt — or, for a script job, its script; the settings only an agent
job has are left out. Add `--json` to either command for the same data as JSON,
prompts and full names included.

### Editing a task

`dispatchq edit` changes a task that already exists. The id is fixed and cannot be
changed. There are two ways to use it.

**With flags.** Only the settings you pass are touched, everything else keeps its
value. This is the form to use from a script or an agent.

```sh
dispatchq edit nightly-sweep --prompt-file ./new-brief.md   # replace just the prompt
dispatchq edit nightly-sweep --cron "30 2 * * 1-5"          # reschedule
dispatchq edit nightly-sweep --model opus --notify=true     # per-task overrides
dispatchq edit nightly-sweep --provider claude             # run it on another provider
dispatchq edit review-branch --provider codex --reasoning-effort xhigh
dispatchq edit prod-watch --quiet-history=true              # drop its successful runs
dispatchq edit nightly-sweep --group "Nightly"              # file it under a queue group
dispatchq edit nightly-sweep --group ""                     # and take it back out
dispatchq edit nightly-sweep --enabled=false                # pause it
dispatchq edit prod-watch --kind script --prompt-file ./watch.sh   # make it a script job
```

- `--prompt-file` reads the prompt from a file, or from stdin when you pass `-`.
  That is the practical way to set a long prompt without fighting shell quoting.
- `--at` implies `--trigger fixed` and `--cron` implies `--trigger cron`, so a
  reschedule is one flag. Changing the trigger clears the timing fields that no
  longer apply.
- `--model ""` drops a per-task model override back to the provider's default
  model, and `--provider ""` back to the default provider.
- `--reasoning-effort` asks the model to think harder or less hard, for the
  harnesses that take such a setting. Claude Code receives `--effort`, Codex its
  reasoning-effort configuration, and opencode its variant. An empty value leaves
  the harness default unchanged. It is
  ignored by the ones that do not, so it survives a move between providers.
- Changing `--provider` without naming a `--model` clears the model too, so the
  new provider's own default applies — one harness's model is never carried into
  another. Pass both to keep an explicit model across the change.
- A task is refused if the provider it names is unknown or cannot run right now;
  the error says which and why (see [Providers](#providers)). A
  [script job](#script-jobs) is never refused for that reason: it runs no
  harness, so no provider can be in its way.
- `--kind script` turns a task into a script job and clears the provider, model,
  reasoning effort and permission bypass it no longer uses. `--kind agent` turns
  it back.
- Every edit is validated before it is written. An invalid cron, an unparseable
  time, or an empty prompt fails with a message and leaves the task as it was.

**Interactively.** `dispatchq edit ID` with no flags opens the whole task as a
commented TOML document in `$VISUAL` or `$EDITOR`, falling back to `vi`. Every
setting is in there, and the prompt is an editable multi-line block. Save and
close to apply; leave the file unchanged to cancel. If what you wrote does not
parse or does not validate, nothing is written and the CLI tells you where it
kept your draft. This form needs a terminal. Without one it stops and points you
at the flags.

### Global settings

`dispatchq settings` with no flags prints every global setting. With flags it
changes the ones you name and prints the result. They are the same values as the
app's [Settings](#settings) view.

```sh
dispatchq settings                                        # show everything
dispatchq settings --default-provider claude              # provider for tasks that name none
dispatchq settings --system-prompt-file ./house-style.md   # custom system prompt
dispatchq settings --default-working-dir ~/Code            # a new task's folder starts here
dispatchq settings --idle-timeout-minutes 45 --max-run-history 1000
dispatchq settings --paused=true                          # stop every run
dispatchq settings --paused=false                         # let the queue run again
dispatchq settings --backfill-urgency 2                   # backfill only when half of what is left would expire
dispatchq settings --prompt-review=false                  # turn the prompt review off
dispatchq settings --prompt-review-provider claude-cheap   # who answers the review ("" = default provider)
dispatchq settings --prompt-review-model haiku            # ("" = the provider's default model)
dispatchq settings --assistant-provider claude-cheap      # who answers the assistant chat and drafts feedback
dispatchq settings --assistant-model haiku                # ("" = dispatchq's own choice per job)
dispatchq settings --artifact-notify=false               # publish artifacts without a notification
dispatchq settings --pushover=true --pushover-token T --pushover-user U
dispatchq settings --ntfy=true --ntfy-topic dispatchq-daniel   # server defaults to ntfy.sh
dispatchq settings --webhook=true --webhook-url https://hooks.slack.com/services/... \
                  --webhook-template '{"text":"{{title}}: {{message}}"}'
```

For the numeric settings `0` means "use the default". `--idle-timeout-minutes`
also takes a negative value for "never kill a run", and `--max-run-history` for
"keep every run". You can set channel credentials here, but the CLI never
prints them back; the output only says whether they are configured. A channel
that is switched on but cannot deliver (no topic, no URL, an unknown
`{{placeholder}}`) is refused at save time, from the CLI same as from the app.
Use `dispatchq notify --title X --message Y` to check a channel actually
delivers — there is no separate Test button in the app.

The binary path and the default model belong to a provider now, not to the
global settings:

```sh
dispatchq provider list                                   # ids, kinds, default models, status
dispatchq provider check claude                           # probe it and print why it is not ready
dispatchq provider limits                                 # how much of each allowance is used
dispatchq provider edit claude --path /Users/me/.local/bin/claude
dispatchq provider edit claude --default-model opus
dispatchq provider disable claude                         # keep it, run nothing on it

# a second Claude account, a Codex provider, and opencode with a local model
dispatchq provider add claude-work --kind claude-code --config-dir ~/.claude-work
dispatchq provider add codex --kind codex --default-model gpt-5.6-sol
dispatchq provider add local --kind opencode
```

`provider list --json` gives each instance's enabled state, readiness, the
reason it is not ready, and its default model — which is how a running job finds
out where it can send follow-up work. `provider rm` refuses while a task or the
default-provider setting still names the instance, and lists what has to change
first.

## From another tool or agent

This README is the whole interface. An external app or agent needs nothing but
this document to work with DispatchQ.

- **Invoke it by full path.** `/Applications/DispatchQ.app/Contents/MacOS/dispatchq`
  is not on `PATH`.
- **Read with `--json`.** `dispatchq list --json`, `dispatchq show ID --json`,
  `dispatchq settings --json`, `dispatchq provider list --json` and
  `dispatchq artifacts list --json` emit structured output. The other commands
  print for humans.
- **A task is refused when its provider cannot run it.** `--provider ID` selects
  one; leaving it out uses the default. An unknown or unready provider exits
  non-zero with the reason, so ask `dispatchq provider list --json` first rather
  than assuming one is there. See [Providers](#providers).
- **Write with `edit`, `add`, `import`, `rm`, `enable`, `disable` and `move`**
  instead of editing `config.toml` by hand. Those commands validate the change
  and write it atomically alongside the app's own writes.
- **Move a task between machines with `export` and `import`.** The `.dispatchq`
  file carries the whole task; see [Sharing tasks as files](#sharing-tasks-as-files).
- **Exit code 0 means success.** Any failure exits non-zero and writes the reason
  to stderr, prefixed `dispatchq:`.
- **Test a change with `dispatchq run-now ID`** rather than waiting for the
  schedule. It exits non-zero while the global pause switch is on
  (`dispatchq settings --paused=false` lifts it).
- Every run is framed as a [headless, unattended run](#what-every-run-is-told)
  with no next turn — it finishes or hands work on, it does not wait.
- A task that is itself a DispatchQ run has three extra abilities, described below:
  [queueing follow-up work](#letting-a-task-queue-follow-up-work),
  [publishing artifacts](#letting-a-task-publish-artifacts) and
  [sending a notification](#letting-a-task-send-a-notification).
- [Data on disk](#data-on-disk) lists the files these commands read and write.

## What every run is told

Every run starts with a built-in system prompt from DispatchQ, ahead of your own
[custom system prompt](#settings). Most of it documents the three abilities
below — queueing, publishing, notifying — but it opens with the one thing a run
cannot work out for itself: **it is headless**. Nobody is at the keyboard, there
is no next turn, and the process is torn down the moment Claude stops writing.
Without that framing a model behaves as if a conversation continues: it
schedules a wakeup for later, leaves a watcher running in the background, or
ends with a question — all of which die with the run. So the prompt tells it to

- decide rather than ask,
- never end a run by announcing that it is waiting for something,
- either finish work in flight inline and blocking, or
  [queue a follow-up task](#letting-a-task-queue-follow-up-work) (`--in 30m`)
  and stop, and
- name anything unfinished concretely — pull request, build, run ids and links —
  in the last message, because that message is what reaches your phone.

It also tells a run never to search the whole disk or home folder (`find /`,
`find ~`) for a tool or file. Such a sweep walks into Music, Photos, OneDrive and
network volumes, and macOS attributes every access by a run to DispatchQ, so you
would see an unexplained privacy prompt for DispatchQ that blocks the run until
answered. Paths a task names, or that its instructions point to, stay
accessible: a job that reads a spreadsheet in OneDrive still does, and macOS asks
once for that folder.

You don't need to repeat any of this in a task's prompt. None of it applies to
a [script job](#script-jobs): there is no model to tell anything to.

## Letting a task queue follow-up work

A running task can enqueue *new* DispatchQ tasks instead of doing everything inline.
Every run is told, via its system prompt, that the capability exists and how to
use it — so a prompt like *"scan for TODOs and, for anything non-trivial, queue a
separate task to fix it"* just works. From inside a run, Claude calls:

```sh
dispatchq queue --prompt "…"
```

with an optional time (`--at <RFC3339>`, `--in <duration>` like `90m`, or
`--cron "<expr>"`; default is as-soon-as-possible), an optional `--dir`, and an
optional `--name`. The new task **inherits** the calling task's provider, model,
permissions, parallelism, and notification settings automatically. This works
because the daemon injects the CLI's path and the parent task into each run's
environment.

When the follow-up needs different settings from the task that queues it, pass
the same flags `dispatchq add` takes; anything you leave out keeps inheriting:

```sh
dispatchq queue --prompt "Review the change thoroughly …" --model claude-opus-5 --notify
```

- `--model M` runs the new task on another model (an empty value means the
  effective provider's default model). This is what lets a cheap watcher — Haiku
  every five minutes, quiet history — hand expensive work to a visible Opus run.
- `--provider ID` runs the new task on another configured harness — which is how
  a Claude run hands a review to Codex, or the other way round. Without a
  `--model` alongside it, that provider's own default model applies — a model
  from the calling task is never carried across.
- `--reasoning-effort E` sets how hard the new task's model should think, where
  the harness takes it. `dispatchq provider list --json`
  says which providers exist and which of them can run right now; queueing for
  one that cannot fails with the reason instead of filing work that would not
  start (see [Providers](#providers)).
- `--parallel=BOOL` and `--notify=BOOL` switch the respective setting on or
  off regardless of what the caller has.
- `--skip-permissions=BOOL` grants or withdraws the task's own permission
  bypass. Note that a run can grant a follow-up more than it has itself; the
  system prompt tells Claude to do so only when the queued work cannot be done
  without it.
- `--quiet-history=BOOL` opts the new task into (or, explicitly, out of) quiet
  history. Without it a queued task is never quiet, even when the caller is
  (see [below](#quiet-history-for-frequent-jobs)).

A long `--name` is accepted as given; `dispatchq list` and the Queue view cut it
to fit their column (hover the app's row for the whole name), `dispatchq show` and
`--json` print it in full.

## Asking several providers at once

Some work wants more than one harness — *"ask Claude and Codex and give me one
summary"*, *"get every provider's take on the release notes"*. That is a
**fan-out** with a **join**, and it is built from the same `queue` command:

```sh
# one job per provider; --json so the ids can be read back out
dispatchq queue --json --provider claude --prompt "Summarise what changed on main today"
dispatchq queue --json --provider codex  --prompt "Summarise what changed on main today"

# one job that waits for both and combines them
dispatchq queue --provider claude \
  --depends-on q-20260914T050000-a1b2c3 \
  --depends-on q-20260914T050000-d4e5f6 \
  --include-results \
  --prompt "Consolidate the attached results into one digest and publish it"
```

Every run is told how to do this in its system prompt, so a task that says "ask
all providers and consolidate" arranges it itself. DispatchQ does not read the
prompt and guess: the harness understands the request, and this just gives it
the vocabulary.

**It is durable, not a live conversation.** The root run queues the children and
the join and then stops — it does not wait, and could not: a run ends when the
harness stops writing. The daemon owns the rest, so a restart, a closed lid, a
long child or a rate limit on one provider costs time and nothing else.

**When the join runs.** Once every job it waits for has a terminal result:
success, failure, auth error or cancellation. A job pausing on a rate limit has
*not* finished — its session is scheduled to continue — so the join keeps
waiting. A job that failed **does** release it: an unattended digest that never
appears is worse than one that says which input is missing. So does a job that
was deleted before it ran, because nothing about it is ever going to change.

**What the join gets.** `--include-results` puts each dependency's final answer
in front of the join's prompt, with the job name, the provider and model it ran
on, its status and any error. It is fenced and labelled as data, and the system
prompt tells the harness to read it as data and never as instructions. The
injected text is bounded; a shortened answer says so and names the run log that
has all of it.

**Rules worth knowing:**

- A job may only wait for jobs that **already exist**. Queue the children first.
  That is also why a cycle cannot happen — dependencies only ever point
  backwards.
- Dependencies are fixed when the job is queued and never change afterwards.
- A recurring (cron) job cannot depend on one-shot jobs: its second occurrence
  would find them long finished and run immediately, which is no dependency at
  all. Nor can you wait *for* a recurring job — it never has a last result.
- Leave `--model` off a cross-provider job unless you mean it. A model name
  belongs to the harness it was chosen for, so each provider uses its own
  default.
- Let only the join publish the artifact or send the notification, unless you
  want each provider's result separately. Otherwise one request produces several
  competing reports.

In the app, a job that is waiting says so in the Queue (*waiting for 2 jobs*,
with the names on hover), and the Log groups the runs of one workflow into a
single **Workflow** block instead of scattering them among unrelated runs. A job
queued by a run joins that run's workflow, which is also what groups an ordinary
self-queued chain.

## Letting a task publish artifacts

When a task produces a file worth keeping — a report, an export, a generated
HTML page or PDF — it can publish it as an **artifact**. Every run is told, via
its system prompt, that the capability exists, so a prompt like *"write the
summary to report.html and publish it"* just works. From inside a run, Claude
calls:

```sh
dispatchq publish --file report.html --title "Nightly summary" --description "…"
```

The file is **copied into DispatchQ** (a permanent snapshot — later changes to the
original don't affect it) and appears in the **Artifacts** view, attributed to
the task and run that produced it, with an unread flag that clears as soon as you
open it. Each publish also raises a notification (macOS, plus every remote channel that
is configured; **Settings → Notifications → Notify about new artifacts** or
`dispatchq settings --artifact-notify=false` turns it off); clicking the macOS one brings up DispatchQ with that artifact open — in
the in-app viewer for HTML, PDF, images, Markdown and text, in your browser for anything
else. Artifacts that were already there when this version first ran are not
announced retroactively. HTML and PDF open in an in-app viewer; any type can be opened in your
browser. Artifacts are kept until you delete them, independent of run-history
pruning. `--title` defaults to the file name; `--file` may be relative to the
task's working directory.

### Where an artifact comes from

A common pattern is a cheap watcher (often a script job) that notices something
and files an agent job to deal with it, and that agent job publishes the
result. The artifact then names the agent job, but what you look for is the
watcher. So every artifact also records its **parent**: the job at the root of
the chain that created the publisher. A job added with `dispatchq add` or
`dispatchq queue` from inside a run remembers the job that ran it, or that job's
own parent if it has one, so a chain of any length leads back to the watcher. A
job nothing created is its own parent while it is in the queue; once a job like
that is deleted, its artifacts have no parent any more, so finished one-off jobs
do not pile up in the parent menu. The artifact also records the publisher's
queue group.

The Artifacts view filters by both. While the parent job is still in the queue,
its current name and group are used, so renaming the watcher or moving it to
another group takes its artifacts along. Artifacts published before this was
recorded are traced back through run history where it still has the runs. When
the run itself does not name a parent, they get one only if the trace leads to a
job still in the queue (or to that job's parent); otherwise they have none and
show up under **All parents** only.

### Listing artifacts from a script

`dispatchq artifacts list` prints the artifacts oldest first, with an unread
mark, the parent and the publish time. With `--json` each entry carries the
title, description, file name, the absolute **path of the stored copy**, size,
content type, publishing task and run, group, parent and the unread flag. The
parent and group are resolved exactly as the Artifacts view shows them, so
`--origin ID` keeps the same artifacts as picking that job in the parent menu.
`--since` keeps what was published after an RFC3339 time, or within a duration
such as `24h`:

```sh
dispatchq artifacts list --json --origin pr-watcher --since 24h
dispatchq artifacts list --json --since 2026-10-07T09:00:00.123456Z   # the last published_at seen
```

Listing does not mark anything read.

## Letting a task send a notification

Some jobs have nothing to hand over — a watcher that checks a deployment, a
feed, or a metric every quarter hour only needs to say *something changed*.
Rather than publishing a throwaway artifact to trigger a push, a run can send a
notification directly. Every run is told, via its system prompt, that the
capability exists, so a prompt like *"compare with the last snapshot and notify
me only if it differs"* just works. From inside a run, Claude calls:

```sh
dispatchq notify --title "Prod drifted" --message "3 commits behind main" --url "https://…"
```

The notification goes out over the **channels you already configured** — macOS
Notification Center, Pushover, ntfy and the webhook, each independently — with
the same look as DispatchQ's own alerts, and attributed to the task that sent it
(its name is appended to the message). Like every notification it is listed
in the app's **Notifications** view; nothing else is stored — no artifact, no
history entry. The run's own outcome is still announced according to the
task's settings, so a watcher that finds nothing sends nothing and stays
silent.

- `--title` and `--message` are required. A title longer than 250 characters
  or a message longer than 1024 (Pushover's limits) is cut, not rejected.
- `--url` is optional and must be an absolute `http` or `https` URL. Clicking
  the macOS notification opens it; Pushover and ntfy show it as the message's
  link, and the webhook fills it into `{{url}}` if the template uses it.
- The CLI hands the notification to the daemon, which sends it on its next tick
  (a few seconds). Exit code 0 means it was queued, not that a channel accepted
  it — a channel that fails is logged by the daemon (`dispatchqd.err.log`).
- A notification that waited more than 24 hours — the daemon was not running —
  is dropped with a log line rather than delivered as if it were current.
- It also works outside a run, from a shell: then it is sent without attribution.

## Script jobs

A job is one of two kinds, and the task form's **Job type** switch (or `--kind`
on `dispatchq add` / `edit` / `queue`) decides which:

- an **agent job** — the default — sends its prompt to a model through a
  provider's CLI, and
- a **script job** runs that same text as a **program**. No model is involved,
  no provider is chosen, nothing is spent, and nothing about it is guessed at.

Everything else is shared: the same queue, the same triggers and priority, the
same groups, the same run log in the Log, the same notifications, the same
quiet history, the same dependencies.

**What it runs.** The script is written to a temporary file and executed. Begin
it with a shebang to choose the interpreter — `#!/bin/zsh`,
`#!/usr/bin/env python3`, `#!/usr/bin/env node` — or leave it out and it runs
under `/bin/sh`. It starts in the task's **working directory**, and it inherits
the daemon's environment plus the variables every run gets: `DISPATCHQ_BIN`,
`DISPATCHQ_HOME`, `DISPATCHQ_RUN_ID`, `DISPATCHQ_TASK_ID`, `DISPATCHQ_WORKFLOW_ID` and
`DISPATCHQ_PARENT_TASK`.

**What counts as the outcome.** Exit code `0` is a success, anything else a
failure — that is the whole classification; nothing reads the output looking for
trouble. Standard output *and* standard error go to the run log, and standard
output alone is kept as the job's **answer**: the text a notification quotes and
a dependent job consolidates. More output than a run record may carry keeps the
**end** — a script says what matters last — with a line in front saying so. A hung script is killed by the same idle timeout
as an agent run, and **Cancel task** stops it and its whole process tree.

**Checked before it is queued.** Like a prompt, a script is read against this
Mac while you write it — missing paths, commands the daemon's PATH does not
have, anything that waits for a person. See
[The prompt review](#the-prompt-review).

**What it cannot have.** A provider, a model, a reasoning effort, or the
permission-prompt bypass. Those all steer a model, and a job that runs none is
refused rather than quietly ignoring them. Switching an existing task to
*Script* drops them for you.

**Why it exists: watchers.** The classic frequent job — poll a query, diff it
against a seen-list, and act on what is new — spends a model's allowance on work
that is pure plumbing. As a script job it spends nothing, and it keeps running
when every provider is out of allowance, because the rate-limit gate belongs to
an account and a script job has none. What it finds, it hands to a real agent
job:

```sh
#!/bin/zsh
set -euo pipefail
new=$(./bin/poll-needs-review --since-file .agents/seen.json)
[[ -z "$new" ]] && exit 0            # nothing changed: say nothing, spend nothing

while IFS= read -r id; do
  "${DISPATCHQ_BIN:-dispatchq}" queue --model opus \
    --name "Review $id" --prompt "Run the review for work item $id."
done <<< "$new"

"${DISPATCHQ_BIN:-dispatchq}" notify --title "Needs review" --message "$(wc -l <<< "$new") new item(s)"
```

A job queued from a script is an **agent job** — that is the point of the split,
so the kind is never inherited. A script that genuinely wants to queue another
script says `--kind script`.

**Dependencies.** A script job can wait for other jobs with `--depends-on` like
any other. With `--include-results` their answers arrive on the script's
**standard input** rather than in front of its text, because a program cannot
have a digest pasted on top of it. Without dependencies, standard input is
closed immediately — an unattended run never waits for input nobody will type.

## Quiet history for frequent jobs

A task that runs every few minutes would, by default, produce hundreds of
successful runs a day: each one unread in the Log, each one counting against
the `Max run history` limit until it pushes a run you actually care about out of
the record. Mark such a task **Quiet history** (the switch in the task form, or
`--quiet-history` on `dispatchq add` / `dispatchq edit` / `dispatchq queue`) and:

- A run that **succeeds leaves no trace**: it is never written to history, and
  its log is deleted when it finishes.
- A run that **fails, hits an auth problem, is cancelled, or is paused by the
  rate limit is recorded** with its log, unread, exactly like an ordinary run,
  and notifies as usual. A pause is kept even though the daemon resumes it by
  itself: it closes that provider's gate and holds up every other task on it,
  so it is the answer to "why is nothing running?" — and its Log entry is
  where **Cancel resume** lives.
- While it is running, the Queue shows the task's *running* badge as usual, but
  there is no Log entry to open (and so no live log or **Cancel task**).
- The Dashboard's **Up next** card leaves it out; the Queue still lists it with
  its next run.

Everything else — notifications the run sends, artifacts it publishes, tasks it
queues — is unaffected. A task queued from inside a quiet-history run is **not**
quiet itself unless the call says `--quiet-history`: follow-up work is real
work, and you will want to see its run.
The trade-off: successful quiet runs leave no log to look at afterwards and are
absent from the Usage statistics.

## Backfill tasks

Some work is worth doing only when it costs nothing: a dependency audit, a
documentation pass, the refactor that is nice to have. Mark such a task
**Backfill** (the switch in the task form, or `--backfill` on `dispatchq add` /
`dispatchq edit` / `dispatchq queue`) and it runs only on allowance that would
otherwise expire unused.

**Urgency** says how far behind a provider is on spending its week:

`urgency = free share of the week × hours in a week ÷ hours until the week resets ÷ (1 + runs already on it)`

At 1, spending evenly from now on uses up exactly what is left; a fresh week
starts there. At 2 it would take twice that pace, so half of what is left is
at risk. The Dashboard's allowance strip shows each provider's urgency against the
**backfill threshold** and turns it green above it. The threshold is the
slider at the bottom of **Settings → Providers** (0.5 to 5, default 1.5); while
it moves, each provider's urgency gauge shows whether
it would qualify, and it applies on release. `dispatchq settings --backfill-urgency X` sets the same
value.

A backfill task is due by its trigger as usual (once, at a time, or on a cron
schedule) and then:

- **On a provider** it starts only while that provider's urgency is above the
  threshold, it has room in its 5-hour window (under 90%), it can run, and it
  is not waiting out a rate limit. It never goes to the provider's fallback:
  that allowance is not the one at risk.
- **On a pool** it goes only to members that meet the same test, the best of
  them by the pool's ranking. So one list of backfill tasks on a pool feeds
  whichever account is about to waste its week.
- Until then it **stays due and waits**, and the Queue says why, for example
  *Backfill: Codex is at urgency 0.8, backfill runs above 1.5*. A cron task
  that waited through several occurrences runs once when it can, not once per
  occurrence missed.
- It goes **after every other due task**: a backfill task at the top of the
  queue never takes the slot of real work due at the same time.
- Runs in flight **share** a provider's urgency. Parallel backfill tasks
  therefore start one after another until what is at risk is covered: at
  urgency 3.5 and threshold 1.5, two start (3.5, then 1.75) and the third waits
  (1.17).
- **Run now** ignores the threshold: it is a deliberate start.

Backfill is for agent jobs: a script job spends no allowance and cannot be
marked. A job queued from a backfill task inherits the flag, like its other
settings; pass `--backfill=false` to queue real work.

```sh
dispatchq add --id audit --prompt "Audit the dependencies and file issues" \
            --dir ~/code/app --pool claude-pool --backfill
dispatchq add --id docs --prompt "Tidy the docs" --dir ~/code/app \
            --provider codex --cron "0 * * * *" --backfill
dispatchq provider limits        # the urgency of each provider
```

## Sharing tasks as files

A task can be handed to a colleague as a single `.dispatchq` file. It is a plain
zip archive with two entries:

| Entry | Contents |
|-------|----------|
| `task.json` | Every setting of the task (id, name, working directory, trigger and schedule, parallel, enabled, model, permissions, notify) inside a small envelope: `format` (`dispatchq-task`), `format_version` (`1`), `exported_at`, and `task`. |
| `prompt.md` | The prompt, byte for byte, as Markdown. |

Unzip it to read or edit either part by hand; zip the two files back up and the
result imports again (a folder around them, as Finder's *Compress* adds, is
fine).

**Export.** In the app, the export button on a task row opens the native save
panel, pre-filled with `<id>.dispatchq`. From the CLI:

```
dispatchq export nightly-sweep                       # ./nightly-sweep.dispatchq
dispatchq export nightly-sweep --out ~/Desktop       # a directory: default name inside it
dispatchq export nightly-sweep --out share/brief     # a file: .dispatchq is appended
dispatchq export nightly-sweep --out x.dispatchq --force   # overwrite an existing file
```

Without `--force` the CLI refuses to overwrite; the app's save panel asks
before replacing a file.

**Import in the app.** **Import…** on the Queue toolbar picks a file and opens
its task in the normal task sheet, titled *Import task*. Nothing is queued yet:
prompt, working directory, schedule and every switch come from the file and can
be changed, and **Add task** creates the task, exactly as if you had typed it in.
Cancel and nothing happened.

The working directory is the one place where a shared file cannot be trusted, so
it is checked against this machine: if that folder does not exist here, the field
is left empty and the sheet says which path was dropped — you pick a real one
before the task can be added. A folder that exists but that DispatchQ may not read
yet counts as existing and is kept.

**The provider travels as a hint, never as an account.** A file records which
*kind* of harness the task was written for (`claude-code`, `codex`), the model,
and what the exporter called their provider — never a provider id, a
configuration directory or anything to do with a login. On import DispatchQ looks
for your own provider of that kind: exactly one match is taken, with the model.
Anything else — no match, or several accounts of that kind — leaves the choice
to you, and the sheet says which harness the file wants. Two accounts are not
interchangeable (separate allowances, separate logins, often separate
employers), so DispatchQ does not pick one for you. Importing never creates a
provider, copies a path, or touches credentials.

**Import from the CLI.** `dispatchq import PATH` (optionally `--id ID` to choose
the id) adds the task straight to the end of the queue with its settings
**exactly as exported** — working directory, schedule, model, permissions and
enabled state included. When the file's provider hint matches no single provider
here, the import is refused and names what it wants; `--provider ID` places it
(and `--model NAME` picks the model, since the exporter's belongs to their
harness). A working directory that does not exist on this machine
is kept, with a warning naming it; fix it with `dispatchq edit ID --dir PATH`.
Because settings arrive as-is, a task exported as *enabled* with an *as soon as
possible* trigger is eligible to run right after a CLI import; pause or edit it
first if that is not what you want.

What the file cannot decide is filled in, either way:

- Ids may contain only letters, digits, `.`, `-` and `_` (they appear in the
  app's own URLs); anything else is rejected. A CLI import keeps the file's id,
  or a numeric suffix (`nightly-sweep-2`, `-3`, …) when that id is already in
  use — the existing task is never touched; a file with no id gets one derived
  from its name. A task added from the sheet gets a fresh id like any other new
  task.
- Missing `permissions` mean `default`; a missing name falls back to the id.
- On a CLI import, any scheduling history left behind by an earlier task with
  the same id (deleted before this version cleaned up after itself) is dropped,
  so the import starts fresh.

A file with an invalid task (no prompt, unknown trigger, bad cron) is rejected
in both paths and nothing is added.

## The prompt review

A prompt written for one Mac rarely fits the next one unchanged, and a task
queued for 3 a.m. has nobody around to notice. So whenever a draft prompt is
written or changed — in a new task, an edit, a replay or an import — DispatchQ has
Claude read it against *this* machine and, if something is off, shows it in a
purple banner under the prompt box:

> ✦ **DispatchQ suggests:** docs/spec.md does not exist here, so the run has
> nothing to read. Also out/weekly.md would be written into an out/ directory
> that does not exist yet, so the rewrite creates it first.
>
> [ Apply ] [ Dismiss ]

**Apply** replaces the prompt with the rewrite, in the box, where you can still
change it before saving; the review then runs again on the result. **Dismiss**
just hides the banner. Nothing is ever saved, queued or changed on your behalf,
and a task can always be added exactly as written — the banner is advice, not a
gate.

What it looks for:

- **A path that isn't there.** DispatchQ resolves every path the prompt mentions
  (absolute, `~/…`, or relative to the task's working directory) and checks it.
  An input file or folder that does not exist is reported; the right path is
  yours to supply, so no rewrite is offered.
- **A file to be written into a folder that doesn't exist.** The missing file is
  normal; the missing parent directory is the problem. The rewrite tells the run
  to create it first.
- **Guidelines it would depend on.** If the prompt says to follow the rules in
  some file and that file is small, the rewrite pastes its content straight into
  the prompt, so the task no longer depends on the file still being readable
  when it runs.
- **A prompt that waits for you.** A question, a confirmation, a choice — an
  unattended run has nobody to answer it, so the rewrite decides up front.

A [script job](#script-jobs) gets the same banner, judged as a program rather
than as a prompt: a path it reads, changes into, sources or names as its
interpreter that isn't there; a file it writes into a folder that doesn't exist
(the rewrite adds `mkdir -p`); `sudo` without `-n`, a `read` from the terminal
or another prompt nobody will answer; and, for a shell script, **a command that
isn't on the PATH it runs with**. The daemon's PATH is launchd's minimal one and
reads no shell profile, so a Homebrew tool you use every day in Terminal is
often simply not there at 3 a.m. DispatchQ looks each command up on that very
PATH and, when one is missing but installed elsewhere (`/opt/homebrew/bin`,
`/usr/local/bin`, `~/.local/bin`, …), the rewrite calls it by its full path.
Paths written as `$HOME/…` are checked like `~/…`.

The same banner sits under the **custom system prompt** in Settings, with the
rules adjusted to what that text is: it applies to every task, in every
directory, so a relative path there means something different every time.

Practicalities:

- It runs on a provider, so it costs a little usage each time. Which provider
  and which model are yours to pick under Settings → Assistant → Prompt review —
  reviewing is frequent, cheap work with no claim on the allowance you reserved
  for real tasks, so a second account or a fast model earns its keep. Or switch
  the whole thing off with the toggle above. Only providers that can answer a
  question of DispatchQ's own are offered (see below); if none can, the banner
  simply stays away.
- The review itself is the narrowest invocation DispatchQ makes: no tools at all
  (DispatchQ hands it the path checks and the small files it read), no CLAUDE.md,
  skills, plugins, hooks or MCP servers, no saved session, and a working
  directory outside any repository. It reads; it never writes. DispatchQ calls
  this an *aside* — a question it asks a harness on its own behalf rather than
  to do your work — and a provider has to be able to hold one to be offered for
  it. Claude Code and Codex can; opencode cannot yet. Codex has no switch that
  removes its tools, so it answers in a read-only sandbox inside that empty
  directory, with your own Codex config, MCP servers and rules left out.
- It runs on a change, not on a look. Editing the prompt, choosing a working
  directory or importing a task starts a review from scratch and cancels the one
  still in flight — including its Claude process — so only the newest answer is
  ever shown. Opening a sheet changes nothing, so it asks nothing: an answer is
  remembered for a day, and reopening the same task, or the app, shows that
  finding again for free, headed *DispatchQ suggested earlier* and with a **Check
  again** button for when the machine has moved on — the missing file exists by
  now — but the prompt has not. Undoing an edit is free too: the finding for the
  text you are back at is still remembered. **Dismiss** forgets a finding rather
  than hiding it, so it does not come back by itself; editing the prompt back and
  forth after that asks once more.
- Switching the review off hides the banner everywhere, remembered findings
  included. A finding is also only ever reused for the reviewer that produced
  it, so changing the review provider or model — or the default provider the
  review follows — never replays an answer the new one did not give.
- A task sheet without a working directory yet — an imported task, whose folder
  came from another Mac — waits for you to choose one before reviewing, since
  every relative path would otherwise be unresolvable.
- File content it was given is treated as data. Instructions found inside a
  file cannot steer the review, and any rewrite is shown to you in the prompt
  box before it can run.

## Asking DispatchQ

**Ask DispatchQ** at the top of the sidebar (**⌘J**, or type a question into
the command palette and pick *Ask DispatchQ*) opens a chat docked to the right
of the window. It stays open while you switch views, and it does only DispatchQ
things: answer about the queue, runs and artifacts, take you to them, and plan
or change jobs. It answers in your language, in a sentence or two; anything else
it declines in one line.

- **Ask about runs.** "What ran last night?", "why did the dependency check
  fail?" It reads the run's outcome, final output and the end of its log before
  answering, and lists what it looked at above the answer.
- **Open things.** "Show me the newest report" opens the artifact in the
  viewer; a run opens in its log, a task in the task form, a view on its own.
  This happens straight away: it only shows what is already there.
- **Plan and change jobs.** "Every weekday at 7, summarise the TODOs in the
  same project as the dependency check" comes back as a card with the name,
  schedule, folder and the prompt it wrote. **Add to queue** files it,
  **Edit** opens it in the task form first (where the prompt review sees it),
  **Dismiss** drops it. Changing a task, running it now, pausing, enabling or
  deleting it, cancelling a run and pausing or resuming all runs arrive the
  same way. **Nothing in the queue changes until you confirm the card**, and a
  newer proposal for the same thing ("make it 8 instead") replaces the older
  card. Before a card is shown, DispatchQ checks it: an invented task or run is
  dropped, and a draft whose folder does not exist on this Mac, whose cron does
  not parse or whose provider is unknown is marked and can only be edited.

Every message carries a compact picture of DispatchQ: the time, the pause
switch, the providers, every queued task (without its prompt), the last runs
and artifacts. For details the assistant asks DispatchQ, read-only: one task
with its prompt, one run's output and log tail, a list of runs or artifacts, a
text artifact's beginning, usage per provider. That takes one to three model
calls per message, a few seconds each. Like the prompt review, the chat runs as
an *aside*: no tools, no MCP servers, no project files, in an empty throwaway
directory, so it cannot touch your machine; it knows only what DispatchQ tells
it, and it acts only through the cards you confirm.

It runs on the provider and model under **Settings → Assistant** (the same
setting the Feedback page uses; *DispatchQ's choice* is Sonnet for the chat on
Claude Code, and the harness's own default on Codex). A provider that cannot
hold an aside (opencode) is not offered; the panel says so and points to
Settings and the task form instead. The
conversation lives in the window: **Clear** starts a new one, and if the daemon
restarted meanwhile, the next message carries the conversation along.

## Sending feedback

**Feedback** at the bottom of the sidebar is a page of its own, and turns a bug
report or a wish into a GitHub issue without you having to write one.

1. Say what is wrong, or what DispatchQ should be able to do — in whatever
   language you think in. The page offers a few examples to start from; picking
   one only fills the message box.
2. Claude reads it and either asks one short clarifying question (at most twice,
   and only when the report cannot be acted on as it stands) or goes straight to
   a draft. This runs on the provider you chose under Settings → Assistant →
   Assistant provider, on a small model by default, and costs a fraction of a cent per
   message; it does not go through the queue, so it works while tasks are
   running.
3. You get the finished issue — an English title and body, labelled either
   `bug` or `enhancement` — in editable fields.
   Your DispatchQ version and macOS version are named there and ride along as a
   last line of the issue.
4. **Open on GitHub** opens GitHub's prefilled *new issue* page in your browser.
   The issue exists only once you press **Create** there — and everything,
   including that version line, can still be changed or deleted on that page.

DispatchQ never talks to GitHub for this and stores no token: your browser is
already signed in, and the whole issue travels in the page's URL. If the
assistant cannot be reached — no provider set up to draft one, or your usage
limit is exhausted — the page keeps what you typed and lets you write the issue
by hand.

The chat runs as an *aside* (see [The prompt review](#the-prompt-review)): tools,
MCP servers, skills and `CLAUDE.md` files all switched off (on Codex: a
read-only sandbox without your own Codex config), in an empty throwaway
directory, so it can neither touch your machine nor pull project
context into a public issue. Only a provider that can hold such a conversation
is offered for it. It is told not to put
personal data (paths, names, addresses, prompt contents) in the issue — and
because you see the text before anything is filed, you have the last word on
that.

## How scheduling and the limit gate behave

- **Eligibility.** On each tick the daemon starts every task that is due and
  permitted by priority, concurrency, and the limit gate.
- **The pause switch wins over everything.** While *Pause all runs* is on, a tick
  starts nothing and records nothing, `dispatchq run-now` and the app's **Run now**
  are refused, and no task wake is registered (only the heartbeat stays, so the
  daemon notices when you switch it off). Nothing is lost: a task that came due
  while paused is still due afterwards.
- **The limit gate belongs to the provider.** When a run reports a rate limit,
  new starts on *that provider* pause until the reset; the other providers carry
  on. The wait comes from the reset time the CLI reports, then from its
  `retry_delay_ms` signal, and falls back to 15 minutes when neither is exposed.
  A terminal rate-limit result is recorded even if the CLI stays alive after
  emitting it. DispatchQ stops that CLI and its remaining tool processes, preserving
  the session and reset time. A warning or subagent rejection alone does not stop
  a working run.
  At reset the gate reopens and the blocked task **resumes its session** rather
  than starting over.
- **An account that may not run at all counts as a limit.** When Claude Code
  answers that the organisation has disabled Claude subscription access, the
  account has no allowance rather than a used-up one, so the provider pauses
  exactly as it does on a limit — and its fallback takes the work. The pause is
  an hour, because that block lifts through an admin or a billing change, never
  by waiting a few minutes. The run says so instead of reading as a plain
  failure.
- **A fallback provider skips the wait.** If the blocked provider names one (see
  [When the limit is reached](#when-the-limit-is-reached)), its tasks run there
  meanwhile — from the start, since a session belongs to the account that issued
  it. The interrupted session is then dropped instead of being resumed after the
  reset, so the task does not run twice.
- **A blocked queue says so.** While the gate is closed a banner names the time
  it reopens, the paused run is marked *rescheduled* in the Log with that time,
  and the task carries a *rescheduled* badge in the Queue — a waiting queue is
  never mistaken for a stuck one. **Cancel resume** on the run drops the plan:
  the pending session is forgotten, the run is recorded as `canceled`, and a
  one-shot task leaves the queue. The gate itself stays closed until the reset,
  since the limit is not yours to lift.
- **Auth problems don't retry.** A login/authentication error is recorded as
  `auth_error` and notified so you can re-login; it is not retried automatically.
- **Wake-ups.** After each pass the daemon registers a `pmset` wake at the nearest
  relevant time (next fixed start, next cron occurrence, or limit reset) plus a
  recurring heartbeat wake, so the Mac can sleep between runs and wake when
  there's work.
- **Run outcomes** are one of: `running`, `success`, `failed`,
  `rate_limited_waiting` (shown as *rescheduled* while its session is still
  queued to continue), `auth_error`, `canceled` (stopped by the user — either the
  running process or a scheduled resume).

## Data on disk

Everything lives under `~/Library/Application Support/dispatchq` (override with the
`DISPATCHQ_HOME` environment variable):

| Path | Contents |
|------|----------|
| `config.toml` | Global settings, the configured [providers](#providers) and [pools](#provider-pools), and the ordered task list — the order is the priority, and tasks of one group sit together in it (human-readable, versionable). No credentials: a provider entry holds its CLI's path and configuration directory, never what is inside them. |
| `history.jsonl` | Append-only index of every run (except a quiet-history task's successful ones, which are never written). Scripts read it through `dispatchq status --all --json`. |
| `runs/<run-id>.log` | Full log for each run. |
| `artifacts.json` | Index of published artifacts (title, source task/run, its group and parent job, file name, size, type). |
| `artifacts/<id>/<file>` | The published files themselves (snapshots copied at publish time). |
| `notifications.json` | Outbox of notifications sent with `dispatchq notify`, waiting for the daemon to deliver them (normally empty). |
| `inbox.json` | The last 200 notifications the daemon sent (title, message, kind, what each points at, read flag), as listed in the Notifications view. |
| `state.json` | Machine bookkeeping: read/unread flags (runs and artifacts), which artifacts have been notified about, cron anchors, pending-resume sessions, the provider health you were last told about, which queue groups are folded shut, dismissed update version, the What's new edition you closed, whether the ClaudeQ rename check is done. |
| `dispatchqd.out.log` / `dispatchqd.err.log` | Daemon stdout/stderr. |
| `../claudeq` | After an update from ClaudeQ: a link from the old data folder to this one (see [Coming from ClaudeQ](#coming-from-claudeq)). |
| `.lock` / `.daemon.lock` | Lock files, both empty of interest. `.lock` serializes config/state writes between the daemon and a `dispatchq` command; `.daemon.lock` holds the running daemon's pid and is what makes a second daemon on the same store refuse to start. Both are released when the holding process exits, so neither needs clearing by hand. |

The LaunchAgent itself is at
`~/Library/LaunchAgents/de.maierdaniel.dispatchq.plist`.

## Uninstall

Dragging DispatchQ.app to the Bin is not enough: the background agent stays
registered and macOS keeps trying to start it. DispatchQ ships its own
uninstaller, `Uninstall DispatchQ.pkg`, inside the app
(`Contents/Resources`). **Settings → System → Uninstall…** opens it in the
macOS Installer: read the welcome page, optionally click **Customize**, then
**Install** (the Installer's button for running a package) and enter your
password.

It removes:

| What | Where |
|------|-------|
| The app | `/Applications/DispatchQ.app`, and the copy the background agent ran if that is another one |
| The background agent | `~/Library/LaunchAgents/de.maierdaniel.dispatchq.plist`; every running DispatchQ daemon and window is stopped, and a run in progress is cancelled |
| The wake permission | `/etc/sudoers.d/dispatchq` |
| The installer receipt | `pkgutil --forget de.maierdaniel.dispatchq` |
| What ClaudeQ left | `ClaudeQ.app` (or the link the installer put in its place), its LaunchAgent `de.maierdaniel.claudeq`, its daemon and window, `/etc/sudoers.d/claudeq` and its receipt, if any of them are still there |
| **Customize → Tasks, history and settings** (off by default) | `~/Library/Application Support/dispatchq` and the `claudeq` link next to it, and the app's own folders (also those of ClaudeQ) under `~/Library` (`WebKit`, `Caches`, `HTTPStorages`, `Saved Application State`, `Preferences`) |

The uninstaller leaves no receipt of its own. It also cleans up after an app
that was already dragged to the Bin: open the `.pkg` from the Bin's copy of
DispatchQ.app (right-click → Show Package Contents → `Contents/Resources`).

From the terminal, the same package runs through `installer`; run it as the
user logged in at the screen (not with sudo, it asks sudo for the password
itself):

```sh
/Applications/DispatchQ.app/Contents/MacOS/dispatchq uninstall          # keep tasks and history
/Applications/DispatchQ.app/Contents/MacOS/dispatchq uninstall --purge  # delete them too
```

The data choice deletes the default data directory only; the Installer cannot
see a `DISPATCHQ_HOME` set in your shell, so delete such a folder yourself
(`--purge` reminds you).

## Build from source

Requires Go 1.26+, Node.js 22+ with npm (for the web UI) and `librsvg`
(`brew install librsvg`) for icon rendering.

```sh
scripts/build-app.sh    # build/DispatchQ.app          (double-click or `open` it)
scripts/build-pkg.sh    # dist/dispatchq-<version>.pkg  (installer)
make web                # only the web UI, into internal/api/web
```

The daemon embeds the web UI when it is compiled, so build the UI first
(`make web`; `build-app.sh` does it for you). A daemon compiled without it serves
a page that says so. To work on the UI with hot reload against a running daemon,
run `npm run dev` in `web/` (it proxies `/api` to `127.0.0.1:10765`, or to
`DISPATCHQ_URL`).

Run the same quality gates CI enforces with `make check` (format, vet, lint,
race tests, and the web UI's typecheck, lint and tests). Releases are built automatically: pushing a `v*` tag runs
[`.github/workflows/release.yml`](.github/workflows/release.yml), which builds the
universal `.pkg` on macOS and attaches it to the GitHub Release.

## Architecture

A headless Go daemon owns all state and logic; a thin WKWebView app is the only
UI, talking to the daemon over loopback. Runs go through a **provider adapter**:
the daemon knows a task runs on a configured provider instance with a model and
an access mode, and the adapter for that provider owns the rest. The Claude Code
adapter spawns the `claude` CLI once per task in the task's directory using
`--output-format stream-json`, which lets it watch for rate-limit and auth events
as they happen and capture the session id, token usage, and cost from the final
result. The daemon also spawns a second, far smaller kind of `claude` call for
the [prompt review](#the-prompt-review): one turn, no tools, no session. DispatchQ
performs **no Git operations** — any branch/commit behavior is driven entirely by
your prompts and the repo's own configuration. The window's UI is a React and
TypeScript app in `web/` (Vite, Tailwind CSS, Radix UI primitives, TanStack
Query), built into `internal/api/web` and embedded in the daemon: design-system
components under `web/src/components/ui`, the shell under `web/src/app`, and one
folder per view under `web/src/features`. The full design, decisions, and
verification notes are in [PLAN.md](PLAN.md).

## Requirements

- macOS 12 or newer
- The [Claude Code](https://claude.com/claude-code) CLI, installed and
  authenticated
- Optionally the [Codex](https://learn.chatgpt.com/docs/developer-commands?surface=cli)
  CLI, installed and logged in, for tasks you want to run on it

## License

[MIT](LICENSE) © 2026 Daniel Maier
