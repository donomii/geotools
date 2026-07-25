package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/donomii/geotools/geodata"
	gogamafgb "github.com/gogama/flatgeobuf/flatgeobuf"
	"github.com/gogama/flatgeobuf/flatgeobuf/flat"
)

const preservedFeatureProperty = "__geotools_feature_json"

func encodeFlatGeobuf(input io.Reader, output io.Writer, inputMode geodata.InputMode, layerName string) error {
	if layerName == "" {
		return fmt.Errorf("FlatGeobuf layer name is empty")
	}
	var sourceFeatures []flatSourceFeature
	featureNumber := 0
	err := geodata.ReadFeatures(input, inputMode, func(feature geodata.Feature) error {
		featureNumber++
		summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{})
		if err != nil {
			return fmt.Errorf("FlatGeobuf input Feature %d with id %s is invalid: %w", featureNumber, feature.EncodedID(), err)
		}
		if summary.CoordinateDimension != 2 {
			return fmt.Errorf("FlatGeobuf input Feature %d with id %s has %d-coordinate positions; this converter supports 2D positions", featureNumber, feature.EncodedID(), summary.CoordinateDimension)
		}
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		if _, exists := properties[preservedFeatureProperty]; exists {
			return fmt.Errorf("Feature %d with id %s uses reserved property %q", featureNumber, feature.EncodedID(), preservedFeatureProperty)
		}
		featureJSON, err := json.Marshal(feature)
		if err != nil {
			return fmt.Errorf("failed to preserve Feature %s: %w", feature.EncodedID(), err)
		}
		geometry, err := geodata.OrbGeometry(feature.Geometry)
		if err != nil {
			return err
		}
		sourceFeatures = append(sourceFeatures, flatSourceFeature{
			Feature:     feature,
			Geometry:    geometry,
			Properties:  properties,
			FeatureJSON: featureJSON,
		})
		return nil
	})
	if err != nil {
		return err
	}
	if len(sourceFeatures) == 0 {
		return fmt.Errorf("FlatGeobuf output requires at least one Feature; input contained none")
	}
	columns, err := inferFlatColumns(sourceFeatures)
	if err != nil {
		return err
	}
	encodedFeatures := make([]flat.Feature, 0, len(sourceFeatures))
	for index, source := range sourceFeatures {
		encoded, err := buildFlatFeature(source, columns)
		if err != nil {
			return fmt.Errorf("failed to encode FlatGeobuf Feature %d with id %s: %w", index+1, source.Feature.EncodedID(), err)
		}
		encodedFeatures = append(encodedFeatures, encoded)
	}
	header, err := buildFlatHeader(layerName, columns, sourceFeatures)
	if err != nil {
		return err
	}
	fileWriter := gogamafgb.NewFileWriter(writerWithoutClose{output})
	if _, err := fileWriter.Header(header); err != nil {
		return fmt.Errorf("failed to write FlatGeobuf header for %d Features: %w", len(sourceFeatures), err)
	}
	if _, err := fileWriter.IndexData(encodedFeatures); err != nil {
		return fmt.Errorf("failed to write FlatGeobuf index and %d Features: %w", len(sourceFeatures), err)
	}
	if err := fileWriter.Close(); err != nil {
		return fmt.Errorf("failed to finish FlatGeobuf output after %d Features: %w", len(sourceFeatures), err)
	}
	return nil
}

func decodeFlatGeobuf(input io.Reader, output io.Writer, outputMode geodata.OutputMode) error {
	data, err := io.ReadAll(input)
	if err != nil {
		return fmt.Errorf("failed to read FlatGeobuf input: %w", err)
	}
	reader := gogamafgb.NewFileReader(bytes.NewReader(data))
	header, err := reader.Header()
	if err != nil {
		return fmt.Errorf("input has an invalid FlatGeobuf header: %w", err)
	}
	if err := validateFlatCRS(header); err != nil {
		return err
	}
	if header.IndexNodeSize() > 0 {
		if _, err := reader.Index(); err != nil {
			return fmt.Errorf("failed to read FlatGeobuf spatial index: %w", err)
		}
	}
	encodedFeatures, err := reader.DataRem()
	if err != nil {
		return fmt.Errorf("failed to read FlatGeobuf Features: %w", err)
	}
	writer := geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	var decodeErr error
	for index := range encodedFeatures {
		feature, err := decodeFlatFeature(&encodedFeatures[index], header)
		if err != nil {
			decodeErr = fmt.Errorf("FlatGeobuf Feature %d is invalid: %w", index+1, err)
			break
		}
		if _, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{}); err != nil {
			decodeErr = fmt.Errorf("FlatGeobuf Feature %d failed GeoJSON validation: %w", index+1, err)
			break
		}
		if err := writer.Write(feature); err != nil {
			decodeErr = err
			break
		}
	}
	return errors.Join(decodeErr, writer.Close())
}

func runBuiltInTest() error {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[{"type":"Feature","id":"one","geometry":{"type":"LineString","coordinates":[[103.8,1.2],[103.9,1.3]]},"properties":{"name":"route","active":true}}]}`)
	var flatData bytes.Buffer
	if err := encodeFlatGeobuf(input, &flatData, geodata.InputAuto, "features"); err != nil {
		return err
	}
	var output bytes.Buffer
	if err := decodeFlatGeobuf(&flatData, &output, geodata.OutputCollection); err != nil {
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
		return fmt.Errorf("FlatGeobuf round trip returned %d Features; expected 1", count)
	}
	return nil
}

func main() {
	mode := flag.String("mode", "encode", "Operation: encode converts GeoJSON to FlatGeobuf; decode converts FlatGeobuf to GeoJSON")
	inputName := flag.String("input", "auto", "GeoJSON input format for encode: auto detects JSONL, arrays, FeatureCollections, and RFC 8142 sequences; seq requires record separators")
	outputName := flag.String("output", "jsonl", "GeoJSON output format for decode: jsonl writes one Feature per line, collection writes a FeatureCollection, and seq writes RFC 8142 records")
	layerName := flag.String("layer", "features", "Layer name stored in encoded FlatGeobuf metadata")
	runTest := flag.Bool("test", false, "Run an in-memory GeoJSON-to-FlatGeobuf-to-GeoJSON round-trip check and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("flatgeobuf reads standard input and writes standard output; positional arguments are not accepted")
	}
	if *runTest {
		if err := runBuiltInTest(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("flatgeobuf built-in test passed")
		return
	}
	switch *mode {
	case "encode":
		inputMode, err := geodata.ParseInputMode(*inputName)
		if err != nil {
			log.Fatal(err)
		}
		if err := encodeFlatGeobuf(os.Stdin, os.Stdout, inputMode, *layerName); err != nil {
			log.Fatal(err)
		}
	case "decode":
		outputMode, err := geodata.ParseOutputMode(*outputName)
		if err != nil {
			log.Fatal(err)
		}
		if err := decodeFlatGeobuf(os.Stdin, os.Stdout, outputMode); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("invalid mode %q; expected encode or decode", *mode)
	}
}
