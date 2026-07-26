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

	"github.com/dustin/go-wikiparse"
)

const realWikipediaCoordinateDump = `<mediawiki xmlns="http://www.mediawiki.org/xml/export-0.11/" version="0.11" xml:lang="en">
  <siteinfo>
    <sitename>Wikipedia</sitename>
    <dbname>enwiki</dbname>
    <base>https://en.wikipedia.org/wiki/Main_Page</base>
    <generator>MediaWiki 1.45</generator>
    <case>first-letter</case>
    <namespaces><namespace key="0" case="first-letter"/></namespaces>
  </siteinfo>
  <page>
    <title>Singapore</title>
    <ns>0</ns>
    <id>27318</id>
    <revision>
      <id>1365256242</id>
      <timestamp>2026-07-22T16:58:24Z</timestamp>
      <model>wikitext</model>
      <format>text/x-wiki</format>
      <text xml:space="preserve">{{Infobox country
| coordinates = {{Coord|1|17|N|103|50|E|type:city(5,700,000)_region:SG|display=inline,title}}
}}
'''Singapore''' is an island country and city-state.</text>
    </revision>
  </page>
  <page>
    <title>Eiffel Tower</title>
    <ns>0</ns>
    <id>9232</id>
    <revision>
      <id>1365733106</id>
      <timestamp>2026-07-24T03:20:11Z</timestamp>
      <model>wikitext</model>
      <format>text/x-wiki</format>
      <text xml:space="preserve">{{Infobox building
| coordinates = {{coord|48|51|29.6|N|2|17|40.2|E|region:FR-75|display=inline,title}}
}}
The '''Eiffel Tower''' is a wrought-iron lattice tower in Paris.</text>
    </revision>
  </page>
  <page>
    <title>Article without coordinates</title>
    <ns>0</ns>
    <id>100000001</id>
    <revision>
      <id>100000002</id>
      <text xml:space="preserve">This page deliberately has no coordinate template.</text>
    </revision>
  </page>
</mediawiki>`

func TestProcessRealWikipediaCoordinatesAsJSONL(t *testing.T) {
	output := processCoordinateDump(t, false, 3)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d coordinate Features, expected 2: %s", len(lines), output)
	}
	var singapore, eiffel wikiFeature
	if err := json.Unmarshal([]byte(lines[0]), &singapore); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &eiffel); err != nil {
		t.Fatal(err)
	}
	assertWikiPoint(t, singapore, "Singapore", 103.83333333333333, 1.2833333333333334)
	assertWikiPoint(t, eiffel, "Eiffel Tower", 2.2945, 48.858222222222224)
}

func TestProcessRealWikipediaCoordinatesAsFeatureCollection(t *testing.T) {
	output := processCoordinateDump(t, true, 4)
	var collection struct {
		Type     string        `json:"type"`
		Features []wikiFeature `json:"features"`
	}
	if err := json.Unmarshal([]byte(output), &collection); err != nil {
		t.Fatal(err)
	}
	if collection.Type != "FeatureCollection" || len(collection.Features) != 2 {
		t.Fatalf("strict output type=%q features=%d", collection.Type, len(collection.Features))
	}
	if collection.Features[0].Properties.Name != "Singapore" || collection.Features[1].Properties.Name != "Eiffel Tower" {
		t.Fatalf("parallel output order changed: %#v", collection.Features)
	}
}

func TestParsePageCoordsSkipsPageWithoutCoordinates(t *testing.T) {
	page := &wikiparse.Page{
		Title:     "No coordinates",
		ID:        70,
		Revisions: []wikiparse.Revision{{Text: "An ordinary article."}},
	}
	feature, found, err := parsePageCoords(page)
	if err != nil {
		t.Fatal(err)
	}
	if found || feature != nil {
		t.Fatalf("page without coordinates returned found=%v feature=%#v", found, feature)
	}
}

func TestParsePageCoordsRejectsPageWithoutRevision(t *testing.T) {
	_, _, err := parsePageCoords(&wikiparse.Page{Title: "Broken", ID: 71})
	if err == nil || !strings.Contains(err.Error(), `page "Broken" (71) has no revisions`) {
		t.Fatalf("received error %v", err)
	}
}

