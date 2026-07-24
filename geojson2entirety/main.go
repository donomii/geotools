package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
)

func writeBytes(n float64, writer *bufio.Writer) error {
	if err := binary.Write(writer, binary.LittleEndian, n); err != nil {
		return fmt.Errorf("failed to write float64 %v: %w", n, err)
	}
	return nil
}

func writeBytesInt(n int64, writer *bufio.Writer) error {
	if err := binary.Write(writer, binary.LittleEndian, n); err != nil {
		return fmt.Errorf("failed to write int64 %d: %w", n, err)
	}
	return nil
}

func string2Bytes(s string) ([]byte, int) {
	b := []byte(s)
	b = append(b, []byte{0}...)
	l := len(b)
	return b, l
}

func writeTag(str string, long, lat float64, tagpointsFile, offsetFile, indexFile, tagcatFile, stringsFile, preoffsetFile *bufio.Writer, indexCount, offset int64) (int64, error) {
	//treeIndexAdd2(str, long, lat)
	//fmt.Println("Parsed: ", string2Bytes(result.Properties["name"].(string)))
	//fmt.Printf("%s ", string2Bytes(result.Properties["name"].(string)))

	//str = strings.Replace(str, "\"", "\\\"", -1)
	if verbose {
		// log.Println("Adding tag ", indexCount, ": ", str, " at ", lat, ",", long, " at offset ", offset)
	}
	outBytes, blength := string2Bytes(str)
	wrote, err := stringsFile.Write(outBytes)
	if err != nil {
		return 0, fmt.Errorf("failed to write tag text %q: %w", str, err)
	}
	if wrote != blength {
		return 0, fmt.Errorf("failed to write tag text %q: wrote %d bytes, expected %d", str, wrote, blength)
	}
	if err := writeBytesInt(offset, offsetFile); err != nil {
		return 0, err
	}
	if _, err := preoffsetFile.WriteString(fmt.Sprintf("%v\n", offset)); err != nil {
		return 0, fmt.Errorf("failed to write pre-offset %d: %w", offset, err)
	}
	if err := writeBytes(lat, tagpointsFile); err != nil {
		return 0, err
	}
	if err := writeBytes(long, tagpointsFile); err != nil {
		return 0, err
	}
	if err := writeBytesInt(indexCount-1, indexFile); err != nil {
		return 0, err
	}
	if err := writeBytesInt(0, tagcatFile); err != nil {
		return 0, err
	}
	return int64(blength), nil
}

var verbose bool

type entiretyFile struct {
	name   string
	file   *os.File
	writer *bufio.Writer
}

type entiretyOutputs struct {
	tagPoints *entiretyFile
	points    *entiretyFile
	pointData *entiretyFile
	tagCat    *entiretyFile
	preOffset *entiretyFile
	offset    *entiretyFile
	strings   *entiretyFile
	index     *entiretyFile
	all       []*entiretyFile
}

func openEntiretyFile(name string) (*entiretyFile, error) {
	file, err := os.Create(name)
	if err != nil {
		return nil, fmt.Errorf("failed to create Entirety output %q: %w", name, err)
	}
	return &entiretyFile{name: name, file: file, writer: bufio.NewWriterSize(file, 10*1024*1024)}, nil
}

func openEntiretyOutputs(mapName string) (*entiretyOutputs, error) {
	outputs := &entiretyOutputs{}
	targets := []struct {
		suffix string
		assign func(*entiretyFile)
	}{
		{".tag_points", func(file *entiretyFile) { outputs.tagPoints = file }},
		{".map_points", func(file *entiretyFile) { outputs.points = file }},
		{".map_data", func(file *entiretyFile) { outputs.pointData = file }},
		{".tag_category", func(file *entiretyFile) { outputs.tagCat = file }},
		{".pre_offset", func(file *entiretyFile) { outputs.preOffset = file }},
		{".tag_offset", func(file *entiretyFile) { outputs.offset = file }},
		{".tag_text", func(file *entiretyFile) { outputs.strings = file }},
		{".tag_index", func(file *entiretyFile) { outputs.index = file }},
	}
	for _, target := range targets {
		file, err := openEntiretyFile(mapName + target.suffix)
		if err != nil {
			closeEntiretyOutputs(outputs)
			return nil, err
		}
		target.assign(file)
		outputs.all = append(outputs.all, file)
	}
	return outputs, nil
}

