package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/donomii/geotools/geodata"
	gogamafgb "github.com/gogama/flatgeobuf/flatgeobuf"
	"github.com/gogama/flatgeobuf/flatgeobuf/flat"
	"github.com/gogama/flatgeobuf/packedrtree"
)

const preservedFeatureProperty = "__geotools_feature_json"

type flatEncodeSettings struct {
	InputMode geodata.InputMode
	LayerName string
	Indexed   bool
}

func encodeFlatGeobuf(input io.Reader, output io.Writer, inputMode geodata.InputMode, layerName string) error {
	return encodeFlatGeobufWithSettings(input, output, flatEncodeSettings{InputMode: inputMode, LayerName: layerName, Indexed: true})
}

func encodeFlatGeobufWithSettings(input io.Reader, output io.Writer, settings flatEncodeSettings) error {
	if settings.LayerName == "" {
		return fmt.Errorf("FlatGeobuf layer name is empty")
	}
	if !settings.Indexed {
		return encodeStreamingFlatGeobuf(input, output, settings)
	}
	var sourceFeatures []flatSourceFeature
	featureNumber := 0
	err := geodata.ReadFeatures(input, settings.InputMode, func(feature geodata.Feature) error {
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
	indexed := len(sourceFeatures) > 0
	header, err := buildFlatHeader(settings.LayerName, columns, sourceFeatures, indexed)
	if err != nil {
		return err
	}
	fileWriter := gogamafgb.NewFileWriter(writerWithoutClose{output})
	if _, err := fileWriter.Header(header); err != nil {
		return fmt.Errorf("failed to write FlatGeobuf header for %d Features: %w", len(sourceFeatures), err)
	}
	if indexed {
		if _, err := fileWriter.IndexData(encodedFeatures); err != nil {
			return fmt.Errorf("failed to write FlatGeobuf index and %d Features: %w", len(sourceFeatures), err)
		}
	} else if _, err := fileWriter.Data(encodedFeatures); err != nil {
		return fmt.Errorf("failed to write empty FlatGeobuf data section: %w", err)
	}
	if err := fileWriter.Close(); err != nil {
		return fmt.Errorf("failed to finish FlatGeobuf output after %d Features: %w", len(sourceFeatures), err)
	}
	return nil
}

func encodeStreamingFlatGeobuf(input io.Reader, output io.Writer, settings flatEncodeSettings) error {
	columns := []flatColumn{{Name: preservedFeatureProperty, Type: flat.ColumnTypeString}}
	header, err := buildFlatHeader(settings.LayerName, columns, nil, false)
	if err != nil {
		return err
	}
	fileWriter := gogamafgb.NewFileWriter(writerWithoutClose{output})
	if _, err := fileWriter.Header(header); err != nil {
		return fmt.Errorf("failed to write streaming FlatGeobuf header: %w", err)
	}
	featureNumber := 0
	readErr := geodata.ReadFeatures(input, settings.InputMode, func(feature geodata.Feature) error {
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
		featureJSON, err := json.Marshal(feature)
		if err != nil {
			return fmt.Errorf("failed to preserve Feature %s: %w", feature.EncodedID(), err)
		}
		geometry, err := geodata.OrbGeometry(feature.Geometry)
		if err != nil {
			return err
		}
		encoded, err := buildFlatFeature(flatSourceFeature{
			Feature:     feature,
			Geometry:    geometry,
			Properties:  properties,
			FeatureJSON: featureJSON,
		}, columns)
		if err != nil {
			return fmt.Errorf("failed to encode streaming FlatGeobuf Feature %d with id %s: %w", featureNumber, feature.EncodedID(), err)
		}
		if _, err := fileWriter.Data([]flat.Feature{encoded}); err != nil {
			return fmt.Errorf("failed to write streaming FlatGeobuf Feature %d with id %s: %w", featureNumber, feature.EncodedID(), err)
		}
		return nil
	})
	if readErr != nil {
		return errors.Join(readErr, fileWriter.Close())
	}
	if err := fileWriter.Close(); err != nil {
		return fmt.Errorf("failed to finish streaming FlatGeobuf output after %d Features: %w", featureNumber, err)
	}
	return nil
}

func decodeFlatGeobuf(input io.Reader, output io.Writer, outputMode geodata.OutputMode) error {
	return decodeFlatGeobufWithBBox(input, output, outputMode, nil)
}

func decodeFlatGeobufWithBBox(input io.Reader, output io.Writer, outputMode geodata.OutputMode, bbox *[4]float64) error {
	if bbox != nil {
		return decodeFlatGeobufBBox(input, output, outputMode, *bbox)
	}
	reader := gogamafgb.NewFileReader(input)
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
	writer := geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	var decodeErr error
	featureNumber := 0
	buffer := make([]flat.Feature, 64)
	for {
		count, err := reader.Data(buffer)
		for index := 0; index < count; index++ {
			featureNumber++
			if writeErr := writeDecodedFlatFeature(&buffer[index], header, writer, featureNumber, nil); writeErr != nil {
				decodeErr = writeErr
				break
			}
		}
		if decodeErr != nil || err == io.EOF {
			break
		}
		if err != nil {
			decodeErr = fmt.Errorf("failed to read FlatGeobuf Feature %d: %w", featureNumber+1, err)
			break
		}
	}
	return errors.Join(decodeErr, writer.Close())
}

func decodeFlatGeobufBBox(input io.Reader, output io.Writer, outputMode geodata.OutputMode, bbox [4]float64) error {
	data, err := io.ReadAll(input)
	if err != nil {
		return fmt.Errorf("failed to read FlatGeobuf input for bbox query: %w", err)
	}
	reader := gogamafgb.NewFileReader(bytes.NewReader(data))
	header, err := reader.Header()
	if err != nil {
		return fmt.Errorf("input has an invalid FlatGeobuf header: %w", err)
	}
	if err := validateFlatCRS(header); err != nil {
		return err
	}
	var encodedFeatures []flat.Feature
	if header.IndexNodeSize() > 0 && header.FeaturesCount() > 0 {
		if _, err := reader.Index(); err != nil {
			return fmt.Errorf("failed to read FlatGeobuf spatial index for bbox %v: %w", bbox, err)
		}
		if err := reader.Rewind(); err != nil {
			return fmt.Errorf("failed to rewind FlatGeobuf spatial index for bbox %v: %w", bbox, err)
		}
		query := packedrtree.Box{XMin: bbox[0], YMin: bbox[1], XMax: bbox[2], YMax: bbox[3]}
		encodedFeatures, err = reader.IndexSearch(query)
	} else {
		encodedFeatures, err = reader.DataRem()
	}
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to query FlatGeobuf Features in bbox %v: %w", bbox, err)
	}
	writer := geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	var decodeErr error
	for index := range encodedFeatures {
		if err := writeDecodedFlatFeature(&encodedFeatures[index], header, writer, index+1, &bbox); err != nil {
			decodeErr = err
			break
		}
	}
	return errors.Join(decodeErr, writer.Close())
}

func writeDecodedFlatFeature(encoded *flat.Feature, header *flat.Header, writer *geodata.FeatureWriter, featureNumber int, bbox *[4]float64) error {
	feature, err := decodeFlatFeature(encoded, header)
	if err != nil {
		return fmt.Errorf("FlatGeobuf Feature %d is invalid: %w", featureNumber, err)
	}
	summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{})
	if err != nil {
		return fmt.Errorf("FlatGeobuf Feature %d failed GeoJSON validation: %w", featureNumber, err)
	}
	if bbox != nil && (!summary.HasBounds || summary.Bounds[2] < bbox[0] || summary.Bounds[0] > bbox[2] || summary.Bounds[3] < bbox[1] || summary.Bounds[1] > bbox[3]) {
		return nil
	}
	return writer.Write(feature)
}

