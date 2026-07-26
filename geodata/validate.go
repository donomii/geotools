package geodata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
)

type ValidationOptions struct {
	AllowNullGeometry bool
	AllowOutOfRange   bool
}

type GeometrySummary struct {
	Type                string
	Bounds              [4]float64
	HasBounds           bool
	PositionCount       int64
	CoordinateDimension int
	mixedDimensions     bool
	minimums            []float64
	maximums            []float64
}

type geometryObject struct {
	Type        string            `json:"type"`
	Coordinates json.RawMessage   `json:"coordinates"`
	Geometries  []json.RawMessage `json:"geometries"`
}

func ValidateFeature(feature Feature, options ValidationOptions) (GeometrySummary, error) {
	if feature.Type != "Feature" {
		return GeometrySummary{}, fmt.Errorf("object type is %q; expected Feature", feature.Type)
	}
	if feature.ID != nil && !validFeatureID(feature.ID) {
		return GeometrySummary{}, fmt.Errorf("Feature id %s is not a string or number", feature.ID)
	}
	if feature.Properties == nil {
		return GeometrySummary{}, fmt.Errorf("Feature %s is missing properties", feature.EncodedID())
	}
	if !bytes.Equal(bytes.TrimSpace(feature.Properties), []byte("null")) {
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(feature.Properties, &properties); err != nil {
			return GeometrySummary{}, fmt.Errorf("Feature %s properties must be an object or null: %w", feature.EncodedID(), err)
		}
	}
	bboxDimension, bboxValues, err := validateBBox(feature.BBox)
	if err != nil {
		return GeometrySummary{}, fmt.Errorf("Feature %s bbox is invalid: %w", feature.EncodedID(), err)
	}
	if feature.Geometry == nil {
		return GeometrySummary{}, fmt.Errorf("Feature %s is missing geometry", feature.EncodedID())
	}
	if bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
		if options.AllowNullGeometry {
			return GeometrySummary{Type: "null"}, nil
		}
		return GeometrySummary{}, fmt.Errorf("Feature %s has null geometry; enable null geometry only when the consumer supports it", feature.EncodedID())
	}
	summary, err := inspectGeometry(feature.Geometry, options, "geometry")
	if err != nil {
		return GeometrySummary{}, fmt.Errorf("Feature %s has invalid geometry: %w", feature.EncodedID(), err)
	}
	if summary.mixedDimensions {
		return GeometrySummary{}, fmt.Errorf("Feature %s geometry mixes positions with different coordinate dimensions", feature.EncodedID())
	}
	if bboxDimension != 0 && bboxDimension != summary.CoordinateDimension {
		return GeometrySummary{}, fmt.Errorf("Feature %s bbox describes %d dimensions but geometry positions contain %d coordinates", feature.EncodedID(), bboxDimension, summary.CoordinateDimension)
	}
	for dimension := 0; dimension < bboxDimension; dimension++ {
		if dimension == 0 && bboxValues[0] > bboxValues[bboxDimension] {
			continue
		}
		if bboxValues[dimension] > summary.minimums[dimension] || bboxValues[bboxDimension+dimension] < summary.maximums[dimension] {
			return GeometrySummary{}, fmt.Errorf("Feature %s bbox range %v..%v for dimension %d does not contain geometry range %v..%v",
				feature.EncodedID(), bboxValues[dimension], bboxValues[bboxDimension+dimension], dimension+1,
				summary.minimums[dimension], summary.maximums[dimension])
		}
	}
	return summary, nil
}

func validFeatureID(raw json.RawMessage) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return true
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return false
	}
	var trailing json.RawMessage
	return decoder.Decode(&trailing) == io.EOF
}

func validateBBox(raw json.RawMessage) (int, []float64, error) {
	if raw == nil {
		return 0, nil, nil
	}
	var values []float64
	if err := json.Unmarshal(raw, &values); err != nil {
		return 0, nil, fmt.Errorf("expected an array of numbers: %w", err)
	}
	if len(values) < 4 || len(values)%2 != 0 {
		return 0, nil, fmt.Errorf("contains %d values; expected two values for each of at least two coordinate dimensions", len(values))
	}
	for index, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, nil, fmt.Errorf("value %d is not finite: %v", index, value)
		}
	}
	dimension := len(values) / 2
	for index := 1; index < dimension; index++ {
		if values[index] > values[dimension+index] {
			return 0, nil, fmt.Errorf("minimum dimension %d value %v exceeds maximum %v", index+1, values[index], values[dimension+index])
		}
	}
	return dimension, values, nil
}

