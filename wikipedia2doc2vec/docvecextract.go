// Extract geodata from wikipedia pages.
package main

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/dustin/go-humanize"
	"github.com/dustin/go-wikiparse"
)

var compression string
var numWorkers int
var limit int64

const docvecResultWindow = 1000

var wikiCommentPattern = regexp.MustCompile(`(?s)<!--.*?-->`)
var wikiReferencePattern = regexp.MustCompile(`(?is)<ref\b[^>]*>.*?</ref>|<ref\b[^>]*/>`)
var wikiTagPattern = regexp.MustCompile(`(?s)<[^>]+>`)
var wikiLinkPattern = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
var wikiExternalLinkPattern = regexp.MustCompile(`\[(?:https?://|//)[^\s\]]+(?:\s+([^\]]+))?\]`)
var wikiURLPattern = regexp.MustCompile(`(?:https?://|//)\S+`)

type docvecPageTask struct {
	Sequence int64
	Page     *wikiparse.Page
}

type docvecPageResult struct {
	Sequence int64
	Line     string
}

func parsePageWords(page *wikiparse.Page) (string, error) {
	if len(page.Revisions) == 0 {
		return "", fmt.Errorf("page %q (%d) has no revisions", page.Title, page.ID)
	}
	cleaned := cleanWikiText(page.Revisions[0].Text)
	words := strings.FieldsFunc(strings.ToLower(cleaned), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	return fmt.Sprintf("%v\t%s\n", page.ID, strings.Join(words, " ")), nil
}

func cleanWikiText(text string) string {
	text = wikiCommentPattern.ReplaceAllString(text, " ")
	text = wikiReferencePattern.ReplaceAllString(text, " ")
	text = stripBalancedWikiText(text, "{{", "}}")
	text = stripBalancedWikiText(text, "{|", "|}")
	text = wikiLinkPattern.ReplaceAllStringFunc(text, func(link string) string {
		contents := strings.TrimSuffix(strings.TrimPrefix(link, "[["), "]]")
		parts := strings.Split(contents, "|")
		return parts[len(parts)-1]
	})
	text = wikiExternalLinkPattern.ReplaceAllString(text, "$1")
	text = wikiURLPattern.ReplaceAllString(text, " ")
	text = wikiTagPattern.ReplaceAllString(text, " ")
	return html.UnescapeString(text)
}

func stripBalancedWikiText(text, opening, closing string) string {
	var output strings.Builder
	depth := 0
	for index := 0; index < len(text); {
		if strings.HasPrefix(text[index:], opening) {
			depth++
			index += len(opening)
		} else if depth > 0 && strings.HasPrefix(text[index:], closing) {
			depth--
			index += len(closing)
		} else {
			if depth == 0 {
				output.WriteByte(text[index])
			}
			index++
		}
	}
	return output.String()
}

func pageHandler(tasks <-chan docvecPageTask, results chan<- docvecPageResult, errorPages chan<- *wikiparse.Page, workerGroup *sync.WaitGroup) {
	defer workerGroup.Done()
	for task := range tasks {
		line, err := parsePageWords(task.Page)
		if err != nil {
			errorPages <- task.Page
			log.Print(err)
		}
		results <- docvecPageResult{Sequence: task.Sequence, Line: line}
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
				firstErr = fmt.Errorf("failed to create doc2vec error file: %w", err)
				continue
			}
			encoder = gob.NewEncoder(file)
		}
		if err := encoder.Encode(page); err != nil {
			firstErr = fmt.Errorf("failed to encode doc2vec error page %q (%d): %w", page.Title, page.ID, err)
		}
	}
	if file != nil {
		if err := file.Close(); err != nil {
			firstErr = errors.Join(firstErr, fmt.Errorf("failed to close doc2vec error file: %w", err))
		}
	}
	return firstErr
}

func process(parser wikiparse.Parser, output io.Writer) error {
	log.Printf("Got site info:  %+v", parser.SiteInfo())
	queueSize := numWorkers * 2
	tasks := make(chan docvecPageTask, queueSize)
	results := make(chan docvecPageResult, queueSize)
	errorPages := make(chan *wikiparse.Page, 10)
	resultSlots := make(chan struct{}, docvecResultWindow)
	var workerGroup sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		workerGroup.Add(1)
		go pageHandler(tasks, results, errorPages, &workerGroup)
	}
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- writeDocvecResultsWithSlots(results, output, resultSlots)
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
		if limit >= 0 && pages >= limit {
			break
		}
		page, err := parser.Next()
		if err != nil {
			parserErr = err
			break
		}
		resultSlots <- struct{}{}
		tasks <- docvecPageTask{Sequence: pages, Page: page}

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

func writeDocvecResults(results <-chan docvecPageResult, output io.Writer) error {
	return writeDocvecResultsWithSlots(results, output, nil)
}

func writeDocvecResultsWithSlots(results <-chan docvecPageResult, output io.Writer, resultSlots chan struct{}) error {
	writer := bufio.NewWriter(output)
	pending := make(map[int64]docvecPageResult)
	nextSequence := int64(0)
	var firstErr error
	for result := range results {
		pending[result.Sequence] = result
		for {
			next, ok := pending[nextSequence]
			if !ok {
				break
			}
			delete(pending, nextSequence)
			if next.Line != "" && firstErr == nil {
				if _, err := writer.WriteString(next.Line); err != nil {
					firstErr = err
				}
			}
			nextSequence++
			if resultSlots != nil {
				<-resultSlots
			}
		}
	}
	if len(pending) != 0 {
		firstErr = errors.Join(firstErr, fmt.Errorf("doc2vec result stream ended with %d out-of-order results pending at sequence %d", len(pending), nextSequence))
	}
	return errors.Join(firstErr, writer.Flush())
}

