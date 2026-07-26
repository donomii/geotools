package geodata

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/maptile"
	"github.com/paulmach/orb/simplify"
)

func EncodeMVT(input io.Reader, output io.Writer, inputMode InputMode, settings MVTEncodeSettings) error {
	tile, err := validateMVTTile(settings.Zoom, settings.X, settings.Y)
	if err != nil {
		return err
	}
	if settings.Layer == "" {
		return fmt.Errorf("MVT layer name is empty")
	}
	if settings.Extent < 256 || settings.Extent > uint(^uint32(0)) {
		return fmt.Errorf("MVT extent %d is outside supported range 256 through %d", settings.Extent, uint(^uint32(0)))
	}
	if settings.Buffer > settings.Extent {
		return fmt.Errorf("MVT buffer %d exceeds extent %d", settings.Buffer, settings.Extent)
	}
	if settings.Simplify < 0 {
		return fmt.Errorf("MVT simplification tolerance %v is negative", settings.Simplify)
	}
	if settings.IDProperty != "" && settings.IDProperty == settings.LayerProperty {
		return fmt.Errorf("MVT id property %q is also the layer-selection property; use distinct property names", settings.IDProperty)
	}
	collections := make(map[string]*geojson.FeatureCollection)
	featureNumber := 0
	if err := ReadFeatures(input, inputMode, func(feature Feature) error {
		featureNumber++
		summary, err := ValidateFeature(feature, ValidationOptions{})
		if err != nil {
			return fmt.Errorf("MVT input Feature %d with id %s is invalid: %w", featureNumber, feature.EncodedID(), err)
		}
		if summary.CoordinateDimension != 2 {
			return fmt.Errorf("MVT input Feature %d with id %s has %d-coordinate positions; MVT supports 2D positions", featureNumber, feature.EncodedID(), summary.CoordinateDimension)
		}
		if summary.Type == "GeometryCollection" {
			return fmt.Errorf("MVT input Feature %d with id %s has GeometryCollection geometry; MVT supports Point, LineString, Polygon, and their multi-geometry forms", featureNumber, feature.EncodedID())
		}
		if summary.HasBounds && (summary.Bounds[1] < -85.0511287798066 || summary.Bounds[3] > 85.0511287798066) {
			return fmt.Errorf("MVT input Feature %d with id %s has latitude bounds %v..%v outside Web Mercator limits -85.0511287798066..85.0511287798066", featureNumber, feature.EncodedID(), summary.Bounds[1], summary.Bounds[3])
		}
		layerName := settings.Layer
		if settings.LayerProperty != "" {
			properties, err := feature.PropertyMap()
			if err != nil {
				return err
			}
			raw := properties[settings.LayerProperty]
			if raw == nil {
				return fmt.Errorf("MVT input Feature %d with id %s has no layer property %q", featureNumber, feature.EncodedID(), settings.LayerProperty)
			}
			if err := json.Unmarshal(raw, &layerName); err != nil || layerName == "" {
				return fmt.Errorf("MVT input Feature %d with id %s layer property %q is %s; expected a non-empty string", featureNumber, feature.EncodedID(), settings.LayerProperty, raw)
			}
			if settings.DropLayerProperty {
				delete(properties, settings.LayerProperty)
				if err := feature.SetPropertyMap(properties); err != nil {
					return err
				}
			}
		}
		if err := preserveMVTFeatureID(&feature, settings.IDProperty, featureNumber); err != nil {
			return err
		}
		converted, err := OrbFeature(feature)
		if err != nil {
			return err
		}
		collection := collections[layerName]
		if collection == nil {
			collection = geojson.NewFeatureCollection()
			collections[layerName] = collection
		}
		collection.Append(converted)
		return nil
	}); err != nil {
		return err
	}
	if len(collections) == 0 {
		collections[settings.Layer] = geojson.NewFeatureCollection()
	}
	layerNames := make([]string, 0, len(collections))
	for name := range collections {
		layerNames = append(layerNames, name)
	}
	sort.Strings(layerNames)
	layers := make(mvt.Layers, 0, len(layerNames))
	extent := float64(settings.Extent)
	buffer := float64(settings.Buffer)
	for _, name := range layerNames {
		layer := mvt.NewLayer(name, collections[name])
		layer.Extent = uint32(settings.Extent)
		layer.ProjectToTile(tile)
		if settings.Simplify > 0 {
			layer.Simplify(simplify.DouglasPeucker(settings.Simplify))
		}
		layer.Clip(orb.Bound{Min: orb.Point{-buffer, -buffer}, Max: orb.Point{extent + buffer, extent + buffer}})
		for _, feature := range layer.Features {
			normalizeMVTPolygonWinding(feature.Geometry)
		}
		layers = append(layers, layer)
	}
	var encoded []byte
	if settings.Gzip {
		encoded, err = mvt.MarshalGzipped(layers)
	} else {
		encoded, err = mvt.Marshal(layers)
	}
	if err != nil {
		return fmt.Errorf("failed to encode tile %d/%d/%d: %w", settings.Zoom, settings.X, settings.Y, err)
	}
	written, err := output.Write(encoded)
	if err != nil {
		return fmt.Errorf("failed to write tile %d/%d/%d: %w", settings.Zoom, settings.X, settings.Y, err)
	}
	if written != len(encoded) {
		return fmt.Errorf("failed to write tile %d/%d/%d: wrote %d of %d bytes: %w", settings.Zoom, settings.X, settings.Y, written, len(encoded), io.ErrShortWrite)
	}
	return nil
}

