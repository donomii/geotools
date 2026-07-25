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
	"reflect"
	"strconv"
	"strings"

	"github.com/donomii/geotools/geodata"
	"github.com/parquet-go/parquet-go"
	geomjson "github.com/twpayne/go-geom/encoding/geojson"
)

const geoParquetVersion = "1.1.0"

type geoParquetMetadata struct {
	Version       string                      `json:"version"`
	PrimaryColumn string                      `json:"primary_column"`
	Columns       map[string]geoParquetColumn `json:"columns"`
}

type geoParquetColumn struct {
	Encoding      string              `json:"encoding"`
	GeometryTypes []string            `json:"geometry_types"`
	CRS           json.RawMessage     `json:"crs,omitempty"`
	BBox          []float64           `json:"bbox,omitempty"`
	Covering      *geoParquetCovering `json:"covering,omitempty"`
}

type geoParquetCovering struct {
	BBox map[string][]string `json:"bbox,omitempty"`
}

type geoParquetSeekableInput interface {
	io.Reader
	io.ReaderAt
	io.Seeker
}

func decodeGeoParquet(input io.Reader, output io.Writer, outputMode geodata.OutputMode) error {
	file, size, err := openGeoParquetInput(input)
	if err != nil {
		return err
	}
	parquetFile, err := parquet.OpenFile(file, size)
	if err != nil {
		return fmt.Errorf("input is not a readable Parquet file: %w", err)
	}
	metadataText, exists := parquetFile.Lookup("geo")
	if !exists {
		return fmt.Errorf("Parquet input has no geo metadata key and is not a GeoParquet file")
	}
	metadata, err := parseGeoParquetMetadata(metadataText)
	if err != nil {
		return err
	}
	var toolMetadata *geoParquetToolMetadata
	if encoded, exists := parquetFile.Lookup(geoParquetToolMetadataKey); exists {
		var parsed geoParquetToolMetadata
		if err := json.Unmarshal([]byte(encoded), &parsed); err != nil {
			return fmt.Errorf("GeoParquet geotools metadata is invalid JSON: %w", err)
		}
		if parsed.Version != 1 {
			return fmt.Errorf("GeoParquet geotools metadata version is %d; expected 1", parsed.Version)
		}
		toolMetadata = &parsed
	}
	columns := parquetFile.Schema().Columns()
	hasGeometryColumn := false
	for _, path := range columns {
		if len(path) > 0 && path[0] == metadata.PrimaryColumn {
			hasGeometryColumn = true
			break
		}
	}
	if !hasGeometryColumn {
		return fmt.Errorf("GeoParquet primary geometry column %q is absent from the Parquet schema", metadata.PrimaryColumn)
	}
	writer := geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	var readErr error
	for _, rowGroup := range parquetFile.RowGroups() {
		rows := rowGroup.Rows()
		buffer := make([]parquet.Row, 64)
		for {
			count, err := rows.ReadRows(buffer)
			for index := 0; index < count; index++ {
				feature, featureErr := decodeGeoParquetRow(buffer[index], parquetFile.Schema(), columns, metadata, toolMetadata)
				if featureErr != nil {
					readErr = fmt.Errorf("GeoParquet row %d is invalid: %w", writer.FeatureCount()+1, featureErr)
					break
				}
				if writeErr := writer.Write(feature); writeErr != nil {
					readErr = writeErr
					break
				}
			}
			if readErr != nil || err == io.EOF {
				break
			}
			if err != nil {
				readErr = fmt.Errorf("failed to read GeoParquet row %d: %w", writer.FeatureCount()+1, err)
				break
			}
		}
		closeErr := rows.Close()
		readErr = errors.Join(readErr, closeErr)
		if readErr != nil {
			break
		}
	}
	return errors.Join(readErr, writer.Close())
}

func openGeoParquetInput(input io.Reader) (io.ReaderAt, int64, error) {
	seekable, ok := input.(geoParquetSeekableInput)
	if !ok {
		return nil, 0, fmt.Errorf("GeoParquet decoding requires seekable input because Parquet stores its schema in the footer; use -file with a Parquet filename")
	}
	size, err := seekable.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, 0, fmt.Errorf("GeoParquet input cannot seek to its footer; use -file with a regular Parquet file: %w", err)
	}
	if _, err := seekable.Seek(0, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("GeoParquet input cannot rewind after reading its footer: %w", err)
	}
	return seekable, size, nil
}

func validateGeoParquetMetadata(value string) error {
	_, err := parseGeoParquetMetadata(value)
	return err
}

