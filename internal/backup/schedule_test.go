package backup

import (
	"testing"
	"time"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestNextRunDaily(t *testing.T) {
	s := Schedule{Enabled: true, Frequency: frequencyDaily, Time: "03:00"}
	loc := time.UTC

	got, ok := NextRun(s, time.Date(2026, 10, 26, 2, 59, 0, 0, loc), loc)
	if want := time.Date(2026, 10, 26, 3, 0, 0, 0, loc); !ok || !got.Equal(want) {
		t.Errorf("at 02:59: NextRun = %v, %v; want %v", got, ok, want)
	}

	got, ok = NextRun(s, time.Date(2026, 10, 26, 3, 0, 0, 0, loc), loc)
	if want := time.Date(2026, 10, 27, 3, 0, 0, 0, loc); !ok || !got.Equal(want) {
		t.Errorf("at 03:00: NextRun = %v, %v; want %v", got, ok, want)
	}
}

func TestNextRunWeekly(t *testing.T) {
	s := Schedule{Enabled: true, Frequency: frequencyWeekly, Weekday: time.Monday, Time: "03:00"}
	loc := time.UTC

	got, ok := NextRun(s, time.Date(2026, 10, 26, 3, 0, 0, 0, loc), loc)
	if want := time.Date(2026, 11, 2, 3, 0, 0, 0, loc); !ok || !got.Equal(want) {
		t.Errorf("Monday 03:00: NextRun = %v, %v; want %v", got, ok, want)
	}
}

func TestNextRunDST(t *testing.T) {
	berlin := mustLocation(t, "Europe/Berlin")
	s := Schedule{Enabled: true, Frequency: frequencyDaily, Time: "02:30"}

	var runs []time.Time
	cursor := time.Date(2026, 10, 24, 12, 0, 0, 0, berlin)
	for i := 0; i < 5; i++ {
		next, ok := NextRun(s, cursor, berlin)
		if !ok {
			t.Fatal("NextRun returned false for an enabled schedule")
		}
		runs = append(runs, next)
		cursor = next
	}

	counts := map[int]int{}
	for _, r := range runs {
		local := r.In(berlin)
		if local.Month() == time.October && (local.Day() == 25 || local.Day() == 26) {
			counts[local.Day()]++
		}
	}
	if counts[25] != 1 || counts[26] != 1 {
		t.Errorf("runs on 25th/26th = %v, want one each; runs = %v", counts, runs)
	}
}

func TestNextRunDisabled(t *testing.T) {
	s := Schedule{Enabled: false, Frequency: frequencyDaily, Time: "03:00"}
	if _, ok := NextRun(s, time.Now(), time.UTC); ok {
		t.Error("NextRun ok = true for a disabled schedule, want false")
	}
}
