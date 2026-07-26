package geodata

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestSequenceRejectsMissingLineFeedsAndEmptyRecords(t *testing.T) {
	feature := `{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}`
	for _, input := range []string{
		"\x1e" + feature,
		"\x1e\n",
		"\x1e" + feature + "\n\x1e",
		"\x1e" + feature + "\x1e" + feature + "\n",
	} {
		if err := ReadFeatures(strings.NewReader(input), InputSequence, func(feature Feature) error { return nil }); err == nil {
			t.Fatalf("accepted invalid GeoJSON sequence %q", input)
		}
	}
}

func TestAutoDetectsSequenceAfterLeadingWhitespace(t *testing.T) {
	input := " \n\t\x1e" + `{"type":"Feature","id":"one","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}` + "\n"
	features := readFeaturesForTest(t, []byte(input), InputAuto)
	if len(features) != 1 || features[0].EncodedID() != `"one"` {
		t.Fatalf("auto-detected sequence returned %#v", features)
	}
}

func TestReadFeaturesAcceptsMultipleTopLevelFeatures(t *testing.T) {
	input := `{"type":"Feature","id":1,"geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}` + "\n" +
		`{"type":"Feature","id":2,"geometry":{"type":"Point","coordinates":[3,4]},"properties":{}}` + "\n"
	features := readFeaturesForTest(t, []byte(input), InputAuto)
	if len(features) != 2 || features[0].EncodedID() != "1" || features[1].EncodedID() != "2" {
		t.Fatalf("JSONL returned %#v", features)
	}
}

func TestReadFeaturesReturnsVisitorError(t *testing.T) {
	expected := errors.New("visitor rejected Feature")
	input := strings.NewReader(`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}`)
	err := ReadFeatures(input, InputAuto, func(feature Feature) error {
		return expected
	})
	if !errors.Is(err, expected) {
		t.Fatalf("visitor error became %q", err)
	}
}

func TestReadFeaturesRejectsInvalidFeaturesMembers(t *testing.T) {
	for _, input := range []string{
		`{"type":"FeatureCollection","features":null}`,
		`{"type":"FeatureCollection","features":{}}`,
		`{"features":"not an array","type":"FeatureCollection"}`,
	} {
		if err := ReadFeatures(strings.NewReader(input), InputAuto, func(feature Feature) error { return nil }); err == nil {
			t.Fatalf("accepted invalid FeatureCollection %s", input)
		}
	}
}

func TestReadFeaturesRejectsInvalidTopLevelValues(t *testing.T) {
	cases := []string{
		"",
		`42`,
		`[[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}]]`,
		`[{"type":"FeatureCollection","features":[]}]`,
		`{"type":"FeatureCollection"}`,
		`{"type":"FeatureCollection","bbox":"invalid","features":[]}`,
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

func TestReadFeaturesRejectsNestedCollectionsBeforeVisitingTheirFeatures(t *testing.T) {
	feature := `{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}`
	for _, input := range []string{
		`[{"type":"FeatureCollection","features":[` + feature + `]}]`,
		`[{"features":[` + feature + `],"type":"FeatureCollection"}]`,
	} {
		visited := 0
		err := ReadFeatures(strings.NewReader(input), InputAuto, func(Feature) error {
			visited++
			return nil
		})
		if err == nil {
			t.Fatalf("accepted nested FeatureCollection %s", input)
		}
		if visited != 0 {
			t.Fatalf("visited %d nested Features before rejecting %s", visited, input)
		}
	}
}

func TestFeatureMayHaveForeignFeaturesMember(t *testing.T) {
	input := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{},"features":"foreign metadata"}`)
	features := readFeaturesForTest(t, input, InputAuto)
	if len(features) != 1 || string(features[0].Foreign["features"]) != `"foreign metadata"` {
		t.Fatalf("foreign features member was not preserved: %#v", features)
	}
}

func TestFeatureCollectionMayPutTypeAfterFeatures(t *testing.T) {
	input := []byte(`{"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{}}],"type":"FeatureCollection"}`)
	features := readFeaturesForTest(t, input, InputAuto)
	if len(features) != 1 {
		t.Fatalf("out-of-order FeatureCollection returned %d Features; expected 1", len(features))
	}
}

func TestReadFeaturesRejectsDuplicateObjectMembers(t *testing.T) {
	cases := []string{
		`{"type":"Feature","type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{}}`,
		`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0],"coordinates":[1,1]},"properties":{}}`,
		`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"name":"first","name":"second"}}`,
		`{"type":"FeatureCollection","features":[],"features":[]}`,
		`{"type":"FeatureCollection","metadata":{"source":"first","source":"second"},"features":[]}`,
	}
	for _, input := range cases {
		if err := ReadFeatures(strings.NewReader(input), InputAuto, func(feature Feature) error { return nil }); err == nil {
			t.Fatalf("accepted duplicate object member in %s", input)
		}
	}
}

func TestStreamingAPIsRejectInvalidModes(t *testing.T) {
	input := strings.NewReader(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{}}`)
	if err := ReadFeatures(input, InputMode("invalid"), func(feature Feature) error { return nil }); err == nil {
		t.Fatal("ReadFeatures accepted an invalid input mode")
	}
	writer := NewFeatureWriter(&bytes.Buffer{}, OutputMode("invalid"))
	if err := writer.Start(); err == nil {
		t.Fatal("FeatureWriter accepted an invalid output mode")
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
