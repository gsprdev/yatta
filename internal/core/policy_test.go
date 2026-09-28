package core

import (
	"testing"
	"time"
)

const m = time.Minute

func TestPolicyApply(t *testing.T) {
	q := 15 * m
	tests := []struct {
		name     string
		p        Policy
		in       time.Duration
		want     time.Duration
		excluded bool
	}{
		{"no rounding", Policy{}, 23 * m, 23 * m, false},
		{"nearest down", Policy{Increment: q}, 22 * m, 15 * m, false},
		{"nearest up", Policy{Increment: q}, 23 * m, 30 * m, false},
		{"nearest tie rounds up", Policy{Increment: q, Direction: Nearest}, 22*m + 30*time.Second, 30 * m, false},
		{"nearest exact", Policy{Increment: q}, 30 * m, 30 * m, false},
		{"up", Policy{Increment: q, Direction: Up}, 16 * m, 30 * m, false},
		{"up exact", Policy{Increment: q, Direction: Up}, 15 * m, 15 * m, false},
		{"down", Policy{Increment: q, Direction: Down}, 29 * m, 15 * m, false},
		{"hour", Policy{Increment: time.Hour}, 95 * m, 2 * time.Hour, false},
		{"zero without minimum is excluded", Policy{Increment: q}, 7 * m, 0, true},
		{"down to zero is excluded", Policy{Increment: q, Direction: Down}, 14 * m, 0, true},
		{"below minimum rounds up", Policy{Minimum: q, BelowMin: RoundUpToMin}, 5 * m, 15 * m, false},
		{"zero raised by round-up minimum", Policy{Increment: q, Minimum: q, BelowMin: RoundUpToMin}, 5 * m, 15 * m, false},
		{"below minimum excluded", Policy{Minimum: q, BelowMin: Exclude}, 10 * m, 10 * m, true},
		{"minimum checked after rounding", Policy{Increment: q, Minimum: q, BelowMin: Exclude}, 8 * m, 15 * m, false},
		{"at minimum is kept", Policy{Minimum: q, BelowMin: Exclude}, 15 * m, 15 * m, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.p.Validate(); err != nil {
				t.Fatalf("test policy invalid: %v", err)
			}
			got, excluded := tt.p.apply(tt.in)
			if got != tt.want || excluded != tt.excluded {
				t.Errorf("apply(%v) = %v, %v; want %v, %v", tt.in, got, excluded, tt.want, tt.excluded)
			}
		})
	}
}

func TestPolicyValidate(t *testing.T) {
	tests := []struct {
		name string
		p    Policy
		ok   bool
	}{
		{"zero value", Policy{}, true},
		{"full", Policy{Increment: 15 * m, Direction: Up, Minimum: 15 * m, BelowMin: Exclude, Aggregate: AggTaskDay}, true},
		{"minimum without behavior", Policy{Minimum: 15 * m}, false},
		{"unknown direction", Policy{Direction: "sideways"}, false},
		{"unknown below-min", Policy{BelowMin: "ignore"}, false},
		{"unknown aggregation", Policy{Aggregate: "week"}, false},
		{"negative increment", Policy{Increment: -m}, false},
		{"negative minimum", Policy{Minimum: -m, BelowMin: Exclude}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.p.Validate(); (err == nil) != tt.ok {
				t.Errorf("Validate() = %v; want ok=%v", err, tt.ok)
			}
		})
	}
}
