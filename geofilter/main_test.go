package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/donomii/geotools/geodata"
)

func TestFilterRealPlacesByPropertiesAndBounds(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := parseWhere("kind=landmark,capital=false")
	if err != nil {
		t.Fatal(err)
	}
	settings := filterSettings{
		GeometryTypes: map[string]bool{"Point": true},
		Expected:      expected,
		Selected:      map[string]bool{"name": true, "opened": true},
		BBox:          [4]float64{-10, -40, 160, 55},
		HasBBox:       true,
		Limit:         -1,
	}
	var output bytes.Buffer
	if err := filterGeoJSON(geodata.InputAuto, geodata.OutputCollection, settings, bytes.NewReader(data), &output); err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		if len(properties) != 2 {
			t.Fatalf("selected Feature has properties %v; expected name and opened", properties)
		}
		var name string
		if err := json.Unmarshal(properties["name"], &name); err != nil {
			return err
		}
		names = append(names, name)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "Eiffel Tower" || names[1] != "Sydney Opera House" {
		t.Fatalf("filtered names are %v", names)
	}
}

func TestFilterLimitAndTypedEquality(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"value":1}},{"type":"Feature","geometry":{"type":"Point","coordinates":[3,4]},"properties":{"value":"1"}}]}`)
	expected, err := parseWhere("value=1")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	settings := filterSettings{Expected: expected, Limit: 1}
	if err := filterGeoJSON(geodata.InputAuto, geodata.OutputJSONL, settings, input, &output); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("filter emitted %d Features; expected 1", count)
	}
}

func TestParseBBoxRejectsNonFiniteValues(t *testing.T) {
	if _, _, err := parseBBox("NaN,0,1,1"); err == nil {
		t.Fatal("accepted non-finite bbox")
	}
}

func TestFilterBBoxCrossesAntimeridian(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[
		{"type":"Feature","geometry":{"type":"Point","coordinates":[175,1]},"properties":{"name":"east"}},
		{"type":"Feature","geometry":{"type":"Point","coordinates":[-175,1]},"properties":{"name":"west"}},
		{"type":"Feature","geometry":{"type":"Point","coordinates":[0,1]},"properties":{"name":"middle"}}
	]}`)
	bbox, hasBBox, err := parseBBox("170,-10,-170,10")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	settings := filterSettings{BBox: bbox, HasBBox: hasBBox, Limit: -1}
	if err := filterGeoJSON(geodata.InputAuto, geodata.OutputJSONL, settings, input, &output); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("antimeridian bbox emitted %d Features; expected 2", count)
	}
}

func TestParseBBoxRejectsCoordinatesOutsideWGS84(t *testing.T) {
	for _, value := range []string{"-181,0,1,1", "0,-91,1,1", "0,0,181,1", "0,0,1,91"} {
		if _, _, err := parseBBox(value); err == nil {
			t.Fatalf("accepted bbox %q outside WGS84", value)
		}
	}
}

func TestGeometryTypeFilterRejectsUnknownAndDisabledNull(t *testing.T) {
	if err := validateGeometryTypes(map[string]bool{"Circle": true}, false); err == nil {
		t.Fatal("accepted an unknown geometry type")
	}
	if err := validateGeometryTypes(map[string]bool{"null": true}, false); err == nil {
		t.Fatal("accepted null geometry filtering while null geometries are disabled")
	}
	if err := validateGeometryTypes(map[string]bool{"Point": true, "null": true}, true); err != nil {
		t.Fatal(err)
	}
}

func TestFilterPreservesNullPropertiesWhenNotProjecting(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":null}`)
	var output bytes.Buffer
	if err := filterGeoJSON(geodata.InputAuto, geodata.OutputJSONL, filterSettings{Limit: -1}, input, &output); err != nil {
		t.Fatal(err)
	}
	var properties json.RawMessage
	if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
		properties = feature.Properties
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if string(properties) != "null" {
		t.Fatalf("properties became %s; expected null", properties)
	}
}

