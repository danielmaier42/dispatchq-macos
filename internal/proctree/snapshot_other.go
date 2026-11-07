//go:build unix && !linux

package proctree

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func processSnapshot(ctx context.Context) []procEntry { return psSnapshot(ctx) }

// StartTime identifies one incarnation of pid by the start time ps reports.
func StartTime(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", errMalformed
	}
	return s, nil
}
