package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/donomii/geotools/geodata"
)

func jsonFGMeasuresEnabled(raw json.RawMessage) (bool, error) {
	if raw == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, nil
	}
	if err := validateJSONFGMeasures(raw); err != nil {
		return false, err
	}
	var measures struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &measures); err != nil {
		return false, err
	}
	return measures.Enabled, nil
}

func jsonFGCRSDimension(crs string) int {
	if crs == geodata.CRSCRS84h {
		return 3
	}
	return 2
}

func encodeJSONFGMeasuredPlace(raw json.RawMessage, targetCRS string) (json.RawMessage, error) {
	spatialDimension := jsonFGCRSDimension(targetCRS)
	spatialGeometry, measures, err := splitJSONFGMeasureGeometry(raw, spatialDimension)
	if err != nil {
		return nil, err
	}
	geometry, err := geodata.DecodeGeomJSON(spatialGeometry)
	if err != nil {
		return nil, err
	}
	sourceCRS := geodata.CRSCRS84
	if spatialDimension == 3 {
		sourceCRS = geodata.CRSCRS84h
	}
	if _, err := geodata.TransformJSONFGGeometry(geometry, sourceCRS, targetCRS); err != nil {
		return nil, fmt.Errorf("measured place reprojection from %s to %s failed: %w", sourceCRS, targetCRS, err)
	}
	encoded, err := geodata.EncodeGeomJSON(geometry)
	if err != nil {
		return nil, err
	}
	return attachJSONFGMeasures(encoded, measures)
}

func splitJSONFGMeasureGeometry(raw json.RawMessage, spatialDimension int) (json.RawMessage, []json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, nil, fmt.Errorf("measured geometry is not an object: %w", err)
	}
	var measures []json.RawMessage
	var err error
	if members["type"] == nil {
		return nil, nil, fmt.Errorf("measured geometry has no type")
	}
	var geometryType string
	if err := json.Unmarshal(members["type"], &geometryType); err != nil {
		return nil, nil, fmt.Errorf("measured geometry type is invalid: %w", err)
	}
	if geometryType == "GeometryCollection" {
		var geometries []json.RawMessage
		if err := json.Unmarshal(members["geometries"], &geometries); err != nil {
			return nil, nil, fmt.Errorf("measured GeometryCollection geometries are invalid: %w", err)
		}
		for index, child := range geometries {
			var childMeasures []json.RawMessage
			geometries[index], childMeasures, err = splitJSONFGMeasureGeometry(child, spatialDimension)
			if err != nil {
				return nil, nil, fmt.Errorf("GeometryCollection geometry %d: %w", index, err)
			}
			measures = append(measures, childMeasures...)
		}
		members["geometries"], err = json.Marshal(geometries)
		if err != nil {
			return nil, nil, err
		}
	} else {
		if members["coordinates"] == nil {
			return nil, nil, fmt.Errorf("measured %s geometry has no coordinates", geometryType)
		}
		members["coordinates"], measures, err = splitJSONFGCoordinateTree(members["coordinates"], spatialDimension, "coordinates")
		if err != nil {
			return nil, nil, err
		}
	}
	delete(members, "coordRefSys")
	delete(members, "measures")
	encoded, err := json.Marshal(members)
	return encoded, measures, err
}

func splitJSONFGCoordinateTree(raw json.RawMessage, spatialDimension int, path string) (json.RawMessage, []json.RawMessage, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, nil, fmt.Errorf("%s is not an array: %w", path, err)
	}
	if len(values) == 0 {
		return json.RawMessage("[]"), nil, nil
	}
	if isJSONArray(values[0]) {
		var measures []json.RawMessage
		for index, child := range values {
			if !isJSONArray(child) {
				return nil, nil, fmt.Errorf("%s mixes coordinate arrays and numbers", path)
			}
			var childMeasures []json.RawMessage
			var err error
			values[index], childMeasures, err = splitJSONFGCoordinateTree(child, spatialDimension, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, nil, err
			}
			measures = append(measures, childMeasures...)
		}
		encoded, err := json.Marshal(values)
		return encoded, measures, err
	}
	if len(values) != spatialDimension+1 {
		return nil, nil, fmt.Errorf("%s contains %d coordinates; expected %d spatial coordinates followed by one measure (%d total)", path, len(values), spatialDimension, spatialDimension+1)
	}
	for index, value := range values {
		if err := validateJSONFGNumber(value); err != nil {
			return nil, nil, fmt.Errorf("%s[%d] is invalid: %w", path, index, err)
		}
	}
	encoded, err := json.Marshal(values[:spatialDimension])
	return encoded, []json.RawMessage{append(json.RawMessage(nil), values[spatialDimension]...)}, err
}

func attachJSONFGMeasures(raw json.RawMessage, measures []json.RawMessage) (json.RawMessage, error) {
	measureIndex := 0
	encoded, err := attachJSONFGGeometryMeasures(raw, measures, &measureIndex)
	if err != nil {
		return nil, err
	}
	if measureIndex != len(measures) {
		return nil, fmt.Errorf("measured geometry has %d positions but %d measure values", measureIndex, len(measures))
	}
	return encoded, nil
}

