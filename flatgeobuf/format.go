package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/donomii/geotools/geodata"
	gogamafgb "github.com/gogama/flatgeobuf/flatgeobuf"
	"github.com/gogama/flatgeobuf/flatgeobuf/flat"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/twpayne/go-geom"
	geomjson "github.com/twpayne/go-geom/encoding/geojson"
)

type flatSourceFeature struct {
	Feature     geodata.Feature
	Geometry    geom.T
	Properties  map[string]json.RawMessage
	FeatureJSON []byte
}

type flatColumn struct {
	Name string
	Type flat.ColumnType
}

type writerWithoutClose struct {
	ioWriter
}

type ioWriter interface {
	Write([]byte) (int, error)
}

func inferFlatColumns(features []flatSourceFeature) ([]flatColumn, error) {
	columnTypes := make(map[string]flat.ColumnType)
	for _, feature := range features {
		for key, raw := range feature.Properties {
			propertyType, hasValue, err := flatColumnType(raw)
			if err != nil {
				return nil, fmt.Errorf("Feature %s property %q is invalid: %w", feature.Feature.EncodedID(), key, err)
			}
			if !hasValue {
				if _, exists := columnTypes[key]; !exists {
					columnTypes[key] = flat.ColumnTypeJson
				}
			} else if existing, exists := columnTypes[key]; exists {
				columnTypes[key] = promoteFlatColumnType(existing, propertyType)
			} else {
				columnTypes[key] = propertyType
			}
		}
	}
	names := make([]string, 0, len(columnTypes))
	for name := range columnTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names)+1 > math.MaxUint16+1 {
		return nil, fmt.Errorf("FlatGeobuf property schema has %d columns; maximum is %d", len(names)+1, math.MaxUint16+1)
	}
	columns := []flatColumn{{Name: preservedFeatureProperty, Type: flat.ColumnTypeString}}
	for _, name := range names {
		columns = append(columns, flatColumn{Name: name, Type: columnTypes[name]})
	}
	return columns, nil
}

func flatColumnType(raw json.RawMessage) (flat.ColumnType, bool, error) {
	value := bytes.TrimSpace(raw)
	if bytes.Equal(value, []byte("null")) {
		return flat.ColumnTypeJson, false, nil
	}
	if len(value) == 0 || !json.Valid(value) {
		return 0, false, fmt.Errorf("value %q is not valid JSON", raw)
	}
	switch value[0] {
	case '"':
		return flat.ColumnTypeString, true, nil
	case 't', 'f':
		return flat.ColumnTypeBool, true, nil
	case '{', '[':
		return flat.ColumnTypeJson, true, nil
	default:
		number := json.Number(value)
		if strings.ContainsAny(string(value), ".eE") {
			if converted, err := number.Float64(); err == nil && !math.IsNaN(converted) && !math.IsInf(converted, 0) {
				return flat.ColumnTypeDouble, true, nil
			}
			return flat.ColumnTypeJson, true, nil
		}
		if _, err := number.Int64(); err == nil {
			return flat.ColumnTypeLong, true, nil
		}
		return flat.ColumnTypeJson, true, nil
	}
}

func promoteFlatColumnType(first, second flat.ColumnType) flat.ColumnType {
	if first == second {
		return first
	}
	if first == flat.ColumnTypeJson || second == flat.ColumnTypeJson {
		return flat.ColumnTypeJson
	}
	if (first == flat.ColumnTypeLong && second == flat.ColumnTypeDouble) ||
		(first == flat.ColumnTypeDouble && second == flat.ColumnTypeLong) {
		return flat.ColumnTypeDouble
	}
	return flat.ColumnTypeJson
}

func buildFlatFeature(source flatSourceFeature, columns []flatColumn) (flat.Feature, error) {
	properties, err := encodeFlatProperties(source, columns)
	if err != nil {
		return flat.Feature{}, err
	}
	builder := flatbuffers.NewBuilder(1024)
	geometryOffset, err := buildFlatGeometry(builder, source.Geometry)
	if err != nil {
		return flat.Feature{}, err
	}
	propertiesOffset := builder.CreateByteVector(properties)
	flat.FeatureStart(builder)
	flat.FeatureAddGeometry(builder, geometryOffset)
	flat.FeatureAddProperties(builder, propertiesOffset)
	featureOffset := flat.FeatureEnd(builder)
	flat.FinishSizePrefixedFeatureBuffer(builder, featureOffset)
	return *flat.GetSizePrefixedRootAsFeature(builder.FinishedBytes(), 0), nil
}

