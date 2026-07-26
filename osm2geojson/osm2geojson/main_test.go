package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const realVancouverOSM = `<?xml version="1.0" encoding="UTF-8"?>
<osm version="0.6" generator="geotools-test">
  <node id="971076208" lat="49.2682544" lon="-123.2548774">
    <tag k="building" v="entrance"/>
  </node>
  <node id="251630268" lat="49.2204222" lon="-123.0696397"/>
  <node id="3945155393" lat="49.2204318" lon="-123.0702035"/>
  <node id="3945155394" lat="49.2204831" lon="-123.0702015"/>
  <node id="4577742541" lat="49.2204838" lon="-123.0702460">
    <tag k="entrance" v="yes"/>
  </node>
  <node id="251630270" lat="49.2205069" lon="-123.0716086"/>
  <way id="23254060">
    <nd ref="251630268"/>
    <nd ref="3945155393"/>
    <nd ref="3945155394"/>
    <nd ref="4577742541"/>
    <nd ref="251630270"/>
    <tag k="building" v="school"/>
    <tag k="name" v="David Thompson Secondary School"/>
  </way>
</osm>`

const relationOSM = `<osm version="0.6">
  <node id="1" lat="0" lon="0"/>
  <node id="2" lat="0" lon="4"/>
  <node id="3" lat="4" lon="4"/>
  <node id="4" lat="4" lon="0"/>
  <node id="5" lat="1" lon="1"/>
  <node id="6" lat="1" lon="2"/>
  <node id="7" lat="2" lon="2"/>
  <node id="8" lat="2" lon="1"/>
  <way id="10">
    <nd ref="1"/><nd ref="2"/><nd ref="3"/><nd ref="4"/><nd ref="1"/>
  </way>
  <way id="11">
    <nd ref="5"/><nd ref="6"/><nd ref="7"/><nd ref="8"/><nd ref="5"/>
  </way>
  <relation id="20">
    <member type="way" ref="10" role="outer"/>
    <member type="way" ref="11" role="inner"/>
    <tag k="type" v="multipolygon"/>
    <tag k="name" v="Courtyard"/>
  </relation>
  <relation id="21">
    <member type="relation" ref="20" role=""/>
    <member type="node" ref="1" role="label"/>
    <tag k="type" v="site"/>
  </relation>
</osm>`

func TestConvertRealVancouverOSMToJSONL(t *testing.T) {
	var output bytes.Buffer
	if err := convertOSM(strings.NewReader(realVancouverOSM), &output, false); err != nil {
		t.Fatal(err)
	}
	features := decodeOSMJSONL(t, output.String())
	if len(features) != 7 {
		t.Fatalf("converted %d Features, expected 7", len(features))
	}
	entrance := featureByID(t, features, "node/971076208")
	assertCoordinates(t, entrance.Geometry.Coordinates, []float64{-123.2548774, 49.2682544})
	if entrance.Properties.Tags["building"] != "entrance" {
		t.Fatalf("node tags are %#v", entrance.Properties.Tags)
	}
	school := featureByID(t, features, "way/23254060")
	if school.Geometry.Type != "LineString" {
		t.Fatalf("school geometry is %q, expected LineString", school.Geometry.Type)
	}
	if school.Properties.Tags["name"] != "David Thompson Secondary School" {
		t.Fatalf("school name is %q", school.Properties.Tags["name"])
	}
	if len(school.BBox) != 4 || school.BBox[0] != -123.0716086 || school.BBox[2] != -123.0696397 {
		t.Fatalf("school bbox is %#v", school.BBox)
	}
}

