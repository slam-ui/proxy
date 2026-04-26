package api

import (
	"fmt"
	"testing"
	"time"
)

func resetGeoCacheForTest(t *testing.T) {
	t.Helper()
	geoCacheMu.Lock()
	old := geoCache
	geoCache = map[string]geoCacheEntry{}
	geoCacheMu.Unlock()

	t.Cleanup(func() {
		geoCacheMu.Lock()
		geoCache = old
		geoCacheMu.Unlock()
	})
}

func TestGeoCache_EvictsAtMax(t *testing.T) {
	resetGeoCacheForTest(t)

	for i := 0; i < geoCacheMax+20; i++ {
		setGeoCached(fmt.Sprintf("host-%d.example", i), "DE", time.Hour)
	}

	geoCacheMu.Lock()
	size := len(geoCache)
	geoCacheMu.Unlock()
	if size > geoCacheMax {
		t.Fatalf("geoCache size=%d, want <= %d", size, geoCacheMax)
	}
}

func TestGeoCache_ExpiredEntryMisses(t *testing.T) {
	resetGeoCacheForTest(t)

	setGeoCached("expired.example", "DE", -time.Second)
	if cc, ok := getGeoCached("expired.example"); ok {
		t.Fatalf("expired cache entry returned ok=true cc=%q", cc)
	}
}

func TestGeoIPAPIURL_EscapesPathSegment(t *testing.T) {
	got := geoIPAPIURL("bad/host?x=1")
	want := "http://ip-api.com/json/bad%2Fhost%3Fx=1?fields=countryCode"
	if got != want {
		t.Fatalf("geoIPAPIURL = %q, want %q", got, want)
	}
}
