package geodata

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestMVTRoundTripPreservesExactStringAndNumericIDs(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[
		{"type":"Feature","id":"Q334","geometry":{"type":"Point","coordinates":[-20,10]},"properties":{"name":"string"}},
		{"type":"Feature","id":-7.5,"geometry":{"type":"Point","coordinates":[20,-10]},"properties":{"name":"number"}},
		{"type":"Feature","id":1e400,"geometry":{"type":"Point","coordinates":[0,0]},"properties":{"name":"large number"}}
	]}`)
	output := roundTripMVTForTest(t, input)
	ids := make(map[string]bool)
	if err := ReadFeatures(bytes.NewReader(output), InputAuto, func(feature Feature) error {
		ids[feature.EncodedID()] = true
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		if properties[DefaultMVTIDProperty] != nil {
			t.Fatalf("decoded Feature retained reserved id property: %s", feature.Properties)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !ids[`"Q334"`] || !ids["-7.5"] || !ids["1e400"] || len(ids) != 3 {
		t.Fatalf("decoded ids are %v", ids)
	}
}

func TestMVTRoundTripPreservesPolygonHolesAndMultiPolygons(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[
		{"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[-60,-30],[-20,-30],[-20,30],[-60,30],[-60,-30]],[[-50,-10],[-50,10],[-30,10],[-30,-10],[-50,-10]]]},"properties":{"name":"polygon"}},
		{"type":"Feature","geometry":{"type":"MultiPolygon","coordinates":[[[[10,-20],[30,-20],[30,0],[10,0],[10,-20]]],[[[40,10],[60,10],[60,30],[40,30],[40,10]]]]},"properties":{"name":"multipolygon"}}
	]}`)
	output := roundTripMVTForTest(t, input)
	seen := make(map[string]bool)
	if err := ReadFeatures(bytes.NewReader(output), InputAuto, func(feature Feature) error {
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		var name string
		if err := json.Unmarshal(properties["name"], &name); err != nil {
			return err
		}
		seen[name] = true
		switch name {
		case "polygon":
			var geometry struct {
				Type        string            `json:"type"`
				Coordinates []json.RawMessage `json:"coordinates"`
			}
			if err := json.Unmarshal(feature.Geometry, &geometry); err != nil {
				return err
			}
			if geometry.Type != "Polygon" || len(geometry.Coordinates) != 2 {
				t.Fatalf("polygon decoded as %s", feature.Geometry)
			}
		case "multipolygon":
			var geometry struct {
				Type        string            `json:"type"`
				Coordinates []json.RawMessage `json:"coordinates"`
			}
			if err := json.Unmarshal(feature.Geometry, &geometry); err != nil {
				return err
			}
			if geometry.Type != "MultiPolygon" || len(geometry.Coordinates) != 2 {
				t.Fatalf("multipolygon decoded as %s", feature.Geometry)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !seen["polygon"] || !seen["multipolygon"] {
		t.Fatalf("decoded geometry names are %v", seen)
	}
}

func TestMVTRejectsReservedIDPropertyInInput(t *testing.T) {
	input := strings.NewReader(`{"type":"Feature","id":"source","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"__geotools_geojson_id":"occupied"}}`)
	settings := defaultMVTEncodeSettingsForTest()
	if err := EncodeMVT(input, &bytes.Buffer{}, InputAuto, settings); err == nil {
		t.Fatal("accepted a source Feature using the reserved id property")
	}
}

func TestMVTDecodeRejectsInvalidPreservedID(t *testing.T) {
	input := strings.NewReader(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"__geotools_geojson_id":123}}`)
	settings := defaultMVTEncodeSettingsForTest()
	settings.IDProperty = ""
	var tile bytes.Buffer
	if err := EncodeMVT(input, &tile, InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	decodeSettings := defaultMVTDecodeSettingsForTest()
	err := DecodeMVT(&tile, &bytes.Buffer{}, OutputCollection, decodeSettings)
	if err == nil || !strings.Contains(err.Error(), "expected a string containing a GeoJSON id") {
		t.Fatalf("invalid preserved id produced error %q", err)
	}
}

func TestMVTRejectsConflictingLayerAndIDProperties(t *testing.T) {
	input := strings.NewReader(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"routing":"places"}}`)
	encodeSettings := defaultMVTEncodeSettingsForTest()
	encodeSettings.LayerProperty = "routing"
	encodeSettings.IDProperty = "routing"
	if err := EncodeMVT(input, &bytes.Buffer{}, InputAuto, encodeSettings); err == nil {
		t.Fatal("accepted one property for layer selection and id preservation")
	}
	decodeSettings := defaultMVTDecodeSettingsForTest()
	decodeSettings.AllLayers = true
	decodeSettings.LayerProperty = "routing"
	decodeSettings.IDProperty = "routing"
	if err := DecodeMVT(bytes.NewReader(nil), &bytes.Buffer{}, OutputCollection, decodeSettings); err == nil {
		t.Fatal("accepted one property for decoded layer names and id preservation")
	}
}

func TestMVTDecodeRejectsLayerPropertyConflict(t *testing.T) {
	input := strings.NewReader(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"source_layer":"wrong"}}`)
	encodeSettings := defaultMVTEncodeSettingsForTest()
	var tile bytes.Buffer
	if err := EncodeMVT(input, &tile, InputAuto, encodeSettings); err != nil {
		t.Fatal(err)
	}
	decodeSettings := defaultMVTDecodeSettingsForTest()
	decodeSettings.AllLayers = true
	decodeSettings.LayerProperty = "source_layer"
	err := DecodeMVT(&tile, &bytes.Buffer{}, OutputCollection, decodeSettings)
	if err == nil || !strings.Contains(err.Error(), "conflicts with the layer name") {
		t.Fatalf("layer property conflict produced error %q", err)
	}
}

func TestMVTRejectsUnsupportedCoordinatesAndGeometry(t *testing.T) {
	cases := map[string]string{
		"three-dimensional": `{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0,3]},"properties":{}}`,
		"latitude":          `{"type":"Feature","geometry":{"type":"Point","coordinates":[0,89]},"properties":{}}`,
		"collection":        `{"type":"Feature","geometry":{"type":"GeometryCollection","geometries":[{"type":"Point","coordinates":[0,0]}]},"properties":{}}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if err := EncodeMVT(strings.NewReader(input), &bytes.Buffer{}, InputAuto, defaultMVTEncodeSettingsForTest()); err == nil {
				t.Fatalf("accepted unsupported %s input", name)
			}
		})
	}
}

