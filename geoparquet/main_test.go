package main

import (
	"bytes"
	"encoding/json"
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
	if err := decodeGeoParquet(&parquetData, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFeatureJSON(t, data), sortedFeatureJSON(t, output.Bytes())) {
		t.Fatal("GeoParquet round trip changed Feature content")
	}
}

func TestGeoParquetRejectsGeometryMismatch(t *testing.T) {
	featureJSON := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[2,2]},"properties":{}}`)
	geometry, err := wkb.Marshal(orb.Point{1, 1})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	writer := parquet.NewGenericWriter[parquetFeatureRow](&data)
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
	if _, err := writer.Write([]parquetFeatureRow{{Geometry: geometry, FeatureJSON: featureJSON}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(&data, &output, geodata.OutputJSONL); err == nil {
		t.Fatal("accepted different WKB and feature_json geometries")
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
	if err := decodeGeoParquet(&data, &output, geodata.OutputCollection); err != nil {
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

func TestGeoParquetRejectsUnconvertedCRS(t *testing.T) {
	metadata := geoParquetMetadata{
		Version:       geoParquetVersion,
		PrimaryColumn: "geometry",
		Columns: map[string]geoParquetColumn{
			"geometry": {
				Encoding:      "WKB",
				GeometryTypes: []string{"Point"},
				CRS:           json.RawMessage(`{"type":"ProjectedCRS","name":"example"}`),
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

func sortedFeatureJSON(t *testing.T, data []byte) []string {
	t.Helper()
	var result []string
	if err := geodata.ReadFeatures(bytes.NewReader(data), geodata.InputAuto, func(feature geodata.Feature) error {
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