func parseGeoParquetMetadata(value string) (geoParquetMetadata, error) {
	var metadata geoParquetMetadata
	if err := json.Unmarshal([]byte(value), &metadata); err != nil {
		return geoParquetMetadata{}, fmt.Errorf("GeoParquet geo metadata is invalid JSON: %w", err)
	}
	if metadata.Version != "1.0.0" && metadata.Version != geoParquetVersion {
		return geoParquetMetadata{}, fmt.Errorf("GeoParquet metadata version is %q; expected 1.0.0 or %q", metadata.Version, geoParquetVersion)
	}
	if metadata.PrimaryColumn == "" {
		return geoParquetMetadata{}, fmt.Errorf("GeoParquet metadata primary_column is empty")
	}
	if _, exists := metadata.Columns[metadata.PrimaryColumn]; !exists {
		return geoParquetMetadata{}, fmt.Errorf("GeoParquet metadata has no entry for primary column %q", metadata.PrimaryColumn)
	}
	for name, column := range metadata.Columns {
		if column.Encoding != "WKB" {
			switch column.Encoding {
			case "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon":
			default:
				return geoParquetMetadata{}, fmt.Errorf("GeoParquet geometry column %q encoding is %q; expected WKB or a GeoArrow native geometry encoding", name, column.Encoding)
			}
		}
		if column.GeometryTypes == nil {
			return geoParquetMetadata{}, fmt.Errorf("GeoParquet geometry column %q metadata is missing geometry_types", name)
		}
		if column.CRS != nil {
			if bytes.Equal(bytes.TrimSpace(column.CRS), []byte("null")) {
				return geoParquetMetadata{}, fmt.Errorf("GeoParquet geometry column %q has an undefined CRS and cannot be converted to RFC 7946 GeoJSON", name)
			}
			if _, err := geodata.ParseCRS(column.CRS); err != nil {
				return geoParquetMetadata{}, fmt.Errorf("GeoParquet geometry column %q CRS is unsupported: %w", name, err)
			}
		}
	}
	return metadata, nil
}

func decodeGeoParquetRow(row parquet.Row, schema *parquet.Schema, paths [][]string, metadata geoParquetMetadata, toolMetadata *geoParquetToolMetadata) (geodata.Feature, error) {
	values := make([][]parquet.Value, len(paths))
	row.Range(func(columnIndex int, columnValues []parquet.Value) bool {
		values[columnIndex] = columnValues
		return true
	})
	columnMetadata := metadata.Columns[metadata.PrimaryColumn]
	sourceCRS := geodata.CRSCRS84
	var err error
	if columnMetadata.CRS != nil {
		sourceCRS, err = geodata.ParseCRS(columnMetadata.CRS)
		if err != nil {
			return geodata.Feature{}, err
		}
	}
	geometry, err := decodeGeoParquetGeometry(values, paths, metadata.PrimaryColumn, columnMetadata.Encoding)
	if err != nil {
		return geodata.Feature{}, err
	}
	geometryJSON := json.RawMessage("null")
	if geometry != nil {
		targetCRS := geodata.CRSCRS84
		if geometry.Stride() == 3 {
			targetCRS = geodata.CRSCRS84h
		}
		if _, err := geodata.TransformGeometryWithCRSAxisOrder(geometry, sourceCRS, targetCRS); err != nil {
			return geodata.Feature{}, fmt.Errorf("primary geometry cannot be reprojected from %s to %s: %w", sourceCRS, targetCRS, err)
		}
		geometryJSON, err = geomjson.Marshal(geometry)
		if err != nil {
			return geodata.Feature{}, fmt.Errorf("failed to encode primary geometry as GeoJSON: %w", err)
		}
	}
	secondaryGeometries := make(map[string]json.RawMessage)
	for name, secondaryMetadata := range metadata.Columns {
		if name == metadata.PrimaryColumn {
			continue
		}
		secondaryGeometry, err := decodeGeoParquetGeometry(values, paths, name, secondaryMetadata.Encoding)
		if err != nil {
			return geodata.Feature{}, fmt.Errorf("secondary geometry column %q is invalid: %w", name, err)
		}
		secondaryJSON := json.RawMessage("null")
		if secondaryGeometry != nil {
			secondaryCRS := geodata.CRSCRS84
			if secondaryMetadata.CRS != nil {
				secondaryCRS, err = geodata.ParseCRS(secondaryMetadata.CRS)
				if err != nil {
					return geodata.Feature{}, err
				}
			}
			targetCRS := geodata.CRSCRS84
			if secondaryGeometry.Stride() == 3 {
				targetCRS = geodata.CRSCRS84h
			}
			if _, err := geodata.TransformGeometryWithCRSAxisOrder(secondaryGeometry, secondaryCRS, targetCRS); err != nil {
				return geodata.Feature{}, fmt.Errorf("secondary geometry column %q cannot be reprojected from %s to %s: %w", name, secondaryCRS, targetCRS, err)
			}
			secondaryJSON, err = geomjson.Marshal(secondaryGeometry)
			if err != nil {
				return geodata.Feature{}, fmt.Errorf("failed to encode secondary geometry column %q as GeoJSON: %w", name, err)
			}
		}
		secondaryGeometries[name] = secondaryJSON
	}
	if toolMetadata != nil {
		return decodeGeotoolsGeoParquetFeature(values, paths, schema, geometryJSON, secondaryGeometries, *toolMetadata)
	}
	properties := make(map[string]json.RawMessage)
	for name, geometry := range secondaryGeometries {
		properties[name] = geometry
	}
	for columnIndex, path := range paths {
		if geoParquetGeometryMetadataPath(metadata, path) {
			continue
		}
		leaf, exists := schema.Lookup(path...)
		if !exists {
			return geodata.Feature{}, fmt.Errorf("Parquet schema column %q cannot be resolved", path[0])
		}
		raw, err := parquetValuesJSON(values[columnIndex], leaf.Node)
		if err != nil {
			return geodata.Feature{}, fmt.Errorf("property column %q cannot be converted to JSON: %w", path[0], err)
		}
		if err := insertNestedGeoParquetProperty(properties, path, raw); err != nil {
			return geodata.Feature{}, fmt.Errorf("property column %q cannot be reconstructed: %w", strings.Join(path, "."), err)
		}
	}
	propertiesJSON, err := json.Marshal(properties)
	if err != nil {
		return geodata.Feature{}, err
	}
	feature := geodata.Feature{Type: "Feature", Geometry: geometryJSON, Properties: propertiesJSON}
	if _, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{AllowNullGeometry: true}); err != nil {
		return geodata.Feature{}, err
	}
	return feature, nil
}

