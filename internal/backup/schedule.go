package backup

import "time"

// scheduleSearchDays bounds the day-by-day search in NextRun. A weekly
// schedule matches within 7 days and a daily one within 1, so this is slack.
const scheduleSearchDays = 14

// NextRun returns the first scheduled time strictly after `after`, computed on
// the calendar in loc so that daylight-saving changes keep the wall-clock time.
// It returns false when the schedule is disabled or its frequency or time is invalid.
func NextRun(s Schedule, after time.Time, loc *time.Location) (time.Time, bool) {
	if !s.Enabled {
		return time.Time{}, false
	}
	if s.Frequency != frequencyDaily && s.Frequency != frequencyWeekly {
		return time.Time{}, false
	}
	clock, err := time.Parse("15:04", s.Time)
	if err != nil {
		return time.Time{}, false
	}

	local := after.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < scheduleSearchDays; i++ {
		if s.Frequency == frequencyWeekly && day.Weekday() != s.Weekday {
			day = nextDay(day, loc)
			continue
		}
		candidate := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
		if candidate.After(after) {
			return candidate, true
		}
		day = nextDay(day, loc)
	}
	return time.Time{}, false
}

// nextDay steps by calendar day rather than by 24 hours, so a day that is 23 or
// 25 hours long does not shift the date.
func nextDay(day time.Time, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc)
}