func preserveMVTFeatureID(feature *Feature, propertyName string, featureNumber int) error {
	if propertyName == "" {
		return nil
	}
	properties, err := feature.PropertyMap()
	if err != nil {
		return err
	}
	if properties[propertyName] != nil {
		return fmt.Errorf("MVT input Feature %d with id %s uses reserved id property %q", featureNumber, feature.EncodedID(), propertyName)
	}
	if feature.ID == nil {
		return nil
	}
	properties[propertyName], err = json.Marshal(string(feature.ID))
	if err != nil {
		return err
	}
	if err := feature.SetPropertyMap(properties); err != nil {
		return err
	}
	feature.ID = nil
	return nil
}

func normalizeMVTPolygonWinding(geometry orb.Geometry) {
	switch value := geometry.(type) {
	case orb.Polygon:
		normalizeMVTPolygon(value)
	case orb.MultiPolygon:
		for _, polygon := range value {
			normalizeMVTPolygon(polygon)
		}
	}
}

func normalizeMVTPolygon(polygon orb.Polygon) {
	for index, ring := range polygon {
		expected := orb.CW
		if index == 0 {
			expected = orb.CCW
		}
		if orientation := ring.Orientation(); orientation != 0 && orientation != expected {
			ring.Reverse()
		}
	}
}

func DecodeMVT(input io.Reader, output io.Writer, outputMode OutputMode, settings MVTDecodeSettings) error {
	tile, err := validateMVTTile(settings.Zoom, settings.X, settings.Y)
	if err != nil {
		return err
	}
	if !settings.AllLayers && settings.Layer == "" {
		return fmt.Errorf("MVT layer name is empty")
	}
	if settings.AllLayers && settings.IDProperty != "" && settings.IDProperty == settings.LayerProperty {
		return fmt.Errorf("MVT id property %q is also the decoded layer-name property; use distinct property names", settings.IDProperty)
	}
	maxInputBytes := settings.MaxInputBytes
	if maxInputBytes == 0 {
		maxInputBytes = DefaultMVTMaxInputBytes
	}
	if maxInputBytes < 0 {
		return fmt.Errorf("MVT maximum decoded input size %d is negative; expected zero for the %d-byte default or a positive byte count", settings.MaxInputBytes, DefaultMVTMaxInputBytes)
	}
	data, err := readMVTData(input, settings.Gzip, maxInputBytes)
	if err != nil {
		return fmt.Errorf("failed to read MVT tile %d/%d/%d: %w", settings.Zoom, settings.X, settings.Y, err)
	}
	layers, err := mvt.Unmarshal(data)
	if err != nil {
		return fmt.Errorf("failed to decode MVT tile %d/%d/%d: %w", settings.Zoom, settings.X, settings.Y, err)
	}
	var selected mvt.Layers
	if settings.AllLayers {
		selected = append(selected, layers...)
		sort.Slice(selected, func(first, second int) bool {
			return selected[first].Name < selected[second].Name
		})
	} else {
		for _, layer := range layers {
			if layer.Name == settings.Layer {
				selected = append(selected, layer)
				break
			}
		}
	}
	if len(selected) == 0 {
		if settings.AllLayers {
			return fmt.Errorf("MVT tile %d/%d/%d contains no layers", settings.Zoom, settings.X, settings.Y)
		}
		return fmt.Errorf("MVT tile %d/%d/%d has no layer named %q", settings.Zoom, settings.X, settings.Y, settings.Layer)
	}
	writer := NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	var decodeErr error
	for _, layer := range selected {
		layer.ProjectToWGS84(tile)
		for index, feature := range layer.Features {
			converted, err := FeatureFromOrb(feature)
			if err != nil {
				decodeErr = fmt.Errorf("MVT layer %q Feature %d cannot be converted to GeoJSON: %w", layer.Name, index+1, err)
				break
			}
			if err := restoreMVTFeatureID(&converted, settings.IDProperty, layer.Name, index+1); err != nil {
				decodeErr = err
				break
			}
			if settings.AllLayers && settings.LayerProperty != "" {
				properties, err := converted.PropertyMap()
				if err != nil {
					decodeErr = err
					break
				}
				if existing := properties[settings.LayerProperty]; existing != nil {
					var existingName string
					if json.Unmarshal(existing, &existingName) != nil || existingName != layer.Name {
						decodeErr = fmt.Errorf("MVT layer %q Feature %d property %q conflicts with the layer name", layer.Name, index+1, settings.LayerProperty)
						break
					}
				}
				properties[settings.LayerProperty], _ = json.Marshal(layer.Name)
				if err := converted.SetPropertyMap(properties); err != nil {
					decodeErr = err
					break
				}
			}
			if _, err := ValidateFeature(converted, ValidationOptions{}); err != nil {
				decodeErr = fmt.Errorf("MVT layer %q Feature %d produced invalid GeoJSON: %w", layer.Name, index+1, err)
				break
			}
			if err := writer.Write(converted); err != nil {
				decodeErr = err
				break
			}
		}
		if decodeErr != nil {
			break
		}
	}
	return errors.Join(decodeErr, writer.Close())
}