func singleParquetBytes(values []parquet.Value, columnName string) ([]byte, error) {
	nonNull := make([]parquet.Value, 0, len(values))
	for _, value := range values {
		if !value.IsNull() {
			nonNull = append(nonNull, value)
		}
	}
	if len(nonNull) == 0 {
		return nil, nil
	}
	if len(nonNull) != 1 {
		return nil, fmt.Errorf("column %q contains %d values; expected exactly one non-null byte array", columnName, len(nonNull))
	}
	if nonNull[0].Kind() != parquet.ByteArray && nonNull[0].Kind() != parquet.FixedLenByteArray {
		return nil, fmt.Errorf("column %q has physical type %s; expected BYTE_ARRAY", columnName, nonNull[0].Kind())
	}
	return append([]byte(nil), nonNull[0].ByteArray()...), nil
}

func parquetValuesJSON(values []parquet.Value, node parquet.Node) (json.RawMessage, error) {
	nonNull := make([]parquet.Value, 0, len(values))
	for _, value := range values {
		if !value.IsNull() {
			nonNull = append(nonNull, value)
		}
	}
	if len(nonNull) == 0 {
		return json.RawMessage("null"), nil
	}
	encoded := make([]json.RawMessage, 0, len(nonNull))
	for _, value := range nonNull {
		raw, err := parquetValueJSON(value, node)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, raw)
	}
	if len(encoded) == 1 && !node.Repeated() {
		return encoded[0], nil
	}
	return json.Marshal(encoded)
}

func parquetValueJSON(value parquet.Value, node parquet.Node) (json.RawMessage, error) {
	switch value.Kind() {
	case parquet.Boolean:
		return json.RawMessage(strconv.FormatBool(value.Boolean())), nil
	case parquet.Int32:
		if parquetNodeKind(node) >= reflect.Uint && parquetNodeKind(node) <= reflect.Uint64 {
			return json.RawMessage(strconv.FormatUint(uint64(value.Uint32()), 10)), nil
		}
		return json.RawMessage(strconv.FormatInt(int64(value.Int32()), 10)), nil
	case parquet.Int64:
		if parquetNodeKind(node) >= reflect.Uint && parquetNodeKind(node) <= reflect.Uint64 {
			return json.RawMessage(strconv.FormatUint(value.Uint64(), 10)), nil
		}
		return json.RawMessage(strconv.FormatInt(value.Int64(), 10)), nil
	case parquet.Float:
		number := value.Float()
		if math.IsNaN(float64(number)) || math.IsInf(float64(number), 0) {
			return nil, fmt.Errorf("floating-point value %v is not finite", number)
		}
		return json.RawMessage(strconv.FormatFloat(float64(number), 'g', -1, 32)), nil
	case parquet.Double:
		number := value.Double()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("floating-point value %v is not finite", number)
		}
		return json.RawMessage(strconv.FormatFloat(number, 'g', -1, 64)), nil
	case parquet.ByteArray, parquet.FixedLenByteArray:
		data := value.ByteArray()
		typeName := strings.ToUpper(node.Type().String())
		if strings.Contains(typeName, "JSON") {
			if !json.Valid(data) {
				return nil, fmt.Errorf("JSON logical value %q is invalid", data)
			}
			return append(json.RawMessage(nil), data...), nil
		}
		if strings.Contains(typeName, "STRING") || parquetNodeKind(node) == reflect.String {
			encoded, err := json.Marshal(string(data))
			return encoded, err
		}
		encoded, err := json.Marshal(data)
		return encoded, err
	default:
		return nil, fmt.Errorf("physical type %s is unsupported", value.Kind())
	}
}

