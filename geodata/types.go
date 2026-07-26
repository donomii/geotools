package geodata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const (
	DefaultMVTIDProperty    = "__geotools_geojson_id"
	DefaultMVTMaxInputBytes = 64 * 1024 * 1024
)

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
	MaxInputBytes int64
}

func (feature *Feature) UnmarshalJSON(data []byte) error {
	if err := validateUniqueJSONMembers(data); err != nil {
		return err
	}
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

func validateUniqueJSONMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateUniqueJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("JSON value has trailing token %v", token)
	}
	return nil
}

func validateUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	if delimiter == '[' {
		for decoder.More() {
			if err := validateUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("JSON array ended with %v instead of ]", end)
		}
		return nil
	}
	if delimiter != '{' {
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	seen := make(map[string]bool)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("JSON object key has type %T; expected string", keyToken)
		}
		if seen[key] {
			return fmt.Errorf("JSON object contains duplicate member %q", key)
		}
		seen[key] = true
		if err := validateUniqueJSONValue(decoder); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim('}') {
		return fmt.Errorf("JSON object ended with %v instead of }", end)
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
