package main

import (
	"testing"
	"time"
)

func TestEvaluateReachability(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		lastRecv time.Time
		lastRtt  float64
		wantUp   bool
		wantRtt  float64
	}{
		{"no reply yet", time.Time{}, 0, false, 0},
		{"reply just now", now, 12.5, true, 12.5},
		{"just inside window", now.Add(-(upWindow - time.Millisecond)), 7, true, 7},
		{"at window boundary", now.Add(-upWindow), 7, false, 0},
		{"past window", now.Add(-(upWindow + time.Millisecond)), 7, false, 0},
		{"down discards rtt", time.Time{}, 99, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateReachability(tc.lastRecv, tc.lastRtt, now)
			if got.up != tc.wantUp || got.rtt != tc.wantRtt {
				t.Errorf("evaluateReachability(%v, %v) = {up:%v rtt:%v}, want {up:%v rtt:%v}",
					tc.lastRecv, tc.lastRtt, got.up, got.rtt, tc.wantUp, tc.wantRtt)
			}
		})
	}
}
