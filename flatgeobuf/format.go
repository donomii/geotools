package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/donomii/geotools/geodata"
	gogamafgb "github.com/gogama/flatgeobuf/flatgeobuf"
	"github.com/gogama/flatgeobuf/flatgeobuf/flat"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
)

type flatSourceFeature struct {
	Feature     geodata.Feature
	Geometry    orb.Geometry
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

func buildFlatHeader(layerName string, columns []flatColumn, features []flatSourceFeature, indexed bool) (*flat.Header, error) {
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
		bounds := features[0].Geometry.Bound()
		geometryType = flatGeometryType(features[0].Geometry)
		for _, feature := range features[1:] {
			bounds = bounds.Union(feature.Geometry.Bound())
			if flatGeometryType(feature.Geometry) != geometryType {
				geometryType = flat.GeometryTypeUnknown
			}
		}
		envelope := []float64{bounds.Min[0], bounds.Min[1], bounds.Max[0], bounds.Max[1]}
		flat.HeaderStartEnvelopeVector(builder, len(envelope))
		for index := len(envelope) - 1; index >= 0; index-- {
			builder.PrependFloat64(envelope[index])
		}
		envelopeOffset = builder.EndVector(len(envelope))
	}
	orgOffset := builder.CreateString("EPSG")
	nameOffset := builder.CreateString("WGS 84")
	flat.CrsStart(builder)
	flat.CrsAddOrg(builder, orgOffset)
	flat.CrsAddCode(builder, 4326)
	flat.CrsAddName(builder, nameOffset)
	crsOffset := flat.CrsEnd(builder)
	layerOffset := builder.CreateString(layerName)
	flat.HeaderStart(builder)
	flat.HeaderAddName(builder, layerOffset)
	if envelopeOffset != 0 {
		flat.HeaderAddEnvelope(builder, envelopeOffset)
	}
	flat.HeaderAddGeometryType(builder, geometryType)
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

func buildFlatGeometry(builder *flatbuffers.Builder, geometry orb.Geometry) (flatbuffers.UOffsetT, error) {
	switch value := geometry.(type) {
	case orb.Point:
		xyOffset := buildFlatXY(builder, []float64{value[0], value[1]})
		return finishFlatGeometry(builder, flat.GeometryTypePoint, 0, xyOffset, 0), nil
	case orb.MultiPoint:
		xy := make([]float64, 0, len(value)*2)
		for _, point := range value {
			xy = append(xy, point[0], point[1])
		}
		xyOffset := buildFlatXY(builder, xy)
		return finishFlatGeometry(builder, flat.GeometryTypeMultiPoint, 0, xyOffset, 0), nil
	case orb.LineString:
		xyOffset := buildFlatXY(builder, lineStringXY(value))
		return finishFlatGeometry(builder, flat.GeometryTypeLineString, 0, xyOffset, 0), nil
	case orb.MultiLineString:
		xy, ends := multiLineXYEnds(value)
		endsOffset := buildFlatEnds(builder, ends)
		xyOffset := buildFlatXY(builder, xy)
		return finishFlatGeometry(builder, flat.GeometryTypeMultiLineString, endsOffset, xyOffset, 0), nil
	case orb.Polygon:
		xy, ends := polygonXYEnds(value)
		endsOffset := buildFlatEnds(builder, ends)
		xyOffset := buildFlatXY(builder, xy)
		return finishFlatGeometry(builder, flat.GeometryTypePolygon, endsOffset, xyOffset, 0), nil
	case orb.MultiPolygon:
		partOffsets := make([]flatbuffers.UOffsetT, len(value))
		for index, polygon := range value {
			offset, err := buildFlatGeometry(builder, polygon)
			if err != nil {
				return 0, err
			}
			partOffsets[index] = offset
		}
		partsOffset := buildFlatParts(builder, partOffsets)
		return finishFlatGeometry(builder, flat.GeometryTypeMultiPolygon, 0, 0, partsOffset), nil
	case orb.Collection:
		partOffsets := make([]flatbuffers.UOffsetT, len(value))
		for index, child := range value {
			offset, err := buildFlatGeometry(builder, child)
			if err != nil {
				return 0, err
			}
			partOffsets[index] = offset
		}
		partsOffset := buildFlatParts(builder, partOffsets)
		return finishFlatGeometry(builder, flat.GeometryTypeGeometryCollection, 0, 0, partsOffset), nil
	default:
		return 0, fmt.Errorf("unsupported geometry type %T", geometry)
	}
}

func finishFlatGeometry(builder *flatbuffers.Builder, geometryType flat.GeometryType, ends, xy, parts flatbuffers.UOffsetT) flatbuffers.UOffsetT {
	flat.GeometryStart(builder)
	flat.GeometryAddType(builder, geometryType)
	if ends != 0 {
		flat.GeometryAddEnds(builder, ends)
	}
	if xy != 0 {
		flat.GeometryAddXy(builder, xy)
	}
	if parts != 0 {
		flat.GeometryAddParts(builder, parts)
	}
	return flat.GeometryEnd(builder)
}

func buildFlatXY(builder *flatbuffers.Builder, values []float64) flatbuffers.UOffsetT {
	flat.GeometryStartXyVector(builder, len(values))
	for index := len(values) - 1; index >= 0; index-- {
		builder.PrependFloat64(values[index])
	}
	return builder.EndVector(len(values))
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

func lineStringXY(line orb.LineString) []float64 {
	xy := make([]float64, 0, len(line)*2)
	for _, point := range line {
		xy = append(xy, point[0], point[1])
	}
	return xy
}

func multiLineXYEnds(lines orb.MultiLineString) ([]float64, []uint32) {
	var xy []float64
	ends := make([]uint32, 0, len(lines))
	for _, line := range lines {
		xy = append(xy, lineStringXY(line)...)
		ends = append(ends, uint32(len(xy)/2))
	}
	return xy, ends
}

func polygonXYEnds(polygon orb.Polygon) ([]float64, []uint32) {
	var xy []float64
	ends := make([]uint32, 0, len(polygon))
	for _, ring := range polygon {
		for _, point := range ring {
			xy = append(xy, point[0], point[1])
		}
		ends = append(ends, uint32(len(xy)/2))
	}
	return xy, ends
}

func flatGeometryType(geometry orb.Geometry) flat.GeometryType {
	switch geometry.(type) {
	case orb.Point:
		return flat.GeometryTypePoint
	case orb.MultiPoint:
		return flat.GeometryTypeMultiPoint
	case orb.LineString:
		return flat.GeometryTypeLineString
	case orb.MultiLineString:
		return flat.GeometryTypeMultiLineString
	case orb.Polygon:
		return flat.GeometryTypePolygon
	case orb.MultiPolygon:
		return flat.GeometryTypeMultiPolygon
	case orb.Collection:
		return flat.GeometryTypeGeometryCollection
	default:
		return flat.GeometryTypeUnknown
	}
}

func validateFlatCRS(header *flat.Header) error {
	var crs flat.Crs
	if header.Crs(&crs) == nil {
		return fmt.Errorf("FlatGeobuf header has no CRS; GeoJSON output requires WGS 84 longitude and latitude")
	}
	isEPSG4326 := string(crs.Org()) == "EPSG" && crs.Code() == 4326
	isCRS84 := string(crs.Org()) == "OGC" && (string(crs.CodeString()) == "CRS84" || string(crs.CodeString()) == "CRS84h")
	if !isEPSG4326 && !isCRS84 {
		return fmt.Errorf("FlatGeobuf CRS is organization %q numeric code %d string code %q; this converter requires EPSG:4326 or OGC:CRS84", crs.Org(), crs.Code(), crs.CodeString())
	}
	return nil
}

func decodeFlatFeature(encoded *flat.Feature, header *flat.Header) (geodata.Feature, error) {
	var flatGeometry flat.Geometry
	if encoded.Geometry(&flatGeometry) == nil {
		return geodata.Feature{}, fmt.Errorf("Feature has no geometry")
	}
	geometry, err := flatGeometryToOrb(&flatGeometry, header.GeometryType())
	if err != nil {
		return geodata.Feature{}, err
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
		originalGeometry, err := geodata.OrbGeometry(feature.Geometry)
		if err != nil {
			return geodata.Feature{}, fmt.Errorf("reserved property %q contains invalid geometry: %w", preservedFeatureProperty, err)
		}
		if !orb.Equal(originalGeometry, geometry) {
			return geodata.Feature{}, fmt.Errorf("FlatGeobuf geometry differs from the geometry in reserved property %q", preservedFeatureProperty)
		}
		return feature, nil
	}
	geometryJSON, err := json.Marshal(geojson.NewGeometry(geometry))
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

func flatGeometryToOrb(geometry *flat.Geometry, inheritedType flat.GeometryType) (orb.Geometry, error) {
	geometryType := geometry.Type()
	if geometryType == flat.GeometryTypeUnknown {
		geometryType = inheritedType
	}
	switch geometryType {
	case flat.GeometryTypePoint:
		points, err := flatPoints(geometry)
		if err != nil {
			return nil, err
		}
		if len(points) != 1 {
			return nil, fmt.Errorf("Point contains %d positions; expected 1", len(points))
		}
		return points[0], nil
	case flat.GeometryTypeMultiPoint:
		points, err := flatPoints(geometry)
		return orb.MultiPoint(points), err
	case flat.GeometryTypeLineString:
		points, err := flatPoints(geometry)
		return orb.LineString(points), err
	case flat.GeometryTypeMultiLineString:
		points, err := flatPoints(geometry)
		if err != nil {
			return nil, err
		}
		parts, err := flatPointParts(points, geometry)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 && len(points) > 0 {
			parts = [][]orb.Point{points}
		}
		lines := make(orb.MultiLineString, len(parts))
		for index := range parts {
			lines[index] = orb.LineString(parts[index])
		}
		return lines, nil
	case flat.GeometryTypePolygon:
		points, err := flatPoints(geometry)
		if err != nil {
			return nil, err
		}
		parts, err := flatPointParts(points, geometry)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 && len(points) > 0 {
			parts = [][]orb.Point{points}
		}
		polygon := make(orb.Polygon, len(parts))
		for index := range parts {
			polygon[index] = orb.Ring(parts[index])
		}
		return polygon, nil
	case flat.GeometryTypeMultiPolygon:
		polygons := make(orb.MultiPolygon, 0, geometry.PartsLength())
		for index := 0; index < geometry.PartsLength(); index++ {
			var part flat.Geometry
			if !geometry.Parts(&part, index) {
				return nil, fmt.Errorf("MultiPolygon part %d is missing", index)
			}
			converted, err := flatGeometryToOrb(&part, flat.GeometryTypePolygon)
			if err != nil {
				return nil, fmt.Errorf("MultiPolygon part %d is invalid: %w", index, err)
			}
			polygon, ok := converted.(orb.Polygon)
			if !ok {
				return nil, fmt.Errorf("MultiPolygon part %d has type %T; expected Polygon", index, converted)
			}
			polygons = append(polygons, polygon)
		}
		return polygons, nil
	case flat.GeometryTypeGeometryCollection:
		collection := make(orb.Collection, 0, geometry.PartsLength())
		for index := 0; index < geometry.PartsLength(); index++ {
			var part flat.Geometry
			if !geometry.Parts(&part, index) {
				return nil, fmt.Errorf("GeometryCollection part %d is missing", index)
			}
			converted, err := flatGeometryToOrb(&part, flat.GeometryTypeUnknown)
			if err != nil {
				return nil, fmt.Errorf("GeometryCollection part %d is invalid: %w", index, err)
			}
			collection = append(collection, converted)
		}
		return collection, nil
	default:
		return nil, fmt.Errorf("unsupported FlatGeobuf geometry type %s", geometryType)
	}
}

func flatPoints(geometry *flat.Geometry) ([]orb.Point, error) {
	if geometry.XyLength()%2 != 0 {
		return nil, fmt.Errorf("geometry has %d XY values; expected an even count", geometry.XyLength())
	}
	points := make([]orb.Point, 0, geometry.XyLength()/2)
	for index := 0; index < geometry.XyLength(); index += 2 {
		points = append(points, orb.Point{geometry.Xy(index), geometry.Xy(index + 1)})
	}
	return points, nil
}

func flatPointParts(points []orb.Point, geometry *flat.Geometry) ([][]orb.Point, error) {
	if geometry.EndsLength() == 0 {
		return nil, nil
	}
	parts := make([][]orb.Point, 0, geometry.EndsLength())
	start := 0
	for index := 0; index < geometry.EndsLength(); index++ {
		end := int(geometry.Ends(index))
		if end <= start || end > len(points) {
			return nil, fmt.Errorf("geometry end %d is %d; expected a value above %d and at most %d", index, end, start, len(points))
		}
		parts = append(parts, points[start:end])
		start = end
	}
	if start != len(points) {
		return nil, fmt.Errorf("geometry ends at position %d but contains %d positions", start, len(points))
	}
	return parts, nil
}
