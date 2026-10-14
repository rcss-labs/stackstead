package metrics

import "time"

// UseTicks replaces the interval ticker with a manually driven channel.
func (s *Sampler) UseTicks(ch <-chan time.Time) { s.tickOverride = ch }

// OnTickDone registers a callback invoked after each tick body completes.
func (s *Sampler) OnTickDone(f func()) { s.tickDone = f }

// UseStore replaces the sample store with a fake.
func (s *Sampler) UseStore(st sampleStore) { s.store = st }

// StatsTimeout exposes the per-container stats timeout for an interval.
func StatsTimeout(interval time.Duration) time.Duration { return statsTimeout(interval) }
