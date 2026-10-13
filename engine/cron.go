package engine

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	// Embed the IANA database so schedule timezones resolve even on hosts
	// without /usr/share/zoneinfo (e.g. scratch/distroless containers).
	_ "time/tzdata"
)

// This file is a port of the parts of cron-parser (v5, the library OpenWorkflow
// uses for recurring schedules) that the backend needs: parsing an expression
// and finding the first occurrence strictly after an instant. The server
// computes next_run_at itself, so its occurrences must match what the
// OpenWorkflow client and its own SQLite/Postgres backends would compute,
// including cron-parser's DST quirks. The port is deliberately literal (same
// steps, same luxon date arithmetic) rather than idiomatic, and is pinned by a
// golden corpus generated from cron-parser itself (testdata/cron_golden.json).

// errInvalidSchedule wraps every cron/timezone validation failure.
var errInvalidSchedule = fmt.Errorf("invalid schedule: %w", ErrBadRequest)

// cronLoopLimit mirrors cron-parser's LOOP_LIMIT.
const cronLoopLimit = 10000

// cronMaxFieldValues mirrors cron-parser's MAX_FIELD_VALUES.
const cronMaxFieldValues = 256

// daysInMonthMax mirrors cron-parser's DAYS_IN_MONTH (February as 29).
var daysInMonthMax = [12]int{31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

var (
	// hashedValuePattern is OpenWorkflow's /\bH\b/i check: schedules must not use
	// cron-parser's randomized H values.
	hashedValuePattern = regexp.MustCompile(`(?i)\bH\b`)
	aliasPattern       = regexp.MustCompile(`(?i)[a-z]{3}`)
	nthSeparators      = regexp.MustCompile(`[,\-/]`)

	monthAliases = map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}
	dayOfWeekAliases = map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}
)

type cronUnit int

const (
	unitSecond cronUnit = iota
	unitMinute
	unitHour
	unitDayOfMonth
	unitMonth
	unitDayOfWeek
)

type cronConstraints struct {
	min, max   int
	chars      []string // special characters allowed as values ("L")
	validChars *regexp.Regexp
}

// constraints mirror the cron-parser field classes, minus the H syntax that
// OpenWorkflow rejects up front.
var cronFieldConstraints = map[cronUnit]cronConstraints{
	unitSecond:     {0, 59, nil, regexp.MustCompile(`^[?,*\d/-]+$`)},
	unitMinute:     {0, 59, nil, regexp.MustCompile(`^[?,*\d/-]+$`)},
	unitHour:       {0, 23, nil, regexp.MustCompile(`^[?,*\d/-]+$`)},
	unitDayOfMonth: {1, 31, []string{"L"}, regexp.MustCompile(`^[?,*\dL/-]+$`)},
	unitMonth:      {1, 12, nil, regexp.MustCompile(`^[?,*\d/-]+$`)},
	unitDayOfWeek:  {0, 7, []string{"L"}, regexp.MustCompile(`^[?,*\dL#/-]+$`)},
}

// cronValue is one parsed field value: a number, or a special string such as
// "L" (day of month) or "5L" (last Friday).
type cronValue struct {
	num   int
	str   string
	isStr bool
}

// cronField is a parsed field. nums holds the numeric values in ascending
// order; values holds every value in cron-parser's sort order (numbers first).
type cronField struct {
	values   []cronValue
	nums     []int
	wildcard bool
	hasLast  bool
}

func (f *cronField) has(v int) bool {
	i := sort.SearchInts(f.nums, v)
	return i < len(f.nums) && f.nums[i] == v
}

// nextAfter is cron-parser's findNearestValue(current, false).
func (f *cronField) nextAfter(v int) (int, bool) {
	for _, n := range f.nums {
		if n > v {
			return n, true
		}
	}
	return 0, false
}

func (f *cronField) first() int { return f.nums[0] }

