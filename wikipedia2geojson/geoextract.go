// Extract geodata from wikipedia pages.
package main

import (
	"encoding/gob"
	"encoding/xml"
	"flag"
	"fmt"
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
var parseCoords bool
var strict bool
var firstLine bool = true

var wg, errwg sync.WaitGroup

func parsePageCoords(p *wikiparse.Page, cherr chan<- *wikiparse.Page) {
	gl, err := wikiparse.ParseCoords(p.Revisions[0].Text)
	if err == nil {
		if firstLine {
			firstLine = false
		} else {
			if strict {
				fmt.Printf(",")
			} else {
				fmt.Printf("\n")
			}
		}
		fmt.Printf("{ \"type\": \"Feature\", \"geometry\": { \"type\": \"Point\", \"coordinates\": [ %v, %v ] }, \"properties\": { \"name\": %q } }", gl.Lon, gl.Lat, p.Title)
	} else {
		if err != wikiparse.ErrNoCoordFound {
			cherr <- p
			log.Printf("Error parsing geo from %#v: %v", *p, err)
		}
	}
}

func pageHandler(ch <-chan *wikiparse.Page, cherr chan<- *wikiparse.Page) {
	defer wg.Done()
	for p := range ch {
		if parseCoords {
			parsePageCoords(p, cherr)
		}
	}
}

func parsePage(d *xml.Decoder, ch chan<- *wikiparse.Page) error {
	page := wikiparse.Page{}
	err := d.Decode(&page)
	if err != nil {
		return err
	}
	ch <- &page
	return nil
}

func errorHandler(ch <-chan *wikiparse.Page) {
	defer errwg.Done()
	f, err := os.Create("errors.gob")
	if err != nil {
		log.Fatalf("Error creating error file: %v", err)
	}
	defer f.Close()
	g := gob.NewEncoder(f)

	for p := range ch {
		err = g.Encode(p)
		if err != nil {
			log.Fatalf("Error gobbing page: %v\n%#v", err, p)
		}
	}
}

func process(p wikiparse.Parser) {
	log.Printf("Got site info:  %+v", p.SiteInfo())

	if strict {
		fmt.Printf("[")
	}
	ch := make(chan *wikiparse.Page, 1000)
	cherr := make(chan *wikiparse.Page, 10)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go pageHandler(ch, cherr)
	}

	errwg.Add(1)
	go errorHandler(cherr)

	pages := int64(0)
	start := time.Now()
	prev := start
	reportfreq := int64(10000)
	var err error
	for {
		var page *wikiparse.Page
		page, err = p.Next()
		if err != nil {
			break
		}
		ch <- page

		pages++
		if pages%reportfreq == 0 {
			now := time.Now()
			d := now.Sub(prev)
			log.Printf("Processed %s pages total (%.2f/s)",
				humanize.Comma(pages), float64(reportfreq)/d.Seconds())
			prev = now
		}
	}
	close(ch)
	wg.Wait()
	close(cherr)
	errwg.Wait()
	d := time.Since(start)
	if strict {
		fmt.Println("]")
	}
	log.Printf("Ended with err after %v:  %v after %s pages (%.2f p/s)",
		d, err, humanize.Comma(pages), float64(pages)/d.Seconds())
}

func processSingleStream(filename string) {

	p, err := wikiparse.NewParser(goof.OpenInput(filename, compression))
	if err != nil {
		log.Fatalf("Error setting up new page parser:  %v", err)
	}

	process(p)
}

func processMultiStream(idx, data string) {
	p, err := wikiparse.NewIndexedParser(idx, data, runtime.GOMAXPROCS(0))
	if err != nil {
		log.Fatalf("Error initializing multistream parser: %v", err)
	}
	process(p)
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

func main() {
	var cpus int
	var wantHelp bool
	flag.IntVar(&numWorkers, "workers", 8, "Number of parsing workers")
	flag.IntVar(&cpus, "cpus", runtime.GOMAXPROCS(0), "Number of CPUS to utilize")
	flag.StringVar(&compression, "compression", "", "Input is compressed with bz2 or gz")
	flag.BoolVar(&strict, "strict", false, "Emit correct geojson format.  By default, emit grep-friendly geojson.")
	flag.BoolVar(&wantHelp, "help", false, "Print help")
	flag.Parse()
	parseCoords = true

	if wantHelp {
		log.Fatalf(helpMessage())
	}

	inputFile := flag.Arg(0)

	if inputFile == "-" {
		inputFile = ""
	}
	switch flag.NArg() {
	case 1:
		processSingleStream(inputFile)
	case 2:
		processMultiStream(inputFile, flag.Arg(1))
	default:
		log.Fatalf(helpMessage())
	}
}