func inspectGeometry(raw json.RawMessage, options ValidationOptions, path string) (GeometrySummary, error) {
	var geometry geometryObject
	if err := json.Unmarshal(raw, &geometry); err != nil {
		return GeometrySummary{}, fmt.Errorf("%s is not an object: %w", path, err)
	}
	if geometry.Type == "" {
		return GeometrySummary{}, fmt.Errorf("%s is missing type", path)
	}
	if geometry.Type == "GeometryCollection" {
		if geometry.Geometries == nil {
			return GeometrySummary{}, fmt.Errorf("%s is missing geometries", path)
		}
		summary := GeometrySummary{Type: geometry.Type}
		for index, child := range geometry.Geometries {
			childSummary, err := inspectGeometry(child, options, fmt.Sprintf("%s.geometries[%d]", path, index))
			if err != nil {
				return GeometrySummary{}, err
			}
			mergeGeometrySummary(&summary, childSummary)
		}
		return summary, nil
	}
	if geometry.Coordinates == nil {
		return GeometrySummary{}, fmt.Errorf("%s %s is missing coordinates", path, geometry.Type)
	}
	summary := GeometrySummary{Type: geometry.Type}
	switch geometry.Type {
	case "Point":
		position, err := decodePosition(geometry.Coordinates, path+".coordinates", options)
		if err != nil {
			return GeometrySummary{}, err
		}
		addPosition(&summary, position)
	case "MultiPoint":
		if err := inspectPositionArray(geometry.Coordinates, path+".coordinates", 1, 0, options, &summary); err != nil {
			return GeometrySummary{}, err
		}
	case "LineString":
		if err := inspectPositionArray(geometry.Coordinates, path+".coordinates", 1, 2, options, &summary); err != nil {
			return GeometrySummary{}, err
		}
	case "MultiLineString":
		if err := inspectPositionArray(geometry.Coordinates, path+".coordinates", 2, 2, options, &summary); err != nil {
			return GeometrySummary{}, err
		}
	case "Polygon":
		if err := inspectPolygon(geometry.Coordinates, path+".coordinates", options, &summary); err != nil {
			return GeometrySummary{}, err
		}
	case "MultiPolygon":
		var polygons []json.RawMessage
		if err := json.Unmarshal(geometry.Coordinates, &polygons); err != nil {
			return GeometrySummary{}, fmt.Errorf("%s must be an array of Polygons: %w", path+".coordinates", err)
		}
		for index, polygon := range polygons {
			if err := inspectPolygon(polygon, fmt.Sprintf("%s.coordinates[%d]", path, index), options, &summary); err != nil {
				return GeometrySummary{}, err
			}
		}
	default:
		return GeometrySummary{}, fmt.Errorf("%s has unsupported geometry type %q", path, geometry.Type)
	}
	return summary, nil
}

func inspectPositionArray(raw json.RawMessage, path string, depth, minimumLength int, options ValidationOptions, summary *GeometrySummary) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("%s must be an array: %w", path, err)
	}
	if minimumLength > 0 && len(values) < minimumLength {
		return fmt.Errorf("%s contains %d positions; expected at least %d", path, len(values), minimumLength)
	}
	for index, value := range values {
		childPath := fmt.Sprintf("%s[%d]", path, index)
		if depth == 1 {
			position, err := decodePosition(value, childPath, options)
			if err != nil {
				return err
			}
			addPosition(summary, position)
		} else if err := inspectPositionArray(value, childPath, depth-1, minimumLength, options, summary); err != nil {
			return err
		}
	}
	return nil
}

func inspectPolygon(raw json.RawMessage, path string, options ValidationOptions, summary *GeometrySummary) error {
	var rings []json.RawMessage
	if err := json.Unmarshal(raw, &rings); err != nil {
		return fmt.Errorf("%s must be an array of linear rings: %w", path, err)
	}
	if len(rings) == 0 {
		return fmt.Errorf("%s contains no linear rings", path)
	}
	for ringIndex, ringRaw := range rings {
		var positions []json.RawMessage
		ringPath := fmt.Sprintf("%s[%d]", path, ringIndex)
		if err := json.Unmarshal(ringRaw, &positions); err != nil {
			return fmt.Errorf("%s must be an array of positions: %w", ringPath, err)
		}
		if len(positions) < 4 {
			return fmt.Errorf("%s contains %d positions; expected at least 4", ringPath, len(positions))
		}
		var first, last []float64
		for positionIndex, positionRaw := range positions {
			position, err := decodePosition(positionRaw, fmt.Sprintf("%s[%d]", ringPath, positionIndex), options)
			if err != nil {
				return err
			}
			if positionIndex == 0 {
				first = position
			}
			last = position
			addPosition(summary, position)
		}
		if !positionsEqual(first, last) {
			return fmt.Errorf("%s is not closed; first position %v differs from last position %v", ringPath, first, last)
		}
	}
	return nil
}

