package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/donomii/geotools/geodata"
)

func TestCheckRealPlaces(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	report, err := checkGeoJSON(geodata.InputAuto, geodata.ValidationOptions{}, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if report.Features != 5 || report.Positions != 11 {
		t.Fatalf("report has %d Features and %d positions; expected 5 and 11", report.Features, report.Positions)
	}
	if report.GeometryTypes["Point"] != 3 || report.GeometryTypes["LineString"] != 1 || report.GeometryTypes["Polygon"] != 1 {
		t.Fatalf("unexpected geometry counts: %#v", report.GeometryTypes)
	}
	expectedBBox := []float64{-122.4783, -33.8568, 151.2153, 48.8584}
	for index := range expectedBBox {
		if report.BBox[index] != expectedBBox[index] {
			t.Fatalf("bbox is %v; expected %v", report.BBox, expectedBBox)
		}
	}
}

func TestCheckReportsFeatureIdentity(t *testing.T) {
	input := strings.NewReader(`{"type":"Feature","id":"broken-place","geometry":{"type":"Point","coordinates":[200,0]},"properties":{}}`)
	_, err := checkGeoJSON(geodata.InputAuto, geodata.ValidationOptions{}, input)
	if err == nil || !strings.Contains(err.Error(), `"broken-place"`) {
		t.Fatalf("error %q does not identify the invalid Feature", err)
	}
}

func TestCheckRejectsInvalidFeatureCollectionBBox(t *testing.T) {
	input := strings.NewReader(`{"type":"FeatureCollection","bbox":"not a bbox","features":[]}`)
	if _, err := checkGeoJSON(geodata.InputAuto, geodata.ValidationOptions{}, input); err == nil {
		t.Fatal("accepted invalid FeatureCollection bbox")
	}
}
