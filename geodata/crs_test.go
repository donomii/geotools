package geodata

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/twpayne/go-geom"
)

func TestParseCRSReferences(t *testing.T) {
	cases := map[string]string{
		`"http://www.opengis.net/def/crs/OGC/0/CRS84"`:   CRSCRS84,
		`"https://www.opengis.net/def/crs/OGC/0/CRS84h"`: CRSCRS84h,
		`"EPSG:4326"`: CRSEPSG4326,
		`{"type":"Reference","href":"https://www.opengis.net/def/crs/EPSG/0/3857"}`: CRSEPSG3857,
		`{"type":"ProjectedCRS","id":{"authority":"EPSG","code":3857}}`:             CRSEPSG3857,
		`{"id":{"authority":"EPSG","code":"4979"}}`:                                 CRSCRS84h,
	}
	for input, expected := range cases {
		actual, err := ParseCRS(json.RawMessage(input))
		if err != nil {
			t.Fatal(err)
		}
		if actual != expected {
			t.Fatalf("parsed %s as %s; expected %s", input, actual, expected)
		}
	}
}

func TestParseCRSRejectsUnsupportedAndIncompleteReferences(t *testing.T) {
	cases := []string{
		`"EPSG:32648"`,
		`{"type":"GeographicCRS","href":"http://www.opengis.net/def/crs/EPSG/0/4326"}`,
		`{"type":"Reference"}`,
		`{"id":{"authority":"EPSG","code":null}}`,
	}
	for _, input := range cases {
		if _, err := ParseCRS(json.RawMessage(input)); err == nil {
			t.Fatalf("accepted invalid CRS %s", input)
		}
	}
}

func TestTransformGeometryRoundTrip(t *testing.T) {
	point := geom.NewPointFlat(geom.XYZ, []float64{103.8198, 1.3521, 12})
	if _, err := TransformGeometry(point, CRSCRS84h, CRSEPSG3857); err != nil {
		t.Fatal(err)
	}
	if math.Abs(point.X()-11557167.27) > 1 || point.FlatCoords()[2] != 12 {
		t.Fatalf("projected coordinates are %v", point.FlatCoords())
	}
	if _, err := TransformGeometry(point, CRSEPSG3857, CRSCRS84h); err != nil {
		t.Fatal(err)
	}
	if math.Abs(point.X()-103.8198) > 1e-9 || math.Abs(point.Y()-1.3521) > 1e-9 || point.FlatCoords()[2] != 12 {
		t.Fatalf("round-trip coordinates are %v", point.FlatCoords())
	}
}

func TestTransformGeometryCollectionRoundTrip(t *testing.T) {
	collection := geom.NewGeometryCollection()
	if err := collection.Push(
		geom.NewPointFlat(geom.XYZ, []float64{103.8198, 1.3521, 12}),
		geom.NewLineStringFlat(geom.XYZ, []float64{103.8, 1.3, 4, 103.9, 1.4, 5}),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := TransformGeometry(collection, CRSCRS84h, CRSEPSG3857); err != nil {
		t.Fatal(err)
	}
	if _, err := TransformGeometry(collection, CRSEPSG3857, CRSCRS84h); err != nil {
		t.Fatal(err)
	}
	point := collection.Geom(0).(*geom.Point)
	line := collection.Geom(1).(*geom.LineString)
	if math.Abs(point.X()-103.8198) > 1e-9 || math.Abs(point.Y()-1.3521) > 1e-9 || point.FlatCoords()[2] != 12 {
		t.Fatalf("round-trip collection point is %v", point.FlatCoords())
	}
	if math.Abs(line.FlatCoords()[0]-103.8) > 1e-9 || math.Abs(line.FlatCoords()[1]-1.3) > 1e-9 || line.FlatCoords()[2] != 4 {
		t.Fatalf("round-trip collection line is %v", line.FlatCoords())
	}
}

func TestTransformJSONFGGeometryUsesEPSG4326AxisOrder(t *testing.T) {
	point := geom.NewPointFlat(geom.XY, []float64{103.8198, 1.3521})
	if _, err := TransformJSONFGGeometry(point, CRSCRS84, CRSEPSG4326); err != nil {
		t.Fatal(err)
	}
	if math.Abs(point.X()-1.3521) > 1e-12 || math.Abs(point.Y()-103.8198) > 1e-12 {
		t.Fatalf("EPSG:4326 coordinates are %v; expected latitude then longitude", point.FlatCoords())
	}
	if _, err := TransformJSONFGGeometry(point, CRSEPSG4326, CRSCRS84); err != nil {
		t.Fatal(err)
	}
	if math.Abs(point.X()-103.8198) > 1e-12 || math.Abs(point.Y()-1.3521) > 1e-12 {
		t.Fatalf("CRS84 coordinates are %v; expected longitude then latitude", point.FlatCoords())
	}
}