func parquetNodeKind(node parquet.Node) reflect.Kind {
	goType := node.GoType()
	for goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}
	if node.Repeated() && goType.Kind() == reflect.Slice {
		goType = goType.Elem()
	}
	return goType.Kind()
}

func runBuiltInTest() error {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[{"type":"Feature","id":"place","geometry":{"type":"Point","coordinates":[103.851959,1.29027]},"properties":{"name":"Singapore"}}]}`)
	var parquetData bytes.Buffer
	if err := encodeGeoParquet(input, &parquetData, geodata.InputAuto); err != nil {
		return err
	}
	var output bytes.Buffer
	if err := decodeGeoParquet(bytes.NewReader(parquetData.Bytes()), &output, geodata.OutputCollection); err != nil {
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
		return fmt.Errorf("GeoParquet round trip returned %d Features; expected 1", count)
	}
	return nil
}

func main() {
	mode := flag.String("mode", "encode", "Operation: encode converts GeoJSON to GeoParquet; decode converts GeoParquet to GeoJSON")
	inputName := flag.String("input", "auto", "GeoJSON input format for encode: auto detects JSONL, arrays, FeatureCollections, and RFC 8142 sequences; seq requires record separators")
	outputName := flag.String("output", "jsonl", "GeoJSON output format for decode: jsonl writes one Feature per line, collection writes a FeatureCollection, and seq writes RFC 8142 records")
	outputCRS := flag.String("crs", geodata.CRSCRS84, "Supported OGC or EPSG CRS for encoded geometry; GeoJSON input is reprojected from WGS 84")
	geometryEncoding := flag.String("geometry-encoding", "wkb", "GeoParquet geometry representation: wkb supports mixed geometry types; native writes GeoArrow columns and requires one geometry type")
	geometryProperties := flag.String("geometry-properties", "", "Comma-separated GeoJSON properties containing geometry objects or null; encode stores each as a secondary WKB geometry column and decode restores it")
	streaming := flag.Bool("stream", false, "Encode in one pass with bounded memory and no temporary file by storing ordinary properties in one JSON column; false writes separately typed property columns after inspecting all input")
	inputFile := flag.String("file", "", "Parquet file to decode; Parquet footer lookup requires seekable input, so piped input cannot be decoded")
	runTest := flag.Bool("test", false, "Run an in-memory GeoJSON-to-GeoParquet-to-GeoJSON round-trip check and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("geoparquet reads standard input and writes standard output; positional arguments are not accepted")
	}
	if *runTest {
		if err := runBuiltInTest(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("geoparquet built-in test passed")
		return
	}
	switch *mode {
	case "encode":
		inputMode, err := geodata.ParseInputMode(*inputName)
		if err != nil {
			log.Fatal(err)
		}
		var secondaryGeometryNames []string
		if strings.TrimSpace(*geometryProperties) != "" {
			secondaryGeometryNames = strings.Split(*geometryProperties, ",")
		}
		settings := geoParquetEncodeSettings{
			InputMode: inputMode, CRS: *outputCRS, GeometryEncoding: *geometryEncoding,
			GeometryProperties: secondaryGeometryNames, Streaming: *streaming,
		}
		if err := encodeGeoParquetWithSettings(os.Stdin, os.Stdout, settings); err != nil {
			log.Fatal(err)
		}
	case "decode":
		outputMode, err := geodata.ParseOutputMode(*outputName)
		if err != nil {
			log.Fatal(err)
		}
		input := io.Reader(os.Stdin)
		var file *os.File
		if *inputFile != "" {
			file, err = os.Open(*inputFile)
			if err != nil {
				log.Fatalf("cannot open GeoParquet input %q: %v", *inputFile, err)
			}
			defer file.Close()
			input = file
		}
		if err := decodeGeoParquet(input, os.Stdout, outputMode); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("invalid mode %q; expected encode or decode", *mode)
	}
}
