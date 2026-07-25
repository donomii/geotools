package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/donomii/geotools/geodata"
	"github.com/parquet-go/parquet-go"
	"github.com/twpayne/go-geom"
	geomwkb "github.com/twpayne/go-geom/encoding/wkb"
)

const (
	geoParquetGeometryColumn      = "geometry"
	geoParquetCoveringColumn      = "bbox"
	geoParquetIDColumn            = "__geotools_id"
	geoParquetFeatureBBoxColumn   = "__geotools_feature_bbox"
	geoParquetForeignColumn       = "__geotools_foreign"
	geoParquetNullsColumn         = "__geotools_property_nulls"
	geoParquetPropertiesNilColumn = "__geotools_properties_null"
	geoParquetPropertiesColumn    = "__geotools_properties"
	geoParquetToolMetadataKey     = "geotools"
)

type geoParquetEncodeSettings struct {
	InputMode          geodata.InputMode
	CRS                string
	GeometryEncoding   string
	GeometryProperties []string
	Streaming          bool
}

type encodedGeoParquetFeature struct {
	Feature             geodata.Feature
	Geometry            geom.T
	GeometryWKB         []byte
	SecondaryGeometries map[string]geom.T
	SecondaryWKB        map[string][]byte
	Properties          map[string]json.RawMessage
	NullProperties      []string
	Bounds              []float64
}

type geoParquetPropertyKind int

const (
	geoParquetPropertyUnknown geoParquetPropertyKind = iota
	geoParquetPropertyBool
	geoParquetPropertyInt
	geoParquetPropertyDouble
	geoParquetPropertyString
	geoParquetPropertyJSON
)

type geoParquetToolMetadata struct {
	Version              int               `json:"version"`
	PropertyColumns      map[string]string `json:"property_columns"`
	IDColumn             string            `json:"id_column"`
	FeatureBBoxColumn    string            `json:"feature_bbox_column"`
	ForeignColumn        string            `json:"foreign_column"`
	NullPropertiesColumn string            `json:"null_properties_column"`
	PropertiesNullColumn string            `json:"properties_null_column"`
	PropertiesColumn     string            `json:"properties_column,omitempty"`
}

type nativeCoordinate struct {
	Coordinate geom.Coord
	Repetition int
}

func encodeGeoParquet(input io.Reader, output io.Writer, inputMode geodata.InputMode) error {
	return encodeGeoParquetWithSettings(input, output, geoParquetEncodeSettings{
		InputMode:        inputMode,
		CRS:              geodata.CRSCRS84,
		GeometryEncoding: "wkb",
	})
}

