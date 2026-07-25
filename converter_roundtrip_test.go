package geotools_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/donomii/geotools/geodata"
	_ "modernc.org/sqlite"
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

func TestGeoParquetCommandRoundTrip(t *testing.T) {
	input := readRealData(t)
	encoded := runConverter(t, input, "./geoparquet", "-mode=encode")
	output := runConverter(t, encoded, "./geoparquet", "-mode=decode", "-output=collection")
	requireSameFeatures(t, input, output)
}

func TestFlatGeobufCommandRoundTrip(t *testing.T) {
	input := readRealData(t)
	for _, indexed := range []bool{true, false} {
		t.Run(map[bool]string{true: "indexed", false: "streaming"}[indexed], func(t *testing.T) {
			encoded := runConverter(t, input, "./flatgeobuf", "-mode=encode", "-layer=real_places", "-index="+map[bool]string{true: "true", false: "false"}[indexed])
			output := runConverter(t, encoded, "./flatgeobuf", "-mode=decode", "-output=collection")
			requireSameFeatures(t, input, output)
		})
	}
}

func TestJSONFGCommandRoundTrip(t *testing.T) {
	input := readRealData(t)
	for _, crs := range []string{geodata.CRSCRS84, geodata.CRSEPSG3857} {
		t.Run(crs, func(t *testing.T) {
			encoded := runConverter(t, input, "./jsonfg", "-mode=encode", "-place-crs="+crs)
			var root map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &root); err != nil {
				t.Fatal(err)
			}
			if root["conformsTo"] == nil {
				t.Fatalf("JSON-FG encoded with %s has no conformsTo declaration", crs)
			}
			output := runConverter(t, encoded, "./jsonfg", "-mode=decode", "-output=collection")
			requireSameFeatures(t, input, output)
		})
	}
}

func TestSequenceFilterCommandPipeline(t *testing.T) {
	sequence := runConverter(t, readRealData(t), "./geojsonseq", "-output=seq")
	if bytes.Count(sequence, []byte{0x1e}) != 5 {
		t.Fatalf("sequence contains %d record separators; expected 5", bytes.Count(sequence, []byte{0x1e}))
	}
	output := runConverter(t, sequence, "./geofilter", "-input=seq", "-output=collection", "-geometry=Point", "-has=name", "-drop=kind", "-limit=2")
	var names []string
	if err := geodata.ReadFeatures(bytes.NewReader(output), geodata.InputAuto, func(feature geodata.Feature) error {
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		if properties["kind"] != nil {
			t.Fatalf("filtered Feature retained dropped property: %s", feature.Properties)
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
	if !equalStrings(names, []string{"Singapore", "Eiffel Tower"}) {
		t.Fatalf("pipeline returned names %v", names)
	}
}

func TestGeoJSONCheckCommandReportsRealData(t *testing.T) {
	output := runConverter(t, readRealData(t), "./geojsoncheck")
	var report struct {
		Features      int            `json:"features"`
		Positions     int64          `json:"positions"`
		GeometryTypes map[string]int `json:"geometry_types"`
		BBox          []float64      `json:"bbox"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	if report.Features != 5 || report.Positions != 11 || report.GeometryTypes["Point"] != 3 {
		t.Fatalf("geojsoncheck report is %#v", report)
	}
	if len(report.BBox) != 4 || report.BBox[0] != -122.4783 || report.BBox[3] != 48.8584 {
		t.Fatalf("geojsoncheck bbox is %v", report.BBox)
	}
}

func TestMBTilesCommandWritesQueryableArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "real_places.mbtiles")
	runConverter(t, readRealData(t), "./geojson2mbtiles", "-output="+path, "-name=real places", "-min-z=0", "-max-z=1", "-simplify=0")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("geojson2mbtiles wrote an empty archive")
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var name string
	if err := database.QueryRow(`SELECT value FROM metadata WHERE name='name'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	var tileCount int
	if err := database.QueryRow(`SELECT count(*) FROM tiles`).Scan(&tileCount); err != nil {
		t.Fatal(err)
	}
	if name != "real places" || tileCount != 4 {
		t.Fatalf("MBTiles archive has name %q and %d tiles; expected name %q and 4 tiles", name, tileCount, "real places")
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

func readRealData(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func requireSameFeatures(t *testing.T, expected, actual []byte) {
	t.Helper()
	expectedFeatures := canonicalFeatureJSON(t, expected)
	actualFeatures := canonicalFeatureJSON(t, actual)
	if !equalStrings(expectedFeatures, actualFeatures) {
		t.Fatalf("round trip changed Features:\nexpected %v\nactual   %v", expectedFeatures, actualFeatures)
	}
}

func canonicalFeatureJSON(t *testing.T, data []byte) []string {
	t.Helper()
	var result []string
	if err := geodata.ReadFeatures(bytes.NewReader(data), geodata.InputAuto, func(feature geodata.Feature) error {
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		if string(bytes.TrimSpace(feature.Properties)) != "null" {
			if err := feature.SetPropertyMap(properties); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(feature)
		if err != nil {
			return err
		}
		result = append(result, string(encoded))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(result)
	return result
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
