package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/donomii/geotools/geodata"
	"github.com/parquet-go/parquet-go"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/wkb"
)

func TestGeoParquetRealDataRoundTrip(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	var parquetData bytes.Buffer
	if err := encodeGeoParquet(bytes.NewReader(data), &parquetData, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(parquetData.Bytes()), int64(parquetData.Len()))
	if err != nil {
		t.Fatal(err)
	}
	metadata, exists := file.Lookup("geo")
	if !exists {
		t.Fatal("encoded Parquet has no geo metadata")
	}
	if err := validateGeoParquetMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	if file.NumRows() != 5 {
		t.Fatalf("GeoParquet contains %d rows; expected 5", file.NumRows())
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(parquetData.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	expected := sortedFeatureJSON(t, data)
	actual := sortedFeatureJSON(t, output.Bytes())
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("GeoParquet round trip changed Feature content:\nexpected %v\nactual   %v", expected, actual)
	}
}

func TestGeoParquetWritesTypedPropertyColumns(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := encodeGeoParquet(bytes.NewReader(data), &encoded, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(encoded.Bytes()), int64(encoded.Len()))
	if err != nil {
		t.Fatal(err)
	}
	expectedKinds := map[string]parquet.Kind{
		"name":       parquet.ByteArray,
		"population": parquet.Int64,
		"capital":    parquet.Boolean,
		"aliases":    parquet.ByteArray,
	}
	for name, expectedKind := range expectedKinds {
		leaf, exists := file.Schema().Lookup(name)
		if !exists {
			t.Fatalf("typed property column %q is absent", name)
		}
		if leaf.Node.Type().Kind() != expectedKind {
			t.Fatalf("property column %q has kind %s; expected %s", name, leaf.Node.Type().Kind(), expectedKind)
		}
	}
	if _, exists := file.Schema().Lookup("feature_json"); exists {
		t.Fatal("GeoParquet still contains an opaque feature_json column")
	}
}

func TestGeoParquetStreamingWKBUsesBoundedSchemaAndRoundTrips(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[{"type":"Feature","id":"two","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"name":"first","nullable":null}},{"type":"Feature","id":"three","geometry":{"type":"LineString","coordinates":[[3,4,5],[6,7,8]]},"properties":{"name":"second","late":{"rank":2}}},{"type":"Feature","id":"none","geometry":null,"properties":{}}]}`)
	settings := geoParquetEncodeSettings{
		InputMode: geodata.InputAuto, CRS: geodata.CRSCRS84, GeometryEncoding: "wkb", Streaming: true,
	}
	var encoded bytes.Buffer
	if err := encodeGeoParquetWithSettings(bytes.NewBuffer(input), &encoded, settings); err != nil {
		t.Fatal(err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(encoded.Bytes()), int64(encoded.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := file.Schema().Lookup(geoParquetPropertiesColumn); !exists {
		t.Fatalf("streaming GeoParquet schema has no %q JSON property column", geoParquetPropertiesColumn)
	}
	if _, exists := file.Schema().Lookup("name"); exists {
		t.Fatal("streaming GeoParquet unexpectedly inferred a typed name column")
	}
	metadataText, exists := file.Lookup("geo")
	if !exists {
		t.Fatal("streaming GeoParquet has no geo metadata")
	}
	metadata, err := parseGeoParquetMetadata(metadataText)
	if err != nil {
		t.Fatal(err)
	}
	primary := metadata.Columns[geoParquetGeometryColumn]
	if len(primary.GeometryTypes) != 2 || primary.BBox != nil || primary.Covering != nil {
		t.Fatalf("streaming mixed-dimension geometry metadata is %#v", primary)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFeatureJSON(t, input), sortedFeatureJSON(t, output.Bytes())) {
		t.Fatalf("streaming GeoParquet round trip changed Feature content: %s", output.Bytes())
	}
}

func TestGeoParquetStreamingSecondaryGeometryAndNullProperties(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"centroid":{"type":"Point","coordinates":[1,2]},"name":"area"}},{"type":"Feature","geometry":null,"properties":{"centroid":null,"name":"missing"}}]}`)
	settings := geoParquetEncodeSettings{
		InputMode: geodata.InputAuto, CRS: geodata.CRSCRS84, GeometryEncoding: "wkb",
		GeometryProperties: []string{"centroid"}, Streaming: true,
	}
	var encoded bytes.Buffer
	if err := encodeGeoParquetWithSettings(bytes.NewBuffer(input), &encoded, settings); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFeatureJSON(t, input), sortedFeatureJSON(t, output.Bytes())) {
		t.Fatalf("streaming secondary geometry round trip changed Feature content: %s", output.Bytes())
	}
	nullProperties := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":null}`)
	encoded.Reset()
	settings.GeometryProperties = nil
	if err := encodeGeoParquetWithSettings(bytes.NewBuffer(nullProperties), &encoded, settings); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFeatureJSON(t, nullProperties), sortedFeatureJSON(t, output.Bytes())) {
		t.Fatalf("streaming null properties round trip changed Feature content: %s", output.Bytes())
	}
}