func decodePosition(raw json.RawMessage, path string, options ValidationOptions) ([]float64, error) {
	var position []float64
	if err := json.Unmarshal(raw, &position); err != nil {
		return nil, fmt.Errorf("%s must be an array of numbers: %w", path, err)
	}
	if len(position) < 2 {
		return nil, fmt.Errorf("%s contains %d numbers; expected at least longitude and latitude", path, len(position))
	}
	for index, value := range position {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("%s[%d] is not finite: %v", path, index, value)
		}
	}
	if !options.AllowOutOfRange {
		if position[0] < -180 || position[0] > 180 {
			return nil, fmt.Errorf("%s longitude is %v; expected -180 through 180", path, position[0])
		}
		if position[1] < -90 || position[1] > 90 {
			return nil, fmt.Errorf("%s latitude is %v; expected -90 through 90", path, position[1])
		}
	}
	return position, nil
}

func positionsEqual(first, second []float64) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func addPosition(summary *GeometrySummary, position []float64) {
	if summary.CoordinateDimension == 0 {
		summary.CoordinateDimension = len(position)
		summary.minimums = append([]float64(nil), position...)
		summary.maximums = append([]float64(nil), position...)
	} else if summary.CoordinateDimension != len(position) {
		summary.mixedDimensions = true
	} else {
		for dimension, value := range position {
			summary.minimums[dimension] = math.Min(summary.minimums[dimension], value)
			summary.maximums[dimension] = math.Max(summary.maximums[dimension], value)
		}
	}
	longitude := position[0]
	latitude := position[1]
	if !summary.HasBounds {
		summary.Bounds = [4]float64{longitude, latitude, longitude, latitude}
		summary.HasBounds = true
	} else {
		summary.Bounds[0] = math.Min(summary.Bounds[0], longitude)
		summary.Bounds[1] = math.Min(summary.Bounds[1], latitude)
		summary.Bounds[2] = math.Max(summary.Bounds[2], longitude)
		summary.Bounds[3] = math.Max(summary.Bounds[3], latitude)
	}
	summary.PositionCount++
}

func mergeGeometrySummary(target *GeometrySummary, source GeometrySummary) {
	target.PositionCount += source.PositionCount
	if target.CoordinateDimension == 0 {
		target.CoordinateDimension = source.CoordinateDimension
		target.minimums = append([]float64(nil), source.minimums...)
		target.maximums = append([]float64(nil), source.maximums...)
	} else if source.CoordinateDimension != 0 && target.CoordinateDimension != source.CoordinateDimension {
		target.mixedDimensions = true
	} else if source.CoordinateDimension != 0 {
		for dimension := range source.minimums {
			target.minimums[dimension] = math.Min(target.minimums[dimension], source.minimums[dimension])
			target.maximums[dimension] = math.Max(target.maximums[dimension], source.maximums[dimension])
		}
	}
	target.mixedDimensions = target.mixedDimensions || source.mixedDimensions
	if !source.HasBounds {
		return
	}
	if !target.HasBounds {
		target.Bounds = source.Bounds
		target.HasBounds = true
		return
	}
	target.Bounds[0] = math.Min(target.Bounds[0], source.Bounds[0])
	target.Bounds[1] = math.Min(target.Bounds[1], source.Bounds[1])
	target.Bounds[2] = math.Max(target.Bounds[2], source.Bounds[2])
	target.Bounds[3] = math.Max(target.Bounds[3], source.Bounds[3])
}

func GeometryType(raw json.RawMessage) (string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "null", nil
	}
	var geometry geometryObject
	if err := json.Unmarshal(raw, &geometry); err != nil {
		return "", err
	}
	if geometry.Type == "" {
		return "", fmt.Errorf("geometry is missing type")
	}
	return geometry.Type, nil
}

func GeometryBounds(raw json.RawMessage, allowOutOfRange bool) ([4]float64, bool, error) {
	summary, err := inspectGeometry(raw, ValidationOptions{AllowOutOfRange: allowOutOfRange}, "geometry")
	return summary.Bounds, summary.HasBounds, err
}
