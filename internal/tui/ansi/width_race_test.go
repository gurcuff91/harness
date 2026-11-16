package ansi

import (
	"fmt"
	"sync"
	"testing"
)

// VisibleWidth is called from every goroutine that draws or measures text. Its cache is a map, and
// two concurrent writes to a Go map are not just a data race: the runtime throws "fatal error:
// concurrent map writes", which cannot be recovered and kills the process. Strings that are not
// plain ASCII take the cached path, so these are CJK/emoji ones, many more than the cache holds so
// it keeps evicting.
func TestVisibleWidthIsSafeFromManyGoroutines(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20000; i++ {
				s := fmt.Sprintf("日本語-%d-%d", g, i%1500)
				want := 2*3 + len(fmt.Sprintf("-%d-%d", g, i%1500))
				if got := VisibleWidth(s); got != want {
					t.Errorf("VisibleWidth(%q) = %d, want %d", s, got, want)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// The cache must stay bounded and still answer correctly after being evicted from.
func TestVisibleWidthCacheStaysBoundedAndCorrect(t *testing.T) {
	for i := 0; i < 5*widthCacheSize; i++ {
		s := fmt.Sprintf("é%d", i)
		if got, want := VisibleWidth(s), 1+len(fmt.Sprint(i)); got != want {
			t.Fatalf("VisibleWidth(%q) = %d, want %d", s, got, want)
		}
	}
	widthMu.Lock()
	n := len(widthCache)
	widthMu.Unlock()
	if n > widthCacheSize {
		t.Fatalf("the cache holds %d entries, more than its bound %d", n, widthCacheSize)
	}
	// the cache is really used: what was just measured is in it
	widthMu.Lock()
	_, cached := widthCache["é"+fmt.Sprint(5*widthCacheSize-1)]
	widthMu.Unlock()
	if !cached {
		t.Fatal("the string measured last is not in the cache: nothing is ever stored")
	}
	// a hit returns what a miss computed
	a := VisibleWidth("日本語")
	b := VisibleWidth("日本語")
	if a != 6 || b != 6 {
		t.Fatalf("widths %d, %d; want 6, 6", a, b)
	}
}