func encodeFlatProperties(source flatSourceFeature, columns []flatColumn) ([]byte, error) {
	var encoded bytes.Buffer
	writer := gogamafgb.NewPropWriter(&encoded)
	for index, column := range columns {
		var raw json.RawMessage
		if column.Name == preservedFeatureProperty {
			raw, _ = json.Marshal(string(source.FeatureJSON))
		} else {
			raw = source.Properties[column.Name]
			if raw == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				continue
			}
		}
		if _, err := writer.WriteUShort(uint16(index)); err != nil {
			return nil, err
		}
		if err := writeFlatProperty(writer, column, raw); err != nil {
			return nil, fmt.Errorf("property %q cannot be encoded as %s: %w", column.Name, column.Type, err)
		}
	}
	return encoded.Bytes(), nil
}

func writeFlatProperty(writer *gogamafgb.PropWriter, column flatColumn, raw json.RawMessage) error {
	switch column.Type {
	case flat.ColumnTypeBool:
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		_, err := writer.WriteBool(value)
		return err
	case flat.ColumnTypeLong:
		value, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return err
		}
		_, err = writer.WriteLong(value)
		return err
	case flat.ColumnTypeDouble:
		value, err := strconv.ParseFloat(string(raw), 64)
		if err != nil {
			return err
		}
		_, err = writer.WriteDouble(value)
		return err
	case flat.ColumnTypeString:
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		_, err := writer.WriteString(value)
		return err
	case flat.ColumnTypeJson:
		_, err := writer.WriteBinary(bytes.TrimSpace(raw))
		return err
	default:
		return fmt.Errorf("unsupported output column type %s", column.Type)
	}
}

func buildFlatHeader(layerName string, columns []flatColumn, features []flatSourceFeature, indexed bool, crs string, dimension int) (*flat.Header, error) {
	builder := flatbuffers.NewBuilder(1024)
	columnOffsets := make([]flatbuffers.UOffsetT, len(columns))
	for index, column := range columns {
		nameOffset := builder.CreateString(column.Name)
		titleOffset := builder.CreateString(column.Name)
		flat.ColumnStart(builder)
		flat.ColumnAddName(builder, nameOffset)
		flat.ColumnAddTitle(builder, titleOffset)
		flat.ColumnAddType(builder, column.Type)
		flat.ColumnAddNullable(builder, true)
		columnOffsets[index] = flat.ColumnEnd(builder)
	}
	flat.HeaderStartColumnsVector(builder, len(columnOffsets))
	for index := len(columnOffsets) - 1; index >= 0; index-- {
		builder.PrependUOffsetT(columnOffsets[index])
	}
	columnsOffset := builder.EndVector(len(columnOffsets))
	var envelopeOffset flatbuffers.UOffsetT
	geometryType := flat.GeometryTypeUnknown
	if len(features) > 0 {
		bounds := features[0].Geometry.Bounds()
		geometryType = flatGeometryType(features[0].Geometry)
		for _, feature := range features[1:] {
			bounds.Extend(feature.Geometry)
			if flatGeometryType(feature.Geometry) != geometryType {
				geometryType = flat.GeometryTypeUnknown
			}
		}
		envelope := []float64{bounds.Min(0), bounds.Min(1), bounds.Max(0), bounds.Max(1)}
		flat.HeaderStartEnvelopeVector(builder, len(envelope))
		for index := len(envelope) - 1; index >= 0; index-- {
			builder.PrependFloat64(envelope[index])
		}
		envelopeOffset = builder.EndVector(len(envelope))
	}
	org := "EPSG"
	code := 4326
	codeString := ""
	if crs == geodata.CRSCRS84h {
		org, code, codeString = "OGC", 0, "CRS84h"
	} else if crs != geodata.CRSCRS84 {
		parts := strings.Split(crs, ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("FlatGeobuf CRS %q cannot be stored as an organization and code", crs)
		}
		parsed, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("FlatGeobuf CRS %q has a non-numeric EPSG code: %w", crs, err)
		}
		code = parsed
	}
	orgOffset := builder.CreateString(org)
	nameOffset := builder.CreateString(crs)
	var codeStringOffset flatbuffers.UOffsetT
	if codeString != "" {
		codeStringOffset = builder.CreateString(codeString)
	}
	flat.CrsStart(builder)
	flat.CrsAddOrg(builder, orgOffset)
	flat.CrsAddCode(builder, int32(code))
	if codeStringOffset != 0 {
		flat.CrsAddCodeString(builder, codeStringOffset)
	}
	flat.CrsAddName(builder, nameOffset)
	crsOffset := flat.CrsEnd(builder)
	layerOffset := builder.CreateString(layerName)
	flat.HeaderStart(builder)
	flat.HeaderAddName(builder, layerOffset)
	if envelopeOffset != 0 {
		flat.HeaderAddEnvelope(builder, envelopeOffset)
	}
	flat.HeaderAddGeometryType(builder, geometryType)
	flat.HeaderAddHasZ(builder, dimension == 3)
	flat.HeaderAddColumns(builder, columnsOffset)
	flat.HeaderAddFeaturesCount(builder, uint64(len(features)))
	indexNodeSize := uint16(0)
	if indexed && len(features) > 0 {
		indexNodeSize = 16
	}
	flat.HeaderAddIndexNodeSize(builder, indexNodeSize)
	flat.HeaderAddCrs(builder, crsOffset)
	headerOffset := flat.HeaderEnd(builder)
	flat.FinishSizePrefixedHeaderBuffer(builder, headerOffset)
	return flat.GetSizePrefixedRootAsHeader(builder.FinishedBytes(), 0), nil
}

