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
	"sort"
	"strconv"
	"strings"

	"github.com/donomii/geotools/geodata"
	gogamafgb "github.com/gogama/flatgeobuf/flatgeobuf"
	"github.com/gogama/flatgeobuf/flatgeobuf/flat"
	"github.com/gogama/flatgeobuf/packedrtree"
	"github.com/twpayne/go-geom"
)

const preservedFeatureProperty = "__geotools_feature_json"

type flatEncodeSettings struct {
	InputMode geodata.InputMode
	LayerName string
	Indexed   bool
	CRS       string
	Dimension int
}

func encodeFlatGeobuf(input io.Reader, output io.Writer, inputMode geodata.InputMode, layerName string) error {
	return encodeFlatGeobufWithSettings(input, output, flatEncodeSettings{InputMode: inputMode, LayerName: layerName, Indexed: true, CRS: geodata.CRSCRS84})
}

func encodeFlatGeobufWithSettings(input io.Reader, output io.Writer, settings flatEncodeSettings) error {
	if settings.LayerName == "" {
		return fmt.Errorf("FlatGeobuf layer name is empty")
	}
	configuredCRS := settings.CRS
	if configuredCRS == "" {
		configuredCRS = geodata.CRSCRS84
	}
	targetCRS, err := geodata.NormalizeCRS(configuredCRS)
	if err != nil {
		return err
	}
	if !settings.Indexed {
		if settings.Dimension == 0 {
			settings.Dimension = 2
		}
		if settings.Dimension != 2 && settings.Dimension != 3 {
			return fmt.Errorf("streaming FlatGeobuf coordinate dimension is %d; expected 2 or 3", settings.Dimension)
		}
		return encodeStreamingFlatGeobuf(input, output, settings, targetCRS)
	}
	var sourceFeatures []flatSourceFeature
	featureNumber := 0
	err = geodata.ReadFeatures(input, settings.InputMode, func(feature geodata.Feature) error {
		featureNumber++
		source, err := prepareFlatSourceFeature(feature, targetCRS)
		if err != nil {
			return fmt.Errorf("FlatGeobuf input Feature %d with id %s is invalid: %w", featureNumber, feature.EncodedID(), err)
		}
		sourceFeatures = append(sourceFeatures, source)
		return nil
	})
	if err != nil {
		return err
	}
	dimension, err := flatFeatureDimension(sourceFeatures)
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
	encodedFeatures = sortFlatFeaturesForIndex(sourceFeatures, encodedFeatures)
	indexed := len(sourceFeatures) > 0
	header, err := buildFlatHeader(settings.LayerName, columns, sourceFeatures, indexed, targetCRS, dimension)
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

func sortFlatFeaturesForIndex(sourceFeatures []flatSourceFeature, encodedFeatures []flat.Feature) []flat.Feature {
	if len(sourceFeatures) < 2 {
		return encodedFeatures
	}
	refs := make([]packedrtree.Ref, len(sourceFeatures))
	allBounds := packedrtree.EmptyBox
	for index, source := range sourceFeatures {
		bounds := source.Geometry.Bounds()
		refs[index] = packedrtree.Ref{
			Box: packedrtree.Box{
				XMin: bounds.Min(0),
				YMin: bounds.Min(1),
				XMax: bounds.Max(0),
				YMax: bounds.Max(1),
			},
			Offset: int64(index),
		}
		allBounds.Expand(&refs[index].Box)
	}
	packedrtree.HilbertSort(refs, allBounds)
	sorted := make([]flat.Feature, len(encodedFeatures))
	for index, ref := range refs {
		sorted[index] = encodedFeatures[ref.Offset]
	}
	return sorted
}

func encodeStreamingFlatGeobuf(input io.Reader, output io.Writer, settings flatEncodeSettings, targetCRS string) error {
	columns := []flatColumn{{Name: preservedFeatureProperty, Type: flat.ColumnTypeString}}
	header, err := buildFlatHeader(settings.LayerName, columns, nil, false, targetCRS, settings.Dimension)
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
		source, err := prepareFlatSourceFeature(feature, targetCRS)
		if err != nil {
			return fmt.Errorf("FlatGeobuf input Feature %d with id %s is invalid: %w", featureNumber, feature.EncodedID(), err)
		}
		if source.Geometry.Stride() != settings.Dimension {
			return fmt.Errorf("FlatGeobuf input Feature %d with id %s is %dD; streaming header declares %dD coordinates", featureNumber, feature.EncodedID(), source.Geometry.Stride(), settings.Dimension)
		}
		encoded, err := buildFlatFeature(source, columns)
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

func flatFeatureDimension(features []flatSourceFeature) (int, error) {
	dimension := 2
	if len(features) > 0 {
		dimension = features[0].Geometry.Stride()
	}
	for index := 1; index < len(features); index++ {
		if features[index].Geometry.Stride() != dimension {
			return 0, fmt.Errorf("FlatGeobuf requires one coordinate dimension; Feature 1 is %dD while Feature %d is %dD", dimension, index+1, features[index].Geometry.Stride())
		}
	}
	return dimension, nil
}

func prepareFlatSourceFeature(feature geodata.Feature, targetCRS string) (flatSourceFeature, error) {
	summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{})
	if err != nil {
		return flatSourceFeature{}, err
	}
	if summary.CoordinateDimension != 2 && summary.CoordinateDimension != 3 {
		return flatSourceFeature{}, fmt.Errorf("geometry has %d-coordinate positions; FlatGeobuf supports 2D or 3D positions", summary.CoordinateDimension)
	}
	properties, err := feature.PropertyMap()
	if err != nil {
		return flatSourceFeature{}, err
	}
	if _, exists := properties[preservedFeatureProperty]; exists {
		return flatSourceFeature{}, fmt.Errorf("Feature uses reserved property %q", preservedFeatureProperty)
	}
	featureJSON, err := json.Marshal(feature)
	if err != nil {
		return flatSourceFeature{}, fmt.Errorf("failed to preserve Feature %s: %w", feature.EncodedID(), err)
	}
	geometry, err := geodata.DecodeGeomJSON(feature.Geometry)
	if err != nil {
		return flatSourceFeature{}, err
	}
	sourceCRS := geodata.CRSCRS84
	if geometry.Stride() == 3 {
		sourceCRS = geodata.CRSCRS84h
	}
	if _, err := geodata.TransformGeometry(geometry, sourceCRS, targetCRS); err != nil {
		return flatSourceFeature{}, fmt.Errorf("geometry cannot be reprojected from %s to %s: %w", sourceCRS, targetCRS, err)
	}
	return flatSourceFeature{Feature: feature, Geometry: geometry, Properties: properties, FeatureJSON: featureJSON}, nil
}

func decodeFlatGeobuf(input io.Reader, output io.Writer, outputMode geodata.OutputMode) error {
	return decodeFlatGeobufWithBBox(input, output, outputMode, nil)
}

func decodeFlatGeobufWithBBox(input io.Reader, output io.Writer, outputMode geodata.OutputMode, bbox *[4]float64) error {
	flatInput, seeker, inputOffset := flatGeobufReader(input)
	reader := gogamafgb.NewFileReader(flatInput)
	header, err := reader.Header()
	if err != nil {
		return fmt.Errorf("input has an invalid FlatGeobuf header: %w", err)
	}
	sourceCRS, err := flatHeaderCRS(header)
	if err != nil {
		return err
	}
	writer := geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	var indexedMatches packedrtree.Results
	if bbox != nil && header.IndexNodeSize() != 0 && header.FeaturesCount() != 0 {
		searchBoxes, usable := flatIndexSearchBoxes(*bbox, sourceCRS)
		if usable {
			if seeker != nil {
				indexOffset, seekErr := seeker.Seek(0, io.SeekCurrent)
				if seekErr != nil {
					err = fmt.Errorf("failed to locate the FlatGeobuf packed index: %w", seekErr)
				}
				for _, searchBox := range searchBoxes {
					if err != nil {
						break
					}
					if _, err = seeker.Seek(indexOffset, io.SeekStart); err == nil {
						var matches packedrtree.Results
						matches, err = packedrtree.Seek(seeker, int(header.FeaturesCount()), header.IndexNodeSize(), searchBox)
						indexedMatches = append(indexedMatches, matches...)
					}
				}
			} else {
				var index *packedrtree.PackedRTree
				index, err = reader.Index()
				if err == nil {
					for _, searchBox := range searchBoxes {
						indexedMatches = append(indexedMatches, index.Search(searchBox)...)
					}
				}
			}
			indexedMatches = uniqueFlatResults(indexedMatches)
			if err == nil && seeker != nil {
				_, err = seeker.Seek(inputOffset, io.SeekStart)
			}
			if err == nil && seeker != nil {
				reader = gogamafgb.NewFileReader(seeker)
				header, err = reader.Header()
			}
			if err != nil {
				return errors.Join(fmt.Errorf("failed to search the FlatGeobuf packed index: %w", err), writer.Close())
			}
			sort.Slice(indexedMatches, func(first, second int) bool {
				return indexedMatches[first].RefIndex < indexedMatches[second].RefIndex
			})
			if len(indexedMatches) == 0 {
				return writer.Close()
			}
		}
	}
	var decodeErr error
	featureNumber := 0
	matchNumber := 0
	matchedAll := false
	buffer := make([]flat.Feature, 64)
	for {
		count, err := reader.Data(buffer)
		for index := 0; index < count; index++ {
			featureNumber++
			if indexedMatches != nil {
				if indexedMatches[matchNumber].RefIndex != featureNumber-1 {
					continue
				}
				matchNumber++
			}
			if writeErr := writeDecodedFlatFeature(&buffer[index], header, sourceCRS, writer, featureNumber, bbox); writeErr != nil {
				decodeErr = writeErr
				break
			}
			if indexedMatches != nil && matchNumber == len(indexedMatches) {
				matchedAll = true
				break
			}
		}
		if decodeErr != nil || matchedAll || err == io.EOF {
			break
		}
		if err != nil {
			decodeErr = fmt.Errorf("failed to read FlatGeobuf Feature %d: %w", featureNumber+1, err)
			break
		}
	}
	if decodeErr == nil && indexedMatches != nil && !matchedAll {
		decodeErr = fmt.Errorf("FlatGeobuf data ended after %d Features before all %d packed-index matches were found", featureNumber, len(indexedMatches))
	}
	return errors.Join(decodeErr, writer.Close())
}

func flatGeobufReader(input io.Reader) (io.Reader, io.ReadSeeker, int64) {
	seeker, ok := input.(io.ReadSeeker)
	if !ok {
		return input, nil, 0
	}
	offset, err := seeker.Seek(0, io.SeekCurrent)
	if err != nil {
		return struct{ io.Reader }{input}, nil, 0
	}
	return input, seeker, offset
}

func flatIndexSearchBoxes(bbox [4]float64, sourceCRS string) ([]packedrtree.Box, bool) {
	boxes := [][4]float64{bbox}
	if bbox[0] > bbox[2] {
		boxes = [][4]float64{
			{bbox[0], bbox[1], 180, bbox[3]},
			{-180, bbox[1], bbox[2], bbox[3]},
		}
	}
	searchBoxes := make([]packedrtree.Box, 0, len(boxes))
	for _, box := range boxes {
		searchBox, usable := flatIndexSearchBox(box, sourceCRS)
		if !usable {
			return nil, false
		}
		searchBoxes = append(searchBoxes, searchBox)
	}
	return searchBoxes, true
}

func flatIndexSearchBox(bbox [4]float64, sourceCRS string) (packedrtree.Box, bool) {
	const segments = 64
	coordinates := make([]float64, 0, (segments+1)*8)
	for segment := 0; segment <= segments; segment++ {
		fraction := float64(segment) / segments
		x := bbox[0] + (bbox[2]-bbox[0])*fraction
		y := bbox[1] + (bbox[3]-bbox[1])*fraction
		coordinates = append(coordinates,
			x, bbox[1],
			bbox[2], y,
			bbox[2]-(bbox[2]-bbox[0])*fraction, bbox[3],
			bbox[0], bbox[3]-(bbox[3]-bbox[1])*fraction,
		)
	}
	geometry := geom.NewLineStringFlat(geom.XY, coordinates)
	if _, err := geodata.TransformGeometry(geometry, geodata.CRSCRS84, sourceCRS); err != nil {
		return packedrtree.Box{}, false
	}
	bounds := geometry.Bounds()
	xPadding := math.Max(1, bounds.Max(0)-bounds.Min(0)) * 1e-7
	yPadding := math.Max(1, bounds.Max(1)-bounds.Min(1)) * 1e-7
	return packedrtree.Box{
		XMin: bounds.Min(0) - xPadding,
		YMin: bounds.Min(1) - yPadding,
		XMax: bounds.Max(0) + xPadding,
		YMax: bounds.Max(1) + yPadding,
	}, true
}

func uniqueFlatResults(results packedrtree.Results) packedrtree.Results {
	sort.Slice(results, func(first, second int) bool {
		return results[first].RefIndex < results[second].RefIndex
	})
	writeIndex := 0
	for _, result := range results {
		if writeIndex == 0 || result.RefIndex != results[writeIndex-1].RefIndex {
			results[writeIndex] = result
			writeIndex++
		}
	}
	return results[:writeIndex]
}

func flatBoundsIntersect(first, second [4]float64) bool {
	if first[1] > second[3] || first[3] < second[1] {
		return false
	}
	if second[0] > second[2] {
		return first[2] >= second[0] || first[0] <= second[2]
	}
	return first[0] <= second[2] && first[2] >= second[0]
}

func writeDecodedFlatFeature(encoded *flat.Feature, header *flat.Header, sourceCRS string, writer *geodata.FeatureWriter, featureNumber int, bbox *[4]float64) error {
	feature, err := decodeFlatFeature(encoded, header, sourceCRS)
	if err != nil {
		return fmt.Errorf("FlatGeobuf Feature %d is invalid: %w", featureNumber, err)
	}
	summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{})
	if err != nil {
		return fmt.Errorf("FlatGeobuf Feature %d failed GeoJSON validation: %w", featureNumber, err)
	}
	if bbox != nil && (!summary.HasBounds || !flatBoundsIntersect(summary.Bounds, *bbox)) {
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
	if bbox[0] < -180 || bbox[0] > 180 || bbox[2] < -180 || bbox[2] > 180 {
		return nil, fmt.Errorf("FlatGeobuf bbox %v has longitude outside -180 through 180", bbox)
	}
	if bbox[1] < -90 || bbox[1] > 90 || bbox[3] < -90 || bbox[3] > 90 {
		return nil, fmt.Errorf("FlatGeobuf bbox %v has latitude outside -90 through 90", bbox)
	}
	if bbox[1] > bbox[3] {
		return nil, fmt.Errorf("FlatGeobuf bbox %v has minimum latitude greater than maximum latitude", bbox)
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
	outputCRS := flag.String("crs", geodata.CRSCRS84, "CRS for encoded FlatGeobuf coordinates; GeoJSON input is reprojected from WGS 84")
	dimension := flag.Int("dimensions", 2, "Coordinate dimension declared by streaming output when -index=false: 2 or 3; indexed output infers it from the Features")
	bboxValue := flag.String("bbox", "", "Decode only Features intersecting minLon,minLat,maxLon,maxLat; minLon greater than maxLon crosses the antimeridian, indexed input is queried directly, and unindexed input is scanned")
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
		settings := flatEncodeSettings{InputMode: inputMode, LayerName: *layerName, Indexed: *indexed, CRS: *outputCRS, Dimension: *dimension}
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