func encodeGeoParquetWithSettings(input io.Reader, output io.Writer, settings geoParquetEncodeSettings) error {
	targetCRS, err := geodata.NormalizeCRS(settings.CRS)
	if err != nil {
		return err
	}
	geometryEncoding := stringsLower(settings.GeometryEncoding)
	if geometryEncoding != "wkb" && geometryEncoding != "native" {
		return fmt.Errorf("GeoParquet geometry encoding is %q; expected wkb or native", settings.GeometryEncoding)
	}
	geometryProperties, err := normalizeGeoParquetGeometryProperties(settings.GeometryProperties)
	if err != nil {
		return err
	}
	if settings.Streaming {
		if geometryEncoding != "wkb" {
			return fmt.Errorf("streaming GeoParquet requires WKB geometry encoding because native encoding needs one geometry type and dimension before the schema is written")
		}
		return encodeStreamingGeoParquet(input, output, settings.InputMode, targetCRS, geometryProperties)
	}
	features, propertyKinds, geometryTypes, err := collectGeoParquetFeatures(input, settings.InputMode, targetCRS, geometryProperties)
	if err != nil {
		return err
	}
	nativeType := ""
	dimension := 2
	for _, feature := range features {
		if feature.Geometry != nil {
			dimension = feature.Geometry.Stride()
			break
		}
	}
	if geometryEncoding == "native" {
		nativeType, dimension, err = nativeGeoParquetType(features)
		if err != nil {
			return err
		}
	} else {
		for index := range features {
			if features[index].Geometry != nil && features[index].Geometry.Stride() != dimension {
				dimension = 0
				break
			}
		}
	}
	propertyColumns := allocateGeoParquetPropertyColumns(propertyKinds, geometryProperties)
	schema, err := buildGeoParquetSchema(propertyKinds, propertyColumns, geometryEncoding, nativeType, dimension, geometryProperties)
	if err != nil {
		return err
	}
	metadata, err := buildGeoParquetMetadata(features, geometryTypes, targetCRS, geometryEncoding, nativeType, dimension, geometryProperties)
	if err != nil {
		return err
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("failed to encode GeoParquet metadata: %w", err)
	}
	toolMetadata := geoParquetToolMetadata{
		Version:              1,
		PropertyColumns:      propertyColumns,
		IDColumn:             geoParquetIDColumn,
		FeatureBBoxColumn:    geoParquetFeatureBBoxColumn,
		ForeignColumn:        geoParquetForeignColumn,
		NullPropertiesColumn: geoParquetNullsColumn,
		PropertiesNullColumn: geoParquetPropertiesNilColumn,
	}
	encodedToolMetadata, err := json.Marshal(toolMetadata)
	if err != nil {
		return err
	}
	writer := parquet.NewWriter(output, schema)
	writer.SetKeyValueMetadata("geo", string(encodedMetadata))
	writer.SetKeyValueMetadata(geoParquetToolMetadataKey, string(encodedToolMetadata))
	for index := range features {
		row, err := buildGeoParquetRow(schema, features[index], propertyKinds, propertyColumns, geometryEncoding, geometryProperties)
		if err != nil {
			return errors.Join(fmt.Errorf("failed to build GeoParquet row %d for Feature %s: %w", index+1, features[index].Feature.EncodedID(), err), writer.Close())
		}
		if _, err := writer.WriteRows([]parquet.Row{row}); err != nil {
			return errors.Join(fmt.Errorf("failed to write GeoParquet row %d for Feature %s: %w", index+1, features[index].Feature.EncodedID(), err), writer.Close())
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to finish GeoParquet output after %d Features: %w", len(features), err)
	}
	return nil
}

type geoParquetGeometryStats struct {
	Types          map[string]bool
	Dimension      int
	MixedDimension bool
	BBox           []float64
}

func encodeStreamingGeoParquet(input io.Reader, output io.Writer, inputMode geodata.InputMode, targetCRS string, geometryProperties []string) error {
	schema := buildStreamingGeoParquetSchema(geometryProperties)
	writer := parquet.NewWriter(output, schema, parquet.MaxRowsPerRowGroup(65536))
	toolMetadata := geoParquetToolMetadata{
		Version:              1,
		PropertyColumns:      map[string]string{},
		IDColumn:             geoParquetIDColumn,
		FeatureBBoxColumn:    geoParquetFeatureBBoxColumn,
		ForeignColumn:        geoParquetForeignColumn,
		NullPropertiesColumn: geoParquetNullsColumn,
		PropertiesNullColumn: geoParquetPropertiesNilColumn,
		PropertiesColumn:     geoParquetPropertiesColumn,
	}
	encodedToolMetadata, err := json.Marshal(toolMetadata)
	if err != nil {
		return errors.Join(err, writer.Close())
	}
	writer.SetKeyValueMetadata(geoParquetToolMetadataKey, string(encodedToolMetadata))
	primaryStats := newGeoParquetGeometryStats()
	secondaryStats := make(map[string]*geoParquetGeometryStats, len(geometryProperties))
	for _, name := range geometryProperties {
		secondaryStats[name] = newGeoParquetGeometryStats()
	}
	featureNumber := 0
	readErr := geodata.ReadFeatures(input, inputMode, func(feature geodata.Feature) error {
		featureNumber++
		encoded, err := prepareEncodedGeoParquetFeature(feature, targetCRS, geometryProperties)
		if err != nil {
			return fmt.Errorf("GeoParquet input Feature %d with id %s is invalid: %w", featureNumber, feature.EncodedID(), err)
		}
		if err := primaryStats.Add(encoded.Geometry, encoded.Bounds); err != nil {
			return fmt.Errorf("GeoParquet input Feature %d with id %s primary geometry metadata is invalid: %w", featureNumber, feature.EncodedID(), err)
		}
		for name, geometry := range encoded.SecondaryGeometries {
			if err := secondaryStats[name].Add(geometry, geoParquetGeometryBounds(geometry)); err != nil {
				return fmt.Errorf("GeoParquet input Feature %d with id %s secondary geometry %q metadata is invalid: %w", featureNumber, feature.EncodedID(), name, err)
			}
		}
		row, err := buildStreamingGeoParquetRow(schema, encoded, geometryProperties)
		if err != nil {
			return fmt.Errorf("failed to build streaming GeoParquet row %d for Feature %s: %w", featureNumber, feature.EncodedID(), err)
		}
		if _, err := writer.WriteRows([]parquet.Row{row}); err != nil {
			return fmt.Errorf("failed to write streaming GeoParquet row %d for Feature %s: %w", featureNumber, feature.EncodedID(), err)
		}
		return nil
	})
	if readErr != nil {
		return errors.Join(readErr, writer.Close())
	}
	metadata, err := buildStreamingGeoParquetMetadata(primaryStats, secondaryStats, targetCRS)
	if err != nil {
		return errors.Join(err, writer.Close())
	}
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return errors.Join(err, writer.Close())
	}
	writer.SetKeyValueMetadata("geo", string(encodedMetadata))
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to finish streaming GeoParquet output after %d Features: %w", featureNumber, err)
	}
	return nil
}

func buildStreamingGeoParquetSchema(geometryProperties []string) *parquet.Schema {
	group := parquet.Group{
		geoParquetGeometryColumn:      parquet.Optional(parquet.Leaf(parquet.ByteArrayType)),
		geoParquetIDColumn:            parquet.Optional(parquet.JSON()),
		geoParquetFeatureBBoxColumn:   parquet.Optional(parquet.JSON()),
		geoParquetForeignColumn:       parquet.Optional(parquet.JSON()),
		geoParquetNullsColumn:         parquet.Optional(parquet.JSON()),
		geoParquetPropertiesNilColumn: parquet.Optional(parquet.Leaf(parquet.BooleanType)),
		geoParquetPropertiesColumn:    parquet.Optional(parquet.JSON()),
	}
	for _, name := range geometryProperties {
		group[name] = parquet.Optional(parquet.Leaf(parquet.ByteArrayType))
	}
	return parquet.NewSchema("geotools", group)
}