func buildFlatGeometry(builder *flatbuffers.Builder, geometry geom.T) (flatbuffers.UOffsetT, error) {
	switch value := geometry.(type) {
	case *geom.Point, *geom.MultiPoint, *geom.LineString:
		xy, z := flatCoordinateVectors(geometry)
		xyOffset := buildFlatFloat64Vector(builder, xy, flat.GeometryStartXyVector)
		zOffset := buildFlatFloat64Vector(builder, z, flat.GeometryStartZVector)
		return finishFlatGeometry(builder, flatGeometryType(geometry), 0, xyOffset, zOffset, 0), nil
	case *geom.MultiLineString:
		xy, z := flatCoordinateVectors(geometry)
		endsOffset := buildFlatEnds(builder, flatEnds(value.Ends(), geometry.Stride()))
		xyOffset := buildFlatFloat64Vector(builder, xy, flat.GeometryStartXyVector)
		zOffset := buildFlatFloat64Vector(builder, z, flat.GeometryStartZVector)
		return finishFlatGeometry(builder, flat.GeometryTypeMultiLineString, endsOffset, xyOffset, zOffset, 0), nil
	case *geom.Polygon:
		xy, z := flatCoordinateVectors(geometry)
		endsOffset := buildFlatEnds(builder, flatEnds(value.Ends(), geometry.Stride()))
		xyOffset := buildFlatFloat64Vector(builder, xy, flat.GeometryStartXyVector)
		zOffset := buildFlatFloat64Vector(builder, z, flat.GeometryStartZVector)
		return finishFlatGeometry(builder, flat.GeometryTypePolygon, endsOffset, xyOffset, zOffset, 0), nil
	case *geom.MultiPolygon:
		partOffsets := make([]flatbuffers.UOffsetT, value.NumPolygons())
		for index := 0; index < value.NumPolygons(); index++ {
			offset, err := buildFlatGeometry(builder, value.Polygon(index))
			if err != nil {
				return 0, err
			}
			partOffsets[index] = offset
		}
		partsOffset := buildFlatParts(builder, partOffsets)
		return finishFlatGeometry(builder, flat.GeometryTypeMultiPolygon, 0, 0, 0, partsOffset), nil
	case *geom.GeometryCollection:
		partOffsets := make([]flatbuffers.UOffsetT, value.NumGeoms())
		for index := 0; index < value.NumGeoms(); index++ {
			offset, err := buildFlatGeometry(builder, value.Geom(index))
			if err != nil {
				return 0, err
			}
			partOffsets[index] = offset
		}
		partsOffset := buildFlatParts(builder, partOffsets)
		return finishFlatGeometry(builder, flat.GeometryTypeGeometryCollection, 0, 0, 0, partsOffset), nil
	default:
		return 0, fmt.Errorf("unsupported geometry type %T", geometry)
	}
}