func TestParseWhereSupportsCommasInsideJSONValues(t *testing.T) {
	expected, err := parseWhere(`name="Washington, D.C.",coordinates=[1,2],details={"kind":"city","capital":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if string(expected["name"]) != `"Washington, D.C."` {
		t.Fatalf("name condition is %s", expected["name"])
	}
	if string(expected["coordinates"]) != `[1,2]` {
		t.Fatalf("coordinates condition is %s", expected["coordinates"])
	}
	if string(expected["details"]) != `{"kind":"city","capital":true}` {
		t.Fatalf("details condition is %s", expected["details"])
	}
}

func TestParseWhereRejectsAmbiguousConditions(t *testing.T) {
	for _, value := range []string{
		`name="unterminated`,
		`coordinates=[1,2`,
		`details={"kind":"city"]`,
		`name=first,name=second`,
	} {
		if _, err := parseWhere(value); err == nil {
			t.Fatalf("accepted invalid conditions %q", value)
		}
	}
}

func TestFilterMatchesJSONObjectsRegardlessOfMemberOrder(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"details":{"capital":true,"kind":"city"}}}`)
	expected, err := parseWhere(`details={"kind":"city","capital":true}`)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := filterGeoJSON(geodata.InputAuto, geodata.OutputJSONL, filterSettings{Expected: expected, Limit: -1}, input, &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("object-valued condition did not match equivalent JSON with a different member order")
	}
}

func TestFilterBBoxUsesGeometryIntersection(t *testing.T) {
	box := [4]float64{1, 1, 2, 2}
	wrapped := [4]float64{170, -10, -170, 10}
	tests := []struct {
		name     string
		geometry string
		bounds   [4]float64
		matches  bool
	}{
		{"diagonal misses box", `{"type":"LineString","coordinates":[[0,0],[4,1]]}`, box, false},
		{"line crosses box", `{"type":"LineString","coordinates":[[0,1.5],[3,1.5]]}`, box, true},
		{"line touches corner", `{"type":"LineString","coordinates":[[0,2],[2,0]]}`, box, true},
		{"line follows edge", `{"type":"LineString","coordinates":[[0,1],[3,1]]}`, box, true},
		{"multipoint gap", `{"type":"MultiPoint","coordinates":[[0,0],[3,3]]}`, box, false},
		{"multipoint match", `{"type":"MultiPoint","coordinates":[[0,0],[1,1]]}`, box, true},
		{"multiline gap", `{"type":"MultiLineString","coordinates":[[[0,0],[0,3]],[[3,0],[3,3]]]}`, box, false},
		{"multiline match", `{"type":"MultiLineString","coordinates":[[[0,0],[0,3]],[[0,1.5],[3,1.5]]]}`, box, true},
		{"polygon contains box", `{"type":"Polygon","coordinates":[[[0,0],[3,0],[3,3],[0,3],[0,0]]]}`, box, true},
		{"box contains polygon", `{"type":"Polygon","coordinates":[[[1.2,1.2],[1.8,1.2],[1.8,1.8],[1.2,1.8],[1.2,1.2]]]}`, box, true},
		{"polygon envelope overlap only", `{"type":"Polygon","coordinates":[[[0,0],[4,0],[0,1.1],[0,0]]]}`, box, false},
		{"box inside hole", `{"type":"Polygon","coordinates":[[[0,0],[3,0],[3,3],[0,3],[0,0]],[[0.5,0.5],[0.5,2.5],[2.5,2.5],[2.5,0.5],[0.5,0.5]]]}`, box, false},
		{"box touches hole edge", `{"type":"Polygon","coordinates":[[[0,0],[3,0],[3,3],[0,3],[0,0]],[[1,0.5],[1,2.5],[2.5,2.5],[2.5,0.5],[1,0.5]]]}`, box, true},
		{"polygon touches corner", `{"type":"Polygon","coordinates":[[[2,2],[3,2],[3,3],[2,3],[2,2]]]}`, box, true},
		{"multipolygon gap", `{"type":"MultiPolygon","coordinates":[[[[0,0],[0.5,0],[0.5,3],[0,3],[0,0]]],[[[2.5,0],[3,0],[3,3],[2.5,3],[2.5,0]]]]}`, box, false},
		{"multipolygon match", `{"type":"MultiPolygon","coordinates":[[[[0,0],[0.5,0],[0.5,3],[0,3],[0,0]]],[[[1.5,0],[3,0],[3,3],[1.5,3],[1.5,0]]]]}`, box, true},
		{"collection gap", `{"type":"GeometryCollection","geometries":[{"type":"Point","coordinates":[0,0]},{"type":"Point","coordinates":[3,3]}]}`, box, false},
		{"nested collection match", `{"type":"GeometryCollection","geometries":[{"type":"GeometryCollection","geometries":[{"type":"LineString","coordinates":[[0,1.5],[3,1.5]]}]}]}`, box, true},
		{"wrapped east match", `{"type":"LineString","coordinates":[[160,0],[175,0]]}`, wrapped, true},
		{"wrapped west match", `{"type":"LineString","coordinates":[[-160,0],[-175,0]]}`, wrapped, true},
		{"wrapped envelope overlap only", `{"type":"LineString","coordinates":[[160,0],[175,40]]}`, wrapped, false},
		{"wrapped center excluded", `{"type":"Polygon","coordinates":[[[-5,-5],[5,-5],[5,5],[-5,5],[-5,-5]]]}`, wrapped, false},
		{"point box on line", `{"type":"LineString","coordinates":[[0,0],[3,3]]}`, [4]float64{1, 1, 1, 1}, true},
		{"point box in polygon", `{"type":"Polygon","coordinates":[[[0,0],[3,0],[3,3],[0,3],[0,0]]]}`, [4]float64{1, 1, 1, 1}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := bytes.NewBufferString(`{"type":"Feature","geometry":` + test.geometry + `,"properties":{}}`)
			var output bytes.Buffer
			settings := filterSettings{BBox: test.bounds, HasBBox: true, Limit: -1}
			if err := filterGeoJSON(geodata.InputAuto, geodata.OutputJSONL, settings, input, &output); err != nil {
				t.Fatal(err)
			}
			if got := output.Len() > 0; got != test.matches {
				t.Fatalf("geometry matched bbox %v: got %t, expected %t; output %s", test.bounds, got, test.matches, &output)
			}
		})
	}
}

func TestFilterLimitDoesNotDecodeFollowingRecord(t *testing.T) {
	feature := `{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}`
	for _, input := range []string{
		feature + "\ninvalid JSON",
		"[" + feature + ",invalid JSON",
		`{"type":"FeatureCollection","features":[` + feature + ",invalid JSON",
		"\x1e" + feature + "\n\x1einvalid JSON",
	} {
		var output bytes.Buffer
		if err := filterGeoJSON(geodata.InputAuto, geodata.OutputCollection, filterSettings{Limit: 1}, bytes.NewBufferString(input), &output); err != nil {
			t.Fatalf("filter continued beyond its limit for input %q: %v", input, err)
		}
		count := 0
		if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
			count++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("filter emitted %d Features; expected 1", count)
		}
	}
}

func TestFilterZeroLimitDoesNotReadInput(t *testing.T) {
	for _, outputMode := range []geodata.OutputMode{geodata.OutputJSONL, geodata.OutputCollection, geodata.OutputSequence} {
		input := bytes.NewBufferString("invalid JSON")
		originalLength := input.Len()
		var output bytes.Buffer
		if err := filterGeoJSON(geodata.InputAuto, outputMode, filterSettings{Limit: 0}, input, &output); err != nil {
			t.Fatal(err)
		}
		if input.Len() != originalLength {
			t.Fatalf("zero limit read %d input bytes", originalLength-input.Len())
		}
		if outputMode == geodata.OutputCollection && !json.Valid(output.Bytes()) {
			t.Fatalf("zero limit left an invalid FeatureCollection: %s", &output)
		}
	}
}
