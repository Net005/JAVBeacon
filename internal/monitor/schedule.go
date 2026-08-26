package monitor

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tues": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

func parseWeekdays(raw string) (map[time.Weekday]bool, error) {
	out := map[time.Weekday]bool{}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*" || strings.EqualFold(raw, "daily") {
		return out, nil
	}
	for _, item := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(item))
		day, ok := weekdayNames[name]
		if !ok {
			if n, err := strconv.Atoi(name); err == nil && n >= 0 && n <= 6 {
				day, ok = time.Weekday(n), true
			}
		}
		if !ok {
			return nil, fmt.Errorf("invalid weekday %q", item)
		}
		out[day] = true
	}
	return out, nil
}

// ValidateCalendarSchedule validates the optional local start time and weekday
// list used by scheduled scrapes. An empty weekday list means every day.
func ValidateCalendarSchedule(startTime, weekdays string) error {
	if strings.TrimSpace(startTime) != "" {
		if _, err := time.Parse("15:04", strings.TrimSpace(startTime)); err != nil {
			return fmt.Errorf("start time must use HH:MM")
		}
	}
	_, err := parseWeekdays(weekdays)
	return err
}

type cronField struct{ allowed map[int]bool }

func parseCronField(raw string, min, max int, names map[string]int) (cronField, error) {
	field := cronField{allowed: map[int]bool{}}
	for _, part := range strings.Split(strings.ToLower(strings.TrimSpace(raw)), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return field, errorsNewCron(raw)
		}
		base, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			var err error
			step, err = strconv.Atoi(stepText)
			if err != nil || step <= 0 {
				return field, errorsNewCron(raw)
			}
		}
		start, end := min, max
		if base != "*" {
			left, right, ranged := strings.Cut(base, "-")
			parse := func(value string) (int, bool) {
				if n, ok := names[value]; ok {
					return n, true
				}
				n, err := strconv.Atoi(value)
				return n, err == nil
			}
			var ok bool
			start, ok = parse(left)
			if !ok {
				return field, errorsNewCron(raw)
			}
			end = start
			if ranged {
				end, ok = parse(right)
				if !ok {
					return field, errorsNewCron(raw)
				}
			}
		}
		if start < min || end > max || start > end {
			return field, errorsNewCron(raw)
		}
		for n := start; n <= end; n += step {
			field.allowed[n] = true
		}
	}
	return field, nil
}

func errorsNewCron(raw string) error { return fmt.Errorf("invalid cron field %q", raw) }

type parsedCron struct {
	minute, hour, day, month, weekday cronField
	dayWildcard, weekdayWildcard      bool
}

func parseCron(raw string) (parsedCron, error) {
	parts := strings.Fields(raw)
	if len(parts) != 5 {
		return parsedCron{}, fmt.Errorf("cron must contain five fields: minute hour day-of-month month day-of-week")
	}
	monthNames := map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dayNames := map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
	fields := []struct {
		raw      string
		min, max int
		names    map[string]int
	}{{parts[0], 0, 59, nil}, {parts[1], 0, 23, nil}, {parts[2], 1, 31, nil}, {parts[3], 1, 12, monthNames}, {parts[4], 0, 6, dayNames}}
	result := parsedCron{}
	result.dayWildcard = parts[2] == "*"
	result.weekdayWildcard = parts[4] == "*"
	destinations := []*cronField{&result.minute, &result.hour, &result.day, &result.month, &result.weekday}
	for i, spec := range fields {
		field, err := parseCronField(spec.raw, spec.min, spec.max, spec.names)
		if err != nil {
			return parsedCron{}, fmt.Errorf("cron: %w", err)
		}
		*destinations[i] = field
	}
	return result, nil
}

func (c parsedCron) matches(now time.Time) bool {
	dayMatch, weekdayMatch := c.day.allowed[now.Day()], c.weekday.allowed[int(now.Weekday())]
	calendarMatch := dayMatch && weekdayMatch
	if !c.dayWildcard && !c.weekdayWildcard {
		calendarMatch = dayMatch || weekdayMatch
	}
	return c.minute.allowed[now.Minute()] && c.hour.allowed[now.Hour()] && c.month.allowed[int(now.Month())] && calendarMatch
}

// ValidateCronSchedule accepts an empty expression (calendar/interval mode)
// or a standard five-field cron expression.
func ValidateCronSchedule(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	_, err := parseCron(raw)
	return err
}

// calendarForecastHorizon bounds how far into the future nextCalendarRuns
// simulates before giving up on finding enough matches - a var (not a
// const) purely so tests can shrink it instead of running a slow ~1.4M
// minute-by-minute scan for edge-case schedules (e.g. weekday+time
// combinations that only occur a handful of times a year).
var calendarForecastHorizon = 400 * 24 * time.Hour

// nextCalendarRuns simulates a calendar-mode schedule (cron expression, or
// start-time/weekdays) forward minute by minute from now and returns up to
// count future match times within calendarForecastHorizon. It returns fewer
// than count (possibly none) if the schedule is invalid or the horizon is
// exhausted before enough matches are found - callers should treat a short
// or empty result as "no upcoming run currently predictable" rather than an
// error.
func nextCalendarRuns(now time.Time, startTime, weekdays, cronText string, count int) []time.Time {
	if count <= 0 {
		return nil
	}
	runs := make([]time.Time, 0, count)
	cursor := now.Truncate(time.Minute).Add(time.Minute)
	deadline := now.Add(calendarForecastHorizon)
	for cursor.Before(deadline) && len(runs) < count {
		matches, err := calendarScheduleMatches(cursor, startTime, weekdays, cronText)
		if err != nil {
			return runs
		}
		if matches {
			runs = append(runs, cursor)
		}
		cursor = cursor.Add(time.Minute)
	}
	return runs
}

func calendarScheduleMatches(now time.Time, startTime, weekdays, cronText string) (bool, error) {
	if strings.TrimSpace(cronText) != "" {
		cron, err := parseCron(cronText)
		return err == nil && cron.matches(now), err
	}
	if strings.TrimSpace(startTime) == "" {
		return false, nil
	}
	start, err := time.Parse("15:04", strings.TrimSpace(startTime))
	if err != nil {
		return false, err
	}
	days, err := parseWeekdays(weekdays)
	if err != nil {
		return false, err
	}
	return now.Hour() == start.Hour() && now.Minute() == start.Minute() && (len(days) == 0 || days[now.Weekday()]), nil
}
