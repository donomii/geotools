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
	Encoding      string             `json:"encoding"`
	GeometryTypes []string           `json:"geometry_types"`
	CRS           json.RawMessage    `json:"crs,omitempty"`
	BBox          []float64          `json:"bbox,omitempty"`
	Covering      geoParquetCovering `json:"covering,omitempty"`
}

type geoParquetCovering struct {
	BBox map[string][]string `json:"bbox,omitempty"`
}

func decodeGeoParquet(input io.Reader, output io.Writer, outputMode geodata.OutputMode) error {
	data, err := io.ReadAll(input)
	if err != nil {
		return fmt.Errorf("failed to read GeoParquet input: %w", err)
	}
	file, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("input is not a readable Parquet file: %w", err)
	}
	metadataText, exists := file.Lookup("geo")
	if !exists {
		return fmt.Errorf("Parquet input has no geo metadata key and is not a GeoParquet file")
	}
	metadata, err := parseGeoParquetMetadata(metadataText)
	if err != nil {
		return err
	}
	var toolMetadata *geoParquetToolMetadata
	if encoded, exists := file.Lookup(geoParquetToolMetadataKey); exists {
		var parsed geoParquetToolMetadata
		if err := json.Unmarshal([]byte(encoded), &parsed); err != nil {
			return fmt.Errorf("GeoParquet geotools metadata is invalid JSON: %w", err)
		}
		if parsed.Version != 1 {
			return fmt.Errorf("GeoParquet geotools metadata version is %d; expected 1", parsed.Version)
		}
		toolMetadata = &parsed
	}
	columns := file.Schema().Columns()
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
	for _, rowGroup := range file.RowGroups() {
		rows := rowGroup.Rows()
		buffer := make([]parquet.Row, 64)
		for {
			count, err := rows.ReadRows(buffer)
			for index := 0; index < count; index++ {
				feature, featureErr := decodeGeoParquetRow(buffer[index], file.Schema(), columns, metadata, toolMetadata)
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
	column, exists := metadata.Columns[metadata.PrimaryColumn]
	if !exists {
		return geoParquetMetadata{}, fmt.Errorf("GeoParquet metadata has no entry for primary column %q", metadata.PrimaryColumn)
	}
	if column.Encoding != "WKB" {
		switch column.Encoding {
		case "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon":
		default:
			return geoParquetMetadata{}, fmt.Errorf("GeoParquet geometry encoding is %q; expected WKB or a GeoArrow native geometry encoding", column.Encoding)
		}
	}
	if column.GeometryTypes == nil {
		return geoParquetMetadata{}, fmt.Errorf("GeoParquet geometry metadata is missing geometry_types")
	}
	if column.CRS != nil {
		if bytes.Equal(bytes.TrimSpace(column.CRS), []byte("null")) {
			return geoParquetMetadata{}, fmt.Errorf("GeoParquet primary geometry column %q has an undefined CRS and cannot be converted to RFC 7946 GeoJSON", metadata.PrimaryColumn)
		}
		if _, err := geodata.ParseCRS(column.CRS); err != nil {
			return geoParquetMetadata{}, fmt.Errorf("GeoParquet primary geometry column %q CRS is unsupported: %w", metadata.PrimaryColumn, err)
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
	targetCRS := geodata.CRSCRS84
	if geometry.Stride() == 3 {
		targetCRS = geodata.CRSCRS84h
	}
	if _, err := geodata.TransformGeometry(geometry, sourceCRS, targetCRS); err != nil {
		return geodata.Feature{}, fmt.Errorf("primary geometry cannot be reprojected from %s to %s: %w", sourceCRS, targetCRS, err)
	}
	geometryJSON, err := geomjson.Marshal(geometry)
	if err != nil {
		return geodata.Feature{}, fmt.Errorf("failed to encode primary geometry as GeoJSON: %w", err)
	}
	if toolMetadata != nil {
		return decodeGeotoolsGeoParquetFeature(values, paths, schema, geometryJSON, *toolMetadata)
	}
	properties := make(map[string]json.RawMessage)
	for columnIndex, path := range paths {
		if path[0] == metadata.PrimaryColumn || geoParquetCoveringPath(columnMetadata, path) {
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
	if _, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{}); err != nil {
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
	if err := decodeGeoParquet(&parquetData, &output, geodata.OutputCollection); err != nil {
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
	outputCRS := flag.String("crs", geodata.CRSCRS84, "CRS for encoded geometry: OGC:CRS84, OGC:CRS84h, EPSG:4326, or EPSG:3857; GeoJSON input is reprojected from WGS 84")
	geometryEncoding := flag.String("geometry-encoding", "wkb", "GeoParquet geometry representation: wkb supports mixed geometry types; native writes GeoArrow columns and requires one geometry type")
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
		settings := geoParquetEncodeSettings{InputMode: inputMode, CRS: *outputCRS, GeometryEncoding: *geometryEncoding}
		if err := encodeGeoParquetWithSettings(os.Stdin, os.Stdout, settings); err != nil {
			log.Fatal(err)
		}
	case "decode":
		outputMode, err := geodata.ParseOutputMode(*outputName)
		if err != nil {
			log.Fatal(err)
		}
		if err := decodeGeoParquet(os.Stdin, os.Stdout, outputMode); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("invalid mode %q; expected encode or decode", *mode)
	}
}