// cronSchedule is a parsed expression bound to its timezone.
type cronSchedule struct {
	second, minute, hour, dayOfMonth, month, dayOfWeek cronField
	nthDayOfWeek                                       int
	loc                                                *time.Location
}

// loadScheduleLocation resolves an IANA timezone, rejecting the host-dependent
// "Local" zone just as OpenWorkflow (via Intl) rejects "local"/"system".
func loadScheduleLocation(timezone string) (*time.Location, error) {
	if timezone == "" || strings.EqualFold(timezone, "local") {
		return nil, fmt.Errorf("unknown timezone %q: %w", timezone, errInvalidSchedule)
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %w", timezone, errInvalidSchedule)
	}
	return loc, nil
}

// parseSchedule validates a cron expression and timezone the way
// OpenWorkflow's parseSchedule does (five or six fields, no randomized H
// values) and parses it with cron-parser's rules.
func parseSchedule(cron, timezone string) (*cronSchedule, error) {
	atoms := strings.Fields(cron)
	if (len(atoms) != 5 && len(atoms) != 6) || hashedValuePattern.MatchString(cron) {
		return nil, fmt.Errorf("schedules require five or six cron fields without randomized values: %w", errInvalidSchedule)
	}
	loc, err := loadScheduleLocation(timezone)
	if err != nil {
		return nil, err
	}
	if len(atoms) == 5 {
		atoms = append([]string{"0"}, atoms...)
	}
	s, err := parseCronFields(atoms)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", err.Error(), errInvalidSchedule)
	}
	s.loc = loc
	return s, nil
}

func parseCronFields(raw []string) (*cronSchedule, error) {
	s := &cronSchedule{}
	var err error
	if s.second, err = parseCronField(unitSecond, raw[0], raw[0]); err != nil {
		return nil, err
	}
	if s.minute, err = parseCronField(unitMinute, raw[1], raw[1]); err != nil {
		return nil, err
	}
	if s.hour, err = parseCronField(unitHour, raw[2], raw[2]); err != nil {
		return nil, err
	}
	if s.month, err = parseCronField(unitMonth, raw[4], raw[4]); err != nil {
		return nil, err
	}
	monthValues := s.month.values

	domValues, err := parseCronValues(unitDayOfMonth, raw[3])
	if err != nil {
		return nil, err
	}
	// CronDayOfMonth.fromMonth: with a single month, drop days it never has.
	if len(monthValues) == 1 {
		dim := daysInMonthMax[monthValues[0].num-1]
		kept := []cronValue{}
		for _, v := range domValues {
			if v.isStr || v.num <= dim {
				kept = append(kept, v)
			}
		}
		if len(kept) > 0 {
			domValues = kept
		}
	}
	if s.dayOfMonth, err = newCronField(unitDayOfMonth, domValues, raw[3]); err != nil {
		return nil, err
	}

	dowExpr, nth, err := parseNthDay(raw[5])
	if err != nil {
		return nil, err
	}
	if s.dayOfWeek, err = parseCronField(unitDayOfWeek, dowExpr, raw[5]); err != nil {
		return nil, err
	}
	for _, v := range s.dayOfWeek.values {
		if v.isStr && v.str == "L" {
			return nil, errors.New("unexpected standalone L in day of week")
		}
	}
	s.nthDayOfWeek = nth

	// CronFieldCollection: an explicit day of month that can never occur.
	if len(monthValues) == 1 && !s.dayOfMonth.hasLast && s.dayOfWeek.wildcard {
		first, ok := jsParseInt(cronValueString(s.dayOfMonth.values[0]))
		if !ok || first > daysInMonthMax[monthValues[0].num-1] {
			return nil, errors.New("invalid explicit day of month definition")
		}
	}
	return s, nil
}

