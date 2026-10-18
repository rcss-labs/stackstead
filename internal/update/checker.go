package update

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/stackstead/internal/apitypes"
)

const (
	initialDelay    = 10 * time.Second
	successInterval = 24 * time.Hour
	failureInterval = time.Hour
	minForcedGap    = 60 * time.Second

	maxCommandBytes = 512
)

// CheckerOptions configures a Checker. Now and After are test seams and
// default to the real clock.
type CheckerOptions struct {
	Source    *Source
	Enabled   bool
	Current   string
	Kind      Kind
	GOOS, Exe string
	Now       func() time.Time
	After     func(time.Duration) <-chan time.Time
	Logger    *slog.Logger
}

// Checker looks for a newer release once a day and caches the answer, so the
// UI gets an instant, offline-safe status and GitHub sees one small request.
type Checker struct {
	opts      CheckerOptions
	current   string
	isRelease bool

	checkMu sync.Mutex // one check at a time; held across the network call

	mu            sync.Mutex // guards the fields below; never held across the network
	status        apitypes.UpdateStatus
	haveSuccess   bool
	lastAttempt   time.Time
	loggedVersion string
	failing       bool
}

func NewChecker(o CheckerOptions) *Checker {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.After == nil {
		o.After = time.After
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	c := &Checker{opts: o, current: strings.TrimPrefix(o.Current, "v")}
	_, err := Parse(c.current)
	c.isRelease = err == nil

	command := UpgradeCommand(o.Kind, o.GOOS, o.Exe)
	if len(command) > maxCommandBytes || !printableASCII(command) {
		command = ""
	}
	c.status = apitypes.UpdateStatus{Current: c.current, Installer: string(o.Kind), Command: command}
	c.refreshState()
	return c
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// active reports whether the checker ever talks to the network.
func (c *Checker) active() bool { return c.opts.Enabled && c.isRelease && c.opts.Source != nil }

// refreshState derives State from the facts; callers hold mu (or are constructing).
func (c *Checker) refreshState() {
	switch {
	case !c.opts.Enabled:
		c.status.State = "disabled"
	case !c.isRelease:
		c.status.State = "unknown"
	case c.haveSuccess:
		c.status.State = "up-to-date"
		if c.newerThanCurrent() {
			c.status.State = "available"
		}
	case c.status.Error != "":
		c.status.State = "failed"
	default:
		c.status.State = "unknown"
	}
}

func (c *Checker) newerThanCurrent() bool {
	cur, err1 := Parse(c.current)
	latest, err2 := Parse(c.status.Latest)
	return err1 == nil && err2 == nil && Compare(latest, cur) > 0
}

// Run checks 10 s after start, then every 24 h (1 h after a failure) until ctx ends.
// It returns at once when the checker is disabled or this is a dev build.
func (c *Checker) Run(ctx context.Context) {
	if !c.active() {
		return
	}
	delay := initialDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.opts.After(delay):
		}
		c.checkMu.Lock()
		ok := c.check(ctx)
		c.checkMu.Unlock()
		delay = successInterval
		if !ok {
			delay = failureInterval
		}
	}
}

// Status is the cached status; it never touches the network.
func (c *Checker) Status() apitypes.UpdateStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// CheckNow runs one check unless the last attempt was under 60 s ago, in
// which case it answers the cached status. Concurrent callers share one check.
func (c *Checker) CheckNow(ctx context.Context) apitypes.UpdateStatus {
	if !c.active() {
		return c.Status()
	}
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	c.mu.Lock()
	recent := !c.lastAttempt.IsZero() && c.opts.Now().Sub(c.lastAttempt) < minForcedGap
	c.mu.Unlock()
	if recent {
		return c.Status()
	}
	c.check(ctx)
	return c.Status()
}

// check performs one fetch and records the outcome; callers hold checkMu.
func (c *Checker) check(ctx context.Context) bool {
	rel, err := c.opts.Source.Latest(ctx)
	now := c.opts.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastAttempt = now
	if err != nil {
		code := ErrorCode(err)
		if code == "" {
			code = "network"
		}
		c.status.Error = code
		if c.failing {
			c.opts.Logger.Debug("update check failed", "error", err)
		} else {
			c.opts.Logger.Warn("update check failed", "error", err)
		}
		c.failing = true
		c.refreshState()
		return false
	}
	c.failing = false
	c.haveSuccess = true
	c.status.Latest = rel.Version.String()
	c.status.NotesURL = rel.NotesURL()
	c.status.CheckedAt = now
	c.status.Error = ""
	c.refreshState()
	if c.status.State == "available" && c.loggedVersion != c.status.Latest {
		c.loggedVersion = c.status.Latest
		c.opts.Logger.Info("update available", "version", c.status.Latest, "current", c.current)
	}
	return true
}
