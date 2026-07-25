package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/donomii/geotools/geodata"
	"github.com/parquet-go/parquet-go"
	"github.com/twpayne/go-geom"
	geomwkb "github.com/twpayne/go-geom/encoding/wkb"
)

func decodeGeoParquetGeometry(values [][]parquet.Value, paths [][]string, primaryColumn, encoding string) (geom.T, error) {
	if encoding == "WKB" {
		for index, path := range paths {
			if len(path) == 1 && path[0] == primaryColumn {
				geometryBytes, err := singleParquetBytes(values[index], primaryColumn)
				if err != nil {
					return nil, err
				}
				if geometryBytes == nil {
					return nil, nil
				}
				geometry, err := geomwkb.Unmarshal(geometryBytes)
				if err != nil {
					return nil, fmt.Errorf("primary geometry contains invalid WKB: %w", err)
				}
				if geometry.Stride() != 2 && geometry.Stride() != 3 {
					return nil, fmt.Errorf("primary geometry has %d coordinate dimensions; GeoParquet supports 2D or 3D geometries", geometry.Stride())
				}
				return geometry, nil
			}
		}
		return nil, fmt.Errorf("WKB primary geometry column %q is absent", primaryColumn)
	}
	axes := make(map[string][]parquet.Value)
	for index, path := range paths {
		if len(path) > 1 && path[0] == primaryColumn {
			name := path[len(path)-1]
			if name == "x" || name == "y" || name == "z" {
				axes[name] = nonNullParquetValues(values[index])
			}
		}
	}
	if len(axes["x"]) == 0 && len(axes["y"]) == 0 {
		return nil, nil
	}
	if len(axes["x"]) == 0 || len(axes["x"]) != len(axes["y"]) {
		return nil, fmt.Errorf("native %s geometry has %d x values and %d y values", encoding, len(axes["x"]), len(axes["y"]))
	}
	dimension := 2
	if len(axes["z"]) > 0 {
		if len(axes["z"]) != len(axes["x"]) {
			return nil, fmt.Errorf("native %s geometry has %d x values and %d z values", encoding, len(axes["x"]), len(axes["z"]))
		}
		dimension = 3
	}
	layout := geom.XY
	if dimension == 3 {
		layout = geom.XYZ
	}
	flat := make([]float64, 0, len(axes["x"])*dimension)
	repetitions := make([]int, len(axes["x"]))
	for index := range axes["x"] {
		x, err := geoParquetCoordinateValue(axes["x"][index])
		if err != nil {
			return nil, fmt.Errorf("native geometry x coordinate %d is invalid: %w", index, err)
		}
		y, err := geoParquetCoordinateValue(axes["y"][index])
		if err != nil {
			return nil, fmt.Errorf("native geometry y coordinate %d is invalid: %w", index, err)
		}
		flat = append(flat, x, y)
		if dimension == 3 {
			z, err := geoParquetCoordinateValue(axes["z"][index])
			if err != nil {
				return nil, fmt.Errorf("native geometry z coordinate %d is invalid: %w", index, err)
			}
			flat = append(flat, z)
		}
		repetitions[index] = axes["x"][index].RepetitionLevel()
	}
	return buildNativeGeoParquetGeometry(encoding, layout, flat, repetitions)
}

func nonNullParquetValues(values []parquet.Value) []parquet.Value {
	result := make([]parquet.Value, 0, len(values))
	for _, value := range values {
		if !value.IsNull() {
			result = append(result, value)
		}
	}
	return result
}

func geoParquetCoordinateValue(value parquet.Value) (float64, error) {
	switch value.Kind() {
	case parquet.Double:
		return value.Double(), nil
	case parquet.Float:
		return float64(value.Float()), nil
	default:
		return 0, fmt.Errorf("physical type is %s; expected DOUBLE or FLOAT", value.Kind())
	}
}

func buildNativeGeoParquetGeometry(encoding string, layout geom.Layout, flat []float64, repetitions []int) (geom.T, error) {
	stride := layout.Stride()
	switch encoding {
	case "point":
		if len(flat) != stride {
			return nil, fmt.Errorf("native point contains %d coordinates; expected %d", len(flat), stride)
		}
		return geom.NewPointFlat(layout, flat), nil
	case "linestring":
		return geom.NewLineStringFlat(layout, flat), nil
	case "multipoint":
		return geom.NewMultiPointFlat(layout, flat), nil
	case "polygon":
		return geom.NewPolygonFlat(layout, flat, nativeGeoParquetEnds(repetitions, stride, 1)), nil
	case "multilinestring":
		return geom.NewMultiLineStringFlat(layout, flat, nativeGeoParquetEnds(repetitions, stride, 1)), nil
	case "multipolygon":
		return geom.NewMultiPolygonFlat(layout, flat, nativeGeoParquetEndss(repetitions, stride)), nil
	default:
		return nil, fmt.Errorf("native GeoParquet encoding %q is unsupported", encoding)
	}
}