func attachJSONFGGeometryMeasures(raw json.RawMessage, measures []json.RawMessage, measureIndex *int) (json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, fmt.Errorf("geometry is not an object: %w", err)
	}
	var geometryType string
	if err := json.Unmarshal(members["type"], &geometryType); err != nil {
		return nil, fmt.Errorf("geometry type is invalid: %w", err)
	}
	var err error
	if geometryType == "GeometryCollection" {
		var geometries []json.RawMessage
		if err := json.Unmarshal(members["geometries"], &geometries); err != nil {
			return nil, fmt.Errorf("GeometryCollection geometries are invalid: %w", err)
		}
		for index, child := range geometries {
			geometries[index], err = attachJSONFGGeometryMeasures(child, measures, measureIndex)
			if err != nil {
				return nil, fmt.Errorf("GeometryCollection geometry %d: %w", index, err)
			}
		}
		members["geometries"], err = json.Marshal(geometries)
	} else {
		members["coordinates"], err = attachJSONFGCoordinateTree(members["coordinates"], measures, measureIndex, "coordinates")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(members)
}

func attachJSONFGCoordinateTree(raw json.RawMessage, measures []json.RawMessage, measureIndex *int, path string) (json.RawMessage, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("%s is not an array: %w", path, err)
	}
	if len(values) == 0 {
		return json.RawMessage("[]"), nil
	}
	if isJSONArray(values[0]) {
		for index, child := range values {
			if !isJSONArray(child) {
				return nil, fmt.Errorf("%s mixes coordinate arrays and numbers", path)
			}
			var err error
			values[index], err = attachJSONFGCoordinateTree(child, measures, measureIndex, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, err
			}
		}
		return json.Marshal(values)
	}
	if *measureIndex >= len(measures) {
		return nil, fmt.Errorf("%s has no corresponding measure value", path)
	}
	for index, value := range values {
		if err := validateJSONFGNumber(value); err != nil {
			return nil, fmt.Errorf("%s[%d] is invalid: %w", path, index, err)
		}
	}
	values = append(values, measures[*measureIndex])
	(*measureIndex)++
	return json.Marshal(values)
}

func validateJSONFGGeometryDimensions(raw json.RawMessage, expectedDimension int, outerGeometry bool) error {
	dimension := 0
	if err := inspectJSONFGGeometryDimension(raw, outerGeometry, &dimension); err != nil {
		return err
	}
	if dimension != 0 && dimension != expectedDimension {
		return fmt.Errorf("positions contain %d coordinates; expected %d", dimension, expectedDimension)
	}
	return nil
}

func validateJSONFGMeasuredGeoJSONGeometry(raw json.RawMessage) error {
	dimension := 0
	if err := inspectJSONFGGeometryDimension(raw, true, &dimension); err != nil {
		return err
	}
	if dimension != 0 && dimension != 3 && dimension != 4 {
		return fmt.Errorf("positions contain %d coordinates; expected three for a 2D CRS plus measure or four for a 3D CRS plus measure", dimension)
	}
	return nil
}

func inspectJSONFGGeometryDimension(raw json.RawMessage, outerGeometry bool, dimension *int) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return fmt.Errorf("geometry is not an object: %w", err)
	}
	if !outerGeometry && members["measures"] != nil {
		return fmt.Errorf("geometry embedded in another geometry includes a measures member")
	}
	var geometryType string
	if err := json.Unmarshal(members["type"], &geometryType); err != nil {
		return fmt.Errorf("geometry type is invalid: %w", err)
	}
	if geometryType == "GeometryCollection" {
		var geometries []json.RawMessage
		if err := json.Unmarshal(members["geometries"], &geometries); err != nil {
			return fmt.Errorf("GeometryCollection geometries are invalid: %w", err)
		}
		for index, child := range geometries {
			if err := inspectJSONFGGeometryDimension(child, false, dimension); err != nil {
				return fmt.Errorf("GeometryCollection geometry %d: %w", index, err)
			}
		}
		return nil
	}
	if members["coordinates"] == nil {
		return fmt.Errorf("%s geometry has no coordinates", geometryType)
	}
	return inspectJSONFGCoordinateDimension(members["coordinates"], "coordinates", dimension)
}

func inspectJSONFGCoordinateDimension(raw json.RawMessage, path string, dimension *int) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("%s is not an array: %w", path, err)
	}
	if len(values) == 0 {
		return nil
	}
	if isJSONArray(values[0]) {
		for index, child := range values {
			if !isJSONArray(child) {
				return fmt.Errorf("%s mixes coordinate arrays and numbers", path)
			}
			if err := inspectJSONFGCoordinateDimension(child, fmt.Sprintf("%s[%d]", path, index), dimension); err != nil {
				return err
			}
		}
		return nil
	}
	for index, value := range values {
		if err := validateJSONFGNumber(value); err != nil {
			return fmt.Errorf("%s[%d] is invalid: %w", path, index, err)
		}
	}
	if *dimension == 0 {
		*dimension = len(values)
	} else if *dimension != len(values) {
		return fmt.Errorf("%s contains %d coordinates; earlier positions contain %d", path, len(values), *dimension)
	}
	return nil
}

func validateJSONFGNumber(raw json.RawMessage) error {
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s is not a number", raw)
	}
	return nil
}

func isJSONArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}
