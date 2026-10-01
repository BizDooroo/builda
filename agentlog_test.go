package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// drain runs copyPrefixed over a reader and returns what it wrote.
func drain(t *testing.T, label, input string) string {
	t.Helper()
	var out bytes.Buffer
	writer := &lockedWriter{w: &out}
	var wg sync.WaitGroup
	wg.Add(1)
	go copyPrefixed(&wg, writer, label, strings.NewReader(input))
	wg.Wait()
	return out.String()
}

// TestCopyPrefixedSplitsVeryLongLines is the regression guard for a run that
// deadlocked on a long log line: the reader stopped, nobody drained the pipe,
// and the child blocked forever writing into a full buffer.
func TestCopyPrefixedSplitsVeryLongLines(t *testing.T) {
	long := strings.Repeat("x", maxLogLineBytes*2+17)
	output := drain(t, "stdout", "before\n"+long+"\nafter\n")

	if strings.Contains(output, "read error") {
		t.Fatalf("a long line must not be reported as a read error:\n%s", output[:200])
	}
	for _, want := range []string{"before", "after"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q to survive a long line, got:\n%s", want, output[:200])
		}
	}
	if total := strings.Count(output, "x"); total != len(long) {
		t.Fatalf("every byte of the long line must be written, got %d of %d", total, len(long))
	}
	// It is split rather than emitted as one unbounded line.
	for _, line := range strings.Split(output, "\n") {
		if len(line) > maxLogLineBytes+64 {
			t.Fatalf("a rendered line is %d bytes, which exceeds the split size", len(line))
		}
	}
}

// TestCopyPrefixedDrainsToEOF proves the reader consumes everything, which is
// what keeps the child process from blocking.
func TestCopyPrefixedDrainsToEOF(t *testing.T) {
	var builder strings.Builder
	for i := 0; i < 500; i++ {
		builder.WriteString(fmt.Sprintf("line-%03d\n", i))
	}
	output := drain(t, "stdout", builder.String())
	for _, probe := range []string{"line-000", "line-250", "line-499"} {
		if !strings.Contains(output, probe) {
			t.Fatalf("expected %q in the drained output", probe)
		}
	}
	// A stream with no trailing newline still delivers its last line.
	if output := drain(t, "stderr", "no trailing newline"); !strings.Contains(output, "no trailing newline") {
		t.Fatalf("an unterminated final line must still be written, got %q", output)
	}
	// An empty stream writes nothing and returns.
	if output := drain(t, "stdout", ""); strings.Count(output, "\n") > 1 {
		t.Fatalf("an empty stream should produce no output, got %q", output)
	}
}

// TestCopyPrefixedKeepsReadingAfterAnError makes sure a transient reader error
// does not leave the pipe unread.
func TestCopyPrefixedKeepsReadingAfterAnError(t *testing.T) {
	reader := io.MultiReader(strings.NewReader("first\n"), errReader{})
	var out bytes.Buffer
	writer := &lockedWriter{w: &out}
	var wg sync.WaitGroup
	wg.Add(1)
	go copyPrefixed(&wg, writer, "stdout", reader)
	wg.Wait()
	output := out.String()
	if !strings.Contains(output, "first") {
		t.Fatalf("expected the readable prefix, got %q", output)
	}
	if !strings.Contains(output, "read error") {
		t.Fatalf("expected the error to be reported, got %q", output)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, fmt.Errorf("synthetic read failure") }
