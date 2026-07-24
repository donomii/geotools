// Extract geodata from wikipedia pages.
package main

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/donomii/goof"

	"github.com/dustin/go-humanize"
	"github.com/dustin/go-wikiparse"
)

var compression string
var numWorkers int
var strict bool

type wikiGeometry struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"`
}

type wikiProperties struct {
	Name string `json:"name"`
}

type wikiFeature struct {
	Type       string         `json:"type"`
	Geometry   wikiGeometry   `json:"geometry"`
	Properties wikiProperties `json:"properties"`
}

type wikiPageTask struct {
	Sequence int64
	Page     *wikiparse.Page
}

type wikiPageResult struct {
	Sequence int64
	Feature  *wikiFeature
}

func parsePageCoords(page *wikiparse.Page) (*wikiFeature, bool, error) {
	if len(page.Revisions) == 0 {
		return nil, false, fmt.Errorf("page %q (%d) has no revisions", page.Title, page.ID)
	}
	location, err := wikiparse.ParseCoords(page.Revisions[0].Text)
	if err == wikiparse.ErrNoCoordFound {
		return nil, false, nil
	} else if err != nil {
		return nil, false, fmt.Errorf("page %q (%d) has invalid coordinates: %w", page.Title, page.ID, err)
	}
	return &wikiFeature{
		Type:       "Feature",
		Geometry:   wikiGeometry{Type: "Point", Coordinates: []float64{location.Lon, location.Lat}},
		Properties: wikiProperties{Name: page.Title},
	}, true, nil
}

func pageHandler(tasks <-chan wikiPageTask, results chan<- wikiPageResult, errorPages chan<- *wikiparse.Page, workerGroup *sync.WaitGroup) {
	defer workerGroup.Done()
	for task := range tasks {
		feature, found, err := parsePageCoords(task.Page)
		if err != nil {
			errorPages <- task.Page
			log.Print(err)
		}
		if !found {
			feature = nil
		}
		results <- wikiPageResult{Sequence: task.Sequence, Feature: feature}
	}
}

func errorHandler(pages <-chan *wikiparse.Page) error {
	var file *os.File
	var encoder *gob.Encoder
	var firstErr error
	for page := range pages {
		if firstErr != nil {
			continue
		}
		if file == nil {
			var err error
			file, err = os.Create("errors.gob")
			if err != nil {
				firstErr = fmt.Errorf("failed to create coordinate error file: %w", err)
				continue
			}
			encoder = gob.NewEncoder(file)
		}
		if err := encoder.Encode(page); err != nil {
			firstErr = fmt.Errorf("failed to encode coordinate error page %q (%d): %w", page.Title, page.ID, err)
		}
	}
	if file != nil {
		if err := file.Close(); err != nil {
			firstErr = errors.Join(firstErr, fmt.Errorf("failed to close coordinate error file: %w", err))
		}
	}
	return firstErr
}

func process(parser wikiparse.Parser, output io.Writer) error {
	log.Printf("Got site info:  %+v", parser.SiteInfo())
	tasks := make(chan wikiPageTask, 1000)
	results := make(chan wikiPageResult, 1000)
	errorPages := make(chan *wikiparse.Page, 10)
	var workerGroup sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		workerGroup.Add(1)
		go pageHandler(tasks, results, errorPages, &workerGroup)
	}
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- writeWikiResults(results, output, strict)
	}()
	errorDone := make(chan error, 1)
	go func() {
		errorDone <- errorHandler(errorPages)
	}()

	pages := int64(0)
	start := time.Now()
	prev := start
	reportfreq := int64(10000)
	var parserErr error
	for {
		page, err := parser.Next()
		if err != nil {
			parserErr = err
			break
		}
		tasks <- wikiPageTask{Sequence: pages, Page: page}

		pages++
		if pages%reportfreq == 0 {
			now := time.Now()
			d := now.Sub(prev)
			log.Printf("Processed %s pages total (%.2f/s)",
				humanize.Comma(pages), float64(reportfreq)/d.Seconds())
			prev = now
		}
	}
	close(tasks)
	workerGroup.Wait()
	close(results)
	writerErr := <-writerDone
	close(errorPages)
	errorErr := <-errorDone
	d := time.Since(start)
	if writerErr != nil {
		return writerErr
	}
	if errorErr != nil {
		return errorErr
	}
	if parserErr != nil && !errors.Is(parserErr, io.EOF) {
		return fmt.Errorf("Wikipedia parser failed after %s pages: %w", humanize.Comma(pages), parserErr)
	}
	log.Printf("Processed %s pages in %v (%.2f p/s)", humanize.Comma(pages), d, float64(pages)/d.Seconds())
	return nil
}

