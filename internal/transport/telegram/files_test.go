package telegram

import (
	"sync"
	"testing"
	"time"
)

// A single-photo album (one message) fires once with that photo.
func TestAlbumSinglePhoto(t *testing.T) {
	a := newAlbums()
	var mu sync.Mutex
	var fired []*albumBuffer
	a.add("g1", 100, "hola", []PhotoSize{{FileID: "a"}}, func(b *albumBuffer) {
		mu.Lock()
		fired = append(fired, b)
		mu.Unlock()
	})
	time.Sleep(albumWindow + 200*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 {
		t.Fatalf("expected 1 fire, got %d", len(fired))
	}
	if len(fired[0].photos) != 1 || fired[0].caption != "hola" {
		t.Errorf("wrong album: photos=%d caption=%q", len(fired[0].photos), fired[0].caption)
	}
}

// Multiple photos of the same media_group_id collapse into ONE fire with all
// photos and the caption (which may arrive on any of the messages).
func TestAlbumGroupsPhotos(t *testing.T) {
	a := newAlbums()
	var mu sync.Mutex
	var fired []*albumBuffer
	fire := func(b *albumBuffer) {
		mu.Lock()
		fired = append(fired, b)
		mu.Unlock()
	}
	// Three photos arrive in quick succession; caption on the second.
	a.add("g1", 100, "", []PhotoSize{{FileID: "a"}}, fire)
	a.add("g1", 100, "las 3 fotos", []PhotoSize{{FileID: "b"}}, fire)
	a.add("g1", 100, "", []PhotoSize{{FileID: "c"}}, fire)

	time.Sleep(albumWindow + 300*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 {
		t.Fatalf("album should fire exactly once, got %d", len(fired))
	}
	if len(fired[0].photos) != 3 {
		t.Errorf("album should carry all 3 photos, got %d", len(fired[0].photos))
	}
	if fired[0].caption != "las 3 fotos" {
		t.Errorf("caption should be captured from any message, got %q", fired[0].caption)
	}
}

// Two different albums (different group ids) fire independently.
func TestAlbumSeparateGroups(t *testing.T) {
	a := newAlbums()
	var mu sync.Mutex
	count := map[string]int{}
	a.add("g1", 1, "", []PhotoSize{{FileID: "a"}}, func(b *albumBuffer) {
		mu.Lock()
		count["g1"] += len(b.photos)
		mu.Unlock()
	})
	a.add("g2", 2, "", []PhotoSize{{FileID: "x"}}, func(b *albumBuffer) {
		mu.Lock()
		count["g2"] += len(b.photos)
		mu.Unlock()
	})
	time.Sleep(albumWindow + 300*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count["g1"] != 1 || count["g2"] != 1 {
		t.Errorf("groups should fire independently: %v", count)
	}
}