// parseNthDay splits a day-of-week "#n" suffix (e.g. "5#3", the third Friday).
func parseNthDay(val string) (string, int, error) {
	atoms := strings.Split(val, "#")
	if len(atoms) <= 1 {
		return atoms[0], 0, nil
	}
	if nthSeparators.MatchString(val) {
		return "", 0, errors.New("day of week # is incompatible with , - and /")
	}
	nth, err := strconv.ParseFloat(atoms[len(atoms)-1], 64)
	if len(atoms) > 2 || err != nil || nth != math.Trunc(nth) || nth < 1 || nth > 5 {
		return "", 0, errors.New("invalid day of week occurrence number (#)")
	}
	return atoms[0], int(nth), nil
}

func parseCronField(unit cronUnit, value, rawValue string) (cronField, error) {
	values, err := parseCronValues(unit, value)
	if err != nil {
		return cronField{}, err
	}
	return newCronField(unit, values, rawValue)
}

// parseCronValues is CronExpressionParser.#parseField.
func parseCronValues(unit cronUnit, value string) ([]cronValue, error) {
	c := cronFieldConstraints[unit]
	if unit == unitMonth || unit == unitDayOfWeek {
		var aliasErr error
		value = aliasPattern.ReplaceAllStringFunc(value, func(match string) string {
			lower := strings.ToLower(match)
			if n, ok := monthAliases[lower]; ok {
				return strconv.Itoa(n)
			}
			if n, ok := dayOfWeekAliases[lower]; ok {
				return strconv.Itoa(n)
			}
			aliasErr = fmt.Errorf("cannot resolve alias %q", lower)
			return match
		})
		if aliasErr != nil {
			return nil, aliasErr
		}
	}
	if !c.validChars.MatchString(value) {
		return nil, fmt.Errorf("invalid characters, got value: %s", value)
	}
	wildcard := fmt.Sprintf("%d-%d", c.min, c.max)
	value = strings.NewReplacer("*", wildcard, "?", wildcard).Replace(value)

	var stack []cronValue
	for _, atom := range strings.Split(value, ",") {
		if len(stack) > cronMaxFieldValues {
			return nil, fmt.Errorf("too many values, expected at most %d", cronMaxFieldValues)
		}
		if atom == "" {
			return nil, errors.New("invalid list value format")
		}
		rng, single, err := parseCronRepeat(unit, atom, c)
		if err != nil {
			return nil, err
		}
		if rng != nil {
			for _, n := range rng {
				stack = append(stack, cronValue{num: n})
			}
			continue
		}
		if single.isStr && hasConstraintChar(c, single.str) {
			stack = append(stack, single)
			continue
		}
		v, ok := jsParseInt(cronValueString(single))
		if !ok || v < c.min || v > c.max {
			return nil, fmt.Errorf("got value %s, expected range %d-%d", cronValueString(single), c.min, c.max)
		}
		if unit == unitDayOfWeek {
			v %= 7
		}
		stack = append(stack, cronValue{num: v})
	}
	return stack, nil
}

func parseCronRepeat(unit cronUnit, val string, c cronConstraints) ([]int, cronValue, error) {
	atoms := strings.Split(val, "/")
	if len(atoms) > 2 {
		return nil, cronValue{}, fmt.Errorf("invalid repeat: %s", val)
	}
	if len(atoms) == 2 {
		if !strings.Contains(atoms[0], "-") {
			atoms[0] = fmt.Sprintf("%s-%d", atoms[0], c.max)
		}
		step, ok := jsParseInt(atoms[1])
		return parseCronRange(unit, atoms[0], step, ok, c)
	}
	return parseCronRange(unit, val, 1, true, c)
}

