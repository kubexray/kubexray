package cluster

import (
	"fmt"
	"math"
	"slices"
	"time"
)

// Baselines (ADR-0016): what's normal for an app's errors and latency,
// learned from its own minutes, so a break means "clearly outside its
// usual range", not a fixed number.

// Sensitivity is how readily a change in traffic is a break.
type Sensitivity struct {
	Name string
	// K is how many typical spreads above the median a value must be.
	K float64
	// Hold is how long it must stay there.
	Hold time.Duration
}

// The sensitivities.
var (
	Calm   = Sensitivity{Name: "calm", K: 6, Hold: 5 * time.Minute}
	Normal = Sensitivity{Name: "normal", K: 4, Hold: 2 * time.Minute}
	Strict = Sensitivity{Name: "strict", K: 3, Hold: time.Minute}
)

// ParseSensitivity reads "calm", "normal" or "strict".
func ParseSensitivity(s string) (Sensitivity, error) {
	for _, v := range []Sensitivity{Calm, Normal, Strict} {
		if s == v.Name {
			return v, nil
		}
	}
	return Sensitivity{}, fmt.Errorf("sensitivity must be calm, normal or strict, got %q", s)
}

const (
	// keepHistory is how much of an app's traffic is kept, at a minute a
	// sample: a week, so each hour can be compared with the same hour on
	// other days.
	keepHistory = 7 * 24 * time.Hour
	// minHistory is how much a baseline needs; less, and the fixed floors
	// apply alone.
	minHistory = 60
	// hourDays is how many days of an hour a time-of-day baseline needs.
	hourDays = 3

	// Floors no baseline goes under: a break is at least this bad.
	minErrorPct = 1.0
	minP99      = 0.2 // seconds
	p99Factor   = 1.5 // and this many times the usual p99
)

// sample is one minute of an app's traffic.
type sample struct {
	at       time.Time
	rps      float64
	errorPct float64
	p99      float64
	broken   bool // a minute of a break: never part of what's normal
}

// history is an app's recent minutes, oldest first.
type history []sample

// add appends a minute, dropping what's older than keepHistory.
func (h history) add(s sample) history {
	h = append(h, s)
	cut := 0
	for cut < len(h) && s.at.Sub(h[cut].at) > keepHistory {
		cut++
	}
	return h[cut:]
}

// normalRange is what's normal for one number: the threshold above which
// it's a break, and the usual range to show people.
type normalRange struct {
	median, high float64
	low5, high95 float64
}

// baseline learns the normal range of one number at the time of now: from
// the same hour on other days when there are hourDays of them, else from
// the last 24 hours. ok is false with less than minHistory minutes.
func (h history) baseline(now time.Time, value func(sample) float64, k float64) (normalRange, bool) {
	var hour, day []float64
	days := map[int]bool{}
	for _, s := range h {
		if s.broken || !s.at.Before(now) {
			continue
		}
		v := value(s)
		if math.IsNaN(v) {
			continue
		}
		if now.Sub(s.at) <= 24*time.Hour {
			day = append(day, v)
		}
		if s.at.Hour() == now.Hour() && now.Sub(s.at) >= time.Hour {
			hour = append(hour, v)
			days[s.at.YearDay()] = true
		}
	}
	use := day
	if len(days) >= hourDays && len(hour) >= minHistory {
		use = hour
	}
	if len(use) < minHistory {
		return normalRange{}, false
	}
	slices.Sort(use)
	med := quantile(use, 0.5)
	dev := make([]float64, len(use))
	for i, v := range use {
		dev[i] = math.Abs(v - med)
	}
	slices.Sort(dev)
	// 1.4826 makes the median absolute deviation match a standard deviation
	// for normal data; a floor keeps a perfectly flat history from making
	// any wobble a break.
	spread := max(1.4826*quantile(dev, 0.5), 0.05*med, 1e-6)
	return normalRange{median: med, high: med + k*spread, low5: quantile(use, 0.05), high95: quantile(use, 0.95)}, true
}

// quantile of sorted values.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := q * float64(len(sorted)-1)
	lo := int(i)
	if lo+1 >= len(sorted) {
		return sorted[lo]
	}
	return sorted[lo] + (i-float64(lo))*(sorted[lo+1]-sorted[lo])
}

// judge decides whether this minute's errors and latency break, given the
// app's history, and returns the normal ranges it judged against (nil
// without a baseline).
func (h history) judge(now time.Time, cur sample, sens Sensitivity) (errorsBreak, slowBreak bool, errNormal, p99Normal *normalRange) {
	if cur.rps < minRPS {
		return false, false, nil, nil
	}
	if r, ok := h.baseline(now, func(s sample) float64 { return s.errorPct }, sens.K); ok {
		errNormal = &r
		errorsBreak = cur.errorPct > r.high && cur.errorPct >= minErrorPct
	} else {
		errorsBreak = cur.errorPct >= errorPctHigh
	}
	if r, ok := h.baseline(now, func(s sample) float64 { return s.p99 }, sens.K); ok && r.median > 0 {
		p99Normal = &r
		slowBreak = cur.p99 > r.high && cur.p99 >= minP99 && cur.p99 >= p99Factor*r.median
	} else {
		slowBreak = cur.p99 >= p99High
	}
	return errorsBreak, slowBreak, errNormal, p99Normal
}
