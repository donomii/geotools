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
	nested := featureByID(t, collection.Features, "relation/21")
	if nested.Geometry.Type != "GeometryCollection" || len(nested.Geometry.Geometries) != 2 {
		t.Fatalf("nested relation geometry is %#v", nested.Geometry)
	}
}

func TestConvertOSMRejectsMissingNodeReference(t *testing.T) {
	input := `<osm><node id="1" lat="1" lon="2"/><way id="7"><nd ref="1"/><nd ref="99"/></way></osm>`
	err := convertOSM(strings.NewReader(input), io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "way 7 references missing node 99") {
		t.Fatalf("received error %v", err)
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
