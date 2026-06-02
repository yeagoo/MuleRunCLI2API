package config

import (
	"math"
	"testing"
)

func TestFloatEnv_RejectsBadValues(t *testing.T) {
	cases := []struct {
		raw      string
		fallback float64
		want     float64
	}{
		// Valid
		{"3", 1.0, 3.0},
		{"1.5", 1.0, 1.5},
		{"1", 1.0, 1.0},
		// Invalid → fallback
		{"", 7.0, 7.0},
		{"abc", 7.0, 7.0},
		{"0.5", 7.0, 7.0},      // below floor
		{"0", 7.0, 7.0},
		{"-3", 7.0, 7.0},
		{"Inf", 7.0, 7.0},      // review #7
		{"+Inf", 7.0, 7.0},
		{"-Inf", 7.0, 7.0},
		{"NaN", 7.0, 7.0},
		{"1e100", 7.0, 7.0},    // above ceiling
		{"99999", 7.0, 7.0},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			t.Setenv("X_TEST_FLOAT", c.raw)
			got := floatEnv("X_TEST_FLOAT", c.fallback)
			if got != c.want && !(math.IsNaN(got) && math.IsNaN(c.want)) {
				t.Errorf("floatEnv(%q, %v) = %v, want %v", c.raw, c.fallback, got, c.want)
			}
		})
	}
}
