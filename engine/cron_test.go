package engine

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// TestCronGolden checks nextScheduleOccurrence against occurrences computed by
// OpenWorkflow itself (see testdata/gen-cron-golden.mjs). OW_CRON_GOLDEN
// points it at a different (e.g. larger, uncommitted) corpus.
func TestCronGolden(t *testing.T) {
	path := os.Getenv("OW_CRON_GOLDEN")
	if path == "" {
		path = "testdata/cron_golden.json"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Cases []struct {
			Cron  string  `json:"cron"`
			TZ    string  `json:"tz"`
			After string  `json:"after"`
			Next  *string `json:"next"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Cases) == 0 {
		t.Fatal("empty corpus")
	}

	failures := 0
	for _, c := range golden.Cases {
		after, err := time.Parse(time.RFC3339Nano, c.After)
		if err != nil {
			t.Fatal(err)
		}
		got, err := nextScheduleOccurrence(c.Cron, c.TZ, after)
		var gotStr *string
		if err == nil {
			s := isoFromTime(got)
			gotStr = &s
		} else if !errors.Is(err, ErrBadRequest) {
			t.Errorf("%q %s after %s: unexpected error kind: %v", c.Cron, c.TZ, c.After, err)
		}
		if (gotStr == nil) != (c.Next == nil) || (gotStr != nil && *gotStr != *c.Next) {
			failures++
			if failures <= 25 {
				t.Errorf("%q in %s after %s: got %v (err %v), want %v", c.Cron, c.TZ, c.After, deref(gotStr), err, deref(c.Next))
			}
		}
	}
	if failures > 0 {
		t.Errorf("%d/%d cases differ from OpenWorkflow", failures, len(golden.Cases))
	}
}

func deref(s *string) string {
	if s == nil {
		return "<error>"
	}
	return *s
}