func finishFlatGeometry(builder *flatbuffers.Builder, geometryType flat.GeometryType, ends, xy, z, parts flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	flat.GeometryStart(builder)
	flat.GeometryAddType(builder, geometryType)
	if ends != 0 {
		flat.GeometryAddEnds(builder, ends)
	}
	if xy != 0 {
		flat.GeometryAddXy(builder, xy)
	}
	if z != 0 {
		flat.GeometryAddZ(builder, z)
	}
	if parts != 0 {
		flat.GeometryAddParts(builder, parts)
	}
	return flat.GeometryEnd(builder)
}

func buildFlatFloat64Vector(builder *flatbuffers.Builder, values []float64, start func(*flatbuffers.Builder, int) flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	if len(values) == 0 {
		return 0
	}
	start(builder, len(values))
	for index := len(values) - 1; index >= 0; index-- {
		builder.PrependFloat64(values[index])
	}
	return builder.EndVector(len(values))
}

func flatCoordinateVectors(geometry geom.T) ([]float64, []float64) {
	flatCoordinates := geometry.FlatCoords()
	stride := geometry.Stride()
	xy := make([]float64, 0, len(flatCoordinates)/stride*2)
	var z []float64
	if stride == 3 {
		z = make([]float64, 0, len(flatCoordinates)/stride)
	}
	for offset := 0; offset < len(flatCoordinates); offset += stride {
		xy = append(xy, flatCoordinates[offset], flatCoordinates[offset+1])
		if stride == 3 {
			z = append(z, flatCoordinates[offset+2])
		}
	}
	return xy, z
}

func flatEnds(ends []int, stride int) []uint32 {
	result := make([]uint32, len(ends))
	for index, end := range ends {
		result[index] = uint32(end / stride)
	}
	return result
}

func buildFlatEnds(builder *flatbuffers.Builder, values []uint32) flatbuffers.UOffsetT {
	flat.GeometryStartEndsVector(builder, len(values))
	for index := len(values) - 1; index >= 0; index-- {
		builder.PrependUint32(values[index])
	}
	return builder.EndVector(len(values))
}

func buildFlatParts(builder *flatbuffers.Builder, values []flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	flat.GeometryStartPartsVector(builder, len(values))
	for index := len(values) - 1; index >= 0; index-- {
		builder.PrependUOffsetT(values[index])
	}
	return builder.EndVector(len(values))
}

func flatGeometryType(geometry geom.T) flat.GeometryType {
	switch geometry.(type) {
	case *geom.Point:
		return flat.GeometryTypePoint
	case *geom.MultiPoint:
		return flat.GeometryTypeMultiPoint
	case *geom.LineString:
		return flat.GeometryTypeLineString
	case *geom.MultiLineString:
		return flat.GeometryTypeMultiLineString
	case *geom.Polygon:
		return flat.GeometryTypePolygon
	case *geom.MultiPolygon:
		return flat.GeometryTypeMultiPolygon
	case *geom.GeometryCollection:
		return flat.GeometryTypeGeometryCollection
	default:
		return flat.GeometryTypeUnknown
	}
}

func flatHeaderCRS(header *flat.Header) (string, error) {
	var crs flat.Crs
	if header.Crs(&crs) == nil {
		return "", fmt.Errorf("FlatGeobuf header has no CRS; GeoJSON output requires a CRS that can be reprojected to WGS 84")
	}
	organization := string(crs.Org())
	if organization == "EPSG" && crs.Code() != 0 {
		normalized, err := geodata.NormalizeCRS(fmt.Sprintf("EPSG:%d", crs.Code()))
		if err != nil {
			return "", fmt.Errorf("FlatGeobuf CRS organization %q numeric code %d cannot be reprojected: %w", crs.Org(), crs.Code(), err)
		}
		return normalized, nil
	}
	if organization == "OGC" && len(crs.CodeString()) != 0 {
		normalized, err := geodata.NormalizeCRS("OGC:" + string(crs.CodeString()))
		if err != nil {
			return "", fmt.Errorf("FlatGeobuf CRS organization %q string code %q cannot be reprojected: %w", crs.Org(), crs.CodeString(), err)
		}
		return normalized, nil
	}
	return "", fmt.Errorf("FlatGeobuf CRS is organization %q numeric code %d string code %q; expected a supported EPSG or OGC identifier", crs.Org(), crs.Code(), crs.CodeString())
}

