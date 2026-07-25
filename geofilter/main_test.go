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
