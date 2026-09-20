package weather

import (
	"math"
	"testing"
)

func TestDistanceKMZeroForSamePoint(t *testing.T) {
	p := Point{Lat: 52.37, Lon: 4.90}
	if d := DistanceKM(p, p); math.Abs(d) > 0.001 {
		t.Errorf("DistanceKM(p, p) = %v, want ~0", d)
	}
}

func TestDistanceKMKnownCities(t *testing.T) {
	// Amsterdam to Rotterdam is about 57km.
	amsterdam := Point{Lat: 52.3676, Lon: 4.9041}
	rotterdam := Point{Lat: 51.9244, Lon: 4.4777}
	d := DistanceKM(amsterdam, rotterdam)
	if d < 55 || d > 60 {
		t.Errorf("Amsterdam-Rotterdam distance = %.1fkm, want ~57km", d)
	}
}

func TestDistanceKMSymmetric(t *testing.T) {
	a := Point{Lat: 52.37, Lon: 4.90}
	b := Point{Lat: 51.92, Lon: 4.48}
	if d1, d2 := DistanceKM(a, b), DistanceKM(b, a); math.Abs(d1-d2) > 0.0001 {
		t.Errorf("distance not symmetric: %v vs %v", d1, d2)
	}
}

func TestBoundingBoxContainsCenterAndExcludesFar(t *testing.T) {
	center := Point{Lat: 52.37, Lon: 4.90}
	box := BoundingBoxAround(center, 30)

	if !box.Contains(center) {
		t.Error("expected the box to contain its own center")
	}
	far := Point{Lat: 52.37, Lon: 40.0} // very far east
	if box.Contains(far) {
		t.Error("expected the box to exclude a point far outside the radius")
	}
}

func TestBoundingBoxNearPoleDoesNotDivideByZero(t *testing.T) {
	center := Point{Lat: 89.9, Lon: 0}
	box := BoundingBoxAround(center, 50)
	if !box.Contains(center) {
		t.Error("expected the box to contain its own center even near the pole")
	}
}