func TestGeoParquetEmptyCollectionRoundTrip(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[]}`)
	var encoded bytes.Buffer
	if err := encodeGeoParquet(bytes.NewReader(input), &encoded, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if string(output.Bytes()) != `{"type":"FeatureCollection","features":[]}`+"\n" {
		t.Fatalf("empty GeoParquet decoded as %s", output.Bytes())
	}
}

func TestGeoParquetDecodeRejectsNonSeekableInputWithoutBuffering(t *testing.T) {
	var encoded bytes.Buffer
	if err := encodeGeoParquet(bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}`), &encoded, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	err := decodeGeoParquet(bytes.NewBuffer(encoded.Bytes()), &bytes.Buffer{}, geodata.OutputJSONL)
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("seekable input")) {
		t.Fatalf("non-seekable GeoParquet decode returned %v", err)
	}
}

func TestGeoParquetPreservesNullPropertiesBBoxAndForeignMembers(t *testing.T) {
	input := []byte(`{"type":"Feature","id":"foreign","bbox":[1,2,1,2],"geometry":{"type":"Point","coordinates":[1,2]},"properties":null,"source":{"name":"fixture"}}`)
	var encoded bytes.Buffer
	if err := encodeGeoParquet(bytes.NewReader(input), &encoded, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFeatureJSON(t, input), sortedFeatureJSON(t, output.Bytes())) {
		t.Fatalf("GeoParquet changed null properties, bbox, or foreign members: %s", output.Bytes())
	}
}

