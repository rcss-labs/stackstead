package api

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A closed input channel must be handled once, not spun on: the loop keeps
// heartbeating until the request ends.
func TestRunSSEDoesNotSpinOnClosedChannel(t *testing.T) {
	s := &server{opts: Options{SSEHeartbeat: 10 * time.Millisecond}}
	rec := httptest.NewRecorder()
	sw, err := startSSE(rec, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	in := make(chan string)
	close(in)
	req := httptest.NewRequest("GET", "/", nil)
	ctx, cancel := context.WithTimeout(req.Context(), 100*time.Millisecond)
	defer cancel()
	var closedCalls atomic.Int32
	runSSE(s, req.WithContext(ctx), sw, in, func(string, bool) (bool, error) {
		closedCalls.Add(1)
		return false, nil
	})
	if n := closedCalls.Load(); n != 1 {
		t.Errorf("handle called %d times for a closed channel, want 1", n)
	}
}