func decodeFlatFeature(encoded *flat.Feature, header *flat.Header, sourceCRS string) (geodata.Feature, error) {
	var flatGeometry flat.Geometry
	if encoded.Geometry(&flatGeometry) == nil {
		return geodata.Feature{}, fmt.Errorf("Feature has no geometry")
	}
	geometry, err := flatGeometryToGeom(&flatGeometry, header.GeometryType(), header.HasZ())
	if err != nil {
		return geodata.Feature{}, err
	}
	targetCRS := geodata.CRSCRS84
	if geometry.Stride() == 3 {
		targetCRS = geodata.CRSCRS84h
	}
	if _, err := geodata.TransformGeometry(geometry, sourceCRS, targetCRS); err != nil {
		return geodata.Feature{}, fmt.Errorf("geometry cannot be reprojected from %s to %s: %w", sourceCRS, targetCRS, err)
	}
	properties, preserved, hasPreserved, err := decodeFlatProperties(encoded.PropertiesBytes(), header)
	if err != nil {
		return geodata.Feature{}, err
	}
	if hasPreserved {
		var feature geodata.Feature
		if err := json.Unmarshal([]byte(preserved), &feature); err != nil {
			return geodata.Feature{}, fmt.Errorf("reserved property %q does not contain a valid Feature: %w", preservedFeatureProperty, err)
		}
		originalGeometry, err := geodata.DecodeGeomJSON(feature.Geometry)
		if err != nil {
			return geodata.Feature{}, fmt.Errorf("reserved property %q contains invalid geometry: %w", preservedFeatureProperty, err)
		}
		if !equalFlatGeometry(originalGeometry, geometry) {
			return geodata.Feature{}, fmt.Errorf("FlatGeobuf geometry differs from the geometry in reserved property %q", preservedFeatureProperty)
		}
		return feature, nil
	}
	geometryJSON, err := geomjson.Marshal(geometry)
	if err != nil {
		return geodata.Feature{}, err
	}
	propertiesJSON, err := json.Marshal(properties)
	if err != nil {
		return geodata.Feature{}, err
	}
	return geodata.Feature{Type: "Feature", Geometry: geometryJSON, Properties: propertiesJSON}, nil
}

func decodeFlatProperties(data []byte, header *flat.Header) (map[string]json.RawMessage, string, bool, error) {
	properties := make(map[string]json.RawMessage)
	buffer := bytes.NewReader(data)
	reader := gogamafgb.NewPropReader(buffer)
	var preserved string
	hasPreserved := false
	for buffer.Len() > 0 {
		columnIndex, err := reader.ReadUShort()
		if err != nil {
			return nil, "", false, fmt.Errorf("failed to read property column index: %w", err)
		}
		if int(columnIndex) >= header.ColumnsLength() {
			return nil, "", false, fmt.Errorf("property column index %d exceeds header column count %d", columnIndex, header.ColumnsLength())
		}
		var column flat.Column
		if !header.Columns(&column, int(columnIndex)) {
			return nil, "", false, fmt.Errorf("header column %d is missing", columnIndex)
		}
		raw, err := readFlatProperty(reader, column.Type())
		if err != nil {
			return nil, "", false, fmt.Errorf("property %q cannot be decoded as %s: %w", column.Name(), column.Type(), err)
		}
		name := string(column.Name())
		if name == preservedFeatureProperty {
			if column.Type() != flat.ColumnTypeString {
				return nil, "", false, fmt.Errorf("reserved property %q has type %s; expected String", name, column.Type())
			}
			if err := json.Unmarshal(raw, &preserved); err != nil {
				return nil, "", false, err
			}
			hasPreserved = true
		} else {
			properties[name] = raw
		}
	}
	return properties, preserved, hasPreserved, nil
}

