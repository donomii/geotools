package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
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

func TestDecodeJSONFGRootFeature(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"coordRefSys":"http://www.opengis.net/def/crs/EPSG/0/4326","geometry":null,"place":{"type":"Point","coordinates":[1.3521,103.8198]},"properties":{"name":"Singapore"}}`)
	var output bytes.Buffer
	if err := decodeJSONFG(input, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, output.Bytes())
	if len(features) != 1 || strings.Contains(features[0], "conformsTo") || strings.Contains(features[0], "coordRefSys") {
		t.Fatalf("root JSON-FG Feature decoded as %s", output.Bytes())
	}
}

func TestJSONFGDefaultEncodingOmitsDuplicatePlace(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"name":"Singapore"}}`)
	var output bytes.Buffer
	if err := encodeJSONFG(input, &output, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(output.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	var feature geodata.Feature
	if err := json.Unmarshal(root.Features[0], &feature); err != nil {
		t.Fatal(err)
	}
	if feature.Foreign["place"] != nil {
		t.Fatalf("default JSON-FG duplicated simple WGS 84 geometry in place: %s", feature.Foreign["place"])
	}
}

func TestJSONFGEncodeRejectsExistingExtensionMembers(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{},"time":{"date":"2026-01-01"}}`)
	var output bytes.Buffer
	if err := encodeJSONFG(input, &output, geodata.InputAuto); err == nil {
		t.Fatal("accepted a Feature that already contains JSON-FG members")
	}
}

func TestJSONFGConvertsProjectedPlaceAndTime(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"coordRefSys":"http://www.opengis.net/def/crs/EPSG/0/3857","features":[{"type":"Feature","id":"projected","geometry":null,"place":{"type":"Point","coordinates":[11557101.59,150541.27]},"time":{"timestamp":"2026-07-25T04:00:00Z"},"properties":{"name":"Singapore"}}]}`)
	var output bytes.Buffer
	if err := decodeJSONFG(input, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, output.Bytes())
	if len(features) != 1 {
		t.Fatalf("decoded %d Features; expected 1", len(features))
	}
	var feature geodata.Feature
	if err := json.Unmarshal([]byte(features[0]), &feature); err != nil {
		t.Fatal(err)
	}
	summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.HasBounds || summary.Bounds[0] < 103.81 || summary.Bounds[0] > 103.83 {
		t.Fatalf("projected place became bounds %v", summary.Bounds)
	}
	properties, err := feature.PropertyMap()
	if err != nil {
		t.Fatal(err)
	}
	if string(properties[defaultJSONFGTimePropertyName]) != `{"timestamp":"2026-07-25T04:00:00Z"}` {
		t.Fatalf("decoded time property is %s", properties[defaultJSONFGTimePropertyName])
	}
}

func TestJSONFGEncodeProjectsPlaceAndMapsTime(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"jsonfg_time":"2026-07-25"}}`)
	var output bytes.Buffer
	settings := jsonFGSettings{PlaceCRS: geodata.CRSEPSG3857, TimeProperty: defaultJSONFGTimePropertyName}
	if err := encodeJSONFGWithSettings(input, &output, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(output.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	var feature geodata.Feature
	if err := json.Unmarshal(root.Features[0], &feature); err != nil {
		t.Fatal(err)
	}
	var place struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Foreign["place"], &place); err != nil {
		t.Fatal(err)
	}
	if place.Coordinates[0] < 11557000 || string(feature.Foreign["time"]) != `{"date":"2026-07-25"}` {
		t.Fatalf("projected place is %v and time is %s", place.Coordinates, feature.Foreign["time"])
	}
	var decoded bytes.Buffer
	if err := decodeJSONFGWithSettings(&output, &decoded, geodata.OutputCollection, settings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(jsonFGFeatures(t, []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"jsonfg_time":"2026-07-25"}}`)), jsonFGFeatures(t, decoded.Bytes())) {
		t.Fatalf("JSON-FG temporal round trip changed the source Feature: %s", decoded.Bytes())
	}
}

func TestJSONFGEPSG4326AxisOrderRoundTrip(t *testing.T) {
	input := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"name":"Singapore"}}`)
	var encoded bytes.Buffer
	settings := jsonFGSettings{PlaceCRS: geodata.CRSEPSG4326, TimeProperty: defaultJSONFGTimePropertyName}
	if err := encodeJSONFGWithSettings(bytes.NewReader(input), &encoded, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(encoded.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	var feature geodata.Feature
	if err := json.Unmarshal(root.Features[0], &feature); err != nil {
		t.Fatal(err)
	}
	var place struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Foreign["place"], &place); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(place.Coordinates, []float64{1.3521, 103.8198}) {
		t.Fatalf("EPSG:4326 place coordinates are %v; expected latitude then longitude", place.Coordinates)
	}
	feature.Geometry = json.RawMessage("null")
	root.Features[0], _ = json.Marshal(feature)
	encoded.Reset()
	if err := json.NewEncoder(&encoded).Encode(root); err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	if err := decodeJSONFGWithSettings(&encoded, &decoded, geodata.OutputCollection, settings); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, decoded.Bytes())
	if len(features) != 1 || !strings.Contains(features[0], `[103.8198,1.3521]`) {
		t.Fatalf("EPSG:4326 place decoded as %s", decoded.Bytes())
	}
}

func TestJSONFGRejectsInvalidTemporalAndConformanceData(t *testing.T) {
	cases := []string{
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"time":{"timestamp":"2026-07-25T04:00:00+08:00"},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"measures":{"enabled":true},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"coordRefSys":"http://www.opengis.net/def/crs/OGC/0/CRS84","features":[{"type":"Feature","geometry":null,"place":{"type":"Point","coordinates":[1,2]},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"time":{"date":"2026-07-25","timestamp":"2026-07-26T04:00:00Z"},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"time":{"interval":["2026-07-26","2026-07-25"]},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"time":{"date":"2026-07-25","interval":["2026-07-26","2026-07-27"]},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"coordRefSys":"http://www.opengis.net/def/crs/EPSG/0/4326","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,1]},"place":{"type":"Point","coordinates":[1,1]},"properties":{}}]}`,
	}
	for _, input := range cases {
		if err := decodeJSONFG(strings.NewReader(input), &bytes.Buffer{}, geodata.OutputJSONL); err == nil {
			t.Fatalf("accepted invalid JSON-FG %s", input)
		}
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