func buildStreamingGeoParquetRow(schema *parquet.Schema, feature encodedGeoParquetFeature, geometryProperties []string) (parquet.Row, error) {
	rowFeature := feature
	rowFeature.Properties = nil
	rowFeature.NullProperties = nil
	row, err := buildGeoParquetRow(schema, rowFeature, map[string]geoParquetPropertyKind{}, map[string]string{}, "wkb", geometryProperties)
	if err != nil {
		return nil, err
	}
	properties := json.RawMessage("null")
	if !bytes.Equal(bytes.TrimSpace(feature.Feature.Properties), []byte("null")) {
		properties, err = json.Marshal(feature.Properties)
		if err != nil {
			return nil, err
		}
	}
	columnValues := make([][]parquet.Value, len(schema.Columns()))
	row.Range(func(columnIndex int, values []parquet.Value) bool {
		columnValues[columnIndex] = append(columnValues[columnIndex], values...)
		return true
	})
	if err := setGeoParquetColumn(schema, columnValues, geoParquetPropertiesColumn, parquet.ByteArrayValue(properties)); err != nil {
		return nil, err
	}
	row = row[:0]
	for _, values := range columnValues {
		row = append(row, values...)
	}
	return row, nil
}

func newGeoParquetGeometryStats() *geoParquetGeometryStats {
	return &geoParquetGeometryStats{Types: make(map[string]bool)}
}

func (stats *geoParquetGeometryStats) Add(geometry geom.T, bounds []float64) error {
	if geometry == nil {
		return nil
	}
	typeName, err := geoParquetGeometryType(geometry)
	if err != nil {
		return err
	}
	if geometry.Stride() == 3 {
		typeName += " Z"
	}
	stats.Types[typeName] = true
	if stats.Dimension == 0 {
		stats.Dimension = geometry.Stride()
	} else if stats.Dimension != geometry.Stride() {
		stats.MixedDimension = true
		stats.BBox = nil
	}
	if stats.MixedDimension || len(bounds) == 0 {
		return nil
	}
	if stats.BBox == nil {
		stats.BBox = append([]float64(nil), bounds...)
	} else {
		mergeNDGeoParquetBounds(stats.BBox, bounds)
	}
	return nil
}

func buildStreamingGeoParquetMetadata(primary *geoParquetGeometryStats, secondary map[string]*geoParquetGeometryStats, targetCRS string) (geoParquetMetadata, error) {
	primaryColumn, err := streamingGeoParquetColumn(primary, targetCRS)
	if err != nil {
		return geoParquetMetadata{}, err
	}
	columns := map[string]geoParquetColumn{geoParquetGeometryColumn: primaryColumn}
	for name, stats := range secondary {
		column, err := streamingGeoParquetColumn(stats, targetCRS)
		if err != nil {
			return geoParquetMetadata{}, err
		}
		columns[name] = column
	}
	return geoParquetMetadata{Version: geoParquetVersion, PrimaryColumn: geoParquetGeometryColumn, Columns: columns}, nil
}

func streamingGeoParquetColumn(stats *geoParquetGeometryStats, targetCRS string) (geoParquetColumn, error) {
	column := geoParquetColumn{Encoding: "WKB", GeometryTypes: make([]string, 0, len(stats.Types))}
	for geometryType := range stats.Types {
		column.GeometryTypes = append(column.GeometryTypes, geometryType)
	}
	sort.Strings(column.GeometryTypes)
	if !stats.MixedDimension {
		column.BBox = append([]float64(nil), stats.BBox...)
	}
	if targetCRS != geodata.CRSCRS84 {
		crs, err := geodata.GeoParquetCRS(targetCRS)
		if err != nil {
			return geoParquetColumn{}, err
		}
		column.CRS = crs
	}
	return column, nil
}

func geoParquetGeometryBounds(geometry geom.T) []float64 {
	if geometry == nil {
		return nil
	}
	bounds := geometry.Bounds()
	values := make([]float64, 0, geometry.Stride()*2)
	for axis := 0; axis < geometry.Stride(); axis++ {
		values = append(values, bounds.Min(axis))
	}
	for axis := 0; axis < geometry.Stride(); axis++ {
		values = append(values, bounds.Max(axis))
	}
	return values
}

func collectGeoParquetFeatures(input io.Reader, inputMode geodata.InputMode, targetCRS string, geometryProperties []string) ([]encodedGeoParquetFeature, map[string]geoParquetPropertyKind, map[string]bool, error) {
	var features []encodedGeoParquetFeature
	propertyKinds := make(map[string]geoParquetPropertyKind)
	geometryTypes := make(map[string]bool)
	err := geodata.ReadFeatures(input, inputMode, func(feature geodata.Feature) error {
		featureNumber := len(features) + 1
		encoded, err := prepareEncodedGeoParquetFeature(feature, targetCRS, geometryProperties)
		if err != nil {
			return fmt.Errorf("GeoParquet input Feature %d with id %s is invalid: %w", featureNumber, feature.EncodedID(), err)
		}
		for name, raw := range encoded.Properties {
			kind, hasValue, err := inferGeoParquetPropertyKind(raw)
			if err != nil {
				return fmt.Errorf("Feature %s property %q is invalid: %w", feature.EncodedID(), name, err)
			}
			if hasValue {
				propertyKinds[name] = promoteGeoParquetPropertyKind(propertyKinds[name], kind)
			}
		}
		if encoded.Geometry != nil {
			typeName, err := geoParquetGeometryType(encoded.Geometry)
			if err != nil {
				return err
			}
			if encoded.Geometry.Stride() == 3 {
				typeName += " Z"
			}
			geometryTypes[typeName] = true
		}
		features = append(features, encoded)
		return nil
	})
	return features, propertyKinds, geometryTypes, err
}