func TestGeoParquetNullGeometryRoundTrip(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[{"type":"Feature","id":"missing","geometry":null,"properties":{"name":"unknown"}},{"type":"Feature","id":"known","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"name":"Singapore"}}]}`)
	for _, encoding := range []string{"wkb", "native"} {
		t.Run(encoding, func(t *testing.T) {
			var encoded bytes.Buffer
			settings := geoParquetEncodeSettings{InputMode: geodata.InputAuto, CRS: geodata.CRSCRS84, GeometryEncoding: encoding}
			if err := encodeGeoParquetWithSettings(bytes.NewReader(input), &encoded, settings); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sortedFeatureJSON(t, input), sortedFeatureJSON(t, output.Bytes())) {
				t.Fatalf("%s GeoParquet changed null geometry data: %s", encoding, output.Bytes())
			}
		})
	}
}

func TestGeoParquetSecondaryGeometryColumnsRoundTrip(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[{"type":"Feature","id":"area","geometry":null,"properties":{"name":"district","centroid":{"type":"Point","coordinates":[103.82,1.35]}}},{"type":"Feature","id":"point","geometry":{"type":"Point","coordinates":[103.81,1.36]},"properties":{"name":"station","centroid":null}}]}`)
	var encoded bytes.Buffer
	settings := geoParquetEncodeSettings{
		InputMode: geodata.InputAuto, CRS: geodata.CRSCRS84, GeometryEncoding: "wkb",
		GeometryProperties: []string{"centroid"},
	}
	if err := encodeGeoParquetWithSettings(bytes.NewReader(input), &encoded, settings); err != nil {
		t.Fatal(err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(encoded.Bytes()), int64(encoded.Len()))
	if err != nil {
		t.Fatal(err)
	}
	metadataText, exists := file.Lookup("geo")
	if !exists {
		t.Fatal("encoded GeoParquet has no geo metadata")
	}
	metadata, err := parseGeoParquetMetadata(metadataText)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Columns["centroid"].Encoding != "WKB" {
		t.Fatalf("secondary geometry metadata is %#v", metadata.Columns["centroid"])
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFeatureJSON(t, input), sortedFeatureJSON(t, output.Bytes())) {
		t.Fatalf("secondary geometry round trip changed Feature content: %s", output.Bytes())
	}
}

func TestDecodeExternalGeoParquetColumns(t *testing.T) {
	type externalRow struct {
		Geom       []byte `parquet:"geom"`
		Name       string `parquet:"name"`
		Population int64  `parquet:"population"`
		Capital    bool   `parquet:"capital"`
	}
	geometry, err := wkb.Marshal(orb.Point{103.8198, 1.3521})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	writer := parquet.NewGenericWriter[externalRow](&data)
	metadata := geoParquetMetadata{
		Version:       geoParquetVersion,
		PrimaryColumn: "geom",
		Columns:       map[string]geoParquetColumn{"geom": {Encoding: "WKB", GeometryTypes: []string{"Point"}}},
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetKeyValueMetadata("geo", string(encodedMetadata))
	if _, err := writer.Write([]externalRow{{Geom: geometry, Name: "Singapore", Population: 5917600, Capital: true}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(data.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
		count++
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		if string(properties["name"]) != `"Singapore"` || string(properties["population"]) != "5917600" || string(properties["capital"]) != "true" {
			t.Fatalf("external GeoParquet properties are %v", properties)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("decoded %d external GeoParquet rows; expected 1", count)
	}
}

func TestDecodeExternalGeoParquetNullAndSecondaryGeometryColumns(t *testing.T) {
	type externalRow struct {
		Geometry []byte `parquet:"geometry,optional"`
		Centroid []byte `parquet:"centroid,optional"`
		Name     string `parquet:"name"`
	}
	primary, err := wkb.Marshal(orb.Point{103.81, 1.36})
	if err != nil {
		t.Fatal(err)
	}
	centroid, err := wkb.Marshal(orb.Point{103.82, 1.35})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	writer := parquet.NewGenericWriter[externalRow](&data)
	metadata := geoParquetMetadata{
		Version:       geoParquetVersion,
		PrimaryColumn: "geometry",
		Columns: map[string]geoParquetColumn{
			"geometry": {Encoding: "WKB", GeometryTypes: []string{"Point"}},
			"centroid": {Encoding: "WKB", GeometryTypes: []string{"Point"}},
		},
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetKeyValueMetadata("geo", string(encodedMetadata))
	if _, err := writer.Write([]externalRow{
		{Centroid: centroid, Name: "null primary"},
		{Geometry: primary, Name: "null secondary"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(data.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := collectGeoParquetFeaturesForTest(t, output.Bytes())
	if len(features) != 2 || string(features[0].Geometry) != "null" {
		t.Fatalf("external null primary geometry decoded as %s", output.Bytes())
	}
	firstProperties, err := features[0].PropertyMap()
	if err != nil {
		t.Fatal(err)
	}
	secondProperties, err := features[1].PropertyMap()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(firstProperties["centroid"], []byte(`"type":"Point"`)) || string(secondProperties["centroid"]) != "null" {
		t.Fatalf("external secondary geometries decoded as first=%s second=%s", firstProperties["centroid"], secondProperties["centroid"])
	}
}

func TestDecodeNativeGeoParquetRejectsPartiallyNullCoordinates(t *testing.T) {
	values := [][]parquet.Value{
		{
			parquet.NullValue().Level(0, 0, 0),
			parquet.DoubleValue(2).Level(0, 1, 0),
		},
		{
			parquet.DoubleValue(1).Level(0, 1, 1),
			parquet.NullValue().Level(0, 0, 1),
		},
	}
	paths := [][]string{{"geometry", "x"}, {"geometry", "y"}}
	if _, err := decodeGeoParquetGeometry(values, paths, "geometry", "point"); err == nil {
		t.Fatal("accepted native GeoParquet coordinate slots with independently null axes")
	}
}

func TestDecodeDuckDBGeoParquetFixture(t *testing.T) {
	data, err := os.ReadFile("../testdata/external/duckdb_geoparquet.parquet")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(data), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := collectGeoParquetFeaturesForTest(t, output.Bytes())
	if len(features) != 1 {
		t.Fatalf("decoded %d DuckDB GeoParquet rows; expected 1", len(features))
	}
	properties, err := features[0].PropertyMap()
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"name":       `"Singapore"`,
		"population": `5917600`,
		"profile":    `{"country":"Singapore","rank":1}`,
		"tags":       `["city","capital"]`,
	}
	for name, value := range expected {
		if string(properties[name]) != value {
			t.Fatalf("DuckDB property %q is %s; expected %s", name, properties[name], value)
		}
	}
}

func TestGeoParquetRejectsUnconvertedCRS(t *testing.T) {
	metadata := geoParquetMetadata{
		Version:       geoParquetVersion,
		PrimaryColumn: "geometry",
		Columns: map[string]geoParquetColumn{
			"geometry": {
				Encoding:      "WKB",
				GeometryTypes: []string{"Point"},
				CRS:           json.RawMessage(`{"type":"ProjectedCRS","name":"example","id":{"authority":"EPSG","code":32661}}`),
			},
		},
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGeoParquetMetadata(string(encoded)); err == nil {
		t.Fatal("accepted GeoParquet coordinates in an unconverted CRS")
	}
}

func TestGeoParquetThreeDimensionalProjectedRoundTrip(t *testing.T) {
	input := []byte(`{"type":"Feature","id":"three-dimensional","geometry":{"type":"Point","coordinates":[103.8198,1.3521,12.5]},"properties":{"name":"Singapore","height":12.5}}`)
	var encoded bytes.Buffer
	settings := geoParquetEncodeSettings{InputMode: geodata.InputAuto, CRS: geodata.CRSEPSG3857, GeometryEncoding: "wkb"}
	if err := encodeGeoParquetWithSettings(bytes.NewReader(input), &encoded, settings); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := collectGeoParquetFeaturesForTest(t, output.Bytes())
	if len(features) != 1 || features[0].EncodedID() != `"three-dimensional"` {
		t.Fatalf("3D projected round trip returned %#v", features)
	}
	var geometry struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(features[0].Geometry, &geometry); err != nil {
		t.Fatal(err)
	}
	if math.Abs(geometry.Coordinates[0]-103.8198) > 1e-9 || math.Abs(geometry.Coordinates[1]-1.3521) > 1e-9 || geometry.Coordinates[2] != 12.5 {
		t.Fatalf("3D projected round trip returned coordinates %v", geometry.Coordinates)
	}
}

func TestGeoParquetUTMRoundTrip(t *testing.T) {
	input := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"name":"Singapore"}}`)
	var encoded bytes.Buffer
	settings := geoParquetEncodeSettings{InputMode: geodata.InputAuto, CRS: "EPSG:32648", GeometryEncoding: "wkb"}
	if err := encodeGeoParquetWithSettings(bytes.NewReader(input), &encoded, settings); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := collectGeoParquetFeaturesForTest(t, output.Bytes())
	var geometry struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if len(features) != 1 || json.Unmarshal(features[0].Geometry, &geometry) != nil ||
		math.Abs(geometry.Coordinates[0]-103.8198) > 1e-8 || math.Abs(geometry.Coordinates[1]-1.3521) > 1e-8 {
		t.Fatalf("UTM GeoParquet round trip returned %s", output.Bytes())
	}
}