func writeWikiResults(results <-chan wikiPageResult, output io.Writer, strictOutput bool) error {
	writer := bufio.NewWriter(output)
	var firstErr error
	if strictOutput {
		if _, err := writer.WriteString(`{"type":"FeatureCollection","features":[`); err != nil {
			firstErr = err
		}
	}
	pending := make(map[int64]wikiPageResult)
	nextSequence := int64(0)
	wroteFeature := false
	for result := range results {
		pending[result.Sequence] = result
		for {
			next, ok := pending[nextSequence]
			if !ok {
				break
			}
			delete(pending, nextSequence)
			if next.Feature != nil && firstErr == nil {
				encoded, err := json.Marshal(next.Feature)
				if err != nil {
					firstErr = fmt.Errorf("failed to encode Wikipedia Feature at sequence %d: %w", nextSequence, err)
				} else {
					if strictOutput && wroteFeature {
						if err := writer.WriteByte(','); err != nil {
							firstErr = err
						}
					}
					if firstErr == nil {
						if _, err := writer.Write(encoded); err != nil {
							firstErr = err
						}
					}
					if firstErr == nil && !strictOutput {
						if err := writer.WriteByte('\n'); err != nil {
							firstErr = err
						}
					}
					wroteFeature = firstErr == nil
				}
			}
			nextSequence++
		}
	}
	if len(pending) != 0 {
		firstErr = errors.Join(firstErr, fmt.Errorf("Wikipedia result stream ended with %d out-of-order results pending at sequence %d", len(pending), nextSequence))
	}
	if strictOutput && firstErr == nil {
		if _, err := writer.WriteString("]}\n"); err != nil {
			firstErr = err
		}
	}
	return errors.Join(firstErr, writer.Flush())
}

func processSingleStream(filename string) error {
	p, err := wikiparse.NewParser(goof.OpenInput(filename, compression))
	if err != nil {
		return fmt.Errorf("failed to set up Wikipedia parser for %q: %w", filename, err)
	}
	return process(p, os.Stdout)
}

func processMultiStream(idx, data string) error {
	p, err := wikiparse.NewIndexedParser(idx, data, runtime.GOMAXPROCS(0))
	if err != nil {
		return fmt.Errorf("failed to initialize Wikipedia multistream parser for index %q and data %q: %w", idx, data, err)
	}
	return process(p, os.Stdout)
}

func helpMessage() string {
	return `
Use:
		
	wikipedia2geojson.exe file.xml
	
		Read from file.xml

		
	wikipedia2geojson.exe file.xml.bz2
	
		Read from file.xml.bz2, automatically uncompressing bz2 format

		
	wikipedia2geojson.exe file.xml.gz
	
		Read from file.xml.bz2, automatically uncompressing gz format

		
	wikipedia2geojson.exe --compression=bz2 file
	
		Read from file, force uncompressing bz2 format

		
	wikipedia2geojson.exe --compression=gz file
	
		Read from file, force uncompressing gz format

		
	wikipedia2geojson.exe -
	
		Read from stdin.

		
	wikipedia2geojson.exe --compression=bz2 -
	
		Read from stdin.  Stdin is in bzip2 format
	
	
	wikipedia2geojson.exe --compression=gz -
	
		Read from stdin.  Stdin is in gz format
	`
}

func runWikipediaGeoJSONBuiltInTests() error {
	first := &wikiFeature{Type: "Feature", Geometry: wikiGeometry{Type: "Point", Coordinates: []float64{1, 2}}, Properties: wikiProperties{Name: "First"}}
	second := &wikiFeature{Type: "Feature", Geometry: wikiGeometry{Type: "Point", Coordinates: []float64{3, 4}}, Properties: wikiProperties{Name: "Second"}}
	results := make(chan wikiPageResult, 2)
	results <- wikiPageResult{Sequence: 1, Feature: second}
	results <- wikiPageResult{Sequence: 0, Feature: first}
	close(results)
	var output bytes.Buffer
	if err := writeWikiResults(results, &output, true); err != nil {
		return err
	}
	if !json.Valid(output.Bytes()) {
		return fmt.Errorf("strict Wikipedia output is not valid JSON: %s", output.String())
	}
	var collection struct {
		Type     string        `json:"type"`
		Features []wikiFeature `json:"features"`
	}
	if err := json.Unmarshal(output.Bytes(), &collection); err != nil {
		return err
	}
	if collection.Type != "FeatureCollection" || len(collection.Features) != 2 {
		return fmt.Errorf("expected a FeatureCollection with two Features")
	}
	if collection.Features[0].Properties.Name != "First" || collection.Features[1].Properties.Name != "Second" {
		return fmt.Errorf("out-of-order worker results were not restored to input order")
	}
	return nil
}

func main() {
	var cpus int
	var wantHelp bool
	var runBuiltInTests bool
	flag.IntVar(&numWorkers, "workers", 8, "Number of parsing workers")
	flag.IntVar(&cpus, "cpus", runtime.GOMAXPROCS(0), "Number of CPUS to utilize")
	flag.StringVar(&compression, "compression", "", "Input is compressed with bz2 or gz")
	flag.BoolVar(&strict, "strict", false, "Emit one GeoJSON FeatureCollection. By default, emit one Feature per line.")
	flag.BoolVar(&wantHelp, "help", false, "Print help")
	flag.BoolVar(&runBuiltInTests, "test", false, "Run built-in tests and exit")
	flag.Parse()

	if wantHelp {
		fmt.Print(helpMessage())
		return
	}
	if runBuiltInTests {
		if err := runWikipediaGeoJSONBuiltInTests(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wikipedia2geojson built-in tests passed")
		return
	}
	if numWorkers < 1 {
		log.Fatal("invalid workers: expected an integer greater than zero")
	}
	if cpus < 1 {
		log.Fatal("invalid cpus: expected an integer greater than zero")
	}
	runtime.GOMAXPROCS(cpus)

	inputFile := flag.Arg(0)

	if inputFile == "-" {
		inputFile = ""
	}
	var err error
	switch flag.NArg() {
	case 1:
		err = processSingleStream(inputFile)
	case 2:
		err = processMultiStream(inputFile, flag.Arg(1))
	default:
		fmt.Print(helpMessage())
		log.Fatal("invalid arguments")
	}
	if err != nil {
		log.Fatal(err)
	}
}