// parseCronRange returns either an expanded range or a single value.
func parseCronRange(unit cronUnit, val string, step int, stepOK bool, c cronConstraints) ([]int, cronValue, error) {
	atoms := strings.Split(val, "-")
	if len(atoms) <= 1 {
		// JS `isNaN(+val) ? val : +val`; valid characters leave only digits or
		// strings with L/#.
		if n, err := strconv.Atoi(val); err == nil && !strings.HasPrefix(val, "+") {
			return nil, cronValue{num: n}, nil
		}
		return nil, cronValue{str: val, isStr: true}, nil
	}
	lo, okLo := jsParseInt(atoms[0])
	hi, okHi := jsParseInt(atoms[1])
	if !okLo || !okHi || lo < c.min || hi > c.max {
		return nil, cronValue{}, fmt.Errorf("got range %s, expected range %d-%d", val, c.min, c.max)
	}
	if lo > hi {
		return nil, cronValue{}, fmt.Errorf("invalid range: %d-%d, min > max", lo, hi)
	}
	if !stepOK || step <= 0 {
		return nil, cronValue{}, fmt.Errorf("cannot repeat at every %d time", step)
	}
	var out []int
	if unit == unitDayOfWeek && hi%7 == 0 && (hi-lo)%step == 0 {
		out = append(out, 0)
	}
	for i := lo; i <= hi; i += step {
		if !containsInt(out, i) {
			out = append(out, i)
		}
	}
	return out, cronValue{}, nil
}

// newCronField is the CronField constructor plus validate().
func newCronField(unit cronUnit, values []cronValue, rawValue string) (cronField, error) {
	c := cronFieldConstraints[unit]
	if len(values) == 0 {
		return cronField{}, errors.New("field contains no values")
	}
	sorted := append([]cronValue(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if !a.isStr && !b.isStr {
			return a.num < b.num
		}
		if a.isStr && b.isStr {
			return a.str < b.str
		}
		return !a.isStr
	})

	f := cronField{values: sorted, wildcard: rawValue == "*" || rawValue == "?"}
	f.hasLast = strings.Contains(rawValue, "L")
	seen := map[cronValue]bool{}
	for _, v := range sorted {
		if v.isStr {
			if v.str == "L" {
				f.hasLast = true
			}
			if !matchesConstraintChar(c, v.str) {
				return cronField{}, fmt.Errorf("got value %s, expected range %d-%d", v.str, c.min, c.max)
			}
		} else {
			if v.num < c.min || v.num > c.max {
				return cronField{}, fmt.Errorf("got value %d, expected range %d-%d", v.num, c.min, c.max)
			}
			f.nums = append(f.nums, v.num)
		}
		if seen[v] {
			return cronField{}, fmt.Errorf("duplicate values found: %s", cronValueString(v))
		}
		seen[v] = true
	}
	return f, nil
}

func hasConstraintChar(c cronConstraints, s string) bool {
	for _, ch := range c.chars {
		if strings.Contains(s, ch) {
			return true
		}
	}
	return false
}

// matchesConstraintChar is validate()'s /^\d{0,2}<char>$/ test.
func matchesConstraintChar(c cronConstraints, s string) bool {
	for _, ch := range c.chars {
		prefix, ok := strings.CutSuffix(s, ch)
		if !ok || len(prefix) > 2 {
			continue
		}
		digits := true
		for _, r := range prefix {
			if r < '0' || r > '9' {
				digits = false
			}
		}
		if digits {
			return true
		}
	}
	return false
}

func cronValueString(v cronValue) string {
	if v.isStr {
		return v.str
	}
	return strconv.Itoa(v.num)
}

// jsParseInt mirrors JavaScript's parseInt(s, 10) for the inputs a cron field
// can contain: an optional sign and leading digits, ignoring any trailing text.
func jsParseInt(s string) (int, bool) {
	s = strings.TrimLeft(s, " \t\n\r\f\v")
	sign := 1
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		if s[0] == '-' {
			sign = -1
		}
		s = s[1:]
	}
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0, false
	}
	return sign * n, true
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// --- occurrence search (CronExpression.#findSchedule, forward only) ---

// nextScheduleOccurrence returns the first occurrence of cron in timezone
// strictly after the given instant (OpenWorkflow's nextScheduleOccurrence).
func nextScheduleOccurrence(cron, timezone string, after time.Time) (time.Time, error) {
	s, err := parseSchedule(cron, timezone)
	if err != nil {
		return time.Time{}, err
	}
	return s.next(after)
}

