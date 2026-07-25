package geodata

import (
	"encoding/json"
	"fmt"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
)

func OrbFeature(feature Feature) (*geojson.Feature, error) {
	encoded, err := json.Marshal(feature)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Feature %s for geometry conversion: %w", feature.EncodedID(), err)
	}
	converted, err := geojson.UnmarshalFeature(encoded)
	if err != nil {
		return nil, fmt.Errorf("failed to convert Feature %s geometry: %w", feature.EncodedID(), err)
	}
	return converted, nil
}

func FeatureFromOrb(feature *geojson.Feature) (Feature, error) {
	encoded, err := json.Marshal(feature)
	if err != nil {
		return Feature{}, fmt.Errorf("failed to encode converted geometry: %w", err)
	}
	var converted Feature
	if err := json.Unmarshal(encoded, &converted); err != nil {
		return Feature{}, fmt.Errorf("failed to decode converted Feature: %w", err)
	}
	return converted, nil
}

func OrbGeometry(raw json.RawMessage) (orb.Geometry, error) {
	geometry, err := geojson.UnmarshalGeometry(raw)
	if err != nil {
		return nil, err
	}
	return geometry.Geometry(), nil
}