func readFlatProperty(reader *gogamafgb.PropReader, columnType flat.ColumnType) (json.RawMessage, error) {
	switch columnType {
	case flat.ColumnTypeByte:
		value, err := reader.ReadByte()
		return json.RawMessage(strconv.FormatInt(int64(value), 10)), err
	case flat.ColumnTypeUByte:
		value, err := reader.ReadUByte()
		return json.RawMessage(strconv.FormatUint(uint64(value), 10)), err
	case flat.ColumnTypeBool:
		value, err := reader.ReadBool()
		return json.RawMessage(strconv.FormatBool(value)), err
	case flat.ColumnTypeShort:
		value, err := reader.ReadShort()
		return json.RawMessage(strconv.FormatInt(int64(value), 10)), err
	case flat.ColumnTypeUShort:
		value, err := reader.ReadUShort()
		return json.RawMessage(strconv.FormatUint(uint64(value), 10)), err
	case flat.ColumnTypeInt:
		value, err := reader.ReadInt()
		return json.RawMessage(strconv.FormatInt(int64(value), 10)), err
	case flat.ColumnTypeUInt:
		value, err := reader.ReadUInt()
		return json.RawMessage(strconv.FormatUint(uint64(value), 10)), err
	case flat.ColumnTypeLong:
		value, err := reader.ReadLong()
		return json.RawMessage(strconv.FormatInt(value, 10)), err
	case flat.ColumnTypeULong:
		value, err := reader.ReadULong()
		return json.RawMessage(strconv.FormatUint(value, 10)), err
	case flat.ColumnTypeFloat:
		value, err := reader.ReadFloat()
		if err == nil && (math.IsNaN(float64(value)) || math.IsInf(float64(value), 0)) {
			err = fmt.Errorf("value %v is not finite", value)
		}
		return json.RawMessage(strconv.FormatFloat(float64(value), 'g', -1, 32)), err
	case flat.ColumnTypeDouble:
		value, err := reader.ReadDouble()
		if err == nil && (math.IsNaN(value) || math.IsInf(value, 0)) {
			err = fmt.Errorf("value %v is not finite", value)
		}
		return json.RawMessage(strconv.FormatFloat(value, 'g', -1, 64)), err
	case flat.ColumnTypeString, flat.ColumnTypeDateTime:
		value, err := reader.ReadString()
		encoded, encodeErr := json.Marshal(value)
		return encoded, errorsJoin(err, encodeErr)
	case flat.ColumnTypeJson:
		value, err := reader.ReadBinary()
		if err == nil && !json.Valid(value) {
			err = fmt.Errorf("value %q is not valid JSON", value)
		}
		return json.RawMessage(value), err
	case flat.ColumnTypeBinary:
		value, err := reader.ReadBinary()
		encoded, encodeErr := json.Marshal(value)
		return encoded, errorsJoin(err, encodeErr)
	default:
		return nil, fmt.Errorf("unsupported FlatGeobuf column type %d", columnType)
	}
}

