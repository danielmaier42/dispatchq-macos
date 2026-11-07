// Package api serves dispatchq's local control surface: a JSON REST API plus an
// embedded web dashboard, bound to loopback only (PLAN.md D11, NFA-04). It is
// the frontend/backend core a native window app would wrap.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielmaier42/claudeq/internal/app"
	"github.com/danielmaier42/claudeq/internal/assistant"
	"github.com/danielmaier42/claudeq/internal/feedback"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/randid"
	"github.com/danielmaier42/claudeq/internal/schedule"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
	"github.com/danielmaier42/claudeq/internal/update"
	"github.com/danielmaier42/claudeq/internal/workflow"
)

// RunNower runs a task immediately (satisfied by *engine.Engine). Optional.
type RunNower interface {
	RunTaskNow(ctx context.Context, taskID string) error
}

// RunCanceler stops a currently running run (satisfied by *engine.Engine).
// Optional; enables the cancel endpoint.
type RunCanceler interface {
	CancelRun(runID string) error
}

// RunTakeover stops a running run so its session can be continued by hand and
// returns the run as finally recorded (satisfied by *engine.Engine). Optional;
// lets the continue endpoint take over a run that is still in flight.
type RunTakeover interface {
	TakeOverRun(ctx context.Context, runID string) (store.Run, error)
}

// FolderChooser opens a native folder-selection dialog and returns the chosen
// POSIX path (chosen=false if the user cancelled). Optional.
type FolderChooser func(ctx context.Context, start string) (path string, chosen bool, err error)

// SaveFileDialog opens a native "Save as" panel titled prompt, pre-filled with
// defaultName, and returns the chosen POSIX path (chosen=false if the user
// cancelled). Optional; enables exporting a task to a file from the app.
type SaveFileDialog func(ctx context.Context, prompt, defaultName string) (path string, chosen bool, err error)

// Deps are the API server's dependencies.
type Deps struct {
	Store        *store.Store
	Runner       RunNower        // optional; enables the run-now endpoint
	Canceler     RunCanceler     // optional; enables the cancel-run endpoint
	TakeOver     RunTakeover     // optional; lets continue take over a running run
	OpenTerminal TerminalOpener  // optional; enables the continue-run endpoint
	ChooseFolder FolderChooser   // optional; enables the native folder dialog
	SaveFile     SaveFileDialog  // optional; enables the task export dialog
	ActiveTasks  func() []string // optional; ids of currently-running tasks (hidden from the queue)
	WakeError    func() string   // optional; last scheduled-wake error ("" if healthy)
	// LimitedUntil reports when the global rate-limit gate reopens (zero time
	// when it is open), so the dashboard can say the queue is waiting rather
	// than stuck. Optional (engine.Engine.LimitedUntil).
	LimitedUntil func() time.Time
	// BlockedProviders reports the provider instance IDs currently waiting out a
	// rate limit, each with the time its own gate reopens, so the dashboard can
	// name which account it is waiting on. Optional (engine.Engine.BlockedProviders).
	BlockedProviders func() map[string]time.Time
	// BlockReasons names, for blocked provider instances whose pause is an
	// account block (a provider.Block such as a used-up credit balance), which
	// block it is. Optional (engine.Engine.BlockReasons).
	BlockReasons func() map[string]string
	// RunningOn reports how many runs are in flight per provider instance,
	// which the urgency the Dashboard shows takes into account. Optional
	// (engine.Engine.RunningOn); nil counts none.
	RunningOn func() map[string]int
	// Holds reports, per task, why the scheduler's last pass did not start it
	// although it was due, and since when it waits. Optional (engine.Engine.Holds).
	Holds func() map[string]schedule.Hold
	// NotifyStatus reports whether macOS will actually show notifications
	// (notify.MacAuthorization). Optional; empty means "don't know".
	NotifyStatus func() string
	// WarmFileAccess reads the given directories so macOS raises its file-access
	// consent prompt now — called right after a task is created or its folder
	// changed, while the user is present. Optional.
	WarmFileAccess func(dirs []string)
	// Updates checks GitHub for newer releases and downloads the installer.
	// Optional; when nil the update endpoints report "unsupported".
	Updates *update.Service
	// Feedback drafts a GitHub issue from a short chat with the user. Optional;
	// when nil the dashboard offers the plain feedback form instead.
	Feedback *feedback.Service
	// Assistant is the chat that operates dispatchq. Optional; when nil the
	// dashboard says the chat is not available.
	Assistant *assistant.Service
	// OSVersion reports the macOS product version (e.g. "15.6") for the
	// environment line of a feedback issue. Optional; empty leaves it out.
	OSVersion func() string
	// Review checks a draft prompt against this machine before it is queued.
	// Optional; when nil the dashboard shows no suggestions.
	Review PromptReviewer
	// Web is the UI the daemon serves at /. Optional; nil serves the build
	// embedded at compile time (see webHandler).
	Web fs.FS
	// Registry holds the provider adapters this build supports. Required for the
	// provider endpoints.
	Registry *provider.Registry
	// Providers answers whether a configured instance can run a job. It is
	// shared with the daemon's scheduler, so the dashboard's polling reuses the
	// scheduler's verdicts instead of probing the CLIs again. Required for the
	// provider endpoints and for the queue's blocked marker.
	Providers *provider.Checker
	// Limits remembers and reads each provider's allowance (5-hour and weekly
	// windows). Optional; when nil the dashboard has no limits to show.
	Limits *provider.LimitMonitor
	// Uninstall opens DispatchQ's uninstaller in the macOS Installer (Settings >
	// System). Optional; when nil the endpoint reports "not implemented".
	Uninstall func(ctx context.Context) error
}