func TestMVTDecodeRejectsInputAboveConfiguredLimit(t *testing.T) {
	var tile bytes.Buffer
	input := strings.NewReader(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{}}`)
	if err := EncodeMVT(input, &tile, InputAuto, defaultMVTEncodeSettingsForTest()); err != nil {
		t.Fatal(err)
	}
	for _, gzipInput := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "gzip"}[gzipInput], func(t *testing.T) {
			data := tile.Bytes()
			if gzipInput {
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				if _, err := writer.Write(data); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				data = compressed.Bytes()
			}
			settings := defaultMVTDecodeSettingsForTest()
			settings.Gzip = gzipInput
			settings.MaxInputBytes = int64(tile.Len() - 1)
			err := DecodeMVT(bytes.NewReader(data), &bytes.Buffer{}, OutputCollection, settings)
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("configured maximum is %d bytes", settings.MaxInputBytes)) {
				t.Fatalf("oversized decoded tile produced error %q", err)
			}
		})
	}
}

func TestMVTEncodeRejectsShortOutputWrite(t *testing.T) {
	input := strings.NewReader(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{}}`)
	if err := EncodeMVT(input, shortMVTWriter{}, InputAuto, defaultMVTEncodeSettingsForTest()); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short MVT output write produced error %v", err)
	}
}

type shortMVTWriter struct{}

func (shortMVTWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

func roundTripMVTForTest(t *testing.T, input []byte) []byte {
	t.Helper()
	var tile bytes.Buffer
	if err := EncodeMVT(bytes.NewReader(input), &tile, InputAuto, defaultMVTEncodeSettingsForTest()); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := DecodeMVT(&tile, &output, OutputCollection, defaultMVTDecodeSettingsForTest()); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func defaultMVTEncodeSettingsForTest() MVTEncodeSettings {
	return MVTEncodeSettings{
		Zoom: 0, X: 0, Y: 0, Layer: "features", Extent: 4096, Buffer: 0,
		Simplify: 0, Gzip: false, IDProperty: DefaultMVTIDProperty,
	}
}

func defaultMVTDecodeSettingsForTest() MVTDecodeSettings {
	return MVTDecodeSettings{
		Zoom: 0, X: 0, Y: 0, Layer: "features", Gzip: false, IDProperty: DefaultMVTIDProperty,
	}
}
