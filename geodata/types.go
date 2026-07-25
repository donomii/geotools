package geodata

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const DefaultMVTIDProperty = "__geotools_geojson_id"

type Feature struct {
	Type       string
	ID         json.RawMessage
	BBox       json.RawMessage
	Geometry   json.RawMessage
	Properties json.RawMessage
	Foreign    map[string]json.RawMessage
}

type MVTEncodeSettings struct {
	Zoom              uint
	X                 uint
	Y                 uint
	Layer             string
	Extent            uint
	Buffer            uint
	Simplify          float64
	Gzip              bool
	LayerProperty     string
	DropLayerProperty bool
	IDProperty        string
}

type MVTDecodeSettings struct {
	Zoom          uint
	X             uint
	Y             uint
	Layer         string
	Gzip          bool
	AllLayers     bool
	LayerProperty string
	IDProperty    string
}

func (feature *Feature) UnmarshalJSON(data []byte) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	typeValue, exists := members["type"]
	if !exists {
		return fmt.Errorf("GeoJSON object is missing type")
	}
	if err := json.Unmarshal(typeValue, &feature.Type); err != nil {
		return fmt.Errorf("GeoJSON type must be a string: %w", err)
	}
	feature.ID = cloneRawMessage(members["id"])
	feature.BBox = cloneRawMessage(members["bbox"])
	feature.Geometry = cloneRawMessage(members["geometry"])
	feature.Properties = cloneRawMessage(members["properties"])
	feature.Foreign = make(map[string]json.RawMessage)
	for key, value := range members {
		switch key {
		case "type", "id", "bbox", "geometry", "properties":
		default:
			feature.Foreign[key] = cloneRawMessage(value)
		}
	}
	return nil
}

func (feature Feature) MarshalJSON() ([]byte, error) {
	members := make(map[string]json.RawMessage, len(feature.Foreign)+5)
	for key, value := range feature.Foreign {
		members[key] = cloneRawMessage(value)
	}
	typeValue, err := json.Marshal(feature.Type)
	if err != nil {
		return nil, err
	}
	members["type"] = typeValue
	if feature.ID != nil {
		members["id"] = feature.ID
	}
	if feature.BBox != nil {
		members["bbox"] = feature.BBox
	}
	if feature.Geometry != nil {
		members["geometry"] = feature.Geometry
	}
	if feature.Properties != nil {
		members["properties"] = feature.Properties
	}
	return json.Marshal(members)
}

func (feature Feature) PropertyMap() (map[string]json.RawMessage, error) {
	if feature.Properties == nil {
		return nil, fmt.Errorf("Feature is missing properties")
	}
	if bytes.Equal(bytes.TrimSpace(feature.Properties), []byte("null")) {
		return make(map[string]json.RawMessage), nil
	}
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(feature.Properties, &properties); err != nil {
		return nil, fmt.Errorf("Feature properties must be an object or null: %w", err)
	}
	return properties, nil
}

func (feature *Feature) SetPropertyMap(properties map[string]json.RawMessage) error {
	encoded, err := json.Marshal(properties)
	if err != nil {
		return err
	}
	feature.Properties = encoded
	return nil
}

func (feature Feature) EncodedID() string {
	if feature.ID == nil {
		return "<absent>"
	}
	return string(feature.ID)
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}
