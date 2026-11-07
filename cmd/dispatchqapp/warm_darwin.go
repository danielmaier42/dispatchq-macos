package main

import (
	"net/http"
	"time"
)

// requestWarm asks the daemon to provoke the macOS file-access prompt for the
// current tasks' folders. Best-effort: any failure is silently ignored.
func requestWarm() {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Post(dashboardURL+"/api/fs/warm", "application/json", nil)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}
