package core

import (
	"fmt"
	"time"
)

type Direction string

const (
	Nearest Direction = "nearest"
	Up      Direction = "up"
	Down    Direction = "down"
)

type BelowMin string

const (
	RoundUpToMin BelowMin = "round_up"
	Exclude      BelowMin = "exclude"
)

type AggKey string

const (
	AggNone    AggKey = "none"
	AggTaskDay AggKey = "task_day"
)

// Policy is the upload policy: how durations are rounded and entries grouped.
// The zero value is the default policy: no rounding, no minimum, no aggregation.
type Policy struct {
	Increment time.Duration // 0 => no rounding
	Direction Direction     // "" => Nearest
	Minimum   time.Duration // 0 => no minimum
	BelowMin  BelowMin      // required when Minimum > 0
	Aggregate AggKey        // "" => AggNone
}

// Validate reports whether the policy can be applied. Group and PlanUpload
// require a valid policy.
func (p Policy) Validate() error {
	if p.Increment < 0 {
		return fmt.Errorf("rounding increment must not be negative")
	}
	if p.Minimum < 0 {
		return fmt.Errorf("minimum duration must not be negative")
	}
	switch p.Direction {
	case "", Nearest, Up, Down:
	default:
		return fmt.Errorf("unknown rounding direction %q", p.Direction)
	}
	switch p.BelowMin {
	case "":
		if p.Minimum > 0 {
			return fmt.Errorf("a minimum duration needs a below-minimum behavior")
		}
	case RoundUpToMin, Exclude:
	default:
		return fmt.Errorf("unknown below-minimum behavior %q", p.BelowMin)
	}
	switch p.Aggregate {
	case "", AggNone, AggTaskDay:
	default:
		return fmt.Errorf("unknown aggregation key %q", p.Aggregate)
	}
	return nil
}

// apply rounds d under the policy and reports whether the result is excluded
// from upload. A result of zero is always excluded.
func (p Policy) apply(d time.Duration) (rounded time.Duration, excluded bool) {
	rounded = d
	if inc := p.Increment; inc > 0 {
		down := d / inc * inc
		switch p.Direction {
		case Down:
			rounded = down
		case Up:
			if down < d {
				rounded = down + inc
			} else {
				rounded = down
			}
		case "", Nearest:
			if d-down >= inc-(d-down) {
				rounded = down + inc
			} else {
				rounded = down
			}
		default:
			panic(fmt.Sprintf("core: invalid policy: direction %q", p.Direction))
		}
	}
	if p.Minimum > 0 && rounded < p.Minimum {
		switch p.BelowMin {
		case RoundUpToMin:
			return p.Minimum, false
		case Exclude:
			return rounded, true
		default:
			panic(fmt.Sprintf("core: invalid policy: below-minimum %q", p.BelowMin))
		}
	}
	return rounded, rounded <= 0
}