type wikipediaInput struct {
	io.Reader
	file       *os.File
	compressor *gzip.Reader
	closeFile  bool
}

func openWikipediaInput(filename, configuredCompression string) (*wikipediaInput, error) {
	selectedCompression := strings.ToLower(strings.TrimSpace(configuredCompression))
	if selectedCompression == "" {
		lowerName := strings.ToLower(filename)
		if strings.HasSuffix(lowerName, ".gz") {
			selectedCompression = "gz"
		} else if strings.HasSuffix(lowerName, ".bz2") {
			selectedCompression = "bz2"
		}
	}
	if selectedCompression != "" && selectedCompression != "gz" && selectedCompression != "bz2" {
		return nil, fmt.Errorf("Wikipedia input compression %q is unsupported; expected gz, bz2, or empty for filename detection", configuredCompression)
	}
	file := os.Stdin
	closeFile := false
	var err error
	if filename != "" {
		file, err = os.Open(filename)
		if err != nil {
			return nil, fmt.Errorf("failed to open Wikipedia input %q: %w", filename, err)
		}
		closeFile = true
	}
	buffered := bufio.NewReaderSize(file, 64*1024)
	input := &wikipediaInput{Reader: buffered, file: file, closeFile: closeFile}
	if selectedCompression == "gz" {
		input.compressor, err = gzip.NewReader(buffered)
		if err != nil {
			openErr := fmt.Errorf("failed to open gzip-compressed Wikipedia input %q: %w", filename, err)
			if closeFile {
				return nil, errors.Join(openErr, file.Close())
			}
			return nil, openErr
		}
		input.Reader = input.compressor
	} else if selectedCompression == "bz2" {
		input.Reader = bzip2.NewReader(buffered)
	}
	return input, nil
}

func (input *wikipediaInput) Close() error {
	var compressorErr error
	if input.compressor != nil {
		compressorErr = input.compressor.Close()
	}
	var fileErr error
	if input.closeFile {
		fileErr = input.file.Close()
	}
	return errors.Join(compressorErr, fileErr)
}

func processSingleStream(filename string) error {
	input, err := openWikipediaInput(filename, compression)
	if err != nil {
		return err
	}
	p, err := wikiparse.NewParser(input)
	if err != nil {
		return errors.Join(fmt.Errorf("failed to set up Wikipedia parser for %q: %w", filename, err), input.Close())
	}
	return errors.Join(process(p, os.Stdout), input.Close())
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
		
	wikipedia2doc2vec.exe file.xml
	
		Read from file.xml

		
		wikipedia2doc2vec.exe file.xml.bz2
	
		Read from file.xml.bz2, automatically uncompressing bz2 format

		
		wikipedia2doc2vec.exe file.xml.gz

		Read from file.xml.gz, automatically uncompressing gz format

		
		wikipedia2doc2vec.exe --compression=bz2 file
	
		Read from file, force uncompressing bz2 format

		
		wikipedia2doc2vec.exe --compression=gz file
	
		Read from file, force uncompressing gz format

		
		wikipedia2doc2vec.exe -
	
		Read from stdin.

		
		wikipedia2doc2vec.exe --compression=bz2 -
	
		Read from stdin.  Stdin is in bzip2 format
	
	
		wikipedia2doc2vec.exe --compression=gz -
	
		Read from stdin.  Stdin is in gz format
	`
}

func runDocvecBuiltInTests() error {
	cleaned := cleanWikiText(`Hello [[Earth|world]] {{template|bad}} <ref>citation</ref> café 42`)
	words := strings.FieldsFunc(strings.ToLower(cleaned), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	if strings.Join(words, " ") != "hello world café 42" {
		return fmt.Errorf("wiki cleanup produced %q", strings.Join(words, " "))
	}
	results := make(chan docvecPageResult, 2)
	results <- docvecPageResult{Sequence: 1, Line: "2\tsecond\n"}
	results <- docvecPageResult{Sequence: 0, Line: "1\tfirst\n"}
	close(results)
	var output bytes.Buffer
	if err := writeDocvecResults(results, &output); err != nil {
		return err
	}
	if output.String() != "1\tfirst\n2\tsecond\n" {
		return fmt.Errorf("out-of-order worker results were written as %q", output.String())
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
	flag.BoolVar(&wantHelp, "help", false, "Print help")
	flag.Int64Var(&limit, "limit", -1, "Stop after this many pages; -1 processes the complete input")
	flag.BoolVar(&runBuiltInTests, "test", false, "Run built-in tests and exit")
	flag.Parse()

	if wantHelp {
		fmt.Print(helpMessage())
		return
	}
	if runBuiltInTests {
		if err := runDocvecBuiltInTests(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wikipedia2doc2vec built-in tests passed")
		return
	}
	if numWorkers < 1 {
		log.Fatal("invalid workers: expected an integer greater than zero")
	}
	if numWorkers > docvecResultWindow {
		log.Fatalf("invalid workers: received %d, maximum is %d", numWorkers, docvecResultWindow)
	}
	if cpus < 1 {
		log.Fatal("invalid cpus: expected an integer greater than zero")
	}
	if limit < -1 {
		log.Fatal("invalid limit: expected -1 or a non-negative integer")
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