func prepareEncodedGeoParquetFeature(feature geodata.Feature, targetCRS string, geometryProperties []string) (encodedGeoParquetFeature, error) {
	summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{AllowNullGeometry: true})
	if err != nil {
		return encodedGeoParquetFeature{}, err
	}
	if summary.Type != "null" && summary.CoordinateDimension != 2 && summary.CoordinateDimension != 3 {
		return encodedGeoParquetFeature{}, fmt.Errorf("geometry has %d-coordinate positions; GeoParquet supports 2D or 3D positions", summary.CoordinateDimension)
	}
	var geometry geom.T
	var geometryWKB []byte
	var featureBounds []float64
	if summary.Type != "null" {
		geometry, geometryWKB, featureBounds, err = encodeGeoParquetGeometry(feature.Geometry, targetCRS)
		if err != nil {
			return encodedGeoParquetFeature{}, fmt.Errorf("primary geometry cannot be encoded: %w", err)
		}
	}
	properties, err := feature.PropertyMap()
	if err != nil {
		return encodedGeoParquetFeature{}, err
	}
	secondaryGeometries := make(map[string]geom.T, len(geometryProperties))
	secondaryWKB := make(map[string][]byte, len(geometryProperties))
	for _, name := range geometryProperties {
		raw, exists := properties[name]
		if !exists {
			return encodedGeoParquetFeature{}, fmt.Errorf("secondary geometry property %q is missing", name)
		}
		delete(properties, name)
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		secondaryGeometry, secondaryBytes, _, err := encodeGeoParquetGeometry(raw, targetCRS)
		if err != nil {
			return encodedGeoParquetFeature{}, fmt.Errorf("property %q cannot become a secondary geometry column: %w", name, err)
		}
		secondaryGeometries[name] = secondaryGeometry
		secondaryWKB[name] = secondaryBytes
	}
	nullProperties := make([]string, 0)
	for name, raw := range properties {
		if _, hasValue, err := inferGeoParquetPropertyKind(raw); err != nil {
			return encodedGeoParquetFeature{}, fmt.Errorf("property %q is invalid: %w", name, err)
		} else if !hasValue {
			nullProperties = append(nullProperties, name)
		}
	}
	sort.Strings(nullProperties)
	return encodedGeoParquetFeature{
		Feature:             feature,
		Geometry:            geometry,
		GeometryWKB:         geometryWKB,
		SecondaryGeometries: secondaryGeometries,
		SecondaryWKB:        secondaryWKB,
		Properties:          properties,
		NullProperties:      nullProperties,
		Bounds:              featureBounds,
	}, nil
}

