package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/donomii/geotools/geodata"
)

func TestRealDataSequenceRoundTrip(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	var sequence bytes.Buffer
	if err := convertSequence(geodata.InputAuto, geodata.OutputSequence, bytes.NewReader(data), &sequence); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(sequence.Bytes(), []byte{0x1e}) != 5 {
		t.Fatalf("sequence has %d record separators; expected 5", bytes.Count(sequence.Bytes(), []byte{0x1e}))
	}
	var collection bytes.Buffer
	if err := convertSequence(geodata.InputSequence, geodata.OutputCollection, &sequence, &collection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(collectFeatures(t, data), collectFeatures(t, collection.Bytes())) {
		t.Fatal("FeatureCollection-to-sequence round trip changed Features")
	}
}

func collectFeatures(t *testing.T, data []byte) []string {
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
	return result
}