func nativeGeoParquetEnds(repetitions []int, stride, newPartLevel int) []int {
	var ends []int
	for index := 1; index < len(repetitions); index++ {
		if repetitions[index] <= newPartLevel {
			ends = append(ends, index*stride)
		}
	}
	return append(ends, len(repetitions)*stride)
}

func nativeGeoParquetEndss(repetitions []int, stride int) [][]int {
	var result [][]int
	var polygonEnds []int
	for index := 1; index < len(repetitions); index++ {
		if repetitions[index] <= 1 {
			polygonEnds = append(polygonEnds, index*stride)
			result = append(result, polygonEnds)
			polygonEnds = nil
		} else if repetitions[index] <= 2 {
			polygonEnds = append(polygonEnds, index*stride)
		}
	}
	polygonEnds = append(polygonEnds, len(repetitions)*stride)
	return append(result, polygonEnds)
}

func decodeGeotoolsGeoParquetFeature(values [][]parquet.Value, paths [][]string, schema *parquet.Schema, geometry json.RawMessage, secondaryGeometries map[string]json.RawMessage, metadata geoParquetToolMetadata) (geodata.Feature, error) {
	feature := geodata.Feature{Type: "Feature", Geometry: append(json.RawMessage(nil), geometry...)}
	if raw, err := geoParquetRootColumnJSON(values, paths, schema, metadata.IDColumn); err != nil {
		return geodata.Feature{}, err
	} else {
		feature.ID = raw
	}
	if raw, err := geoParquetRootColumnJSON(values, paths, schema, metadata.FeatureBBoxColumn); err != nil {
		return geodata.Feature{}, err
	} else {
		feature.BBox = raw
	}
	if raw, err := geoParquetRootColumnJSON(values, paths, schema, metadata.ForeignColumn); err != nil {
		return geodata.Feature{}, err
	} else if raw != nil {
		if err := json.Unmarshal(raw, &feature.Foreign); err != nil {
			return geodata.Feature{}, fmt.Errorf("column %q is not a JSON object: %w", metadata.ForeignColumn, err)
		}
	}
	if feature.Foreign == nil {
		feature.Foreign = make(map[string]json.RawMessage)
	}
	properties := make(map[string]json.RawMessage)
	if metadata.PropertiesColumn != "" {
		raw, err := geoParquetRootColumnJSON(values, paths, schema, metadata.PropertiesColumn)
		if err != nil {
			return geodata.Feature{}, err
		}
		if raw == nil {
			return geodata.Feature{}, fmt.Errorf("column %q is null; expected a JSON object or JSON null", metadata.PropertiesColumn)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if len(secondaryGeometries) != 0 {
				return geodata.Feature{}, fmt.Errorf("secondary geometry columns cannot be restored because column %q contains null properties", metadata.PropertiesColumn)
			}
			feature.Properties = json.RawMessage("null")
		} else {
			if err := json.Unmarshal(raw, &properties); err != nil {
				return geodata.Feature{}, fmt.Errorf("column %q is not a JSON object: %w", metadata.PropertiesColumn, err)
			}
			for name, geometry := range secondaryGeometries {
				if _, exists := properties[name]; exists {
					return geodata.Feature{}, fmt.Errorf("secondary geometry column %q conflicts with the JSON properties column", name)
				}
				properties[name] = geometry
			}
			feature.Properties, err = json.Marshal(properties)
			if err != nil {
				return geodata.Feature{}, err
			}
		}
	} else if propertiesNull, err := geoParquetRootColumnBool(values, paths, metadata.PropertiesNullColumn); err != nil {
		return geodata.Feature{}, err
	} else if !propertiesNull {
		for propertyName, columnName := range metadata.PropertyColumns {
			raw, err := geoParquetRootColumnJSON(values, paths, schema, columnName)
			if err != nil {
				return geodata.Feature{}, err
			}
			if raw != nil {
				properties[propertyName] = raw
			}
		}
		nulls, err := geoParquetRootColumnJSON(values, paths, schema, metadata.NullPropertiesColumn)
		if err != nil {
			return geodata.Feature{}, err
		}
		if nulls != nil {
			var names []string
			if err := json.Unmarshal(nulls, &names); err != nil {
				return geodata.Feature{}, fmt.Errorf("column %q is not an array of property names: %w", metadata.NullPropertiesColumn, err)
			}
			for _, name := range names {
				properties[name] = json.RawMessage("null")
			}
		}
		for name, geometry := range secondaryGeometries {
			if _, exists := properties[name]; exists {
				return geodata.Feature{}, fmt.Errorf("secondary geometry column %q conflicts with a regular property column", name)
			}
			properties[name] = geometry
		}
		feature.Properties, err = json.Marshal(properties)
		if err != nil {
			return geodata.Feature{}, err
		}
	} else {
		if len(secondaryGeometries) != 0 {
			return geodata.Feature{}, fmt.Errorf("secondary geometry columns cannot be restored because geotools metadata marks Feature properties as null")
		}
		feature.Properties = json.RawMessage("null")
	}
	if _, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{AllowNullGeometry: true}); err != nil {
		return geodata.Feature{}, err
	}
	return feature, nil
}

