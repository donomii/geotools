package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/dustin/go-wikiparse"
)

const realWikipediaDocumentDump = `<mediawiki xmlns="http://www.mediawiki.org/xml/export-0.11/" version="0.11" xml:lang="en">
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
      <text xml:space="preserve">{{Infobox building
| coordinates = {{coord|48|51|29.6|N|2|17|40.2|E|region:FR-75|display=inline,title}}
}}
The '''Eiffel Tower''' is a wrought-iron lattice tower in [[Paris]].</text>
    </revision>
  </page>
  <page>
    <title>Café</title>
    <ns>0</ns>
    <id>5096</id>
    <revision>
      <id>1360000000</id>
      <text xml:space="preserve">A '''café''' is an establishment that serves coffee &amp; tea.</text>
    </revision>
  </page>
</mediawiki>`

func TestProcessRealWikipediaDocuments(t *testing.T) {
	output := processDocumentDump(t, -1, 4)
	expected := "" +
		"27318\tsingapore is an island country and city state\n" +
		"9232\tthe eiffel tower is a wrought iron lattice tower in paris\n" +
		"5096\ta café is an establishment that serves coffee tea\n"
	if output != expected {
		t.Fatalf("document output is:\n%s\nexpected:\n%s", output, expected)
	}
}

func TestProcessWikipediaDocumentLimitIsExact(t *testing.T) {
	output := processDocumentDump(t, 1, 3)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "27318\t") {
		t.Fatalf("limit output is %q", output)
	}
}

func TestProcessWikipediaZeroLimitWritesNothing(t *testing.T) {
	if output := processDocumentDump(t, 0, 2); output != "" {
		t.Fatalf("zero limit wrote %q", output)
	}
}

func TestCleanWikiTextRemovesMarkupAndKeepsLabels(t *testing.T) {
	input := `<!-- hidden --> Start [[OpenStreetMap|OSM]] [https://example.test useful site]
{{template|discard}} {| class="wikitable" | discard |} <ref>citation</ref>
<span>Café &amp; tea</span> https://example.test/end`
	cleaned := cleanWikiText(input)
	words := strings.FieldsFunc(strings.ToLower(cleaned), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	actual := strings.Join(words, " ")
	expected := "start osm useful site café tea"
	if actual != expected {
		t.Fatalf("cleaned text is %q, expected %q", actual, expected)
	}
}

func TestParsePageWordsRejectsPageWithoutRevision(t *testing.T) {
	_, err := parsePageWords(&wikiparse.Page{Title: "Broken", ID: 72})
	if err == nil || !strings.Contains(err.Error(), `page "Broken" (72) has no revisions`) {
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

func TestWriteDocvecResultsHandlesLargeOutOfOrderWindow(t *testing.T) {
	results := make(chan docvecPageResult, 20)
	for sequence := int64(19); sequence >= 0; sequence-- {
		results <- docvecPageResult{Sequence: sequence, Line: documentLine(sequence)}
	}
	close(results)
	var output bytes.Buffer
	if err := writeDocvecResults(results, &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 20 {
		t.Fatalf("wrote %d lines", len(lines))
	}
	for sequence, line := range lines {
		if line != strings.TrimSpace(documentLine(int64(sequence))) {
			t.Fatalf("line %d is %q", sequence, line)
		}
	}
}

func TestWriteDocvecResultsReleasesOrderedResultSlots(t *testing.T) {
	results := make(chan docvecPageResult, 3)
	slots := make(chan struct{}, 3)
	for sequence := int64(2); sequence >= 0; sequence-- {
		slots <- struct{}{}
		results <- docvecPageResult{Sequence: sequence}
	}
	close(results)
	if err := writeDocvecResultsWithSlots(results, &bytes.Buffer{}, slots); err != nil {
		t.Fatal(err)
	}
	if len(slots) != 0 {
		t.Fatalf("writer left %d result slots occupied", len(slots))
	}
}

func TestProcessReportsMalformedDocumentDump(t *testing.T) {
	parser, err := wikiparse.NewParser(strings.NewReader(strings.TrimSuffix(realWikipediaDocumentDump, "</mediawiki>")))
	if err != nil {
		t.Fatal(err)
	}
	numWorkers = 2
	limit = -1
	err = process(parser, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "Wikipedia parser failed after") {
		t.Fatalf("received error %v", err)
	}
}

func processDocumentDump(t *testing.T, pageLimit int64, workers int) string {
	t.Helper()
	parser, err := wikiparse.NewParser(strings.NewReader(realWikipediaDocumentDump))
	if err != nil {
		t.Fatal(err)
	}
	numWorkers = workers
	limit = pageLimit
	var output bytes.Buffer
	if err := process(parser, &output); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func documentLine(sequence int64) string {
	return fmt.Sprintf("%02d\tline\n", sequence)
}
