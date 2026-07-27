/*
Copyright 2026 HAL.
*/

package gemini

import (
	"strings"
	"testing"
)

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	if got := TruncateRunes("short", 10); got != "short" {
		t.Fatalf("short unchanged: %q", got)
	}

	long := strings.Repeat("a", 20)
	got := TruncateRunes(long, 5)
	if got != "aaaaa…" {
		t.Fatalf("got %q", got)
	}

	multi := "éééééé"
	got = TruncateRunes(multi, 3)
	if got != "ééé…" {
		t.Fatalf("multibyte cut mid-rune avoided: %q", got)
	}
}
