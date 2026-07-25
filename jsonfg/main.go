package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/donomii/geotools/geodata"
)

const jsonFGCoreConformance = "http://www.opengis.net/spec/json-fg-1/1.0/conf/core"

var jsonFGFeatureMembers = []string{"conformsTo", "coordRefSys", "featureSchema", "featureType", "geometryDimension", "measures", "place", "time"}

func encodeJSONFG(input io.Reader, output io.Writer, inputMode geodata.InputMode) error {
	buffered := bufio.NewWriter(output)
	if _, err := buffered.WriteString(`{"type":"FeatureCollection","conformsTo":["` + jsonFGCoreConformance + `"],"features":[`); err != nil {
		return err
	}
	featureNumber := 0
	readErr := geodata.ReadFeatures(input, inputMode, func(feature geodata.Feature) error {
		featureNumber++
		if _, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{AllowNullGeometry: true}); err != nil {
			return fmt.Errorf("JSON-FG input Feature %d with id %s is invalid: %w", featureNumber, feature.EncodedID(), err)
		}
		for _, member := range jsonFGFeatureMembers {
			if _, exists := feature.Foreign[member]; exists {
				return fmt.Errorf("Feature %d with id %s already contains JSON-FG member %q; encode expects plain GeoJSON input", featureNumber, feature.EncodedID(), member)
			}
		}
		encoded, err := json.Marshal(feature)
		if err != nil {
			return fmt.Errorf("failed to encode Feature %s as JSON-FG: %w", feature.EncodedID(), err)
		}
		if featureNumber > 1 {
			if err := buffered.WriteByte(','); err != nil {
				return err
			}
		}
		_, err = buffered.Write(encoded)
		return err
	})
	if readErr != nil {
		return errors.Join(readErr, buffered.Flush())
	}
	if _, err := buffered.WriteString("]}\n"); err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("failed to finish JSON-FG output after %d Features: %w", featureNumber, err)
	}
	return nil
}

func decodeJSONFG(input io.Reader, output io.Writer, outputMode geodata.OutputMode) error {
	data, err := io.ReadAll(input)
	if err != nil {
		return fmt.Errorf("failed to read JSON-FG input: %w", err)
	}
	if err := requireCoreConformance(data); err != nil {
		return err
	}
	writer := geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	readErr := geodata.ReadFeatures(bytes.NewReader(data), geodata.InputAuto, func(feature geodata.Feature) error {
		delete(feature.Foreign, "conformsTo")
		if _, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{AllowNullGeometry: true}); err != nil {
			return err
		}
		return writer.Write(feature)
	})
	return errors.Join(readErr, writer.Close())
}

func requireCoreConformance(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("JSON-FG root is invalid JSON: %w", err)
	}
	var conformance []string
	raw, exists := root["conformsTo"]
	if !exists {
		return fmt.Errorf("JSON-FG root is missing conformsTo")
	}
	if err := json.Unmarshal(raw, &conformance); err != nil {
		return fmt.Errorf("JSON-FG root conformsTo must be an array of strings: %w", err)
	}
	for _, value := range conformance {
		if value == jsonFGCoreConformance {
			return nil
		}
	}
	return fmt.Errorf("JSON-FG root conformsTo does not include %q", jsonFGCoreConformance)
}

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
		if err := encodeJSONFG(os.Stdin, os.Stdout, inputMode); err != nil {
			log.Fatal(err)
		}
	case "decode":
		outputMode, err := geodata.ParseOutputMode(*outputName)
		if err != nil {
			log.Fatal(err)
		}
		if err := decodeJSONFG(os.Stdin, os.Stdout, outputMode); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("invalid mode %q; expected encode or decode", *mode)
	}
}
