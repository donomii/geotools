package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/donomii/geotools/geodata"
)

func runBuiltInTest() error {
	input := bytes.NewBufferString(`{"type":"Feature","id":"one","geometry":{"type":"Point","coordinates":[2.2945,48.8584]},"properties":{"name":"Eiffel Tower"}}`)
	var jsonFG bytes.Buffer
	if err := encodeJSONFG(input, &jsonFG, geodata.InputAuto); err != nil {
		return err
	}
	var output bytes.Buffer
	if err := decodeJSONFG(&jsonFG, &output, geodata.OutputCollection); err != nil {
		return err
	}
	count := 0
	if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
		count++
		return nil
	}); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("JSON-FG round trip returned %d Features; expected 1", count)
	}
	return nil
}

func main() {
	mode := flag.String("mode", "encode", "Operation: encode adds the JSON-FG 1.0 core declaration; decode verifies and removes the root declaration")
	inputName := flag.String("input", "auto", "GeoJSON input format for encode: auto detects JSONL, arrays, FeatureCollections, and RFC 8142 sequences; seq requires record separators")
	outputName := flag.String("output", "jsonl", "GeoJSON output format for decode: jsonl writes one Feature per line, collection writes a FeatureCollection, and seq writes RFC 8142 records")
	placeCRS := flag.String("place-crs", geodata.CRSCRS84, "CRS for encoded JSON-FG place geometries: OGC:CRS84, OGC:CRS84h, EPSG:4326, or EPSG:3857; geometry remains the WGS 84 GeoJSON fallback")
	timeProperty := flag.String("time-property", defaultJSONFGTimePropertyName, "GeoJSON property mapped to JSON-FG time while encoding and restored while decoding; empty disables temporal mapping")
	runTest := flag.Bool("test", false, "Run an in-memory GeoJSON-to-JSON-FG-to-GeoJSON round-trip check and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("jsonfg reads standard input and writes standard output; positional arguments are not accepted")
	}
	if *runTest {
		if err := runBuiltInTest(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("jsonfg built-in test passed")
		return
	}
	switch *mode {
	case "encode":
		inputMode, err := geodata.ParseInputMode(*inputName)
		if err != nil {
			log.Fatal(err)
		}
		settings := jsonFGSettings{PlaceCRS: *placeCRS, TimeProperty: *timeProperty}
		if err := encodeJSONFGWithSettings(os.Stdin, os.Stdout, inputMode, settings); err != nil {
			log.Fatal(err)
		}
	case "decode":
		outputMode, err := geodata.ParseOutputMode(*outputName)
		if err != nil {
			log.Fatal(err)
		}
		settings := jsonFGSettings{TimeProperty: *timeProperty}
		if err := decodeJSONFGWithSettings(os.Stdin, os.Stdout, outputMode, settings); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("invalid mode %q; expected encode or decode", *mode)
	}
}
