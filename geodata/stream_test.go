package geodata

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestReadRealPlaces(t *testing.T) {
	data := readRealPlaces(t)
	var types []string
	var positions int64
	err := ReadFeatures(bytes.NewReader(data), InputAuto, func(feature Feature) error {
		summary, err := ValidateFeature(feature, ValidationOptions{})
		if err != nil {
			return err
		}
		types = append(types, summary.Type)
		positions += summary.PositionCount
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedTypes := []string{"Point", "Point", "Point", "LineString", "Polygon"}
	if !reflect.DeepEqual(types, expectedTypes) {
		t.Fatalf("geometry types are %v; expected %v", types, expectedTypes)
	}
	if positions != 11 {
		t.Fatalf("position count is %d; expected 11", positions)
	}
}

func TestFeatureWriterRoundTripsAllFormats(t *testing.T) {
	source := readFeaturesForTest(t, readRealPlaces(t), InputAuto)
	formats := []OutputMode{OutputJSONL, OutputCollection, OutputSequence}
	for _, format := range formats {
		t.Run(string(format), func(t *testing.T) {
			var output bytes.Buffer
			writer := NewFeatureWriter(&output, format)
			if err := writer.Start(); err != nil {
				t.Fatal(err)
			}
			for _, feature := range source {
				if err := writer.Write(feature); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			mode := InputAuto
			if format == OutputSequence {
				mode = InputSequence
			}
			roundTripped := readFeaturesForTest(t, output.Bytes(), mode)
			if !reflect.DeepEqual(canonicalFeatures(t, roundTripped), canonicalFeatures(t, source)) {
				t.Fatalf("%s round trip changed Features", format)
			}
		})
	}
}

func TestSequenceAllowsMultilineJSON(t *testing.T) {
	input := "\x1e{\n\"type\":\"Feature\",\n\"geometry\":{\"type\":\"Point\",\"coordinates\":[1,2]},\n\"properties\":{}\n}\n"
	features := readFeaturesForTest(t, []byte(input), InputSequence)
	if len(features) != 1 {
		t.Fatalf("sequence returned %d Features; expected 1", len(features))
	}
}

func TestSequenceRejectsMultipleValuesInOneRecord(t *testing.T) {
	input := "\x1e" +
		`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}` +
		`{"type":"Feature","geometry":{"type":"Point","coordinates":[3,4]},"properties":{}}` + "\n"
	if err := ReadFeatures(strings.NewReader(input), InputSequence, func(feature Feature) error { return nil }); err == nil {
		t.Fatal("accepted two JSON values in one sequence record")
	}
}

func TestReadFeaturesRejectsInvalidTopLevelValues(t *testing.T) {
	cases := []string{
		"",
		`42`,
		`{"type":"FeatureCollection"}`,
		`{"type":"Point","coordinates":[1,2]}`,
		"\x1e42\n",
	}
	for _, input := range cases {
		t.Run(strings.ReplaceAll(input, "\n", " "), func(t *testing.T) {
			err := ReadFeatures(strings.NewReader(input), InputAuto, func(feature Feature) error { return nil })
			if err == nil {
				t.Fatalf("accepted invalid input %q", input)
			}
		})
	}
}

func readRealPlaces(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readFeaturesForTest(t *testing.T, data []byte, mode InputMode) []Feature {
	t.Helper()
	var features []Feature
	if err := ReadFeatures(bytes.NewReader(data), mode, func(feature Feature) error {
		features = append(features, feature)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return features
}

func canonicalFeatures(t *testing.T, features []Feature) []string {
	t.Helper()
	result := make([]string, 0, len(features))
	for _, feature := range features {
		encoded, err := json.Marshal(feature)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, string(encoded))
	}
	return result
}
