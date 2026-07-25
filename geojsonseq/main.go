package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/donomii/geotools/geodata"
)

func convertSequence(inputMode geodata.InputMode, outputMode geodata.OutputMode, input io.Reader, output io.Writer) error {
	writer := geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return fmt.Errorf("failed to start %s output: %w", outputMode, err)
	}
	convertErr := geodata.ReadFeatures(input, inputMode, writer.Write)
	closeErr := writer.Close()
	if convertErr != nil {
		return convertErr
	}
	if closeErr != nil {
		return fmt.Errorf("failed to finish %s output after %d Features: %w", outputMode, writer.FeatureCount(), closeErr)
	}
	return nil
}

func runBuiltInTest() error {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[{"type":"Feature","id":"one","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}]}`)
	var sequence bytes.Buffer
	writer := geodata.NewFeatureWriter(&sequence, geodata.OutputSequence)
	if err := writer.Start(); err != nil {
		return err
	}
	if err := geodata.ReadFeatures(input, geodata.InputAuto, writer.Write); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	count := 0
	if err := geodata.ReadFeatures(&sequence, geodata.InputSequence, func(feature geodata.Feature) error {
		count++
		return nil
	}); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("GeoJSON sequence round trip returned %d Features; expected 1", count)
	}
	return nil
}

func main() {
	inputName := flag.String("input", "auto", "Input format: auto detects JSONL, arrays, FeatureCollections, and sequences; seq requires RFC 8142 record separators")
	outputName := flag.String("output", "seq", "Output format: seq writes RFC 8142 records, jsonl writes one compact Feature per line, and collection writes one FeatureCollection")
	runTest := flag.Bool("test", false, "Run a built-in FeatureCollection-to-sequence round-trip check and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("geojsonseq reads standard input and writes standard output; positional arguments are not accepted")
	}
	if *runTest {
		if err := runBuiltInTest(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("geojsonseq built-in test passed")
		return
	}
	inputMode, err := geodata.ParseInputMode(*inputName)
	if err != nil {
		log.Fatal(err)
	}
	outputMode, err := geodata.ParseOutputMode(*outputName)
	if err != nil {
		log.Fatal(err)
	}
	if err := convertSequence(inputMode, outputMode, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
