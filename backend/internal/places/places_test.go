package places

import (
	"math"
	"testing"
)

func TestNearest(t *testing.T) {
	tests := []struct {
		name     string
		lat, lon float64
		want     string
	}{
		{"Brandenburger Tor", 52.5163, 13.3777, "Berlin"},
		{"Marienplatz", 48.1374, 11.5755, "München"},
		{"Kolosseum", 41.8902, 12.4922, "Rom"},
		{"mitten im Pazifik", 0, -140, ""},
	}
	for _, tt := range tests {
		if got := Nearest(tt.lat, tt.lon); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestList(t *testing.T) {
	once.Do(load)
	if len(towns) < 7000 {
		t.Fatalf("%d towns read", len(towns))
	}
	for _, town := range towns {
		if town.name == "" || math.Abs(town.lat) > 90 || math.Abs(town.lon) > 180 {
			t.Errorf("bad town %+v", town)
		}
	}
}

func TestDistance(t *testing.T) {
	// Berlin to München, about 504 km.
	if km := distance(52.52, 13.405, 48.137, 11.575); math.Abs(km-504) > 5 {
		t.Errorf("%.0f km", km)
	}
}

func BenchmarkNearest(b *testing.B) {
	for b.Loop() {
		Nearest(47.99, 7.85)
	}
}
