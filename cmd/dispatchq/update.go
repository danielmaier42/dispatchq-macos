package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/danielmaier42/claudeq/internal/update"
)

// updateCheckTimeout covers the daemon's own 20-second GitHub check plus the
// round trip to it.
const updateCheckTimeout = 30 * time.Second

// updateView is the part of the daemon's /api/update answer the CLI reports;
// --json prints the daemon's answer unchanged.
type updateView struct {
	Current   string `json:"current"`
	Supported bool   `json:"supported"`
	Available bool   `json:"available"`
	Latest    string `json:"latest"`
	HTMLURL   string `json:"html_url"`
	Dismissed string `json:"dismissed"`
	Installed string `json:"installed"`
	Restart   bool   `json:"restart_required"`
	Error     string `json:"error"`
}

// cmdUpdate runs `dispatchq update check`: the running daemon checks GitHub
// now, exactly like Check for updates in About, so the app shows its update
// prompt without waiting for the hourly check.
func cmdUpdate(args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return errors.New("usage: dispatchq update check [--json]")
	}
	fs := flag.NewFlagSet("update check", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the daemon's update status as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	raw, err := daemonUpdateCheck()
	if err != nil {
		return err
	}
	var v updateView
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("read the daemon's answer: %w", err)
	}
	if *asJSON {
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("read the daemon's answer: %w", err)
		}
		return printJSON(out)
	}
	if v.Error != "" {
		return fmt.Errorf("update check failed: %s", v.Error)
	}
	fmt.Println(updateLine(v))
	return nil
}

// updateLine says in one line what the check found and what the app shows.
func updateLine(v updateView) string {
	switch {
	case v.Restart:
		return fmt.Sprintf("DispatchQ %s is installed; restart the app to run it (running %s).", v.Installed, v.Current)
	case !v.Supported:
		return fmt.Sprintf("This is a development build (%s); it is not offered updates. Latest release: %s.", v.Current, orNone(v.Latest))
	case v.Available:
		return fmt.Sprintf("DispatchQ %s is available (running %s). The app shows the update prompt.\n%s", v.Latest, v.Current, v.HTMLURL)
	case v.Latest != "" && v.Latest == update.Normalize(v.Dismissed) && v.Latest != v.Current:
		return fmt.Sprintf("DispatchQ %s is available but was dismissed (running %s).", v.Latest, v.Current)
	case v.Latest != "" && update.IsNewer(v.Latest, v.Current):
		// Released, but its installer is not uploaded yet.
		return fmt.Sprintf("DispatchQ %s is released but has no installer yet (running %s); check again shortly.", v.Latest, v.Current)
	default:
		return fmt.Sprintf("DispatchQ %s is up to date.", v.Current)
	}
}

func orNone(s string) string {
	if s == "" {
		return "none found"
	}
	return s
}

// daemonUpdateCheck asks the running daemon to check for updates now and
// returns its answer. Only the daemon can do it: the update prompt shows what
// its checker last found.
func daemonUpdateCheck() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, daemonURL+"/api/update/check", nil)
	if err != nil {
		return nil, fmt.Errorf("update request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: is the DispatchQ app running? %w", errNoDaemon, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body json.RawMessage
	decodeErr := json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if decodeErr == nil && json.Unmarshal(body, &e) == nil && e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return nil, fmt.Errorf("daemon answered %s", resp.Status)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("read the daemon's answer: %w", decodeErr)
	}
	return body, nil
}
