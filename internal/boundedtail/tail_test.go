package boundedtail

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func assertBudget(t *testing.T, tail *Tail, max int) {
	t.Helper()
	retained := cap(tail.partial)
	for _, s := range tail.lines {
		retained += len(s)
	}
	if retained > max {
		t.Fatalf("retained %d bytes, budget %d", retained, max)
	}
	if tail.size > cap(tail.partial) {
		t.Fatal("partial exceeds capacity")
	}
}

func TestHugeLinesStayBounded(t *testing.T) {
	const budget, perLine = 256 * 1024, 16 * 1024
	tail := New(10, budget, perLine)
	huge := []byte(strings.Repeat("x", 4*1024*1024))
	for i := 0; i < 32; i++ {
		n, err := tail.Write(huge)
		if n != len(huge) || err != nil {
			t.Fatal(n, err)
		}
		assertBudget(t, tail, budget)
	}
	if got := tail.Lines(); len(got) != 1 || len(got[0]) != perLine {
		t.Fatal("unterminated suffix not bounded")
	}
	for i := 0; i < 32; i++ {
		tail.Write(huge)
		tail.Write([]byte(fmt.Sprintf("%02d\n", i)))
		assertBudget(t, tail, budget)
	}
	got := tail.Lines()
	if len(got) != 10 || !strings.HasSuffix(got[9], "31") {
		t.Fatal("recent diagnostics lost")
	}
	for _, line := range got {
		if len(line) > perLine {
			t.Fatal("line budget exceeded")
		}
	}
}

func TestChunkBoundaries(t *testing.T) {
	for _, chunks := range [][]string{
		{"a\r\nb\r\n\nc\r"},
		{"a", "\r", "\n", "b\r", "\n\n", "c", "\r"},
	} {
		tail := New(4, 32, 8)
		if tail.Lines() == nil {
			t.Fatal("empty snapshot is nil")
		}
		for _, chunk := range chunks {
			tail.Write([]byte(chunk))
		}
		if got := tail.Lines(); !reflect.DeepEqual(got, []string{"a", "b", "", "c"}) {
			t.Fatal(got)
		}
		snapshot := tail.Lines()
		snapshot[0] = "changed"
		tail.Write([]byte("\nd\n"))
		if got := tail.Lines(); !reflect.DeepEqual(got, []string{"b", "", "c", "d"}) {
			t.Fatal(got)
		}
	}
}

func TestRingWrapAndByteEviction(t *testing.T) {
	tail := New(10, 21, 8)
	for _, chunk := range []string{"old\n", "12345", "678", "9AB", "C\r", "\n", "recent\n"} {
		tail.Write([]byte(chunk))
		assertBudget(t, tail, 21)
	}
	if got := tail.Lines(); !reflect.DeepEqual(got, []string{"6789ABC", "recent"}) {
		t.Fatal(got)
	}
	for i := 0; i < 100; i++ {
		tail.Write([]byte("a\n"))
	}
	tail.Write([]byte("z"))
	got := tail.Lines()
	if len(got) != 10 || got[9] != "z" {
		t.Fatal(got)
	}
	assertBudget(t, tail, 21)
}

func TestConcurrentStreamsAndSnapshots(t *testing.T) {
	tail := New(10, 1024, 64)
	var wg sync.WaitGroup
	for _, stream := range []string{"stdout", "stderr"} {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				tail.Write([]byte(s + "\r\n"))
				tail.Lines()
			}
		}(stream)
	}
	wg.Wait()
	got := tail.Lines()
	if len(got) != 10 {
		t.Fatal(got)
	}
	for _, line := range got {
		if line != "stdout" && line != "stderr" {
			t.Fatal(line)
		}
	}
	assertBudget(t, tail, 1024)
}

// Small writes to an ever-longer unterminated line must not allocate or copy the
// accumulated input. This catches the former partial += string(p) regression.
func TestUnterminatedAppendsDoNotAllocate(t *testing.T) {
	tail := New(10, 256*1024, 16*1024)
	chunk := []byte(strings.Repeat("x", 1024))
	if allocations := testing.AllocsPerRun(1000, func() { tail.Write(chunk) }); allocations != 0 {
		t.Fatalf("%g allocations per append", allocations)
	}
}

func BenchmarkUnterminated(b *testing.B) {
	tail := New(10, 256*1024, 16*1024)
	chunk := []byte(strings.Repeat("x", 1024))
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tail.Write(chunk)
	}
}