func TestConvertOSMStrictFeatureCollection(t *testing.T) {
	var output bytes.Buffer
	if err := convertOSM(strings.NewReader(realVancouverOSM), &output, true); err != nil {
		t.Fatal(err)
	}
	var collection struct {
		Type     string       `json:"type"`
		Features []osmFeature `json:"features"`
	}
	if err := json.Unmarshal(output.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	if collection.Type != "FeatureCollection" || len(collection.Features) != 7 {
		t.Fatalf("strict output type=%q features=%d", collection.Type, len(collection.Features))
	}
}

func TestConvertOSMMultipolygonAndNestedRelation(t *testing.T) {
	var output bytes.Buffer
	if err := convertOSM(strings.NewReader(relationOSM), &output, true); err != nil {
		t.Fatal(err)
	}
	var collection struct {
		Features []osmFeature `json:"features"`
	}
	if err := json.Unmarshal(output.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	multipolygon := featureByID(t, collection.Features, "relation/20")
	if multipolygon.Geometry.Type != "Polygon" {
		t.Fatalf("multipolygon geometry is %q", multipolygon.Geometry.Type)
	}
	var rings [][][]float64
	if err := json.Unmarshal(multipolygon.Geometry.Coordinates, &rings); err != nil {
		t.Fatal(err)
	}
	if len(rings) != 2 {
		t.Fatalf("multipolygon contains %d rings, expected outer and inner rings", len(rings))
	}
	if osmRingSignedArea(rings[0]) <= 0 || osmRingSignedArea(rings[1]) >= 0 {
		t.Fatalf("multipolygon ring winding is outer=%v inner=%v; expected counterclockwise outer and clockwise inner", osmRingSignedArea(rings[0]), osmRingSignedArea(rings[1]))
	}
	nested := featureByID(t, collection.Features, "relation/21")
	if nested.Geometry.Type != "GeometryCollection" || len(nested.Geometry.Geometries) != 2 {
		t.Fatalf("nested relation geometry is %#v", nested.Geometry)
	}
}

func TestOSMClosedWayGeometryUsesAreaTags(t *testing.T) {
	coordinates := [][]float64{{0, 0}, {0, 1}, {1, 1}, {1, 0}, {0, 0}}
	tests := []struct {
		name     string
		tags     map[string]string
		expected string
	}{
		{name: "building", tags: map[string]string{"building": "yes"}, expected: "Polygon"},
		{name: "roundabout", tags: map[string]string{"highway": "primary", "junction": "roundabout"}, expected: "LineString"},
		{name: "explicit area", tags: map[string]string{"highway": "pedestrian", "area": "yes"}, expected: "Polygon"},
		{name: "explicit line", tags: map[string]string{"building": "yes", "area": "no"}, expected: "LineString"},
		{name: "linear natural feature", tags: map[string]string{"natural": "coastline"}, expected: "LineString"},
		{name: "untagged", expected: "LineString"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			geometry, err := osmGeometryFromCoordinates(coordinates, test.tags)
			if err != nil {
				t.Fatal(err)
			}
			if geometry.Type != test.expected {
				t.Fatalf("closed way became %s, expected %s", geometry.Type, test.expected)
			}
			if geometry.Type == "Polygon" {
				var rings [][][]float64
				if err := json.Unmarshal(geometry.Coordinates, &rings); err != nil {
					t.Fatal(err)
				}
				if osmRingSignedArea(rings[0]) <= 0 {
					t.Fatalf("polygon outer ring has signed area %v; expected counterclockwise winding", osmRingSignedArea(rings[0]))
				}
			}
		})
	}
}

func TestConvertOSMRejectsMissingNodeReference(t *testing.T) {
	input := `<osm><node id="1" lat="1" lon="2"/><way id="7"><nd ref="1"/><nd ref="99"/></way></osm>`
	err := convertOSM(strings.NewReader(input), io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "way 7 references missing node 99") {
		t.Fatalf("received error %v", err)
	}
}

func TestOSMNodeRejectsCoordinatesOutsideWGS84(t *testing.T) {
	tests := []osmXMLNode{
		{ID: "1", Lat: "NaN", Lon: "0"},
		{ID: "2", Lat: "91", Lon: "0"},
		{ID: "3", Lat: "0", Lon: "-181"},
		{ID: "4", Lat: "0", Lon: "Inf"},
	}
	for _, node := range tests {
		if _, _, err := osmNodeFeature(node); err == nil {
			t.Fatalf("accepted OSM node %s at latitude %q longitude %q", node.ID, node.Lat, node.Lon)
		}
	}
}

func TestConvertOSMPreservesEmptyTagValue(t *testing.T) {
	input := `<osm><node id="1" lat="1" lon="2"><tag k="name" v=""/></node></osm>`
	var output bytes.Buffer
	if err := convertOSM(strings.NewReader(input), &output, false); err != nil {
		t.Fatal(err)
	}
	feature := decodeOSMJSONL(t, output.String())[0]
	value, exists := feature.Properties.Tags["name"]
	if !exists || value != "" {
		t.Fatalf("empty name tag was not preserved: %#v", feature.Properties.Tags)
	}
}

func TestSeekableAndStreamingOSMConversionsMatch(t *testing.T) {
	var indexedOutput bytes.Buffer
	if err := convertOSM(strings.NewReader(relationOSM), &indexedOutput, true); err != nil {
		t.Fatal(err)
	}
	var streamingOutput bytes.Buffer
	input := struct{ io.Reader }{strings.NewReader(relationOSM)}
	if err := convertOSM(input, &streamingOutput, true); err != nil {
		t.Fatal(err)
	}
	if indexedOutput.String() != streamingOutput.String() {
		t.Fatalf("seekable and streaming conversions differ:\nseekable: %s\nstreaming: %s", indexedOutput.String(), streamingOutput.String())
	}
}

func TestOSMReferenceIndexExcludesUnreferencedNodesAndWays(t *testing.T) {
	input := `<osm>
<node id="1" lat="0" lon="0"/><node id="2" lat="1" lon="1"/><node id="99" lat="2" lon="2"/>
<way id="10"><nd ref="1"/><nd ref="2"/></way><way id="11"><nd ref="2"/><nd ref="1"/></way>
<relation id="20"><member type="way" ref="10" role=""/><member type="node" ref="2" role="label"/></relation>
</osm>`
	index, err := indexOSMReferences(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if index.nodeUses[1] != 2 || index.nodeUses[2] != 3 {
		t.Fatalf("node reference counts are %#v", index.nodeUses)
	}
	if _, exists := index.nodeUses[99]; exists {
		t.Fatalf("unreferenced node 99 was indexed: %#v", index.nodeUses)
	}
	if !index.relationWays[10] || index.relationWays[11] {
		t.Fatalf("relation way set is %#v", index.relationWays)
	}
}

func TestConvertGzipOSMFileWithoutScratchStorage(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "vancouver.osm.gz")
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	compressor := gzip.NewWriter(file)
	if _, err := compressor.Write([]byte(realVancouverOSM)); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := convertOSMFile(filename, "", &output, true); err != nil {
		t.Fatal(err)
	}
	var collection struct {
		Features []osmFeature `json:"features"`
	}
	if err := json.Unmarshal(output.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	if len(collection.Features) != 7 {
		t.Fatalf("gzip OSM conversion emitted %d Features", len(collection.Features))
	}
}

func TestOpenOSMOutputWritesGzip(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "real-vancouver.geojson.gz")
	destination, err := openOSMOutput(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := convertOSM(strings.NewReader(realVancouverOSM), destination.writer, true); err != nil {
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	var collection struct {
		Features []osmFeature `json:"features"`
	}
	if err := json.Unmarshal(decoded, &collection); err != nil {
		t.Fatal(err)
	}
	if len(collection.Features) != 7 {
		t.Fatalf("gzip output contains %d Features", len(collection.Features))
	}
}

func decodeOSMJSONL(t *testing.T, output string) []osmFeature {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	features := make([]osmFeature, 0, len(lines))
	for lineNumber, line := range lines {
		var feature osmFeature
		if err := json.Unmarshal([]byte(line), &feature); err != nil {
			t.Fatalf("line %d is invalid JSON: %v", lineNumber+1, err)
		}
		features = append(features, feature)
	}
	return features
}

func featureByID(t *testing.T, features []osmFeature, id string) osmFeature {
	t.Helper()
	for _, feature := range features {
		if feature.ID == id {
			return feature
		}
	}
	t.Fatalf("Feature %q was not emitted", id)
	return osmFeature{}
}

func assertCoordinates(t *testing.T, encoded json.RawMessage, expected []float64) {
	t.Helper()
	var actual []float64
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("coordinates are %#v, expected %#v", actual, expected)
	}
	for index := range actual {
		if math.Abs(actual[index]-expected[index]) > 1e-9 {
			t.Fatalf("coordinates are %#v, expected %#v", actual, expected)
		}
	}
}