func geoParquetRootColumnJSON(values [][]parquet.Value, paths [][]string, schema *parquet.Schema, name string) (json.RawMessage, error) {
	if name == "" {
		return nil, nil
	}
	for index, path := range paths {
		if len(path) != 1 || path[0] != name {
			continue
		}
		nonNull := nonNullParquetValues(values[index])
		if len(nonNull) == 0 {
			return nil, nil
		}
		leaf, exists := schema.Lookup(path...)
		if !exists {
			return nil, fmt.Errorf("column %q cannot be resolved", name)
		}
		return parquetValuesJSON(nonNull, leaf.Node)
	}
	return nil, fmt.Errorf("column %q declared in geotools metadata is absent", name)
}

func geoParquetRootColumnBool(values [][]parquet.Value, paths [][]string, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	for index, path := range paths {
		if len(path) != 1 || path[0] != name {
			continue
		}
		nonNull := nonNullParquetValues(values[index])
		if len(nonNull) == 0 {
			return false, nil
		}
		if len(nonNull) != 1 || nonNull[0].Kind() != parquet.Boolean {
			return false, fmt.Errorf("column %q must contain one boolean value", name)
		}
		return nonNull[0].Boolean(), nil
	}
	return false, fmt.Errorf("column %q declared in geotools metadata is absent", name)
}

func geoParquetCoveringPath(metadata geoParquetColumn, path []string) bool {
	if metadata.Covering == nil {
		return false
	}
	for _, coveringPath := range metadata.Covering.BBox {
		if reflect.DeepEqual(path, coveringPath) {
			return true
		}
	}
	return false
}

func geoParquetGeometryMetadataPath(metadata geoParquetMetadata, path []string) bool {
	for name, column := range metadata.Columns {
		if path[0] == name || geoParquetCoveringPath(column, path) {
			return true
		}
	}
	return false
}

func insertNestedGeoParquetProperty(properties map[string]json.RawMessage, path []string, raw json.RawMessage) error {
	cleaned := make([]string, 0, len(path))
	for _, segment := range path {
		if segment != "list" && segment != "element" && segment != "key_value" {
			cleaned = append(cleaned, segment)
		}
	}
	if len(cleaned) == 0 {
		return fmt.Errorf("column path is empty")
	}
	if len(cleaned) == 1 {
		properties[cleaned[0]] = raw
		return nil
	}
	var root map[string]json.RawMessage
	if existing := properties[cleaned[0]]; existing != nil {
		if err := json.Unmarshal(existing, &root); err != nil {
			return fmt.Errorf("column conflicts with scalar property %q", cleaned[0])
		}
	} else {
		root = make(map[string]json.RawMessage)
	}
	if err := insertNestedGeoParquetObject(root, cleaned[1:], raw); err != nil {
		return err
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return err
	}
	properties[cleaned[0]] = encoded
	return nil
}

func insertNestedGeoParquetObject(object map[string]json.RawMessage, path []string, raw json.RawMessage) error {
	if len(path) == 1 {
		object[path[0]] = raw
		return nil
	}
	var child map[string]json.RawMessage
	if existing := object[path[0]]; existing != nil {
		if err := json.Unmarshal(existing, &child); err != nil {
			return fmt.Errorf("nested column %q conflicts with a scalar value", path[0])
		}
	} else {
		child = make(map[string]json.RawMessage)
	}
	if err := insertNestedGeoParquetObject(child, path[1:], raw); err != nil {
		return err
	}
	encoded, err := json.Marshal(child)
	if err != nil {
		return err
	}
	object[path[0]] = encoded
	return nil
}
