package proxmox

import (
	"context"
	"fmt"
	"time"
)

// Defaults for WaitTask when the caller passes zero.
const (
	DefaultTaskPollInterval = 2 * time.Second
	DefaultTaskTimeout      = 2 * time.Minute
)

// WaitTask polls upid until it finishes, fails, or timeout elapses
// (CLAUDE.md: "Wait for the task (UPID) to finish before continuing").
// Zero poll or timeout use the defaults. It uses wall time, not an
// injected clock: this is an operational wait on a real Proxmox job, not
// a scheduling decision.
func WaitTask(ctx context.Context, c Client, upid UPID, poll, timeout time.Duration) error {
	if poll <= 0 {
		poll = DefaultTaskPollInterval
	}
	if timeout <= 0 {
		timeout = DefaultTaskTimeout
	}
	deadline := time.Now().Add(timeout)

	for {
		status, err := c.TaskStatus(ctx, upid)
		if err != nil {
			return fmt.Errorf("proxmox: check task %s: %w", upid, err)
		}
		switch status.State {
		case TaskOK:
			return nil
		case TaskError:
			return fmt.Errorf("proxmox: task %s failed: %s", upid, status.ExitStatus)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("proxmox: task %s did not complete within %s", upid, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}