func closeEntiretyOutputs(outputs *entiretyOutputs) error {
	if outputs == nil {
		return nil
	}
	var closeErrors []error
	for _, output := range outputs.all {
		if err := output.writer.Flush(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("failed to flush %q: %w", output.name, err))
		}
		if err := output.file.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("failed to close %q: %w", output.name, err))
		}
	}
	return errors.Join(closeErrors...)
}

type entiretyGeometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

type entiretyFeature struct {
	Type       string                     `json:"type"`
	Geometry   entiretyGeometry           `json:"geometry"`
	Properties map[string]json.RawMessage `json:"properties"`
}

var errEntiretyLimitReached = errors.New("Entirety conversion limit reached")

func forEachEntiretyFeature(input io.Reader, visit func(entiretyFeature) error) error {
	decoder := json.NewDecoder(input)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("failed to decode GeoJSON input: %w", err)
		}
		if err := visitEntiretyToken(decoder, token, visit); err != nil {
			return err
		}
	}
}

func visitEntiretyToken(decoder *json.Decoder, token json.Token, visit func(entiretyFeature) error) error {
	delimiter, ok := token.(json.Delim)
	if !ok {
		return fmt.Errorf("unsupported top-level JSON value %v; expected a GeoJSON object or array", token)
	}
	switch delimiter {
	case '[':
		for decoder.More() {
			elementToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("failed to decode GeoJSON array element: %w", err)
			}
			if err := visitEntiretyToken(decoder, elementToken, visit); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("failed to close GeoJSON array: %w", err)
		}
		if end != json.Delim(']') {
			return fmt.Errorf("GeoJSON array ended with %v instead of ]", end)
		}
		return nil
	case '{':
		var feature entiretyFeature
		featuresSeen := false
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("failed to decode GeoJSON object key: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("GeoJSON object key has type %T; expected string", keyToken)
			}
			switch key {
			case "type":
				if err := decoder.Decode(&feature.Type); err != nil {
					return fmt.Errorf("failed to decode GeoJSON type: %w", err)
				}
			case "geometry":
				if err := decoder.Decode(&feature.Geometry); err != nil {
					return fmt.Errorf("failed to decode GeoJSON geometry: %w", err)
				}
			case "properties":
				if err := decoder.Decode(&feature.Properties); err != nil {
					return fmt.Errorf("failed to decode GeoJSON properties: %w", err)
				}
			case "features":
				featuresSeen = true
				arrayStart, err := decoder.Token()
				if err != nil {
					return fmt.Errorf("failed to decode FeatureCollection features: %w", err)
				}
				if arrayStart != json.Delim('[') {
					return fmt.Errorf("FeatureCollection features is %v; expected an array", arrayStart)
				}
				for decoder.More() {
					featureStart, err := decoder.Token()
					if err != nil {
						return fmt.Errorf("failed to decode FeatureCollection Feature: %w", err)
					}
					if err := visitEntiretyToken(decoder, featureStart, visit); err != nil {
						return err
					}
				}
				arrayEnd, err := decoder.Token()
				if err != nil {
					return fmt.Errorf("failed to close FeatureCollection features: %w", err)
				}
				if arrayEnd != json.Delim(']') {
					return fmt.Errorf("FeatureCollection features ended with %v instead of ]", arrayEnd)
				}
			default:
				var ignored json.RawMessage
				if err := decoder.Decode(&ignored); err != nil {
					return fmt.Errorf("failed to decode GeoJSON field %q: %w", key, err)
				}
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("failed to close GeoJSON object: %w", err)
		}
		if end != json.Delim('}') {
			return fmt.Errorf("GeoJSON object ended with %v instead of }", end)
		}
		if feature.Type == "Feature" {
			return visit(feature)
		} else if feature.Type == "FeatureCollection" && featuresSeen {
			return nil
		}
		return fmt.Errorf("unsupported GeoJSON object type %q; expected Feature or FeatureCollection", feature.Type)
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func entiretyPoint(feature entiretyFeature) (float64, float64, string, bool, error) {
	if feature.Type != "Feature" {
		return 0, 0, "", false, fmt.Errorf("expected GeoJSON Feature, received %q", feature.Type)
	}
	if feature.Geometry.Type != "Point" {
		return 0, 0, "", false, fmt.Errorf("unsupported GeoJSON geometry %q; Entirety accepts Point geometries", feature.Geometry.Type)
	}
	var coordinates []float64
	if err := json.Unmarshal(feature.Geometry.Coordinates, &coordinates); err != nil {
		return 0, 0, "", false, fmt.Errorf("Point has invalid coordinates: %w", err)
	}
	if len(coordinates) < 2 {
		return 0, 0, "", false, fmt.Errorf("Point has %d coordinates; expected at least longitude and latitude", len(coordinates))
	}
	if math.IsNaN(coordinates[0]) || math.IsInf(coordinates[0], 0) || math.IsNaN(coordinates[1]) || math.IsInf(coordinates[1], 0) {
		return 0, 0, "", false, fmt.Errorf("Point coordinates must be finite, received [%v, %v]", coordinates[0], coordinates[1])
	}
	nameRaw, hasName := feature.Properties["name"]
	if !hasName || bytes.Equal(bytes.TrimSpace(nameRaw), []byte("null")) {
		return coordinates[0], coordinates[1], "", false, nil
	}
	var name string
	if err := json.Unmarshal(nameRaw, &name); err != nil {
		return 0, 0, "", false, fmt.Errorf("Feature name must be a string: %w", err)
	}
	return coordinates[0], coordinates[1], name, name != "", nil
}

func writeEntiretyPoint(longitude, latitude float64, outputs *entiretyOutputs) error {
	if err := writeBytes(latitude*60, outputs.points.writer); err != nil {
		return err
	}
	if err := writeBytes(longitude*-60, outputs.points.writer); err != nil {
		return err
	}
	for value := 0; value < 3; value++ {
		if err := writeBytes(0, outputs.pointData.writer); err != nil {
			return err
		}
	}
	return nil
}

func convertEntirety(input io.Reader, outputs *entiretyOutputs, limit int64, pointsOnly, tagsOnly bool) (int64, error) {
	offset := int64(0)
	indexCount := int64(0)
	if !pointsOnly {
		written, err := writeTag("FAIL", -60000, -6000, outputs.tagPoints.writer, outputs.offset.writer, outputs.index.writer, outputs.tagCat.writer, outputs.strings.writer, outputs.preOffset.writer, indexCount, offset)
		if err != nil {
			return 0, err
		}
		offset += written
	}
	count := int64(0)
	err := forEachEntiretyFeature(input, func(feature entiretyFeature) error {
		if limit >= 0 && count >= limit {
			return errEntiretyLimitReached
		}
		longitude, latitude, name, hasName, err := entiretyPoint(feature)
		if err != nil {
			return fmt.Errorf("Feature %d is invalid: %w", count+1, err)
		}
		count++
		if pointsOnly {
			if verbose {
				log.Printf("Adding point %d at %v,%v", count, latitude, longitude)
			}
			return writeEntiretyPoint(longitude, latitude, outputs)
		}
		if tagsOnly {
			if !hasName {
				return nil
			}
		} else if !hasName {
			return writeEntiretyPoint(longitude, latitude, outputs)
		}
		indexCount++
		if verbose {
			log.Printf("Adding tag %d %q at %v,%v", indexCount, name, latitude, longitude)
		}
		written, err := writeTag(name, longitude*-60, latitude*60, outputs.tagPoints.writer, outputs.offset.writer, outputs.index.writer, outputs.tagCat.writer, outputs.strings.writer, outputs.preOffset.writer, indexCount, offset)
		if err != nil {
			return err
		}
		offset += written
		return nil
	})
	if errors.Is(err, errEntiretyLimitReached) {
		err = nil
	}
	return count, err
}

func runEntiretyBuiltInTests() error {
	input := `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"name":"A"}},{"type":"Feature","geometry":{"type":"Point","coordinates":[3,4]},"properties":{}}]}`
	names := make([]string, 0)
	if err := forEachEntiretyFeature(bytes.NewBufferString(input), func(feature entiretyFeature) error {
		_, _, name, _, err := entiretyPoint(feature)
		if err == nil {
			names = append(names, name)
		}
		return err
	}); err != nil {
		return err
	}
	if len(names) != 2 || names[0] != "A" {
		return fmt.Errorf("FeatureCollection decoding did not preserve a one-character name")
	}
	var encoded bytes.Buffer
	writer := bufio.NewWriter(&encoded)
	if err := writeBytesInt(7, writer); err != nil {
		return err
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if encoded.Len() != 8 {
		return fmt.Errorf("int64 output used %d bytes, expected 8", encoded.Len())
	}
	originalTree := tree
	tree = nil
	defer func() {
		tree = originalTree
	}()
	treeIndexAdd("first", 1.1, 2.2)
	treeIndexAdd("second", 1.1, 2.2)
	treeIndexAdd("third", 1.2, 2.3)
	packed := buildFinal()
	treeEntries := 0
	IterateMp(packed, func(float64, float64, leaf) {
		treeEntries++
	})
	if treeEntries != 3 {
		return fmt.Errorf("spatial index retained %d entries, expected 3", treeEntries)
	}
	var tagPointsBuffer, pointsBuffer, pointDataBuffer, tagCategoryBuffer, preOffsetBuffer, offsetBuffer, stringsBuffer, indexBuffer bytes.Buffer
	memoryOutputs := &entiretyOutputs{
		tagPoints: &entiretyFile{writer: bufio.NewWriter(&tagPointsBuffer)},
		points:    &entiretyFile{writer: bufio.NewWriter(&pointsBuffer)},
		pointData: &entiretyFile{writer: bufio.NewWriter(&pointDataBuffer)},
		tagCat:    &entiretyFile{writer: bufio.NewWriter(&tagCategoryBuffer)},
		preOffset: &entiretyFile{writer: bufio.NewWriter(&preOffsetBuffer)},
		offset:    &entiretyFile{writer: bufio.NewWriter(&offsetBuffer)},
		strings:   &entiretyFile{writer: bufio.NewWriter(&stringsBuffer)},
		index:     &entiretyFile{writer: bufio.NewWriter(&indexBuffer)},
	}
	converted, err := convertEntirety(bytes.NewBufferString(input), memoryOutputs, 1, true, false)
	if err != nil {
		return err
	}
	if err := memoryOutputs.points.writer.Flush(); err != nil {
		return err
	}
	if err := memoryOutputs.pointData.writer.Flush(); err != nil {
		return err
	}
	if converted != 1 || pointsBuffer.Len() != 16 || pointDataBuffer.Len() != 24 || tagPointsBuffer.Len() != 0 {
		return fmt.Errorf("points-only limit wrote count=%d point-bytes=%d data-bytes=%d tag-bytes=%d", converted, pointsBuffer.Len(), pointDataBuffer.Len(), tagPointsBuffer.Len())
	}
	return nil
}

func main() {
	mapName := flag.String("outFile", "default_map", "Output filename prefix for the eight Entirety map files")
	limit := flag.Int64("limit", -1, "Stop after this many Features; -1 processes the complete input")
	pointsOnly := flag.Bool("points", false, "Write every Point to map point files and omit tag records")
	tagsOnly := flag.Bool("tags", false, "Write named Points to tag files and omit unnamed map points")
	runBuiltInTests := flag.Bool("test", false, "Run built-in tests and exit")
	flag.BoolVar(&verbose, "verbose", false, "Print each converted Point or tag")
	flag.Parse()

	if *runBuiltInTests {
		if err := runEntiretyBuiltInTests(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("geojson2entirety built-in tests passed")
		return
	}
	if *pointsOnly && *tagsOnly {
		log.Fatal("invalid output mode: --points and --tags cannot be used together")
	}
	if *limit < -1 {
		log.Fatal("invalid limit: expected -1 or a non-negative integer")
	}
	if flag.NArg() != 0 {
		log.Fatal("geojson2entirety reads GeoJSON from standard input and does not accept positional arguments")
	}

	outputs, err := openEntiretyOutputs(*mapName)
	if err != nil {
		log.Fatal(err)
	}
	count, convertErr := convertEntirety(os.Stdin, outputs, *limit, *pointsOnly, *tagsOnly)
	closeErr := closeEntiretyOutputs(outputs)
	if convertErr != nil {
		log.Fatal(convertErr)
	}
	if closeErr != nil {
		log.Fatal(closeErr)
	}
	log.Printf("Converted %d Features into Entirety map files with prefix %q", count, *mapName)
}
