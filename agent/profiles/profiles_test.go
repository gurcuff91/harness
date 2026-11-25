package profiles

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/gurcuff91/harness/agent/tools"
)

func TestRegistryBasics(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("  ", Profile{}); err == nil {
		t.Fatal("blank name must be refused")
	}
	if err := r.Register("b", Profile{Directives: []string{"B"}}); err != nil {
		t.Fatal(err)
	}
	_ = r.Register("a", Profile{Directives: []string{"A"}, Tools: func() []tools.Tool { return []tools.Tool{{}} }})
	if got := r.Names(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("names = %v", got)
	}
	d, ts, missing := r.Resolve([]string{"b", "zz", "a"})
	if !slices.Equal(d, []string{"B", "A"}) || len(ts) != 1 || !slices.Equal(missing, []string{"zz"}) {
		t.Fatalf("resolve = %v %d %v", d, len(ts), missing)
	}
	r.Unregister("a")
	if _, ok := r.Get("a"); ok {
		t.Fatal("still registered")
	}
}

func TestRegistryIsSafeConcurrently(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				n := fmt.Sprintf("p%d", j%5)
				_ = r.Register(n, Profile{})
				r.Names()
				r.Resolve([]string{n})
				r.Unregister(n)
			}
		}(i)
	}
	wg.Wait()
}
