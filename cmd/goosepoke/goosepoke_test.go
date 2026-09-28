package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func newTestPinger(t *testing.T, o options, target string) *pinger {
	t.Helper()
	o.URL = target
	return &pinger{client: newClient(o), url: target, userAgent: "watchgoose-poke/1"}
}

// The first poke happens immediately, not one interval later. This is what
// lets someone verify a fresh install without waiting five minutes.
func TestRunPokesImmediately(t *testing.T) {
	hits := make(chan time.Time, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/reassure" {
			t.Errorf("path = %q, want /reassure", r.URL.Path)
		}
		hits <- time.Now()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// A long interval: only the immediate poke can land in the test window.
	opts := options{Interval: time.Hour, Timeout: 5 * time.Second}
	p := newTestPinger(t, opts, srv.URL+"/reassure")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, p, opts, discardLogger()) }()

	select {
	case <-hits:
	case <-time.After(2 * time.Second):
		t.Fatal("no immediate poke")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned %v, want nil on cancellation", err)
	}
}

// After the immediate poke, subsequent pokes arrive on the interval.
func TestRunInterval(t *testing.T) {
	var (
		mu    sync.Mutex
		count int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	opts := options{Interval: 20 * time.Millisecond, Timeout: 2 * time.Second}
	p := newTestPinger(t, opts, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, p, opts, discardLogger()) }()

	// Wait for the immediate poke plus at least two on the interval.
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := count
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("got %d pokes in 3s at a 20ms interval, want at least 3", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned %v, want nil", err)
	}
}

// A failing server must not end the loop: the client is the only thing keeping
// the machine from rebooting itself.
func TestRunKeepsGoingThroughFailures(t *testing.T) {
	var (
		mu       sync.Mutex
		attempts int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		http.Error(w, "not today", http.StatusInternalServerError)
	}))
	defer srv.Close()

	opts := options{Interval: 15 * time.Millisecond, Timeout: time.Second}
	p := newTestPinger(t, opts, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, p, opts, discardLogger()) }()

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := attempts
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("stopped retrying after %d attempts; a transient failure must not end the loop", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned %v, want nil: failures must not be fatal", err)
	}
}

// -once exits non-zero when the poke failed, so a cron job or monitoring check
// can act on the result.
func TestOnceExitStatus(t *testing.T) {
	var hits int32
	var mu sync.Mutex
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()

	opts := options{Interval: time.Hour, Timeout: 2 * time.Second, Once: true}
	if err := run(context.Background(), newTestPinger(t, opts, ok.URL), opts, discardLogger()); err != nil {
		t.Fatalf("run on a healthy endpoint = %v, want nil", err)
	}
	mu.Lock()
	n := hits
	mu.Unlock()
	if n != 1 {
		t.Errorf("got %d pokes in -once mode, want exactly 1", n)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer bad.Close()

	if err := run(context.Background(), newTestPinger(t, opts, bad.URL), opts, discardLogger()); err == nil {
		t.Error("run against a 503 = nil, want an error so the exit status is non-zero")
	}
}

// A non-2xx is a failure, not a success: only a 2xx means the arrival was
// recorded.
func TestNonSuccessStatusIsAnError(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		opts := options{Interval: time.Hour, Timeout: 2 * time.Second, Once: true}
		if err := run(context.Background(), newTestPinger(t, opts, srv.URL), opts, discardLogger()); err == nil {
			t.Errorf("status %d: run = nil, want an error", code)
		}
		srv.Close()
	}
}

// The request must carry nothing but the arrival: no body, no auth header.
func TestPokeSendsNoCredentials(t *testing.T) {
	got := make(chan *http.Request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Clone(context.Background())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	opts := options{Interval: time.Hour, Timeout: 2 * time.Second, Once: true}
	if err := run(context.Background(), newTestPinger(t, opts, srv.URL), opts, discardLogger()); err != nil {
		t.Fatalf("run = %v", err)
	}
	r := <-got
	if r.ContentLength > 0 {
		t.Errorf("content-length = %d, want 0", r.ContentLength)
	}
	if r.Header.Get("Authorization") != "" {
		t.Error("request carried an Authorization header")
	}
	if r.Header.Get("User-Agent") == "" {
		t.Error("request had no User-Agent")
	}
}

// A hung server must not wedge the loop; the per-request timeout bounds it.
func TestTimeoutBoundsAHungRequest(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	// Close the server's wait first, then release the handler, then let
	// httptest tear down: srv.Close blocks until handlers return.
	defer srv.Close()
	defer close(block)

	opts := options{Interval: time.Hour, Timeout: 150 * time.Millisecond, Once: true}
	start := time.Now()
	err := run(context.Background(), newTestPinger(t, opts, srv.URL), opts, discardLogger())
	if err == nil {
		t.Error("run against a hung server = nil, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %s to give up on a 150ms timeout", elapsed)
	}
}