func readMVTData(input io.Reader, gzipInput bool, maxInputBytes int64) ([]byte, error) {
	source := input
	var gzipReader *gzip.Reader
	if gzipInput {
		var err error
		gzipReader, err = gzip.NewReader(input)
		if err != nil {
			return nil, fmt.Errorf("gzip header is invalid: %w", err)
		}
		source = gzipReader
	}
	readLimit := maxInputBytes
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	data, err := io.ReadAll(io.LimitReader(source, readLimit))
	if err != nil {
		if gzipInput {
			return nil, fmt.Errorf("gzip-compressed input cannot be decompressed: %w", err)
		}
		return nil, err
	}
	if int64(len(data)) > maxInputBytes {
		return nil, fmt.Errorf("decoded input is at least %d bytes; configured maximum is %d bytes", len(data), maxInputBytes)
	}
	if gzipReader != nil {
		if err := gzipReader.Close(); err != nil {
			return nil, fmt.Errorf("failed to finish reading gzip-compressed input: %w", err)
		}
	}
	return data, nil
}

func restoreMVTFeatureID(feature *Feature, propertyName, layerName string, featureNumber int) error {
	if propertyName == "" {
		return nil
	}
	properties, err := feature.PropertyMap()
	if err != nil {
		return err
	}
	raw := properties[propertyName]
	if raw == nil {
		return nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return fmt.Errorf("MVT layer %q Feature %d reserved id property %q is %s; expected a string containing a GeoJSON id", layerName, featureNumber, propertyName, raw)
	}
	id := json.RawMessage(encoded)
	trimmed := bytes.TrimSpace(id)
	if !json.Valid(trimmed) || len(trimmed) == 0 || (trimmed[0] != '"' && trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
		return fmt.Errorf("MVT layer %q Feature %d reserved id property %q contains %q; expected a JSON string or number", layerName, featureNumber, propertyName, encoded)
	}
	feature.ID = append(json.RawMessage(nil), id...)
	delete(properties, propertyName)
	return feature.SetPropertyMap(properties)
}

func validateMVTTile(zoom, x, y uint) (maptile.Tile, error) {
	if zoom > 30 {
		return maptile.Tile{}, fmt.Errorf("tile zoom %d exceeds supported maximum 30", zoom)
	}
	if x > uint(^uint32(0)) || y > uint(^uint32(0)) {
		return maptile.Tile{}, fmt.Errorf("tile coordinates %d/%d exceed 32-bit MVT limits", x, y)
	}
	tile := maptile.New(uint32(x), uint32(y), maptile.Zoom(zoom))
	if !tile.Valid() {
		return maptile.Tile{}, fmt.Errorf("tile %d/%d/%d is invalid; x and y must be below %d", zoom, x, y, uint64(1)<<zoom)
	}
	return tile, nil
}