func (s *cronSchedule) next(after time.Time) (time.Time, error) {
	d := newCronDate(after.UnixMilli(), s.loc)
	hoursLength := len(s.hour.nums)
	start := d.ms
	if d.wall().ms > 0 {
		d.set(dateFields{ms: intp(0)})
		d.applyAdd(unitSecond, hoursLength)
	}
	for step := 1; step < cronLoopLimit; step++ {
		w := d.wall()
		if !s.matchDayOfMonth(w) {
			d.applyAdd(unitDayOfMonth, hoursLength)
			continue
		}
		if !s.month.has(w.month) {
			d.applyAdd(unitMonth, hoursLength)
			continue
		}
		if !s.matchHour(d) {
			continue
		}
		w = d.wall()
		if !s.minute.has(w.minute) {
			s.moveToNextMinute(d, w.minute)
			continue
		}
		if !s.second.has(w.second) {
			s.moveToNextSecond(d, w.second)
			continue
		}
		if d.ms == start {
			d.applyAdd(unitSecond, hoursLength)
			continue
		}
		return time.UnixMilli(d.ms).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid expression, loop limit exceeded: %w", errInvalidSchedule)
}

func (s *cronSchedule) moveToNextSecond(d *cronDate, current int) {
	if next, ok := s.second.nextAfter(current); ok {
		d.set(dateFields{second: intp(next)})
		return
	}
	d.applyAdd(unitMinute, len(s.hour.nums))
	d.set(dateFields{second: intp(s.second.first())})
}

func (s *cronSchedule) moveToNextMinute(d *cronDate, current int) {
	if next, ok := s.minute.nextAfter(current); ok {
		d.set(dateFields{minute: intp(next)})
		d.set(dateFields{second: intp(s.second.first())})
		return
	}
	d.applyAdd(unitHour, len(s.hour.nums))
	d.set(dateFields{minute: intp(s.minute.first())})
	d.set(dateFields{second: intp(s.second.first())})
}

// matchDayOfMonth applies cron-parser's day-of-month/day-of-week rules: when
// both are restricted either may match, otherwise the restricted one must.
func (s *cronSchedule) matchDayOfMonth(w wallTime) bool {
	domRestricted := !s.dayOfMonth.wildcard
	dowRestricted := !s.dayOfWeek.wildcard

	matchedDOM := s.dayOfMonth.has(w.day) || (s.dayOfMonth.hasLast && isLastDayOfMonth(w))
	weekday := w.weekday
	matchedDOW := (s.dayOfWeek.has(weekday) && (s.nthDayOfWeek <= 0 || (w.day+6)/7 == s.nthDayOfWeek)) ||
		(s.dayOfWeek.hasLast && s.isLastWeekdayOfMonthMatch(w))

	if domRestricted && dowRestricted && (matchedDOM || matchedDOW) {
		return true
	}
	if matchedDOM && !dowRestricted {
		return true
	}
	if !domRestricted && dowRestricted && matchedDOW {
		return true
	}
	return false
}

func (s *cronSchedule) isLastWeekdayOfMonthMatch(w wallTime) bool {
	if w.day <= lastDayOfMonth(w)-7 {
		return false
	}
	for _, v := range s.dayOfWeek.values {
		first, ok := jsParseInt(cronValueString(v)[:1])
		if ok && w.weekday == first%7 {
			return true
		}
	}
	return false
}

func (s *cronSchedule) matchHour(d *cronDate) bool {
	hoursLength := len(s.hour.nums)
	current := d.wall().hour
	isMatch := s.hour.has(current)
	isDstEnd := d.dstEnd == current

	// DST start: accept the landing hour when a skipped hour was scheduled.
	if d.dstStart != -1 && d.dstStartLandingHour == current {
		for skipped := d.dstStart; skipped != current; skipped = (skipped + 1) % 24 {
			if s.hour.has(skipped) {
				return true
			}
		}
	}
	// DST end: avoid returning the repeated hour twice.
	if isDstEnd {
		d.dstEnd = -1
		d.applyAdd(unitHour, hoursLength)
		return false
	}
	if isMatch {
		return true
	}

	d.dstStart, d.dstStartLandingHour = -1, -1
	next, ok := s.hour.nextAfter(current)
	if !ok {
		d.applyAdd(unitDayOfMonth, hoursLength)
		return false
	}
	if d.isDstTransitionDay() {
		for i := 0; i < next-current; i++ {
			d.applyAdd(unitHour, hoursLength)
			if d.wall().hour >= next {
				break
			}
		}
	} else {
		d.set(dateFields{hour: intp(next)})
	}
	d.set(dateFields{minute: intp(s.minute.first())})
	d.set(dateFields{second: intp(s.second.first())})
	return false
}

func isLeapYear(y int) bool { return (y%4 == 0 && y%100 != 0) || y%400 == 0 }

// lastDayOfMonth uses cron-parser's DAYS_IN_MONTH table with its leap-year
// adjustment for February.
func lastDayOfMonth(w wallTime) int {
	if w.month == 2 && !isLeapYear(w.year) {
		return 28
	}
	return daysInMonthMax[w.month-1]
}

func isLastDayOfMonth(w wallTime) bool { return w.day == lastDayOfMonth(w) }

// --- CronDate over luxon DateTime semantics ---

// cronDate is cron-parser's CronDate. Like a luxon DateTime it is an instant
// (epoch milliseconds) viewed in a zone; wall-clock edits resolve back to an
// instant with luxon's fixOffset, which is what decides where nonexistent
// (spring-forward) and repeated (fall-back) local times land.
type cronDate struct {
	ms                                    int64
	loc                                   *time.Location
	dstStart, dstStartLandingHour, dstEnd int // -1 when unset
}

type wallTime struct {
	year, month, day, hour, minute, second, ms, weekday int
}

// dateFields are the wall-clock components to override in set(); nil leaves a
// component unchanged.
type dateFields struct {
	day, hour, minute, second, ms *int
}

func intp(v int) *int { return &v }

func newCronDate(ms int64, loc *time.Location) *cronDate {
	return &cronDate{ms: ms, loc: loc, dstStart: -1, dstStartLandingHour: -1, dstEnd: -1}
}

func (d *cronDate) wall() wallTime {
	t := time.UnixMilli(d.ms).In(d.loc)
	return wallTime{
		year: t.Year(), month: int(t.Month()), day: t.Day(),
		hour: t.Hour(), minute: t.Minute(), second: t.Second(),
		ms: t.Nanosecond() / int(time.Millisecond), weekday: int(t.Weekday()),
	}
}

// offset is the zone's UTC offset at the instant, in seconds.
func (d *cronDate) offset() int64 { return zoneOffsetAt(d.loc, d.ms) }

func zoneOffsetAt(loc *time.Location, ms int64) int64 {
	_, off := time.UnixMilli(ms).In(loc).Zone()
	return int64(off)
}

// luxonDaysInMonth is luxon's daysInMonth, which accepts an overflowing month.
func luxonDaysInMonth(year, month int) int {
	modMonth := ((month-1)%12+12)%12 + 1
	modYear := year + (month-modMonth)/12
	if modMonth == 2 {
		if isLeapYear(modYear) {
			return 29
		}
		return 28
	}
	return daysInMonthMax[modMonth-1]
}

// localTS is luxon's objToLocalTS: the wall clock read as if it were UTC.
func localTS(year, month, day, hour, minute, second, ms int) int64 {
	return time.Date(year, time.Month(month), day, hour, minute, second, ms*int(time.Millisecond), time.UTC).UnixMilli()
}

// fixOffset is luxon's fixOffset: resolve a local timestamp to an instant,
// guessing the offset o (seconds) first.
func (d *cronDate) fixOffset(local int64, o int64) int64 {
	utcGuess := local - o*1000
	o2 := zoneOffsetAt(d.loc, utcGuess)
	if o == o2 {
		return utcGuess
	}
	utcGuess -= (o2 - o) * 1000
	o3 := zoneOffsetAt(d.loc, utcGuess)
	if o2 == o3 {
		return utcGuess
	}
	// A hole (nonexistent local time): keep the wall clock, shifted forward.
	return local - min(o2, o3)*1000
}

// set is luxon's DateTime.set for the components cron-parser changes.
func (d *cronDate) set(f dateFields) {
	w := d.wall()
	day, hour, minute, second, ms := w.day, w.hour, w.minute, w.second, w.ms
	if f.day != nil {
		day = *f.day
	} else {
		day = min(luxonDaysInMonth(w.year, w.month), day)
	}
	if f.hour != nil {
		hour = *f.hour
	}
	if f.minute != nil {
		minute = *f.minute
	}
	if f.second != nil {
		second = *f.second
	}
	if f.ms != nil {
		ms = *f.ms
	}
	d.ms = d.fixOffset(localTS(w.year, w.month, day, hour, minute, second, ms), d.offset())
}

// plusCalendar is luxon's plus({months}) / plus({days}): calendar arithmetic on
// the wall clock, resolved with the pre-change offset as the guess.
func (d *cronDate) plusCalendar(months, days int) {
	w := d.wall()
	month := w.month + months
	day := min(w.day, luxonDaysInMonth(w.year, month)) + days
	d.ms = d.fixOffset(localTS(w.year, month, day, w.hour, w.minute, w.second, w.ms), d.offset())
}

func (d *cronDate) startOfDay() {
	d.set(dateFields{hour: intp(0), minute: intp(0), second: intp(0), ms: intp(0)})
}

// add is CronDate.addUnit.
func (d *cronDate) add(unit cronUnit) {
	switch unit {
	case unitMonth:
		d.plusCalendar(1, 0)
		d.set(dateFields{day: intp(1), hour: intp(0), minute: intp(0), second: intp(0), ms: intp(0)})
	case unitDayOfMonth:
		d.plusCalendar(0, 1)
		d.startOfDay()
	case unitHour:
		d.ms += int64(time.Hour / time.Millisecond)
		d.set(dateFields{minute: intp(0), second: intp(0), ms: intp(0)})
	case unitMinute:
		d.ms += int64(time.Minute / time.Millisecond)
		d.set(dateFields{second: intp(0), ms: intp(0)})
	case unitSecond:
		d.ms += 1000
	}
}

// applyAdd is CronDate.applyDateOperation(Add, ...), which also records the
// DST transitions that matchHour consults. hoursLength == 24 (every hour
// scheduled) disables the tracking.
func (d *cronDate) applyAdd(unit cronUnit, hoursLength int) {
	if unit == unitMonth || unit == unitDayOfMonth {
		d.add(unit)
		return
	}
	previousHour := d.wall().hour
	previousOffset := d.offset()
	d.add(unit)
	w := d.wall()
	advance := (w.hour - previousHour + 24) % 24
	if d.offset() > previousOffset && advance >= 2 {
		if hoursLength != 24 {
			d.dstStart = (previousHour + 1) % 24
			d.dstStartLandingHour = w.hour
		}
	} else if advance == 0 && w.minute == 0 && w.second == 0 {
		if hoursLength != 24 {
			d.dstEnd = w.hour
		}
	}
}

// isDstTransitionDay reports whether the UTC offset differs between the start
// and the end (luxon endOf("day")) of the current local day.
func (d *cronDate) isDstTransitionDay() bool {
	start := newCronDate(d.ms, d.loc)
	start.startOfDay()
	end := newCronDate(d.ms, d.loc)
	end.plusCalendar(0, 1)
	end.startOfDay()
	end.ms--
	return start.offset() != end.offset()
}
