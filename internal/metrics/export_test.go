package metrics

import "time"

// UseTicks replaces the interval ticker with a manually driven channel.
func (s *Sampler) UseTicks(ch <-chan time.Time) { s.tickOverride = ch }

// OnTickDone registers a callback invoked after each tick body completes.
func (s *Sampler) OnTickDone(f func()) { s.tickDone = f }
