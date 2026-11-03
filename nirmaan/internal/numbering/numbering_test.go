package numbering

import (
	"testing"
	"time"
)

func TestFYAndFormat(t *testing.T) {
	if got := FY(time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)); got != "2025-26" {
		t.Errorf("31 Mar = %s", got)
	}
	if got := FY(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)); got != "2026-27" {
		t.Errorf("1 Apr = %s", got)
	}
	if got := FY(time.Date(2099, 12, 1, 0, 0, 0, 0, time.UTC)); got != "2099-00" {
		t.Errorf("century = %s", got)
	}
	for _, p := range []string{ProductionRun, JobWorkChallan, JobWorkReceipt} {
		n := Format(p, "2026-27", 7)
		if len(n) != 16 {
			t.Errorf("%s is %d chars", n, len(n))
		}
	}
	if Format(JobWorkChallan, "2026-27", 42) != "JW/2026-27/00042" || Format(JobWorkReceipt, "2026-27", 42) != "JWR/2026-27/0042" {
		t.Error(Format(JobWorkChallan, "2026-27", 42), Format(JobWorkReceipt, "2026-27", 42))
	}
}
