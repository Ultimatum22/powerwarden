package proxmox

import (
	"context"
	"strings"
	"testing"
	"time"
)

// taskSeq is a Client whose TaskStatus returns states in order, repeating
// the last one.
type taskSeq struct {
	Client
	states []TaskStatus
	calls  int
}

func (s *taskSeq) TaskStatus(context.Context, UPID) (TaskStatus, error) {
	i := min(s.calls, len(s.states)-1)
	s.calls++
	return s.states[i], nil
}

func TestWaitTask(t *testing.T) {
	running := TaskStatus{State: TaskRunning}
	tests := []struct {
		name    string
		states  []TaskStatus
		wantErr string
	}{
		{name: "finishes after polling", states: []TaskStatus{running, running, {State: TaskOK, ExitStatus: "OK"}}},
		{name: "failed task", states: []TaskStatus{running, {State: TaskError, ExitStatus: "shutdown timeout"}}, wantErr: "shutdown timeout"},
		{name: "never finishes", states: []TaskStatus{running}, wantErr: "did not complete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &taskSeq{states: tt.states}
			err := WaitTask(context.Background(), c, "UPID:test", time.Millisecond, 50*time.Millisecond)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("WaitTask: %v", err)
				}
				if c.calls != len(tt.states) {
					t.Fatalf("polled %d times, want %d", c.calls, len(tt.states))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}
