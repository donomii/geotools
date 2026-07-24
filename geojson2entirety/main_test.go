package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const realPlaceGeoJSON = `{"type":"FeatureCollection","features":[
  {"type":"Feature","geometry":{"type":"Point","coordinates":[103.83333333333333,1.2833333333333334]},"properties":{"name":"Singapore"}},
  {"type":"Feature","geometry":{"type":"Point","coordinates":[2.2945,48.858222222222224]},"properties":{"name":"Eiffel Tower"}},
  {"type":"Feature","geometry":{"type":"Point","coordinates":[-123.1207,49.2827]},"properties":{}}
]}`

func TestConvertRealPlacesCreatesEntiretyFiles(t *testing.T) {
	prefix, count := convertToEntiretyFiles(t, realPlaceGeoJSON, -1, false, false)
	if count != 3 {
		t.Fatalf("converted %d Features, expected 3", count)
	}
	tagText := readEntiretyFile(t, prefix+".tag_text")
	if string(tagText) != "FAIL\x00Singapore\x00Eiffel Tower\x00" {
		t.Fatalf("tag text is %q", tagText)
	}
	assertFloat64s(t, readEntiretyFile(t, prefix+".tag_points"), []float64{
		-6000, -60000,
		77, -6230,
		2931.4933333333336, -137.67,
	})
	assertFloat64s(t, readEntiretyFile(t, prefix+".map_points"), []float64{2956.962, 7387.242})
	assertInt64s(t, readEntiretyFile(t, prefix+".tag_offset"), []int64{0, 5, 15})
	assertInt64s(t, readEntiretyFile(t, prefix+".tag_index"), []int64{-1, 0, 1})
	assertInt64s(t, readEntiretyFile(t, prefix+".tag_category"), []int64{0, 0, 0})
	if string(readEntiretyFile(t, prefix+".pre_offset")) != "0\n5\n15\n" {
		t.Fatalf("pre-offset file is %q", readEntiretyFile(t, prefix+".pre_offset"))
	}
	if len(readEntiretyFile(t, prefix+".map_data")) != 24 {
		t.Fatalf("map data has %d bytes", len(readEntiretyFile(t, prefix+".map_data")))
	}
}

func TestConvertEntiretyPointsModeWritesEveryFeatureAsPoint(t *testing.T) {
	prefix, count := convertToEntiretyFiles(t, realPlaceGeoJSON, -1, true, false)
	if count != 3 {
		t.Fatalf("converted %d Features", count)
	}
	if len(readEntiretyFile(t, prefix+".map_points")) != 3*16 {
		t.Fatalf("point file has %d bytes", len(readEntiretyFile(t, prefix+".map_points")))
	}
	if len(readEntiretyFile(t, prefix+".map_data")) != 3*24 {
		t.Fatalf("point data file has %d bytes", len(readEntiretyFile(t, prefix+".map_data")))
	}
	if len(readEntiretyFile(t, prefix+".tag_points")) != 0 || len(readEntiretyFile(t, prefix+".tag_text")) != 0 {
		t.Fatal("points mode wrote tag records")
	}
}

func TestConvertEntiretyTagsModeOmitsUnnamedPoints(t *testing.T) {
	prefix, count := convertToEntiretyFiles(t, realPlaceGeoJSON, -1, false, true)
	if count != 3 {
		t.Fatalf("converted %d Features", count)
	}
	if len(readEntiretyFile(t, prefix+".map_points")) != 0 || len(readEntiretyFile(t, prefix+".map_data")) != 0 {
		t.Fatal("tags mode wrote unnamed map points")
	}
	if len(readEntiretyFile(t, prefix+".tag_points")) != 3*16 {
		t.Fatalf("tag point file has %d bytes", len(readEntiretyFile(t, prefix+".tag_points")))
	}
}

func TestConvertEntiretyLimitCountsInputFeaturesExactly(t *testing.T) {
	prefix, count := convertToEntiretyFiles(t, realPlaceGeoJSON, 2, true, false)
	if count != 2 {
		t.Fatalf("converted %d Features, expected 2", count)
	}
	if len(readEntiretyFile(t, prefix+".map_points")) != 2*16 {
		t.Fatalf("point file has %d bytes", len(readEntiretyFile(t, prefix+".map_points")))
	}
}

