package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/donomii/geotools/geodata"
)

func TestJSONFGRealDataRoundTrip(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	var jsonFG bytes.Buffer
	if err := encodeJSONFG(bytes.NewReader(data), &jsonFG, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	if err := requireCoreConformance(jsonFG.Bytes()); err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(jsonFG.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	if string(root["type"]) != `"FeatureCollection"` {
		t.Fatalf("JSON-FG root type is %s", root["type"])
	}
	var output bytes.Buffer
	if err := decodeJSONFG(&jsonFG, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(jsonFGFeatures(t, data), jsonFGFeatures(t, output.Bytes())) {
		t.Fatal("JSON-FG round trip changed Feature content")
	}
}

func TestJSONFGRejectsMissingCoreDeclaration(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[]}`)
	var output bytes.Buffer
	if err := decodeJSONFG(input, &output, geodata.OutputJSONL); err == nil {
		t.Fatal("accepted JSON-FG without the core declaration")
	}
}

func TestJSONFGEncodeRejectsExistingExtensionMembers(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{},"time":{"date":"2026-01-01"}}`)
	var output bytes.Buffer
	if err := encodeJSONFG(input, &output, geodata.InputAuto); err == nil {
		t.Fatal("accepted a Feature that already contains JSON-FG members")
	}
}

func jsonFGFeatures(t *testing.T, data []byte) []string {
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