func TestGeoParquetEPSG4326UsesLatitudeLongitudeAxisOrder(t *testing.T) {
	input := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{}}`)
	var encoded bytes.Buffer
	settings := geoParquetEncodeSettings{InputMode: geodata.InputAuto, CRS: geodata.CRSEPSG4326, GeometryEncoding: "wkb"}
	if err := encodeGeoParquetWithSettings(bytes.NewReader(input), &encoded, settings); err != nil {
		t.Fatal(err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(encoded.Bytes()), int64(encoded.Len()))
	if err != nil {
		t.Fatal(err)
	}
	rows := file.RowGroups()[0].Rows()
	rowBuffer := make([]parquet.Row, 1)
	count, err := rows.ReadRows(rowBuffer)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("read %d GeoParquet rows; expected 1", count)
	}
	values := make([][]parquet.Value, len(file.Schema().Columns()))
	rowBuffer[0].Range(func(columnIndex int, columnValues []parquet.Value) bool {
		values[columnIndex] = columnValues
		return true
	})
	var geometryBytes []byte
	for index, path := range file.Schema().Columns() {
		if len(path) == 1 && path[0] == geoParquetGeometryColumn {
			geometryBytes, err = singleParquetBytes(values[index], geoParquetGeometryColumn)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	storedGeometry, err := wkb.Unmarshal(geometryBytes)
	if err != nil {
		t.Fatal(err)
	}
	point := storedGeometry.(orb.Point)
	if !reflect.DeepEqual(point, orb.Point{1.3521, 103.8198}) {
		t.Fatalf("EPSG:4326 GeoParquet stores coordinates %v; expected latitude then longitude", point)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFeatureJSON(t, input), sortedFeatureJSON(t, output.Bytes())) {
		t.Fatalf("EPSG:4326 GeoParquet round trip changed Feature content: %s", output.Bytes())
	}
}

func TestGeoParquetMixedCoordinateDimensionsOmitCovering(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"name":"2d"}},{"type":"Feature","geometry":{"type":"Point","coordinates":[3,4,5]},"properties":{"name":"3d"}}]}`)
	var encoded bytes.Buffer
	if err := encodeGeoParquet(bytes.NewReader(input), &encoded, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(encoded.Bytes()), int64(encoded.Len()))
	if err != nil {
		t.Fatal(err)
	}
	metadataText, exists := file.Lookup("geo")
	if !exists {
		t.Fatal("mixed-dimension GeoParquet has no geo metadata")
	}
	metadata, err := parseGeoParquetMetadata(metadataText)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Columns[geoParquetGeometryColumn].Covering != nil || metadata.Columns[geoParquetGeometryColumn].BBox != nil {
		t.Fatalf("mixed-dimension metadata contains an invalid covering or bbox: %#v", metadata.Columns[geoParquetGeometryColumn])
	}
	if _, exists := file.Schema().Lookup(geoParquetCoveringColumn, "xmin"); exists {
		t.Fatal("mixed-dimension GeoParquet contains a fixed-dimension covering column")
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFeatureJSON(t, input), sortedFeatureJSON(t, output.Bytes())) {
		t.Fatalf("mixed-dimension GeoParquet round trip changed Feature content: %s", output.Bytes())
	}
}