func TestParsePageCoordsRejectsNonFiniteCoordinates(t *testing.T) {
	page := &wikiparse.Page{
		Title:     "Not a location",
		ID:        72,
		Revisions: []wikiparse.Revision{{Text: "{{coord|NaN|0}}"}},
	}
	_, _, err := parsePageCoords(page)
	if err == nil || !strings.Contains(err.Error(), "non-finite coordinates") {
		t.Fatalf("received error %v", err)
	}
}

func TestOpenWikipediaInputReadsGzipAndReturnsErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pages.xml.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	compressor := gzip.NewWriter(file)
	if _, err := compressor.Write([]byte("wikipedia payload")); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	input, err := openWikipediaInput(path, "")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(input)
	closeErr := input.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if string(data) != "wikipedia payload" {
		t.Fatalf("gzip input decoded as %q", data)
	}
	if _, err := openWikipediaInput(path, "zip"); err == nil {
		t.Fatal("accepted unsupported input compression")
	}
	if _, err := openWikipediaInput(filepath.Join(t.TempDir(), "missing.xml"), ""); err == nil {
		t.Fatal("accepted a missing input file")
	}
}

func TestWriteWikiResultsHandlesLargeOutOfOrderWindow(t *testing.T) {
	results := make(chan wikiPageResult, 5)
	for sequence := int64(4); sequence >= 0; sequence-- {
		results <- wikiPageResult{
			Sequence: sequence,
			Feature: &wikiFeature{
				Type:       "Feature",
				Geometry:   wikiGeometry{Type: "Point", Coordinates: []float64{float64(sequence), 0}},
				Properties: wikiProperties{Name: string(rune('A' + sequence))},
			},
		}
	}
	close(results)
	var output bytes.Buffer
	if err := writeWikiResults(results, &output, true); err != nil {
		t.Fatal(err)
	}
	var collection struct {
		Features []wikiFeature `json:"features"`
	}
	if err := json.Unmarshal(output.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	for index, feature := range collection.Features {
		expected := string(rune('A' + index))
		if feature.Properties.Name != expected {
			t.Fatalf("Feature %d is %q, expected %q", index, feature.Properties.Name, expected)
		}
	}
}

func TestWriteWikiResultsReleasesOrderedResultSlots(t *testing.T) {
	results := make(chan wikiPageResult, 3)
	slots := make(chan struct{}, 3)
	for sequence := int64(2); sequence >= 0; sequence-- {
		slots <- struct{}{}
		results <- wikiPageResult{Sequence: sequence}
	}
	close(results)
	if err := writeWikiResultsWithSlots(results, &bytes.Buffer{}, false, slots); err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Fatalf("writer left %d result slots occupied", len(slots))
	}
}

func TestProcessReportsMalformedWikipediaXML(t *testing.T) {
	parser, err := wikiparse.NewParser(strings.NewReader(strings.TrimSuffix(realWikipediaCoordinateDump, "</mediawiki>")))
	if err != nil {
		t.Fatal(err)
	}
	numWorkers = 2
	strict = false
	err = process(parser, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "Wikipedia parser failed after") {
		t.Fatalf("received error %v", err)
	}
}

func processCoordinateDump(t *testing.T, strictOutput bool, workers int) string {
	t.Helper()
	parser, err := wikiparse.NewParser(strings.NewReader(realWikipediaCoordinateDump))
	if err != nil {
		t.Fatal(err)
	}
	numWorkers = workers
	strict = strictOutput
	var output bytes.Buffer
	if err := process(parser, &output); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func assertWikiPoint(t *testing.T, feature wikiFeature, name string, longitude, latitude float64) {
	t.Helper()
	if feature.Type != "Feature" || feature.Geometry.Type != "Point" || feature.Properties.Name != name {
		t.Fatalf("Feature is %#v", feature)
	}
	if len(feature.Geometry.Coordinates) != 2 ||
		math.Abs(feature.Geometry.Coordinates[0]-longitude) > 1e-9 ||
		math.Abs(feature.Geometry.Coordinates[1]-latitude) > 1e-9 {
		t.Fatalf("%s coordinates are %#v, expected [%v, %v]", name, feature.Geometry.Coordinates, longitude, latitude)
	}
}
