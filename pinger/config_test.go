package main

import "testing"

func TestOtlpEndpoint(t *testing.T) {
	cases := []struct {
		name, env, want string
	}{
		{"default when empty", "", "alloy:4317"},
		{"bare host:port passthrough", "alloy:4317", "alloy:4317"},
		{"strips http prefix", "http://alloy:4317", "alloy:4317"},
		{"strips https prefix", "https://alloy:4317", "alloy:4317"},
		{"strips trailing slash", "http://alloy:4317/", "alloy:4317"},
		{"strips https prefix and trailing slash", "https://alloy:4317/", "alloy:4317"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", tc.env)
			if got := otlpEndpoint(); got != tc.want {
				t.Errorf("otlpEndpoint() with env %q = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}
