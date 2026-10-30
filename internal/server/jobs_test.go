package server

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestJobLogIsBoundedAndCursored(t *testing.T) {
	j := &job{log: []string{}, changed: make(chan struct{})}
	for i := range 3 {
		j.appendLine(fmt.Sprint(i))
	}
	lines, next, _, _ := j.since(0)
	if diff := cmp.Diff([]string{"0", "1", "2"}, lines); diff != "" || next != 3 {
		t.Errorf("since(0) = %v, %d, want [0 1 2], 3 (%s)", lines, next, diff)
	}
	if lines, next, _, _ = j.since(3); len(lines) != 0 || next != 3 {
		t.Errorf("since(3) with nothing new = %v, %d, want [], 3", lines, next)
	}
	j.appendLine("3")
	if lines, next, _, _ = j.since(3); !cmp.Equal(lines, []string{"3"}) || next != 4 {
		t.Errorf("since(3) = %v, %d, want [3], 4", lines, next)
	}

	for i := 4; i < maxJobLogLines+10; i++ {
		j.appendLine(fmt.Sprint(i))
	}
	rec := j.record()
	if len(rec.Log) != maxJobLogLines+1 || rec.Log[0] != truncatedMarker || rec.Log[1] != "10" || rec.Log[maxJobLogLines] != fmt.Sprint(maxJobLogLines+9) {
		t.Errorf("record log: len %d, head %q, want marker, then lines 10..%d", len(rec.Log), rec.Log[:2], maxJobLogLines+9)
	}
	// A subscriber that fell behind the kept window gets the marker, then the window.
	lines, next, _, _ = j.since(4)
	if len(lines) != maxJobLogLines+1 || lines[0] != truncatedMarker || lines[1] != "10" || next != maxJobLogLines+10 {
		t.Errorf("since(4) after truncation: len %d head %q next %d", len(lines), lines[:2], next)
	}
	// One inside the window gets only what is new.
	if lines, _, _, _ = j.since(maxJobLogLines + 8); !cmp.Equal(lines, []string{fmt.Sprint(maxJobLogLines + 8), fmt.Sprint(maxJobLogLines + 9)}) {
		t.Errorf("since(window) = %v", lines)
	}
}
