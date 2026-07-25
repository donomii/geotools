package geotools_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"sort"
	"testing"

	"github.com/donomii/geotools/geodata"
)

func TestVectorTileCommandRoundTrip(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[
		{"type":"Feature","id":"point-one","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"name":"point","active":true}},
		{"type":"Feature","id":2,"geometry":{"type":"LineString","coordinates":[[-30,-10],[30,10]]},"properties":{"name":"line","rank":2}},
		{"type":"Feature","id":3,"geometry":{"type":"Polygon","coordinates":[[[-30,-30],[30,-30],[30,30],[-30,30],[-30,-30]],[[-10,-10],[-10,10],[10,10],[10,-10],[-10,-10]]]},"properties":{"name":"polygon"}},
		{"type":"Feature","id":4,"geometry":{"type":"MultiPoint","coordinates":[[-40,0],[40,0]]},"properties":{"name":"multipoint"}},
		{"type":"Feature","id":5,"geometry":{"type":"MultiLineString","coordinates":[[[-60,-5],[-40,5]],[[40,-5],[60,5]]]},"properties":{"name":"multiline"}},
		{"type":"Feature","id":6,"geometry":{"type":"MultiPolygon","coordinates":[[[[-60,-20],[-40,-20],[-40,0],[-60,0],[-60,-20]]],[[[40,-20],[60,-20],[60,0],[40,0],[40,-20]]]]},"properties":{"name":"multipolygon"}}
	]}`)
	tile := runConverter(t, input, "./geojson2mvt", "-z=0", "-x=0", "-y=0", "-layer=shapes", "-buffer=0", "-simplify=0")
	output := runConverter(t, tile, "./mvt2geojson", "-z=0", "-x=0", "-y=0", "-layer=shapes", "-output=collection")
	var names []string
	geometryTypes := make(map[string]int)
	if err := geodata.ReadFeatures(bytes.NewReader(output), geodata.InputAuto, func(feature geodata.Feature) error {
		summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{})
		if err != nil {
			return err
		}
		geometryTypes[summary.Type]++
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		var name string
		if err := json.Unmarshal(properties["name"], &name); err != nil {
			return err
		}
		if name == "polygon" {
			var geometry struct {
				Coordinates []json.RawMessage `json:"coordinates"`
			}
			if err := json.Unmarshal(feature.Geometry, &geometry); err != nil {
				return err
			}
			if len(geometry.Coordinates) != 2 {
				t.Fatalf("round-trip polygon contains %d rings; expected its exterior and hole", len(geometry.Coordinates))
			}
		}
		if name == "point" && feature.EncodedID() != `"point-one"` {
			t.Fatalf("round-trip point id is %s; expected string id %q", feature.EncodedID(), "point-one")
		}
		names = append(names, name)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	expectedNames := []string{"line", "multiline", "multipoint", "multipolygon", "point", "polygon"}
	if !equalStrings(names, expectedNames) {
		t.Fatalf("round-trip names are %v; expected %v", names, expectedNames)
	}
	for _, geometryType := range []string{"Point", "LineString", "Polygon", "MultiPoint", "MultiLineString", "MultiPolygon"} {
		if geometryTypes[geometryType] != 1 {
			t.Fatalf("round trip geometry counts are %v", geometryTypes)
		}
	}
}

func TestVectorTileMultipleLayerCommandRoundTrip(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[
		{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"name":"capital","source_layer":"places"}},
		{"type":"Feature","geometry":{"type":"LineString","coordinates":[[-10,0],[10,0]]},"properties":{"name":"road","source_layer":"roads"}}
	]}`)
	tile := runConverter(t, input, "./geojson2mvt", "-z=0", "-x=0", "-y=0", "-layer-property=source_layer", "-buffer=0", "-simplify=0")
	output := runConverter(t, tile, "./mvt2geojson", "-z=0", "-x=0", "-y=0", "-all-layers", "-layer-property=source_layer", "-output=collection")
	var layers []string
	if err := geodata.ReadFeatures(bytes.NewReader(output), geodata.InputAuto, func(feature geodata.Feature) error {
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		var layer string
		if err := json.Unmarshal(properties["source_layer"], &layer); err != nil {
			return err
		}
		layers = append(layers, layer)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(layers)
	if !equalStrings(layers, []string{"places", "roads"}) {
		t.Fatalf("round-trip layers are %v", layers)
	}
}

func runConverter(t *testing.T, input []byte, packagePath string, arguments ...string) []byte {
	t.Helper()
	commandArguments := append([]string{"run", packagePath}, arguments...)
	command := exec.Command("go", commandArguments...)
	command.Stdin = bytes.NewReader(input)
	var output bytes.Buffer
	var standardError bytes.Buffer
	command.Stdout = &output
	command.Stderr = &standardError
	if err := command.Run(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", packagePath, arguments, err, standardError.Bytes())
	}
	return output.Bytes()
}

func equalStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