func encodeGeoParquetGeometry(raw json.RawMessage, targetCRS string) (geom.T, []byte, []float64, error) {
	geometry, err := geodata.DecodeGeomJSON(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	if geometry.Stride() != 2 && geometry.Stride() != 3 {
		return nil, nil, nil, fmt.Errorf("geometry has %d-coordinate positions; expected 2D or 3D", geometry.Stride())
	}
	sourceCRS := geodata.CRSCRS84
	if geometry.Stride() == 3 {
		sourceCRS = geodata.CRSCRS84h
	}
	if _, err := geodata.TransformGeometryWithCRSAxisOrder(geometry, sourceCRS, targetCRS); err != nil {
		return nil, nil, nil, err
	}
	encoded, err := geomwkb.Marshal(geometry, binary.LittleEndian)
	if err != nil {
		return nil, nil, nil, err
	}
	bounds := geometry.Bounds()
	encodedBounds := make([]float64, 0, geometry.Stride()*2)
	for coordinateIndex := 0; coordinateIndex < geometry.Stride(); coordinateIndex++ {
		encodedBounds = append(encodedBounds, bounds.Min(coordinateIndex))
	}
	for coordinateIndex := 0; coordinateIndex < geometry.Stride(); coordinateIndex++ {
		encodedBounds = append(encodedBounds, bounds.Max(coordinateIndex))
	}
	return geometry, encoded, encodedBounds, nil
}

func normalizeGeoParquetGeometryProperties(names []string) ([]string, error) {
	reserved := map[string]bool{
		geoParquetGeometryColumn: true, geoParquetCoveringColumn: true, geoParquetIDColumn: true,
		geoParquetFeatureBBoxColumn: true, geoParquetForeignColumn: true, geoParquetNullsColumn: true,
		geoParquetPropertiesNilColumn: true, geoParquetPropertiesColumn: true,
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("GeoParquet secondary geometry property name is empty")
		}
		if reserved[name] {
			return nil, fmt.Errorf("GeoParquet secondary geometry property %q conflicts with a reserved column", name)
		}
		reserved[name] = true
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func inferGeoParquetPropertyKind(raw json.RawMessage) (geoParquetPropertyKind, bool, error) {
	value := bytes.TrimSpace(raw)
	if bytes.Equal(value, []byte("null")) {
		return geoParquetPropertyUnknown, false, nil
	}
	if len(value) == 0 || !json.Valid(value) {
		return geoParquetPropertyUnknown, false, fmt.Errorf("value %q is not valid JSON", raw)
	}
	switch value[0] {
	case '"':
		return geoParquetPropertyString, true, nil
	case 't', 'f':
		return geoParquetPropertyBool, true, nil
	case '{', '[':
		return geoParquetPropertyJSON, true, nil
	default:
		number := json.Number(value)
		if !stringsContainsAny(string(value), ".eE") {
			if _, err := number.Int64(); err == nil {
				return geoParquetPropertyInt, true, nil
			}
		}
		converted, err := number.Float64()
		if err != nil || math.IsNaN(converted) || math.IsInf(converted, 0) {
			return geoParquetPropertyJSON, true, nil
		}
		return geoParquetPropertyDouble, true, nil
	}
}

func promoteGeoParquetPropertyKind(first, second geoParquetPropertyKind) geoParquetPropertyKind {
	if first == geoParquetPropertyUnknown || first == second {
		return second
	}
	if (first == geoParquetPropertyInt && second == geoParquetPropertyDouble) ||
		(first == geoParquetPropertyDouble && second == geoParquetPropertyInt) {
		return geoParquetPropertyDouble
	}
	return geoParquetPropertyJSON
}

func allocateGeoParquetPropertyColumns(kinds map[string]geoParquetPropertyKind, geometryProperties []string) map[string]string {
	used := map[string]bool{
		geoParquetGeometryColumn:      true,
		geoParquetCoveringColumn:      true,
		geoParquetIDColumn:            true,
		geoParquetFeatureBBoxColumn:   true,
		geoParquetForeignColumn:       true,
		geoParquetNullsColumn:         true,
		geoParquetPropertiesNilColumn: true,
		geoParquetPropertiesColumn:    true,
	}
	for _, name := range geometryProperties {
		used[name] = true
	}
	names := make([]string, 0, len(kinds))
	for name := range kinds {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make(map[string]string, len(names))
	for _, name := range names {
		column := name
		for used[column] {
			column = "_property_" + column
		}
		used[column] = true
		result[name] = column
	}
	return result
}

func buildGeoParquetSchema(kinds map[string]geoParquetPropertyKind, propertyColumns map[string]string, geometryEncoding, nativeType string, dimension int, geometryProperties []string) (*parquet.Schema, error) {
	group := parquet.Group{
		geoParquetIDColumn:            parquet.Optional(parquet.JSON()),
		geoParquetFeatureBBoxColumn:   parquet.Optional(parquet.JSON()),
		geoParquetForeignColumn:       parquet.Optional(parquet.JSON()),
		geoParquetNullsColumn:         parquet.Optional(parquet.JSON()),
		geoParquetPropertiesNilColumn: parquet.Optional(parquet.Leaf(parquet.BooleanType)),
	}
	if geometryEncoding == "wkb" {
		group[geoParquetGeometryColumn] = parquet.Optional(parquet.Leaf(parquet.ByteArrayType))
	} else {
		node, err := nativeGeoParquetNode(nativeType, dimension)
		if err != nil {
			return nil, err
		}
		group[geoParquetGeometryColumn] = parquet.Optional(node)
	}
	for _, name := range geometryProperties {
		group[name] = parquet.Optional(parquet.Leaf(parquet.ByteArrayType))
	}
	if dimension == 2 || dimension == 3 {
		bboxGroup := parquet.Group{
			"xmin": parquet.Leaf(parquet.DoubleType),
			"ymin": parquet.Leaf(parquet.DoubleType),
			"xmax": parquet.Leaf(parquet.DoubleType),
			"ymax": parquet.Leaf(parquet.DoubleType),
		}
		if dimension == 3 {
			bboxGroup["zmin"] = parquet.Leaf(parquet.DoubleType)
			bboxGroup["zmax"] = parquet.Leaf(parquet.DoubleType)
		}
		group[geoParquetCoveringColumn] = parquet.Optional(bboxGroup)
	}
	for name, kind := range kinds {
		group[propertyColumns[name]] = parquet.Optional(geoParquetPropertyNode(kind))
	}
	return parquet.NewSchema("geotools", group), nil
}

func geoParquetPropertyNode(kind geoParquetPropertyKind) parquet.Node {
	switch kind {
	case geoParquetPropertyBool:
		return parquet.Leaf(parquet.BooleanType)
	case geoParquetPropertyInt:
		return parquet.Int(64)
	case geoParquetPropertyDouble:
		return parquet.Leaf(parquet.DoubleType)
	case geoParquetPropertyString:
		return parquet.String()
	default:
		return parquet.JSON()
	}
}

func buildGeoParquetMetadata(features []encodedGeoParquetFeature, geometryTypes map[string]bool, targetCRS, encoding, nativeType string, dimension int, geometryProperties []string) (geoParquetMetadata, error) {
	types := make([]string, 0, len(geometryTypes))
	for geometryType := range geometryTypes {
		types = append(types, geometryType)
	}
	sort.Strings(types)
	columnEncoding := "WKB"
	if encoding == "native" {
		columnEncoding = nativeType
	}
	column := geoParquetColumn{
		Encoding:      columnEncoding,
		GeometryTypes: types,
	}
	if dimension == 2 || dimension == 3 {
		column.Covering = &geoParquetCovering{
			BBox: map[string][]string{
				"xmin": {geoParquetCoveringColumn, "xmin"},
				"ymin": {geoParquetCoveringColumn, "ymin"},
				"xmax": {geoParquetCoveringColumn, "xmax"},
				"ymax": {geoParquetCoveringColumn, "ymax"},
			},
		}
	}
	if dimension == 3 {
		column.Covering.BBox["zmin"] = []string{geoParquetCoveringColumn, "zmin"}
		column.Covering.BBox["zmax"] = []string{geoParquetCoveringColumn, "zmax"}
	}
	if targetCRS != geodata.CRSCRS84 {
		crs, err := geodata.GeoParquetCRS(targetCRS)
		if err != nil {
			return geoParquetMetadata{}, err
		}
		column.CRS = crs
	}
	if dimension == 2 || dimension == 3 {
		for _, feature := range features {
			if len(feature.Bounds) == 0 {
				continue
			}
			if column.BBox == nil {
				column.BBox = append([]float64(nil), feature.Bounds...)
			} else {
				mergeNDGeoParquetBounds(column.BBox, feature.Bounds)
			}
		}
	}
	columns := map[string]geoParquetColumn{geoParquetGeometryColumn: column}
	for _, name := range geometryProperties {
		secondary := geoParquetColumn{Encoding: "WKB", GeometryTypes: []string{}}
		secondaryTypes := make(map[string]bool)
		secondaryDimension := 0
		mixedDimensions := false
		for _, feature := range features {
			geometry := feature.SecondaryGeometries[name]
			if geometry == nil {
				continue
			}
			typeName, err := geoParquetGeometryType(geometry)
			if err != nil {
				return geoParquetMetadata{}, err
			}
			if geometry.Stride() == 3 {
				typeName += " Z"
			}
			secondaryTypes[typeName] = true
			if secondaryDimension == 0 {
				secondaryDimension = geometry.Stride()
			} else if secondaryDimension != geometry.Stride() {
				mixedDimensions = true
			}
			bounds := geometry.Bounds()
			geometryBounds := make([]float64, 0, geometry.Stride()*2)
			for axis := 0; axis < geometry.Stride(); axis++ {
				geometryBounds = append(geometryBounds, bounds.Min(axis))
			}
			for axis := 0; axis < geometry.Stride(); axis++ {
				geometryBounds = append(geometryBounds, bounds.Max(axis))
			}
			if mixedDimensions {
				secondary.BBox = nil
			} else if secondary.BBox == nil {
				secondary.BBox = geometryBounds
			} else {
				mergeNDGeoParquetBounds(secondary.BBox, geometryBounds)
			}
		}
		for geometryType := range secondaryTypes {
			secondary.GeometryTypes = append(secondary.GeometryTypes, geometryType)
		}
		sort.Strings(secondary.GeometryTypes)
		if targetCRS != geodata.CRSCRS84 {
			crs, err := geodata.GeoParquetCRS(targetCRS)
			if err != nil {
				return geoParquetMetadata{}, err
			}
			secondary.CRS = crs
		}
		columns[name] = secondary
	}
	return geoParquetMetadata{
		Version:       geoParquetVersion,
		PrimaryColumn: geoParquetGeometryColumn,
		Columns:       columns,
	}, nil
}

func buildGeoParquetRow(schema *parquet.Schema, feature encodedGeoParquetFeature, kinds map[string]geoParquetPropertyKind, propertyColumns map[string]string, geometryEncoding string, geometryProperties []string) (parquet.Row, error) {
	columnValues := make([][]parquet.Value, len(schema.Columns()))
	for index, path := range schema.Columns() {
		leaf, exists := schema.Lookup(path...)
		if !exists {
			return nil, fmt.Errorf("schema column %q cannot be resolved", path)
		}
		columnValues[index] = []parquet.Value{parquet.NullValue().Level(0, 0, leaf.ColumnIndex)}
	}
	if feature.Geometry != nil && geometryEncoding == "wkb" {
		index, leaf, err := geoParquetLeaf(schema, geoParquetGeometryColumn)
		if err != nil {
			return nil, err
		}
		columnValues[index] = []parquet.Value{parquet.ByteArrayValue(feature.GeometryWKB).Level(0, leaf.MaxDefinitionLevel, index)}
	} else if feature.Geometry != nil {
		if err := setNativeGeoParquetGeometry(schema, columnValues, feature.Geometry); err != nil {
			return nil, err
		}
	}
	for _, name := range geometryProperties {
		if feature.SecondaryGeometries[name] == nil {
			continue
		}
		if err := setGeoParquetColumn(schema, columnValues, name, parquet.ByteArrayValue(feature.SecondaryWKB[name])); err != nil {
			return nil, err
		}
	}
	if feature.Geometry == nil && len(feature.Bounds) != 0 {
		return nil, fmt.Errorf("null primary geometry unexpectedly has covering bounds")
	}
	if feature.Feature.ID != nil {
		if err := setGeoParquetColumn(schema, columnValues, geoParquetIDColumn, parquet.ByteArrayValue(feature.Feature.ID)); err != nil {
			return nil, err
		}
	}
	if feature.Feature.BBox != nil {
		if err := setGeoParquetColumn(schema, columnValues, geoParquetFeatureBBoxColumn, parquet.ByteArrayValue(feature.Feature.BBox)); err != nil {
			return nil, err
		}
	}
	if len(feature.Feature.Foreign) > 0 {
		encoded, err := json.Marshal(feature.Feature.Foreign)
		if err != nil {
			return nil, err
		}
		if err := setGeoParquetColumn(schema, columnValues, geoParquetForeignColumn, parquet.ByteArrayValue(encoded)); err != nil {
			return nil, err
		}
	}
	if len(feature.NullProperties) > 0 {
		encoded, err := json.Marshal(feature.NullProperties)
		if err != nil {
			return nil, err
		}
		if err := setGeoParquetColumn(schema, columnValues, geoParquetNullsColumn, parquet.ByteArrayValue(encoded)); err != nil {
			return nil, err
		}
	}
	if bytes.Equal(bytes.TrimSpace(feature.Feature.Properties), []byte("null")) {
		if err := setGeoParquetColumn(schema, columnValues, geoParquetPropertiesNilColumn, parquet.BooleanValue(true)); err != nil {
			return nil, err
		}
	}
	for name, raw := range feature.Properties {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		value, err := geoParquetPropertyValue(raw, kinds[name])
		if err != nil {
			return nil, fmt.Errorf("property %q cannot be encoded: %w", name, err)
		}
		if err := setGeoParquetColumn(schema, columnValues, propertyColumns[name], value); err != nil {
			return nil, err
		}
	}
	if len(feature.Bounds) > 0 {
		if _, exists := schema.Lookup(geoParquetCoveringColumn, "xmin"); exists {
			if err := setGeoParquetBounds(schema, columnValues, feature.Bounds); err != nil {
				return nil, err
			}
		}
	}
	var row parquet.Row
	for _, values := range columnValues {
		row = append(row, values...)
	}
	return row, nil
}

func geoParquetPropertyValue(raw json.RawMessage, kind geoParquetPropertyKind) (parquet.Value, error) {
	switch kind {
	case geoParquetPropertyBool:
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return parquet.Value{}, err
		}
		return parquet.BooleanValue(value), nil
	case geoParquetPropertyInt:
		value, err := strconv.ParseInt(string(raw), 10, 64)
		return parquet.Int64Value(value), err
	case geoParquetPropertyDouble:
		value, err := strconv.ParseFloat(string(raw), 64)
		return parquet.DoubleValue(value), err
	case geoParquetPropertyString:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return parquet.Value{}, err
		}
		return parquet.ByteArrayValue([]byte(value)), nil
	default:
		return parquet.ByteArrayValue(bytes.TrimSpace(raw)), nil
	}
}

func setGeoParquetColumn(schema *parquet.Schema, values [][]parquet.Value, column string, value parquet.Value) error {
	index, leaf, err := geoParquetLeaf(schema, column)
	if err != nil {
		return err
	}
	values[index] = []parquet.Value{value.Level(0, leaf.MaxDefinitionLevel, index)}
	return nil
}

func setGeoParquetBounds(schema *parquet.Schema, values [][]parquet.Value, bounds []float64) error {
	dimension := len(bounds) / 2
	names := []string{"xmin", "ymin"}
	if dimension == 3 {
		names = append(names, "zmin")
	}
	names = append(names, "xmax", "ymax")
	if dimension == 3 {
		names = append(names, "zmax")
	}
	for index, name := range names {
		columnIndex, leaf, err := geoParquetLeaf(schema, geoParquetCoveringColumn, name)
		if err != nil {
			return err
		}
		values[columnIndex] = []parquet.Value{parquet.DoubleValue(bounds[index]).Level(0, leaf.MaxDefinitionLevel, columnIndex)}
	}
	return nil
}

func geoParquetLeaf(schema *parquet.Schema, path ...string) (int, parquet.LeafColumn, error) {
	leaf, exists := schema.Lookup(path...)
	if !exists {
		return -1, parquet.LeafColumn{}, fmt.Errorf("schema column %q is absent", path)
	}
	return leaf.ColumnIndex, leaf, nil
}

func nativeGeoParquetType(features []encodedGeoParquetFeature) (string, int, error) {
	firstIndex := -1
	for index := range features {
		if features[index].Geometry != nil {
			firstIndex = index
			break
		}
	}
	if firstIndex == -1 {
		return "", 2, fmt.Errorf("native GeoParquet encoding requires at least one non-null geometry so its schema can be determined")
	}
	firstType, err := geoParquetGeometryType(features[firstIndex].Geometry)
	if err != nil {
		return "", 0, err
	}
	if firstType == "GeometryCollection" {
		return "", 0, fmt.Errorf("native GeoParquet encoding does not support GeometryCollection; use WKB encoding")
	}
	dimension := features[firstIndex].Geometry.Stride()
	for index := firstIndex + 1; index < len(features); index++ {
		if features[index].Geometry == nil {
			continue
		}
		geometryType, err := geoParquetGeometryType(features[index].Geometry)
		if err != nil {
			return "", 0, err
		}
		if geometryType != firstType || features[index].Geometry.Stride() != dimension {
			return "", 0, fmt.Errorf("native GeoParquet encoding requires one geometry type and coordinate dimension; Feature %d is %s %dD while Feature %d is %s %dD", firstIndex+1, firstType, dimension, index+1, geometryType, features[index].Geometry.Stride())
		}
	}
	return stringsLower(firstType), dimension, nil
}

func nativeGeoParquetNode(geometryType string, dimension int) (parquet.Node, error) {
	coordinate := parquet.Group{
		"x": parquet.Leaf(parquet.DoubleType),
		"y": parquet.Leaf(parquet.DoubleType),
	}
	if dimension == 3 {
		coordinate["z"] = parquet.Leaf(parquet.DoubleType)
	}
	switch geometryType {
	case "point":
		return coordinate, nil
	case "linestring", "multipoint":
		return parquet.List(coordinate), nil
	case "polygon", "multilinestring":
		return parquet.List(parquet.List(coordinate)), nil
	case "multipolygon":
		return parquet.List(parquet.List(parquet.List(coordinate))), nil
	default:
		return nil, fmt.Errorf("native GeoParquet geometry type %q is unsupported", geometryType)
	}
}

func setNativeGeoParquetGeometry(schema *parquet.Schema, values [][]parquet.Value, geometry geom.T) error {
	coordinates, err := nativeGeoParquetCoordinates(geometry)
	if err != nil {
		return err
	}
	axes := []string{"x", "y"}
	if geometry.Stride() == 3 {
		axes = append(axes, "z")
	}
	for axisIndex, axis := range axes {
		columnIndex, leaf, err := geoParquetGeometryAxis(schema, axis)
		if err != nil {
			return err
		}
		axisValues := make([]parquet.Value, len(coordinates))
		for index, coordinate := range coordinates {
			axisValues[index] = parquet.DoubleValue(coordinate.Coordinate[axisIndex]).Level(coordinate.Repetition, leaf.MaxDefinitionLevel, columnIndex)
		}
		values[columnIndex] = axisValues
	}
	return nil
}

func geoParquetGeometryAxis(schema *parquet.Schema, axis string) (int, parquet.LeafColumn, error) {
	for _, path := range schema.Columns() {
		if path[0] == geoParquetGeometryColumn && path[len(path)-1] == axis {
			return geoParquetLeaf(schema, path...)
		}
	}
	return -1, parquet.LeafColumn{}, fmt.Errorf("native geometry axis %q is absent", axis)
}

func nativeGeoParquetCoordinates(geometry geom.T) ([]nativeCoordinate, error) {
	stride := geometry.Stride()
	flat := geometry.FlatCoords()
	coordinates := make([]nativeCoordinate, 0, len(flat)/stride)
	appendRange := func(start, end, samePartRepetition, newPartRepetition int) {
		for offset := start; offset < end; offset += stride {
			repetition := samePartRepetition
			if len(coordinates) == 0 {
				repetition = 0
			} else if offset == start {
				repetition = newPartRepetition
			}
			coordinates = append(coordinates, nativeCoordinate{Coordinate: geom.Coord(flat[offset : offset+stride]), Repetition: repetition})
		}
	}
	switch value := geometry.(type) {
	case *geom.Point:
		appendRange(0, len(flat), 0, 0)
	case *geom.LineString, *geom.MultiPoint:
		appendRange(0, len(flat), 1, 0)
	case *geom.Polygon:
		start := 0
		for _, end := range value.Ends() {
			appendRange(start, end, 2, 1)
			start = end
		}
	case *geom.MultiLineString:
		start := 0
		for _, end := range value.Ends() {
			appendRange(start, end, 2, 1)
			start = end
		}
	case *geom.MultiPolygon:
		start := 0
		for _, ends := range value.Endss() {
			for ringIndex, end := range ends {
				newPartRepetition := 2
				if ringIndex == 0 {
					newPartRepetition = 1
				}
				appendRange(start, end, 3, newPartRepetition)
				start = end
			}
		}
	default:
		return nil, fmt.Errorf("native GeoParquet does not support geometry %T", geometry)
	}
	return coordinates, nil
}

func geoParquetGeometryType(geometry geom.T) (string, error) {
	switch geometry.(type) {
	case *geom.Point:
		return "Point", nil
	case *geom.LineString:
		return "LineString", nil
	case *geom.Polygon:
		return "Polygon", nil
	case *geom.MultiPoint:
		return "MultiPoint", nil
	case *geom.MultiLineString:
		return "MultiLineString", nil
	case *geom.MultiPolygon:
		return "MultiPolygon", nil
	case *geom.GeometryCollection:
		return "GeometryCollection", nil
	default:
		return "", fmt.Errorf("unsupported geometry type %T", geometry)
	}
}

func mergeNDGeoParquetBounds(target, source []float64) {
	dimension := len(target) / 2
	for index := 0; index < dimension; index++ {
		target[index] = math.Min(target[index], source[index])
		target[index+dimension] = math.Max(target[index+dimension], source[index+dimension])
	}
}

func stringsLower(value string) string {
	result := make([]byte, len(value))
	for index := range value {
		character := value[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		result[index] = character
	}
	return string(result)
}

func stringsContainsAny(value, characters string) bool {
	for index := range value {
		for characterIndex := range characters {
			if value[index] == characters[characterIndex] {
				return true
			}
		}
	}
	return false
}
