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