// Handler builds the HTTP handler (REST API under /api + dashboard at /).
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	s := &server{d: d}

	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.addTask)
	mux.HandleFunc("POST /api/tasks/import", s.readImport)
	mux.HandleFunc("POST /api/tasks/{id}/export", s.exportTask)
	mux.HandleFunc("PUT /api/tasks/{id}", s.updateTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	mux.HandleFunc("POST /api/tasks/{id}/enable", s.enableTask(true))
	mux.HandleFunc("POST /api/tasks/{id}/disable", s.enableTask(false))
	mux.HandleFunc("POST /api/tasks/{id}/move", s.moveTask)
	mux.HandleFunc("POST /api/tasks/{id}/run-now", s.runNow)
	mux.HandleFunc("GET /api/groups", s.listGroups)
	mux.HandleFunc("POST /api/groups/collapse", s.setGroupCollapsed)
	mux.HandleFunc("POST /api/groups/move", s.moveGroup)
	mux.HandleFunc("POST /api/groups/rename", s.renameGroup)
	mux.HandleFunc("GET /api/runs", s.listRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	mux.HandleFunc("POST /api/runs/read-all", s.readAll)
	mux.HandleFunc("POST /api/runs/{id}/read", s.readRun)
	mux.HandleFunc("POST /api/runs/{id}/cancel", s.cancelRun)
	mux.HandleFunc("POST /api/runs/{id}/continue", s.continueRun)
	mux.HandleFunc("GET /api/runs/{id}/log", s.runLog)
	mux.HandleFunc("GET /api/artifacts", s.listArtifacts)
	mux.HandleFunc("POST /api/artifacts/read-all", s.readAllArtifacts)
	mux.HandleFunc("POST /api/artifacts/{id}/read", s.readArtifact)
	mux.HandleFunc("DELETE /api/artifacts/{id}", s.deleteArtifact)
	mux.HandleFunc("GET /api/artifacts/{id}/content", s.artifactContent)
	mux.HandleFunc("GET /api/notifications", s.listNotifications)
	mux.HandleFunc("POST /api/notifications/read-all", s.readAllNotifications)
	mux.HandleFunc("POST /api/notifications/{id}/read", s.readNotification)
	mux.HandleFunc("GET /api/providers", s.listProviders)
	mux.HandleFunc("GET /api/providers/kinds", s.listProviderKinds)
	mux.HandleFunc("GET /api/limits", s.listLimits)
	mux.HandleFunc("GET /api/pools", s.listPools)
	mux.HandleFunc("GET /api/pools/{id}", s.getPool)
	mux.HandleFunc("POST /api/pools", s.addPool)
	mux.HandleFunc("PUT /api/pools/{id}", s.updatePool)
	mux.HandleFunc("DELETE /api/pools/{id}", s.deletePool)
	mux.HandleFunc("POST /api/providers", s.addProvider)
	mux.HandleFunc("PUT /api/providers/{id}", s.updateProvider)
	mux.HandleFunc("DELETE /api/providers/{id}", s.deleteProvider)
	mux.HandleFunc("POST /api/providers/{id}/check", s.checkProvider)
	mux.HandleFunc("POST /api/providers/{id}/enable", s.enableProvider(true))
	mux.HandleFunc("POST /api/providers/{id}/disable", s.enableProvider(false))
	mux.HandleFunc("POST /api/providers/{id}/default", s.setDefaultProvider)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("POST /api/pause", s.setPaused)
	mux.HandleFunc("POST /api/backfill", s.setBackfill)
	mux.HandleFunc("GET /api/models", s.listModels)
	mux.HandleFunc("GET /api/cron/check", s.checkCron)
	mux.HandleFunc("POST /api/review/prompt", s.reviewPrompt)
	mux.HandleFunc("GET /api/review/context", s.reviewContext)
	mux.HandleFunc("POST /api/fs/choose", s.chooseFolder)
	mux.HandleFunc("POST /api/fs/warm", s.warmNow)
	mux.HandleFunc("GET /api/stats", s.getStats)
	mux.HandleFunc("GET /api/health", s.getHealth)
	mux.HandleFunc("GET /api/ping", s.getPing)
	mux.HandleFunc("GET /api/update", s.getUpdate)
	mux.HandleFunc("POST /api/update/check", s.checkUpdate)
	mux.HandleFunc("POST /api/update/dismiss", s.dismissUpdate)
	mux.HandleFunc("GET /api/whats-new", s.getWhatsNew)
	mux.HandleFunc("POST /api/whats-new/dismiss", s.dismissWhatsNew)
	mux.HandleFunc("GET /api/rename-check", s.getRenameCheck)
	mux.HandleFunc("POST /api/rename-check", s.startRenameCheck)
	mux.HandleFunc("POST /api/rename-check/apply", s.applyRename)
	mux.HandleFunc("POST /api/rename-check/dismiss", s.dismissRenameCheck)
	mux.HandleFunc("POST /api/update/download", s.downloadUpdate)
	mux.HandleFunc("POST /api/update/relaunch", s.relaunchUpdate)
	mux.HandleFunc("POST /api/update/reinstall", s.reinstallRelease)
	mux.HandleFunc("POST /api/uninstall", s.uninstall)
	mux.HandleFunc("GET /api/assistant", s.getAssistant)
	mux.HandleFunc("POST /api/assistant/turn", s.assistantTurn)
	mux.HandleFunc("POST /api/assistant/cli", s.assistantCLI)
	mux.HandleFunc("GET /api/feedback", s.getFeedback)
	mux.HandleFunc("POST /api/feedback/turn", s.feedbackTurn)
	mux.HandleFunc("POST /api/feedback/url", s.feedbackURL)

	mux.Handle("GET /", webHandler(d.Web))
	return noCache(mux)
}

// noCache tells the WKWebView (and any client) never to reuse a cached copy of
// anything the daemon serves: a rebuilt daemon's new page always shows instead
// of a stale cached version, and a list of tasks or runs is always the present
// one rather than what the queue looked like on an earlier request. The UI's
// bundles under /assets/ are the exception (see webHandler): their names carry
// a hash of their content, so a cached copy can never be a stale one.
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, assetsPrefix) {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}
		h.ServeHTTP(w, r)
	})
}

type server struct {
	d Deps
	// Only the newest prompt review matters, so each one cancels its
	// predecessor; the generation counter keeps a finishing review from
	// clearing a newer one's cancel (see beginReview).
	reviewMu     sync.Mutex
	reviewGen    uint64
	reviewCancel context.CancelFunc
	// rename is the check of the queue for what still names ClaudeQ.
	rename renameJob
}

// activeTasks is the set of task ids running right now (empty when the daemon
// does not report them).
func (s *server) activeTasks() map[string]bool {
	active := map[string]bool{}
	if s.d.ActiveTasks != nil {
		for _, id := range s.d.ActiveTasks() {
			active[id] = true
		}
	}
	return active
}