func errorsJoin(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

func flatGeometryToGeom(geometry *flat.Geometry, inheritedType flat.GeometryType, hasZ bool) (geom.T, error) {
	geometryType := geometry.Type()
	if geometryType == flat.GeometryTypeUnknown {
		geometryType = inheritedType
	}
	layout, coordinates, err := flatCoordinates(geometry, hasZ)
	if err != nil {
		return nil, err
	}
	stride := layout.Stride()
	switch geometryType {
	case flat.GeometryTypePoint:
		if len(coordinates) != stride {
			return nil, fmt.Errorf("Point contains %d positions; expected 1", len(coordinates)/stride)
		}
		return geom.NewPointFlat(layout, coordinates), nil
	case flat.GeometryTypeMultiPoint:
		return geom.NewMultiPointFlat(layout, coordinates), nil
	case flat.GeometryTypeLineString:
		return geom.NewLineStringFlat(layout, coordinates), nil
	case flat.GeometryTypeMultiLineString:
		ends, err := flatGeomEnds(geometry, len(coordinates)/stride, stride)
		if err != nil {
			return nil, err
		}
		if len(ends) == 0 && len(coordinates) > 0 {
			ends = []int{len(coordinates)}
		}
		return geom.NewMultiLineStringFlat(layout, coordinates, ends), nil
	case flat.GeometryTypePolygon:
		ends, err := flatGeomEnds(geometry, len(coordinates)/stride, stride)
		if err != nil {
			return nil, err
		}
		if len(ends) == 0 && len(coordinates) > 0 {
			ends = []int{len(coordinates)}
		}
		return geom.NewPolygonFlat(layout, coordinates, ends), nil
	case flat.GeometryTypeMultiPolygon:
		polygons := geom.NewMultiPolygon(layout)
		for index := 0; index < geometry.PartsLength(); index++ {
			var part flat.Geometry
			if !geometry.Parts(&part, index) {
				return nil, fmt.Errorf("MultiPolygon part %d is missing", index)
			}
			converted, err := flatGeometryToGeom(&part, flat.GeometryTypePolygon, hasZ)
			if err != nil {
				return nil, fmt.Errorf("MultiPolygon part %d is invalid: %w", index, err)
			}
			polygon, ok := converted.(*geom.Polygon)
			if !ok {
				return nil, fmt.Errorf("MultiPolygon part %d has type %T; expected Polygon", index, converted)
			}
			if err := polygons.Push(polygon); err != nil {
				return nil, err
			}
		}
		return polygons, nil
	case flat.GeometryTypeGeometryCollection:
		collection := geom.NewGeometryCollection()
		for index := 0; index < geometry.PartsLength(); index++ {
			var part flat.Geometry
			if !geometry.Parts(&part, index) {
				return nil, fmt.Errorf("GeometryCollection part %d is missing", index)
			}
			converted, err := flatGeometryToGeom(&part, flat.GeometryTypeUnknown, hasZ)
			if err != nil {
				return nil, fmt.Errorf("GeometryCollection part %d is invalid: %w", index, err)
			}
			if err := collection.Push(converted); err != nil {
				return nil, err
			}
		}
		return collection, nil
	default:
		return nil, fmt.Errorf("unsupported FlatGeobuf geometry type %s", geometryType)
	}
}

func flatCoordinates(geometry *flat.Geometry, hasZ bool) (geom.Layout, []float64, error) {
	if geometry.XyLength()%2 != 0 {
		return geom.NoLayout, nil, fmt.Errorf("geometry has %d XY values; expected an even count", geometry.XyLength())
	}
	positionCount := geometry.XyLength() / 2
	layout := geom.XY
	if hasZ {
		layout = geom.XYZ
		if geometry.ZLength() != positionCount {
			return geom.NoLayout, nil, fmt.Errorf("3D geometry has %d positions and %d Z values", positionCount, geometry.ZLength())
		}
	} else if geometry.ZLength() != 0 {
		return geom.NoLayout, nil, fmt.Errorf("2D FlatGeobuf header accompanies %d unexpected Z values", geometry.ZLength())
	}
	coordinates := make([]float64, 0, positionCount*layout.Stride())
	for index := 0; index < geometry.XyLength(); index += 2 {
		coordinates = append(coordinates, geometry.Xy(index), geometry.Xy(index+1))
		if hasZ {
			coordinates = append(coordinates, geometry.Z(index/2))
		}
	}
	return layout, coordinates, nil
}

func flatGeomEnds(geometry *flat.Geometry, positionCount, stride int) ([]int, error) {
	if geometry.EndsLength() == 0 {
		return nil, nil
	}
	ends := make([]int, 0, geometry.EndsLength())
	start := 0
	for index := 0; index < geometry.EndsLength(); index++ {
		end := int(geometry.Ends(index))
		if end <= start || end > positionCount {
			return nil, fmt.Errorf("geometry end %d is %d; expected a value above %d and at most %d", index, end, start, positionCount)
		}
		ends = append(ends, end*stride)
		start = end
	}
	if start != positionCount {
		return nil, fmt.Errorf("geometry ends at position %d but contains %d positions", start, positionCount)
	}
	return ends, nil
}

func equalFlatGeometry(first, second geom.T) bool {
	if reflect.TypeOf(first) != reflect.TypeOf(second) || first.Stride() != second.Stride() {
		return false
	}
	firstCoordinates := first.FlatCoords()
	secondCoordinates := second.FlatCoords()
	if len(firstCoordinates) != len(secondCoordinates) {
		return false
	}
	for index := range firstCoordinates {
		if math.Abs(firstCoordinates[index]-secondCoordinates[index]) > 0.00002 {
			return false
		}
	}
	return true
}
