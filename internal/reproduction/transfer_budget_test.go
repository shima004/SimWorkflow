package reproduction

import "testing"

func TestTransferBudgetWholeExperimentBoundary(t *testing.T) {
	for _, tc := range []struct {
		runs, parallel int
		timeout        string
		valid          bool
	}{
		{3000, 20, "60m", true},
		{3000, 16, "60m", false},
		{3000, 16, "45m", true},
		{3000, 20, "90m", false},
		{1, 1, "166h54m", true},
		{1, 1, "166h54m1s", false},
		{1, 1, "2562047h47m16s", false},
		{0, 1, "1m", false},
		{1, 0, "1m", false},
		{1, 1, "invalid", false},
	} {
		err := ValidateTransferBudget(tc.runs, tc.parallel, tc.timeout)
		if (err == nil) != tc.valid {
			t.Fatalf("runs=%d parallel=%d timeout=%s: %v", tc.runs, tc.parallel, tc.timeout, err)
		}
	}
}