func parseFlatBBox(value string) (*[4]float64, error) {
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) != 4 {
		return nil, fmt.Errorf("FlatGeobuf bbox %q has %d values; expected minLon,minLat,maxLon,maxLat", value, len(parts))
	}
	var bbox [4]float64
	for index, part := range parts {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil, fmt.Errorf("FlatGeobuf bbox value %d %q is not a finite number", index+1, part)
		}
		bbox[index] = parsed
	}
	if bbox[0] > bbox[2] || bbox[1] > bbox[3] {
		return nil, fmt.Errorf("FlatGeobuf bbox %v has a minimum greater than its maximum", bbox)
	}
	return &bbox, nil
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
	indexed := flag.Bool("index", true, "Build a packed spatial index and native property columns while encoding; false streams immediately with exact Features in one JSON column")
	bboxValue := flag.String("bbox", "", "Decode only Features intersecting minLon,minLat,maxLon,maxLat; an indexed input is queried directly and an unindexed input is scanned")
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
		settings := flatEncodeSettings{InputMode: inputMode, LayerName: *layerName, Indexed: *indexed}
		if err := encodeFlatGeobufWithSettings(os.Stdin, os.Stdout, settings); err != nil {
			log.Fatal(err)
		}
	case "decode":
		outputMode, err := geodata.ParseOutputMode(*outputName)
		if err != nil {
			log.Fatal(err)
		}
		bbox, err := parseFlatBBox(*bboxValue)
		if err != nil {
			log.Fatal(err)
		}
		if err := decodeFlatGeobufWithBBox(os.Stdin, os.Stdout, outputMode, bbox); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("invalid mode %q; expected encode or decode", *mode)
	}
}
