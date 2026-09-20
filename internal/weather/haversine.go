package weather

import "math"

const earthRadiusKM = 6371.0

// Point is a location in decimal degrees.
type Point struct {
	Lat, Lon float64
}

// DistanceKM returns the great-circle distance between a and b in
// kilometers, via the haversine formula.
func DistanceKM(a, b Point) float64 {
	lat1, lon1 := degToRad(a.Lat), degToRad(a.Lon)
	lat2, lon2 := degToRad(b.Lat), degToRad(b.Lon)

	dLat := lat2 - lat1
	dLon := lon2 - lon1

	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
	return earthRadiusKM * c
}

func degToRad(d float64) float64 {
	return d * math.Pi / 180
}

// BoundingBox is a coarse lat/lon rectangle around a point, used to filter
// lightning data before the heavier per-point haversine check — CLAUDE.md:
// "Filter lightning data by a bounding box around home before heavier
// processing (memory is limited)".
type BoundingBox struct {
	MinLat, MaxLat float64
	MinLon, MaxLon float64
}

// BoundingBoxAround returns a box comfortably containing every point within
// radiusKM of center (a bit generous, since degrees-per-km varies with
// latitude; callers still haversine-filter afterward).
func BoundingBoxAround(center Point, radiusKM float64) BoundingBox {
	// ~111.32km per degree of latitude, everywhere.
	latDelta := radiusKM / 110.0
	// Degrees per km of longitude shrinks toward the poles; guard against
	// cos(90°)=0 near them.
	cosLat := math.Cos(degToRad(center.Lat))
	if cosLat < 0.01 {
		cosLat = 0.01
	}
	lonDelta := radiusKM / (110.0 * cosLat)

	return BoundingBox{
		MinLat: center.Lat - latDelta,
		MaxLat: center.Lat + latDelta,
		MinLon: center.Lon - lonDelta,
		MaxLon: center.Lon + lonDelta,
	}
}

// Contains reports whether p falls within the box.
func (b BoundingBox) Contains(p Point) bool {
	return p.Lat >= b.MinLat && p.Lat <= b.MaxLat && p.Lon >= b.MinLon && p.Lon <= b.MaxLon
}