func (s *server) listTasks(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.d.Store.LoadConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Scheduling bookkeeping is a nice-to-have here: without it the queue simply
	// shows no last-run time.
	st, _ := s.d.Store.LoadState()
	active := s.activeTasks()
	blocked := s.blockedReasons(r.Context(), cfg)
	waiting := s.waitingFor(cfg)
	var holds map[string]schedule.Hold
	if s.d.Holds != nil {
		holds = s.d.Holds()
	}
	out := make([]taskView, 0, len(cfg.Tasks))
	for _, t := range cfg.Tasks {
		// A running one-shot task moves to the Log and is hidden here. Recurring
		// (cron) tasks stay in the queue even while running, since they remain
		// queued for their next occurrence — just flagged as running.
		if active[t.ID] && t.Trigger != task.TriggerCron {
			continue
		}
		v := taskView{Task: t, Running: active[t.ID], WaitingFor: waiting[t.ID]}
		if h, ok := holds[t.ID]; ok && !active[t.ID] {
			v.Hold = &h
		}
		// A script job runs on no provider, so a provider that cannot run is
		// not what is holding it up — nothing is.
		if !t.IsScript() {
			v.BlockedReason = blocked[targetKey(t)]
		}
		// A task whose session is waiting for the rate limit is not idle: say so
		// in the queue, so it does not look like a job that simply hangs.
		if !active[t.ID] && st != nil && hasPendingResume(st, t.ID) {
			v.WaitingForLimit = true
		}
		// For recurring tasks, surface the next scheduled occurrence so the UI can
		// show it (e.g. as a tooltip on the cron expression). The task is already
		// validated on save, so a parse error here is not expected; skip silently.
		if t.Trigger == task.TriggerCron {
			if sched, err := t.CronSchedule(); err == nil {
				next := sched.Next(time.Now())
				v.NextRun = &next
			}
			if st != nil {
				if last, ok := st.LastRun(t.ID); ok {
					v.LastRun = &last
				}
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// taskView is a task plus its transient running state, for the queue view.
type taskView struct {
	task.Task
	Running bool `json:"running"`
	// NextRun is the next scheduled occurrence for a cron task, if computable.
	NextRun *time.Time `json:"next_run,omitempty"`
	// LastRun is when a cron task last actually started a run, if it ever did.
	LastRun *time.Time `json:"last_run,omitempty"`
	// WaitingForLimit marks a task whose interrupted Claude session is queued to
	// resume once the rate-limit gate reopens.
	WaitingForLimit bool `json:"waiting_for_limit,omitempty"`
	// BlockedReason says why the task's provider cannot run it. The task stays
	// queued and starts by itself once the provider works again, so the queue
	// has to say that rather than showing a job that silently never runs.
	BlockedReason string `json:"blocked_reason,omitempty"`
	// WaitingFor names the jobs this one is still waiting for. A join sitting in
	// the queue looks like a job that never starts unless the queue says what it
	// is waiting on.
	WaitingFor []string `json:"waiting_for,omitempty"`
	// Hold is why the scheduler did not start this task on its last pass
	// although it was due, and since when it has been waiting. It is the
	// scheduler's own verdict, so it covers what the markers above cannot see:
	// a pause, a task that runs alone, an exhausted allowance.
	Hold *schedule.Hold `json:"hold,omitempty"`
}

// waitingFor names, per task, the jobs it is still waiting for. Only tasks that
// depend on something appear, and history is read at most once.
func (s *server) waitingFor(cfg store.Config) map[string][]string {
	var dependent []task.Task
	for _, t := range cfg.Tasks {
		if len(t.DependsOn) > 0 {
			dependent = append(dependent, t)
		}
	}
	if len(dependent) == 0 {
		return nil
	}
	runs, err := s.d.Store.Runs()
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, t := range dependent {
		if _, names := workflow.Ready(workflow.Resolve(t, cfg.Tasks, runs)); len(names) > 0 {
			out[t.ID] = names
		}
	}
	return out
}

// blockedReasons maps a task's provider field to why that provider cannot run
// anything right now, or holds no entry when it can. It is keyed by the field
// as stored (empty means "the default provider"), so the caller needs no second
// resolution step. Verdicts come from the shared checker, which the scheduler
// keeps warm — listing the queue never probes a CLI on its own.
func (s *server) blockedReasons(ctx context.Context, cfg store.Config) map[string]string {
	set, err := provider.FromConfig(cfg)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	// One question per *instance*, not per field: twenty tasks naming the same
	// harness — some by its id, some by leaving the field empty for the default
	// — are one harness to ask.
	insts := []provider.Instance{}
	fields := map[string][]string{} // instance id -> the task fields that resolve to it
	asked := map[string]bool{}
	var pools []provider.Pool
	for _, t := range cfg.Tasks {
		key := targetKey(t)
		if asked[key] {
			continue
		}
		asked[key] = true
		if t.Pool != "" {
			if p, ok := set.LookupPool(t.Pool); ok {
				pools = append(pools, p)
			} else {
				out[key] = fmt.Sprintf("%v %q", provider.ErrUnknownPool, t.Pool)
			}
			continue
		}
		resolved, err := set.Resolve(provider.Selection{ProviderID: t.Provider})
		if err != nil {
			out[key] = err.Error()
			continue
		}
		id := resolved.Instance.ID
		if _, seen := fields[id]; !seen {
			insts = append(insts, resolved.Instance)
		}
		fields[id] = append(fields[id], key)
	}
	// A pool's members are asked with everything else; the pool is blocked
	// only when none of them can run, because one working account gets its
	// work done.
	for _, p := range pools {
		for _, inst := range set.PoolMembers(p) {
			if _, seen := fields[inst.ID]; !seen && inst.Enabled {
				insts = append(insts, inst)
				fields[inst.ID] = nil
			}
		}
	}
	// And they are asked together: when the verdicts have gone stale — which is
	// exactly the state the app finds them in after a while with no window open
	// — each one spawns a CLI, and the queue is the first thing the dashboard
	// asks for.
	health := map[string]provider.Health{}
	for i, h := range s.d.Providers.CheckEach(ctx, insts) {
		health[insts[i].ID] = h
		if h.Ready() {
			continue
		}
		reason := h.ReasonOr(insts[i].Label() + " cannot run tasks right now.")
		for _, field := range fields[insts[i].ID] {
			out[field] = reason
		}
	}
	for _, p := range pools {
		var reasons []string
		ready := false
		for _, inst := range set.PoolMembers(p) {
			h, checked := health[inst.ID]
			switch {
			case !inst.Enabled:
				reasons = append(reasons, inst.Label()+" is switched off.")
			case checked && !h.Ready():
				reasons = append(reasons, inst.Label()+": "+h.ReasonOr("it cannot run tasks right now."))
			default:
				ready = true
			}
		}
		if !ready {
			out[poolKey(p.ID)] = "No member of pool " + p.Label() + " can run: " + strings.Join(reasons, " ")
		}
	}
	return out
}

// targetKey is what blockedReasons files a task's verdict under: its provider
// field as stored, or its pool. The two cannot collide, because a pool's key
// carries a prefix no provider id can have.
func targetKey(t task.Task) string {
	if t.Pool != "" {
		return poolKey(t.Pool)
	}
	return t.Provider
}

func poolKey(id string) string { return "pool:" + id }

func (s *server) addTask(w http.ResponseWriter, r *http.Request) {
	var t task.Task
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if t.Permissions == "" {
		t.Permissions = task.PermissionsDefault
	}
	if t.ID == "" {
		t.ID = genTaskID(t.Name)
	} else if err := task.CheckID(t.ID); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if t.Name == "" {
		t.Name = t.ID
	}
	// A newly queued task is active by default; pausing is an explicit action
	// via the enable/disable endpoint. This avoids a silently-disabled task that
	// never runs on schedule (only via "run now").
	t.Enabled = true
	if err := t.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// A script job needs no harness, so it is filed whatever state the
	// providers are in — that is half the point of having one.
	if !t.IsScript() {
		if err := s.ensureRunnable(r.Context(), t.Provider, t.Pool); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if err := app.AddTask(s.d.Store, t); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.warmAccess(t.WorkingDir)
	writeJSON(w, http.StatusCreated, t)
}

// storedTask reads a task as it is currently stored.
func (s *server) storedTask(id string) (task.Task, error) {
	cfg, err := s.d.Store.LoadConfig()
	if err != nil {
		return task.Task{}, err
	}
	for _, t := range cfg.Tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return task.Task{}, fmt.Errorf("task %q not found", id)
}

// ensureRunnable refuses to file work for a provider (or a pool none of whose
// members) can run it, so a task is never queued that is known in advance to
// fail unattended.
func (s *server) ensureRunnable(ctx context.Context, providerID, poolID string) error {
	set, err := app.Providers(s.d.Store)
	if err != nil {
		return err
	}
	return app.EnsureTaskTarget(ctx, set, s.d.Providers, providerID, poolID)
}

// warmAccess provokes the macOS file-access prompt for a task's folder right
// after it is created or changed, while the user is present in the app — so a
// newly-used protected location (Downloads, Desktop, …) is authorised now, not
// at 3am mid-run. Best-effort; a no-op when the hook or path is unset.
func (s *server) warmAccess(dir string) {
	if s.d.WarmFileAccess != nil && dir != "" {
		go s.d.WarmFileAccess([]string{dir})
	}
}

// warmNow provokes the file-access prompt for every enabled task's folder. The
// app calls it on launch, so simply opening the window re-checks access while
// the user is present — covering folders that were added or edited while the app
// was closed, or that were never authorised. The daemon (this process) does the
// probing, so the grant lands on the identity that actually runs the tasks.
func (s *server) warmNow(w http.ResponseWriter, _ *http.Request) {
	if s.d.WarmFileAccess == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	cfg, err := s.d.Store.LoadConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	dirs := make([]string, 0, len(cfg.Tasks))
	for _, t := range cfg.Tasks {
		if t.Enabled {
			dirs = append(dirs, t.WorkingDir)
		}
	}
	go s.d.WarmFileAccess(dirs)
	w.WriteHeader(http.StatusAccepted)
}

func (s *server) updateTask(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var t task.Task
	if err := json.Unmarshal(body, &t); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t.ID = r.PathValue("id") // the id is fixed by the URL
	// The group is set by dragging the row in the queue, not in the task form,
	// so a payload that says nothing about it leaves it alone. Without this,
	// saving the form would quietly take the task out of its group.
	var sent struct {
		Group *string `json:"group"`
	}
	_ = json.Unmarshal(body, &sent)
	// The origin is set by whichever job created this one and is never edited.
	if prev, err := s.storedTask(t.ID); err == nil {
		if sent.Group == nil {
			t.Group = prev.Group
		}
		t.OriginID, t.OriginName = prev.OriginID, prev.OriginName
	}
	if t.Permissions == "" {
		t.Permissions = task.PermissionsDefault
	}
	if t.Name == "" {
		t.Name = t.ID
	}
	if err := t.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Only a *move* to another provider is checked. An existing task keeps the
	// one it has, and an unready provider must not stand in the way of editing
	// the prompt, the folder or the schedule — that edit may well be how the
	// operator is fixing it.
	if prev, err := s.storedTask(t.ID); err == nil && (prev.Provider != t.Provider || prev.Pool != t.Pool) && !t.IsScript() {
		if err := s.ensureRunnable(r.Context(), t.Provider, t.Pool); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	var prevDir string
	err = s.d.Store.UpdateConfig(func(cfg *store.Config) error {
		for i := range cfg.Tasks {
			if cfg.Tasks[i].ID == t.ID {
				prevDir = cfg.Tasks[i].WorkingDir
				cfg.Tasks[i] = t
				// An edit may name another group; the stored order follows the
				// one the queue renders (see app.GroupedOrder).
				cfg.Tasks = app.GroupedOrder(cfg.Tasks)
				return nil
			}
		}
		return fmt.Errorf("task %q not found", t.ID)
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The edit may have taken the last task out of a group; the fold state of a
	// group that no longer exists goes with it.
	_ = app.PruneGroups(s.d.Store)
	// Only warm when the folder actually changed — editing just the prompt/model
	// keeps the same (already-authorised) directory, so re-probing it is wasted
	// work. A genuine folder change still provokes the prompt for the new one.
	if t.WorkingDir != prevDir {
		s.warmAccess(t.WorkingDir)
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := app.RemoveTask(s.d.Store, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) enableTask(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := app.SetEnabled(s.d.Store, r.PathValue("id"), enabled); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// moveTask reorders a task and, when the request names a group, moves it into
// that group in the same write — the queue's drag and drop is one gesture, and
// a half-applied drop would leave a task in a group at the wrong position.
// An empty 'group' is not the same as none: it means "no group", which is how a
// task is dragged back out of one.
func (s *server) moveTask(w http.ResponseWriter, r *http.Request) {
	to, err := strconv.Atoi(r.URL.Query().Get("to"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("query param 'to' must be an integer"))
		return
	}
	id := r.PathValue("id")
	if r.URL.Query().Has("group") {
		err = app.MoveToGroup(s.d.Store, id, r.URL.Query().Get("group"), to)
	} else {
		err = app.Move(s.d.Store, id, to)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listGroups answers with the queue's groups in the order they appear, each
// with how many tasks it holds and whether its section is folded shut. The
// dashboard needs the fold state on every poll, and a group is nothing but the
// tasks that name it, so both come from one place.
func (s *server) listGroups(w http.ResponseWriter, _ *http.Request) {
	cfg, err := s.d.Store.LoadConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	st, _ := s.d.Store.LoadState() // a missing state file just means nothing is collapsed
	out := make([]groupView, 0, 4)
	at := map[string]int{}
	for _, t := range cfg.Tasks {
		if t.Group == "" {
			continue
		}
		if i, ok := at[t.Group]; ok {
			out[i].Count++
			continue
		}
		at[t.Group] = len(out)
		out = append(out, groupView{Name: t.Group, Count: 1, Collapsed: st != nil && st.GroupCollapsed(t.Group)})
	}
	writeJSON(w, http.StatusOK, out)
}

// groupView is one queue group: its name, how many tasks it holds, and whether
// the dashboard shows it folded shut.
type groupView struct {
	Name      string `json:"name"`
	Count     int    `json:"count"`
	Collapsed bool   `json:"collapsed"`
}

// moveGroup puts a whole section in front of another one. "before" is the group
// to sit in front of — "" is the ungrouped section, and leaving it out moves the
// group to the end of the queue.
func (s *server) moveGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string  `json:"name"`
		Before *string `json:"before"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := app.MoveGroup(s.d.Store, in.Name, in.Before); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// renameGroup gives a section another name. Renaming onto a name that is
// already in use merges the two sections, which is what the dashboard warns
// about before it sends the request.
func (s *server) renameGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := app.RenameGroup(s.d.Store, in.Name, in.To); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setGroupCollapsed remembers a folded or unfolded section. It is a view
// preference, so it goes to the state file, not the config.
func (s *server) setGroupCollapsed(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string `json:"name"`
		Collapsed bool   `json:"collapsed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := app.SetGroupCollapsed(s.d.Store, in.Name, in.Collapsed); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) runNow(w http.ResponseWriter, r *http.Request) {
	if s.d.Runner == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("run-now not available"))
		return
	}
	// A paused queue refuses manual runs too, and the caller should hear about it:
	// the run itself is fire-and-forget, so its error would go nowhere.
	cfg, err := s.d.Store.LoadConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if cfg.Settings.Paused {
		writeErr(w, http.StatusConflict, store.ErrPaused)
		return
	}
	id := r.PathValue("id")
	// Run asynchronously; the result shows up in the run history.
	go func() { _ = s.d.Runner.RunTaskNow(context.Background(), id) }()
	w.WriteHeader(http.StatusAccepted)
}

// cancelRun stops a currently running run. 409 when the run is not in flight
// (already finished, or unknown) — the UI then just refreshes its state.
func (s *server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if s.d.Canceler == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("cancel not available"))
		return
	}
	if err := s.d.Canceler.CancelRun(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// continueRun opens a Terminal window that resumes the run's session
// interactively, in the run's provider and in the task's working directory,
// so an unattended chat can be picked up by hand with its full context. A
// finished run qualifies as it is. A running one still owns its session, so it
// is taken over: its process is stopped first and the terminal opens only once
// it is gone. A rate-limited one will be resumed by the queue itself.
func (s *server) continueRun(w http.ResponseWriter, r *http.Request) {
	if s.d.OpenTerminal == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("continue not available"))
		return
	}
	runs, err := s.d.Store.Runs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	id := r.PathValue("id")
	var run *store.Run
	for i := range runs {
		if runs[i].RunID == id {
			run = &runs[i]
			break
		}
	}
	running := run != nil && run.Status == store.StatusRunning
	switch {
	case run == nil:
		writeErr(w, http.StatusNotFound, errors.New("run not found"))
		return
	case running && s.d.TakeOver == nil:
		writeErr(w, http.StatusConflict, errors.New("the run is still in progress; wait for it to finish (or cancel it)"))
		return
	case !running && !run.Status.Terminal():
		writeErr(w, http.StatusConflict, errors.New("the run is waiting to resume automatically; continue it after it finishes"))
		return
	case run.SessionID == "":
		writeErr(w, http.StatusConflict, errors.New("no session recorded for this run"))
		return
	case run.Task == nil || run.Task.WorkingDir == "":
		writeErr(w, http.StatusConflict, errors.New("no working directory recorded for this run"))
		return
	}
	// The session lives in Claude Code's per-project store keyed by this
	// directory — a vanished folder means the resume cannot work.
	if _, err := os.Stat(run.Task.WorkingDir); err != nil {
		writeErr(w, http.StatusConflict, fmt.Errorf("the task's working directory is gone: %w", err))
		return
	}
	// The interactive resume goes to the provider instance that owns the
	// session, and the adapter says how that harness reopens one. dispatchq never
	// offers to continue a session on another account, let alone another harness.
	// Asked before a take-over too, so a run that could not be reopened by hand
	// is never stopped for nothing.
	argv, secrets, err := s.resumeCommand(*run, *run.Task)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	// Once a run is being stopped for the user, the terminal has to open even
	// if the app stops waiting for the answer — otherwise the run is gone and
	// nothing took its place.
	ctx := r.Context()
	if running {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), takeOverTimeout)
		defer cancel()
		stopped, err := s.takeOver(ctx, run.RunID)
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeErr(w, http.StatusGatewayTimeout, err)
			return
		case err != nil:
			writeErr(w, http.StatusConflict, err)
			return
		}
		// A harness may report the session it really used only when the run
		// ends, so the command is built again from what was finally recorded.
		if stopped.SessionID == "" {
			writeErr(w, http.StatusConflict, errors.New("the run was stopped before it had a session to continue"))
			return
		}
		if argv, secrets, err = s.resumeCommand(stopped, *run.Task); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
	}
	// The variables the operator set (an API key, say) must reach the session
	// too, or it would reopen on the login instead of the key. They are handed
	// over in a file the shell reads and deletes, never on the command line,
	// which Terminal prints and the shell keeps in its history. The file is made
	// only now, right before the terminal opens, and removed again when it does
	// not.
	argv, cleanup, err := secretEnvArgv(secrets, argv)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.d.OpenTerminal(ctx, run.Task.WorkingDir, argv); err != nil {
		cleanup()
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// takeOverTimeout bounds the wait for a taken-over run's process to go; the
// executor's own kill escalation is far shorter.
const takeOverTimeout = time.Minute

// takeOver stops a running run and returns its final record. A run that ended
// on its own between being read and being stopped is not an error: it is
// finished now, and continuing it is exactly what was asked.
func (s *server) takeOver(ctx context.Context, runID string) (store.Run, error) {
	stopped, err := s.d.TakeOver.TakeOverRun(ctx, runID)
	if err == nil || ctx.Err() != nil {
		return stopped, err
	}
	runs, rerr := s.d.Store.Runs()
	if rerr != nil {
		return store.Run{}, err
	}
	for _, r := range runs {
		if r.RunID == runID && r.Status.Terminal() {
			return r, nil
		}
	}
	return store.Run{}, err
}

// resumeCommand builds the argv that reopens a run's session in a terminal.
//
// The provider that owns the session is the one the run used, not the one the
// task points at today: a task moved to another provider since then has left
// its old conversation where it was, and offering to continue it with a harness
// that never had it would open an empty terminal at best. What the run recorded
// is therefore what is asked; only a run from before that was recorded falls
// back to the task's own provider.
//
// The second result is the instance's own environment as NAME=value entries,
// which the caller delivers without putting them on the command line.
func (s *server) resumeCommand(run store.Run, t task.Task) ([]string, []string, error) {
	set, err := app.Providers(s.d.Store)
	if err != nil {
		return nil, nil, err
	}
	owner := run.Provider.ID
	if owner == "" {
		owner = t.Provider
	}
	resolved, err := set.Resolve(provider.Selection{ProviderID: owner})
	if err != nil {
		return nil, nil, err
	}
	ad, err := s.d.Registry.Lookup(resolved.Instance.Kind)
	if err != nil {
		return nil, nil, err
	}
	if !ad.Capabilities().InteractiveResume {
		return nil, nil, fmt.Errorf("%s cannot continue a session in a terminal", resolved.Instance.Label())
	}
	if ad.ResolveBinary(resolved.Instance) == "" {
		return nil, nil, fmt.Errorf("%s is not installed on this Mac", resolved.Instance.Label())
	}
	cmd, err := ad.InteractiveResumeCommand(resolved.Instance, provider.Request{
		SessionID:  run.SessionID,
		WorkingDir: t.WorkingDir,
		AccessMode: resumeAccess(run, t),
	})
	if err != nil {
		return nil, nil, err
	}
	argv := append([]string{cmd.Path}, cmd.Args...)
	// The adapter's environment is what picks the account: a Claude Code
	// instance is its config directory. Terminal starts a fresh login shell
	// that knows nothing of it, so without `env` the session would reopen
	// under whichever account the shell's default login belongs to.
	secrets := resolved.Instance.EnvPairs()
	if plain := withoutEntries(cmd.Env, secrets); len(plain) > 0 {
		argv = append(append([]string{"/usr/bin/env"}, plain...), argv...)
	}
	return argv, secrets, nil
}

// resumeAccess is the authority a continued session gets: the one the run
// actually had, so changing the task's permissions afterwards does not hand a
// finished conversation more authority than it ever ran with. A run from before
// that was recorded falls back to the task.
func resumeAccess(run store.Run, t task.Task) provider.AccessMode {
	if run.Provider.AccessMode != "" {
		return provider.AccessMode(run.Provider.AccessMode)
	}
	return accessMode(t.Permissions)
}

// accessMode maps a task's permission setting onto dispatchq's provider-neutral
// access intent, the same way the engine does for a run — so a continued session
// gets exactly the authority the run it continues had.
func accessMode(p task.Permissions) provider.AccessMode {
	if p == task.PermissionsSkip {
		return provider.AccessFullAccess
	}
	return provider.AccessProviderDefault
}

// runView is a run plus its unread flag and whether its interrupted session is
// still scheduled to resume.
type runView struct {
	store.Run
	// FinalOutput shadows the run record's own: the answer can be kilobytes and
	// the list carries hundreds of runs, so it is not sent with the list. What
	// the run said is read from its log.
	FinalOutput string `json:"final_output,omitempty"`
	// Task shadows the run record's own snapshot so the list can send it
	// without the prompt — see listRuns.
	Task   *task.Task `json:"task,omitempty"`
	Unread bool       `json:"unread"`
	// ResumePending marks a rate-limited run whose session the daemon is still
	// going to pick up once the gate reopens. It is what separates a run that is
	// merely waiting from one whose pause is history (already resumed, canceled,
	// or the task is gone).
	ResumePending bool `json:"resume_pending,omitempty"`
}

// listRuns answers with the whole history, newest first — the dashboard filters
// and pages it, and counts the unread across all of it.
//
// What it leaves out is the prompt of each run's task snapshot. The list is
// polled every few seconds by several views at once, and the prompts are the
// bulk of it (on a year-old queue, 85% of a 3 MB answer) for text no list row
// shows. Whoever needs it — replay, the log sheet — asks for that one run.
func (s *server) listRuns(w http.ResponseWriter, _ *http.Request) {
	runs, err := s.d.Store.Runs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	st, err := s.d.Store.LoadState()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	active := s.activeTasks()
	views := make([]runView, 0, len(runs))
	// Newest first for the dashboard.
	for i := len(runs) - 1; i >= 0; i-- {
		v := s.runView(runs[i], st, active)
		v.Task = withoutPrompt(runs[i].Task)
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, views)
}

// getRun answers with one run, prompt and all: the whole record the list sends
// a lighter version of.
func (s *server) getRun(w http.ResponseWriter, r *http.Request) {
	runs, err := s.d.Store.Runs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	st, err := s.d.Store.LoadState()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	id := r.PathValue("id")
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].RunID != id {
			continue
		}
		v := s.runView(runs[i], st, s.activeTasks())
		v.Task = runs[i].Task
		writeJSON(w, http.StatusOK, v)
		return
	}
	writeErr(w, http.StatusNotFound, fmt.Errorf("no run %q", id))
}

// runView builds the view of one run, minus its task snapshot — which the list
// and the single-run endpoint fill in differently.
func (s *server) runView(r store.Run, st *store.State, active map[string]bool) runView {
	return runView{
		Run:           r,
		Unread:        !st.IsRead(r.RunID),
		ResumePending: resumePending(r, st, active),
	}
}

// withoutPrompt copies a task snapshot with its prompt left out, so sending it
// does not send the text of the job as well.
func withoutPrompt(t *task.Task) *task.Task {
	if t == nil {
		return nil
	}
	light := *t
	light.Prompt = ""
	return &light
}

// resumePending reports whether a rate-limited run's session is still queued to
// be resumed: the task must still hold exactly this run's session as its
// pending resume, and must not be running right now (a resume already in
// flight shows up as its own running run).
func resumePending(r store.Run, st *store.State, active map[string]bool) bool {
	if r.Status != store.StatusRateLimited || active[r.TaskID] {
		return false
	}
	pending, ok := st.PendingResume(r.TaskID)
	return ok && r.SessionID != "" && pending.SessionID == r.SessionID
}

// hasPendingResume reports whether a task holds a session waiting to continue.
func hasPendingResume(st *store.State, taskID string) bool {
	_, ok := st.PendingResume(taskID)
	return ok
}

func (s *server) readRun(w http.ResponseWriter, r *http.Request) {
	if err := app.MarkRead(s.d.Store, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) readAll(w http.ResponseWriter, _ *http.Request) {
	if err := app.MarkAllRead(s.d.Store); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) runLog(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.d.Store.LogPath(r.PathValue("id")))
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusNotFound, errors.New("log not found"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

// artifactView is a published artifact plus its unread flag.
type artifactView struct {
	store.Artifact
	Unread bool `json:"unread"`
}

func (s *server) listArtifacts(w http.ResponseWriter, _ *http.Request) {
	arts, err := s.d.Store.Artifacts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	st, err := s.d.Store.LoadState()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// The queue and history give each artifact the group and origin the
	// view filters by (see app.ResolveArtifactSources).
	cfg, err := s.d.Store.LoadConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	runs, err := s.d.Store.Runs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	app.ResolveArtifactSources(arts, cfg.Tasks, runs)
	views := make([]artifactView, 0, len(arts))
	// Newest first for the dashboard.
	for i := len(arts) - 1; i >= 0; i-- {
		views = append(views, artifactView{Artifact: arts[i], Unread: !st.IsArtifactRead(arts[i].ID)})
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *server) readArtifact(w http.ResponseWriter, r *http.Request) {
	if err := app.MarkArtifactRead(s.d.Store, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) readAllArtifacts(w http.ResponseWriter, _ *http.Request) {
	if err := app.MarkAllArtifactsRead(s.d.Store); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) deleteArtifact(w http.ResponseWriter, r *http.Request) {
	if err := app.DeleteArtifact(s.d.Store, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// artifactContent serves an artifact's stored file for the in-app viewer or a
// download. The file is user-generated, so it is served with a restrictive CSP
// (no network access) and is meant to be shown inside a sandboxed iframe, so a
// malicious HTML artifact can neither reach the loopback API nor exfiltrate data.
func (s *server) artifactContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	arts, err := s.d.Store.Artifacts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var art *store.Artifact
	for i := range arts {
		if arts[i].ID == id {
			art = &arts[i]
			break
		}
	}
	if art == nil {
		writeErr(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}

	f, err := os.Open(s.d.Store.ArtifactContentPath(*art))
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusNotFound, errors.New("artifact file missing"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	ct := art.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Self-contained pages render (inline CSS/JS, data: images) but the artifact
	// can make no network requests, so it cannot phone home or reach the API.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; img-src data: blob:; media-src data: blob:; "+
			"style-src 'unsafe-inline'; font-src data:; script-src 'unsafe-inline'; "+
			"form-action 'none'; base-uri 'none'")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(art.FileName))
	}
	if r.URL.Query().Get("frame") != "" && strings.HasPrefix(strings.ToLower(ct), "text/html") {
		page, err := io.ReadAll(f)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("read artifact: %w", err))
			return
		}
		page = append(page, frameLinkScript...)
		http.ServeContent(w, r, art.FileName, info.ModTime(), bytes.NewReader(page))
		return
	}
	http.ServeContent(w, r, art.FileName, info.ModTime(), f)
}

// frameLinkScript is appended to an HTML artifact shown in the viewer's frame.
// The frame is sandboxed and cannot open a link anywhere useful itself, so a
// click on an http(s) link is handed to the window, which opens it in the
// browser. Links within the page (#section) keep working in place.
const frameLinkScript = `
<script>
document.addEventListener('click', function (e) {
  if (e.defaultPrevented || e.button !== 0) return;
  var a = e.target && e.target.closest ? e.target.closest('a[href]') : null;
  if (!a) return;
  var u;
  try { u = new URL(a.href); } catch (_) { return; }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return;
  var here = new URL(location.href);
  if (u.origin === here.origin && u.pathname === here.pathname && u.search === here.search) return;
  e.preventDefault();
  parent.postMessage({type: 'dispatchq:open-link', url: u.href}, '*');
});
</script>
`

func (s *server) getSettings(w http.ResponseWriter, _ *http.Request) {
	cfg, err := s.d.Store.LoadConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg.Settings)
}

func (s *server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in store.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := s.d.Store.LoadConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// A channel that is switched on but cannot deliver is refused here, while
	// the operator is looking at the form — not at three in the morning, where
	// the only trace would be a line in the daemon's log.
	if err := app.ValidateNotifications(in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !store.ValidAppearance(in.Appearance) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("appearance %q: want %q, %q or empty to follow macOS", in.Appearance, store.AppearanceLight, store.AppearanceDark))
		return
	}
	if !store.ValidAccent(in.Accent) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("accent %q: want a colour like #0a84ff, or empty to follow macOS", in.Accent))
		return
	}
	in.Accent = strings.ToLower(in.Accent)
	// The pause switch belongs to POST /api/pause alone. It can be flipped from
	// the Queue banner or the CLI at any time, so a settings form filled in
	// before that must not carry a stale value back and quietly resume the queue.
	in.Paused = cfg.Settings.Paused
	// Same for the default provider, which the Providers section sets on its own
	// endpoint: a form that does not show it must not be able to reset it.
	in.DefaultProvider = cfg.Settings.DefaultProvider
	// And for the backfill threshold, which the Dashboard's slider sets.
	in.BackfillUrgency = cfg.Settings.BackfillUrgency
	cfg.Settings = in
	if err := s.d.Store.SaveConfig(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg.Settings)
}

// setPaused flips the global pause switch on its own, without going through the
// whole settings payload: the dashboard toggles it like a task's enable switch
// (no Save press) and from the Queue banner, so it must not carry — and thus
// overwrite — every other setting on the way.
func (s *server) setPaused(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := app.SetPaused(s.d.Store, in.Paused); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": in.Paused})
}

// setBackfill sets the backfill threshold on its own, the way setPaused flips
// the pause switch: the Dashboard's slider applies on release, without Save.
func (s *server) setBackfill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Urgency float64 `json:"urgency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := app.SetBackfillUrgency(s.d.Store, in.Urgency); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrInvalidBackfillUrgency) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]float64{"urgency": in.Urgency})
}

// getStats answers with the consumption summary over the window the optional
// `days` query parameter names (see parseStatsWindow).
func (s *server) getStats(w http.ResponseWriter, r *http.Request) {
	win, err := parseStatsWindow(r.URL.Query().Get("days"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	runs, err := s.d.Store.Runs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, computeStats(runs, time.Now(), win))
}

// listModels answers with the models one provider suggests. The provider is
// named by the `provider` query parameter; without one the default provider's
// list is returned, which is what the task form wants before anything is chosen.
//
// A catalog is never validation: a model a task names but this list does not is
// passed to the harness unchanged, so a discovery failure costs suggestions and
// nothing else.
func (s *server) listModels(w http.ResponseWriter, r *http.Request) {
	set, ok := s.providerSet(w)
	if !ok {
		return
	}
	resolved, err := set.Resolve(provider.Selection{ProviderID: r.URL.Query().Get("provider")})
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	ad, err := s.d.Registry.Lookup(resolved.Instance.Kind)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	models := ad.ListModels(r.Context(), resolved.Instance, provider.ExecProber{})
	if models == nil {
		models = []provider.Model{}
	}
	writeJSON(w, http.StatusOK, models)
}

// getPing answers "is a daemon serving here?" and nothing else: no store, no
// provider check, no lock. The app window asks it before deciding to start a
// daemon of its own, and that question must not be answered by whatever the
// daemon happens to be busy with.
func (s *server) getPing(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// limitedProvider names one provider instance waiting out a rate limit, for
// the health response's limited_providers list.
type limitedProvider struct {
	Name  string `json:"name"`
	Until string `json:"until"`
	// Fallback is the label of the provider that takes this one's tasks in the
	// meantime, empty when it has none that could. A banner that names it says
	// the queue is moving; without one the queue really is waiting.
	Fallback string `json:"fallback,omitempty"`
	// Reason names an account block behind the pause (a provider.Block), empty
	// for an allowance limit that resets on its own.
	Reason string `json:"reason,omitempty"`
}

// getHealth reports daemon health the UI can warn about: whether scheduled-wake
// setup is working, and whether macOS is set to show DispatchQ's notifications at
// all (a denied app posts into the void).
func (s *server) getHealth(w http.ResponseWriter, r *http.Request) {
	wakeErr := ""
	if s.d.WakeError != nil {
		wakeErr = s.d.WakeError()
	}
	notifyStatus := ""
	if s.d.NotifyStatus != nil {
		notifyStatus = s.d.NotifyStatus()
	}
	limitedUntil := ""
	if s.d.LimitedUntil != nil {
		if until := s.d.LimitedUntil(); !until.IsZero() {
			limitedUntil = until.Format(time.RFC3339)
		}
	}
	limitedProviders := []limitedProvider{}
	if s.d.BlockedProviders != nil {
		blocked := s.d.BlockedProviders()
		if len(blocked) > 0 {
			// Best-effort labels: a config that fails to load still leaves the
			// generic limited_until banner working, just without provider names.
			set, _ := app.Providers(s.d.Store)
			reasons := s.blockReasons()
			for id, until := range blocked {
				name, fallback := id, ""
				if inst, ok := set.Lookup(id); ok {
					name = inst.Label()
					fallback = s.fallbackLabel(r.Context(), set, inst, blocked)
				}
				limitedProviders = append(limitedProviders,
					limitedProvider{Name: name, Until: until.Format(time.RFC3339), Fallback: fallback, Reason: reasons[id]})
			}
			sort.Slice(limitedProviders, func(i, j int) bool { return limitedProviders[i].Until < limitedProviders[j].Until })
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"wake_error":        wakeErr,
		"notify_status":     notifyStatus,
		"limited_until":     limitedUntil,
		"limited_providers": limitedProviders,
	})
}

// blockReasons asks the engine which blocked providers are account blocks;
// none without one.
func (s *server) blockReasons() map[string]string {
	if s.d.BlockReasons == nil {
		return nil
	}
	return s.d.BlockReasons()
}

// fallbackLabel names the provider that is actually taking inst's tasks while
// its allowance is used up, by asking the same resolver the scheduler asks: the
// banner says the queue keeps moving only where it really does. Readiness comes
// from the remembered verdict — this is a banner, not a start, and a probe per
// poll would spawn a CLI every few seconds.
func (s *server) fallbackLabel(ctx context.Context, set provider.Set, inst provider.Instance, blocked map[string]time.Time) string {
	res, err := set.ResolveAvailable(provider.Selection{ProviderID: inst.ID}, provider.Availability{
		OutOfAllowance: func(id string) bool { _, limited := blocked[id]; return limited },
		CanTakeWork:    func(i provider.Instance) bool { return !s.d.Providers.Check(ctx, i).KnownUnready() },
		Limits:         s.cachedLimits(),
		Now:            time.Now,
	})
	if err != nil || !res.Substituted() {
		return ""
	}
	return res.Instance.Label()
}

func (s *server) chooseFolder(w http.ResponseWriter, r *http.Request) {
	if s.d.ChooseFolder == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("folder dialog not available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	path, chosen, err := s.d.ChooseFolder(ctx, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if !chosen {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

// genTaskID builds a URL-safe id from a name plus a short random suffix, so the
// user never has to supply one.
func genTaskID(name string) string {
	return task.Slug(name) + "-" + randid.Hex(3)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