func TestConvertEntiretyAcceptsArrayAndJSONL(t *testing.T) {
	array := `[
{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"name":"Array"}},
{"type":"Feature","geometry":{"type":"Point","coordinates":[3,4]},"properties":{}}
]`
	_, arrayCount := convertToEntiretyFiles(t, array, -1, false, false)
	if arrayCount != 2 {
		t.Fatalf("array converted %d Features", arrayCount)
	}
	jsonl := `{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"name":"First"}}
{"type":"Feature","geometry":{"type":"Point","coordinates":[3,4]},"properties":{"name":"Second"}}`
	_, jsonlCount := convertToEntiretyFiles(t, jsonl, -1, false, false)
	if jsonlCount != 2 {
		t.Fatalf("JSONL converted %d Features", jsonlCount)
	}
}

func TestConvertEntiretyRejectsNonPointGeometry(t *testing.T) {
	input := `{"type":"Feature","geometry":{"type":"LineString","coordinates":[[1,2],[3,4]]},"properties":{}}`
	outputs, err := openEntiretyOutputs(filepath.Join(t.TempDir(), "invalid"))
	if err != nil {
		t.Fatal(err)
	}
	_, convertErr := convertEntirety(strings.NewReader(input), outputs, -1, false, false)
	closeErr := closeEntiretyOutputs(outputs)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if convertErr == nil || !strings.Contains(convertErr.Error(), `unsupported GeoJSON geometry "LineString"`) {
		t.Fatalf("received error %v", convertErr)
	}
}

func TestConvertEntiretyRejectsNonStringName(t *testing.T) {
	input := `{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"name":42}}`
	outputs, err := openEntiretyOutputs(filepath.Join(t.TempDir(), "invalid"))
	if err != nil {
		t.Fatal(err)
	}
	_, convertErr := convertEntirety(strings.NewReader(input), outputs, -1, false, false)
	closeErr := closeEntiretyOutputs(outputs)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if convertErr == nil || !strings.Contains(convertErr.Error(), "Feature name must be a string") {
		t.Fatalf("received error %v", convertErr)
	}
}

func TestSpatialIndexRetainsDuplicateNegativeCoordinates(t *testing.T) {
	tree = nil
	treeIndexAdd("Vancouver", -123.1207, 49.2827)
	treeIndexAdd("Duplicate", -123.1207, 49.2827)
	treeIndexAdd("Singapore", 103.83333333333333, 1.2833333333333334)
	packed := buildFinal()
	entries := make([]leaf, 0)
	IterateMp(packed, func(_, _ float64, item leaf) {
		entries = append(entries, item)
	})
	if len(entries) != 3 {
		t.Fatalf("spatial index retained %d entries", len(entries))
	}
	if entries[0].Text != "Vancouver" || entries[1].Text != "Duplicate" || entries[0].Longitude != -123.1207 {
		t.Fatalf("duplicate entries are %#v", entries)
	}
}

func convertToEntiretyFiles(t *testing.T, input string, limit int64, pointsOnly, tagsOnly bool) (string, int64) {
	t.Helper()
	prefix := filepath.Join(t.TempDir(), "places")
	outputs, err := openEntiretyOutputs(prefix)
	if err != nil {
		t.Fatal(err)
	}
	count, convertErr := convertEntirety(strings.NewReader(input), outputs, limit, pointsOnly, tagsOnly)
	closeErr := closeEntiretyOutputs(outputs)
	if convertErr != nil {
		t.Fatal(convertErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return prefix, count
}

func readEntiretyFile(t *testing.T, filename string) []byte {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertFloat64s(t *testing.T, data []byte, expected []float64) {
	t.Helper()
	if len(data) != len(expected)*8 {
		t.Fatalf("float data has %d bytes, expected %d", len(data), len(expected)*8)
	}
	reader := bytes.NewReader(data)
	for index, expectedValue := range expected {
		var actual float64
		if err := binary.Read(reader, binary.LittleEndian, &actual); err != nil {
			t.Fatal(err)
		}
		if math.Abs(actual-expectedValue) > 1e-9 {
			t.Fatalf("float %d is %v, expected %v", index, actual, expectedValue)
		}
	}
}

func assertInt64s(t *testing.T, data []byte, expected []int64) {
	t.Helper()
	if len(data) != len(expected)*8 {
		t.Fatalf("integer data has %d bytes, expected %d", len(data), len(expected)*8)
	}
	reader := bytes.NewReader(data)
	for index, expectedValue := range expected {
		var actual int64
		if err := binary.Read(reader, binary.LittleEndian, &actual); err != nil {
			t.Fatal(err)
		}
		if actual != expectedValue {
			t.Fatalf("integer %d is %d, expected %d", index, actual, expectedValue)
		}
	}
}