func TestNativeGeoParquetRoundTripsGeometryTypes(t *testing.T) {
	cases := map[string]string{
		"point":           `{"type":"Point","coordinates":[1,2]}`,
		"point-z":         `{"type":"Point","coordinates":[1,2,3]}`,
		"linestring":      `{"type":"LineString","coordinates":[[1,2],[3,4]]}`,
		"polygon":         `{"type":"Polygon","coordinates":[[[0,0],[4,0],[4,4],[0,0]],[[1,1],[1,2],[2,1],[1,1]]]}`,
		"multipoint":      `{"type":"MultiPoint","coordinates":[[1,2],[3,4]]}`,
		"multilinestring": `{"type":"MultiLineString","coordinates":[[[1,2],[3,4]],[[5,6],[7,8]]]}`,
		"multipolygon":    `{"type":"MultiPolygon","coordinates":[[[[0,0],[2,0],[2,2],[0,0]]],[[[3,3],[5,3],[5,5],[3,3]]]]}`,
	}
	for name, geometry := range cases {
		t.Run(name, func(t *testing.T) {
			input := []byte(`{"type":"Feature","geometry":` + geometry + `,"properties":{"name":"native"}}`)
			var encoded bytes.Buffer
			settings := geoParquetEncodeSettings{InputMode: geodata.InputAuto, CRS: geodata.CRSCRS84, GeometryEncoding: "native"}
			if err := encodeGeoParquetWithSettings(bytes.NewReader(input), &encoded, settings); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := decodeGeoParquet(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sortedFeatureJSON(t, input), sortedFeatureJSON(t, output.Bytes())) {
				t.Fatalf("native %s round trip changed Feature:\n%s", name, output.Bytes())
			}
		})
	}
}

func TestDecodeNestedExternalGeoParquetColumns(t *testing.T) {
	type profile struct {
		Name string `parquet:"name"`
		Rank int64  `parquet:"rank"`
	}
	type externalRow struct {
		Geometry []byte  `parquet:"geometry"`
		Profile  profile `parquet:"profile"`
	}
	geometry, err := wkb.Marshal(orb.Point{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	writer := parquet.NewGenericWriter[externalRow](&data)
	metadata := geoParquetMetadata{
		Version:       geoParquetVersion,
		PrimaryColumn: "geometry",
		Columns:       map[string]geoParquetColumn{"geometry": {Encoding: "WKB", GeometryTypes: []string{"Point"}}},
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetKeyValueMetadata("geo", string(encodedMetadata))
	if _, err := writer.Write([]externalRow{{Geometry: geometry, Profile: profile{Name: "source", Rank: 7}}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(data.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := collectGeoParquetFeaturesForTest(t, output.Bytes())
	properties, err := features[0].PropertyMap()
	if err != nil {
		t.Fatal(err)
	}
	if string(properties["profile"]) != `{"name":"source","rank":7}` {
		t.Fatalf("nested profile is %s", properties["profile"])
	}
}

func collectGeoParquetFeaturesForTest(t *testing.T, data []byte) []geodata.Feature {
	t.Helper()
	var features []geodata.Feature
	if err := geodata.ReadFeatures(bytes.NewReader(data), geodata.InputAuto, func(feature geodata.Feature) error {
		features = append(features, feature)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return features
}

func sortedFeatureJSON(t *testing.T, data []byte) []string {
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
