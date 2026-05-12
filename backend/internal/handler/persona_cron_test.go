package handler

import "testing"

func TestValidateProactiveCron(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"empty allowed (cron disabled)", "", false},
		{"every day 9am", "0 9 * * *", false},
		{"every hour", "0 * * * *", false},
		{"complex weekday range", "30 8-18 * * 1-5", false},
		{"with whitespace", "  0 9 * * *  ", false},

		{"six fields rejected (scheduler is 5-field)", "0 0 9 * * *", true},
		{"out of range minute", "60 9 * * *", true},
		{"junk", "not-a-cron", true},
		{"missing fields", "0 9 *", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProactiveCron(tc.expr)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for %q", tc.expr)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for %q: %v", tc.expr, err)
			}
		})
	}
}
