package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/donomii/geotools/geodata"
)

const (
	jsonFGCoreConformance         = "http://www.opengis.net/spec/json-fg-1/1.0/conf/core"
	jsonFGMeasuresConformance     = "http://www.opengis.net/spec/json-fg-1/1.0/conf/measures"
	jsonFGTypesSchemaConformance  = "http://www.opengis.net/spec/json-fg-1/1.0/conf/types-schemas"
	defaultJSONFGTimePropertyName = "jsonfg_time"
)

var jsonFGFeatureMembers = []string{"conformsTo", "coordRefSys", "featureSchema", "featureType", "geometryDimension", "measures", "place", "time"}

type jsonFGSettings struct {
	PlaceCRS     string
	TimeProperty string
}

type jsonFGRoot struct {
	Type        string            `json:"type"`
	ConformsTo  []string          `json:"conformsTo"`
	CoordRefSys json.RawMessage   `json:"coordRefSys,omitempty"`
	Features    []json.RawMessage `json:"features"`
}

type jsonFGTimeValue struct {
	Date      string            `json:"date,omitempty"`
	Timestamp string            `json:"timestamp,omitempty"`
	Interval  []json.RawMessage `json:"interval,omitempty"`
}

func encodeJSONFG(input io.Reader, output io.Writer, inputMode geodata.InputMode) error {
	return encodeJSONFGWithSettings(input, output, inputMode, jsonFGSettings{
		PlaceCRS:     geodata.CRSCRS84,
		TimeProperty: defaultJSONFGTimePropertyName,
	})
}

func encodeJSONFGWithSettings(input io.Reader, output io.Writer, inputMode geodata.InputMode, settings jsonFGSettings) error {
	placeCRS, err := geodata.NormalizeCRS(settings.PlaceCRS)
	if err != nil {
		return err
	}
	placeCRSURI, err := geodata.CRSURI(placeCRS)
	if err != nil {
		return err
	}
	buffered := bufio.NewWriter(output)
	rootPrefix, err := json.Marshal(struct {
		Type        string   `json:"type"`
		ConformsTo  []string `json:"conformsTo"`
		CoordRefSys string   `json:"coordRefSys"`
	}{
		Type:        "FeatureCollection",
		ConformsTo:  []string{jsonFGCoreConformance},
		CoordRefSys: placeCRSURI,
	})
	if err != nil {
		return err
	}
	if _, err := buffered.Write(bytes.TrimSuffix(rootPrefix, []byte("}"))); err != nil {
		return err
	}
	if _, err := buffered.WriteString(`,"features":[`); err != nil {
		return err
	}
	featureNumber := 0
	readErr := geodata.ReadFeatures(input, inputMode, func(feature geodata.Feature) error {
		featureNumber++
		if _, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{AllowNullGeometry: true}); err != nil {
			return fmt.Errorf("JSON-FG input Feature %d with id %s is invalid: %w", featureNumber, feature.EncodedID(), err)
		}
		for _, member := range jsonFGFeatureMembers {
			if _, exists := feature.Foreign[member]; exists {
				return fmt.Errorf("Feature %d with id %s already contains JSON-FG member %q; encode expects plain GeoJSON input", featureNumber, feature.EncodedID(), member)
			}
		}
		if !bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
			if placeCRS != geodata.CRSCRS84 && placeCRS != geodata.CRSCRS84h {
				geometry, err := geodata.DecodeGeomJSON(feature.Geometry)
				if err != nil {
					return fmt.Errorf("Feature %d with id %s geometry cannot become JSON-FG place: %w", featureNumber, feature.EncodedID(), err)
				}
				sourceCRS := geodata.CRSCRS84
				if geometry.Stride() == 3 {
					sourceCRS = geodata.CRSCRS84h
				}
				if _, err := geodata.TransformJSONFGGeometry(geometry, sourceCRS, placeCRS); err != nil {
					return fmt.Errorf("Feature %d with id %s place reprojection failed: %w", featureNumber, feature.EncodedID(), err)
				}
				place, err := geodata.EncodeGeomJSON(geometry)
				if err != nil {
					return err
				}
				feature.Foreign["place"] = place
			}
		}
		if settings.TimeProperty != "" {
			properties, err := feature.PropertyMap()
			if err != nil {
				return err
			}
			if raw := properties[settings.TimeProperty]; raw != nil {
				timeValue, err := normalizeJSONFGTime(raw)
				if err != nil {
					return fmt.Errorf("Feature %d with id %s property %q cannot become JSON-FG time: %w", featureNumber, feature.EncodedID(), settings.TimeProperty, err)
				}
				feature.Foreign["time"] = timeValue
			}
		}
		encoded, err := json.Marshal(feature)
		if err != nil {
			return fmt.Errorf("failed to encode Feature %s as JSON-FG: %w", feature.EncodedID(), err)
		}
		if featureNumber > 1 {
			if err := buffered.WriteByte(','); err != nil {
				return err
			}
		}
		_, err = buffered.Write(encoded)
		return err
	})
	if readErr != nil {
		return errors.Join(readErr, buffered.Flush())
	}
	if _, err := buffered.WriteString("]}\n"); err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("failed to finish JSON-FG output after %d Features: %w", featureNumber, err)
	}
	return nil
}

func decodeJSONFG(input io.Reader, output io.Writer, outputMode geodata.OutputMode) error {
	return decodeJSONFGWithSettings(input, output, outputMode, jsonFGSettings{TimeProperty: defaultJSONFGTimePropertyName})
}

func decodeJSONFGWithSettings(input io.Reader, output io.Writer, outputMode geodata.OutputMode, settings jsonFGSettings) error {
	data, err := io.ReadAll(input)
	if err != nil {
		return fmt.Errorf("failed to read JSON-FG input: %w", err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("JSON-FG root is invalid JSON: %w", err)
	}
	rootCRS, declarations, err := validateJSONFGRoot(root)
	if err != nil {
		return err
	}
	featureData := root.Features
	if root.Type == "Feature" {
		featureData = []json.RawMessage{data}
	}
	writer := geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	var decodeErr error
	for index, raw := range featureData {
		var feature geodata.Feature
		if err := json.Unmarshal(raw, &feature); err != nil {
			decodeErr = fmt.Errorf("JSON-FG Feature %d is invalid: %w", index+1, err)
			break
		}
		if root.Type == "Feature" {
			delete(feature.Foreign, "conformsTo")
			delete(feature.Foreign, "coordRefSys")
		}
		if err := validateJSONFGFeature(feature, rootCRS, declarations); err != nil {
			decodeErr = fmt.Errorf("JSON-FG Feature %d with id %s is invalid: %w", index+1, feature.EncodedID(), err)
			break
		}
		if err := convertJSONFGPlace(&feature, rootCRS); err != nil {
			decodeErr = fmt.Errorf("JSON-FG Feature %d with id %s cannot become GeoJSON: %w", index+1, feature.EncodedID(), err)
			break
		}
		if settings.TimeProperty != "" {
			if timeValue := feature.Foreign["time"]; timeValue != nil {
				properties, err := feature.PropertyMap()
				if err != nil {
					decodeErr = err
					break
				}
				if existing := properties[settings.TimeProperty]; existing != nil {
					normalizedExisting, err := normalizeJSONFGTime(existing)
					if err != nil {
						decodeErr = fmt.Errorf("JSON-FG Feature %d with id %s property %q cannot be compared with its time member: %w", index+1, feature.EncodedID(), settings.TimeProperty, err)
						break
					}
					equal, err := equalJSONFGTime(normalizedExisting, timeValue)
					if err != nil {
						decodeErr = err
						break
					}
					if !equal {
						decodeErr = fmt.Errorf("JSON-FG Feature %d with id %s property %q conflicts with its time member", index+1, feature.EncodedID(), settings.TimeProperty)
						break
					}
				} else {
					properties[settings.TimeProperty] = timeValue
				}
				if err := feature.SetPropertyMap(properties); err != nil {
					decodeErr = err
					break
				}
			}
		}
		for _, member := range []string{"coordRefSys", "geometryDimension", "measures", "place", "time"} {
			delete(feature.Foreign, member)
		}
		if _, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{AllowNullGeometry: true}); err != nil {
			decodeErr = err
			break
		}
		if err := writer.Write(feature); err != nil {
			decodeErr = err
			break
		}
	}
	return errors.Join(decodeErr, writer.Close())
}

func validateJSONFGRoot(root jsonFGRoot) (string, map[string]bool, error) {
	if root.Type != "FeatureCollection" && root.Type != "Feature" {
		return "", nil, fmt.Errorf("JSON-FG root type is %q; expected FeatureCollection or Feature", root.Type)
	}
	if root.Type == "FeatureCollection" && root.Features == nil {
		return "", nil, fmt.Errorf("JSON-FG root is missing features")
	}
	declarations := make(map[string]bool)
	for _, value := range root.ConformsTo {
		declarations[value] = true
	}
	if !declarations[jsonFGCoreConformance] {
		return "", nil, fmt.Errorf("JSON-FG root conformsTo does not include %q", jsonFGCoreConformance)
	}
	rootCRS, err := geodata.ParseCRS(root.CoordRefSys)
	if err != nil {
		return "", nil, fmt.Errorf("JSON-FG root coordRefSys is invalid: %w", err)
	}
	return rootCRS, declarations, nil
}

func requireCoreConformance(data []byte) error {
	var root jsonFGRoot
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("JSON-FG root is invalid JSON: %w", err)
	}
	_, _, err := validateJSONFGRoot(root)
	return err
}

func validateJSONFGFeature(feature geodata.Feature, rootCRS string, declarations map[string]bool) error {
	if _, exists := feature.Foreign["conformsTo"]; exists {
		return fmt.Errorf("embedded Feature includes conformsTo; only the JSON-FG root may declare conformance")
	}
	if _, exists := feature.Foreign["coordRefSys"]; exists {
		return fmt.Errorf("embedded Feature includes coordRefSys; JSON-FG core permits it only on the root")
	}
	if _, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{AllowNullGeometry: true}); err != nil {
		return err
	}
	if err := rejectJSONFGGeometryExtensions(feature.Geometry); err != nil {
		return err
	}
	if raw := feature.Foreign["time"]; raw != nil {
		if err := validateJSONFGTime(raw); err != nil {
			return err
		}
	}
	if raw := feature.Foreign["measures"]; raw != nil && !declarations[jsonFGMeasuresConformance] {
		return fmt.Errorf("Feature uses measures but root conformsTo omits %q", jsonFGMeasuresConformance)
	}
	if (feature.Foreign["featureType"] != nil || feature.Foreign["featureSchema"] != nil) && !declarations[jsonFGTypesSchemaConformance] {
		return fmt.Errorf("Feature uses featureType or featureSchema but root conformsTo omits %q", jsonFGTypesSchemaConformance)
	}
	place := feature.Foreign["place"]
	if place == nil || bytes.Equal(bytes.TrimSpace(place), []byte("null")) {
		return nil
	}
	placeCRS := rootCRS
	if raw := feature.Foreign["coordRefSys"]; raw != nil {
		var err error
		placeCRS, err = geodata.ParseCRS(raw)
		if err != nil {
			return fmt.Errorf("Feature coordRefSys is invalid: %w", err)
		}
	}
	var placeMembers map[string]json.RawMessage
	if err := json.Unmarshal(place, &placeMembers); err != nil {
		return fmt.Errorf("place is not a geometry object: %w", err)
	}
	if raw := placeMembers["coordRefSys"]; raw != nil {
		return fmt.Errorf("place includes coordRefSys; JSON-FG core permits it only on the root")
	}
	if raw := placeMembers["measures"]; raw != nil && !declarations[jsonFGMeasuresConformance] {
		return fmt.Errorf("place uses measures but root conformsTo omits %q", jsonFGMeasuresConformance)
	}
	if (placeCRS == geodata.CRSCRS84 || placeCRS == geodata.CRSCRS84h) && placeMembers["measures"] == nil {
		return fmt.Errorf("place uses %s without measures; simple WGS 84 geometry belongs only in the GeoJSON geometry member", placeCRS)
	}
	if !bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
		fallbackGeometry, err := geodata.DecodeGeomJSON(feature.Geometry)
		if err != nil {
			return err
		}
		placeGeometry, err := geodata.DecodeGeomJSON(place)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(fallbackGeometry, placeGeometry) {
			return fmt.Errorf("place and fallback geometry have identical values; JSON-FG requires a distinct fallback")
		}
	}
	temporary := geodata.Feature{Type: "Feature", Geometry: place, Properties: json.RawMessage("{}")}
	summary, err := geodata.ValidateFeature(temporary, geodata.ValidationOptions{
		AllowNullGeometry: true,
		AllowOutOfRange:   placeCRS == geodata.CRSEPSG3857 || placeCRS == geodata.CRSEPSG4326,
	})
	if err != nil {
		return fmt.Errorf("place geometry failed validation: %w", err)
	}
	if placeCRS == geodata.CRSEPSG4326 && summary.HasBounds &&
		(summary.Bounds[0] < -90 || summary.Bounds[2] > 90 || summary.Bounds[1] < -180 || summary.Bounds[3] > 180) {
		return fmt.Errorf("place geometry EPSG:4326 bounds are latitude %v..%v and longitude %v..%v; expected latitude -90..90 and longitude -180..180", summary.Bounds[0], summary.Bounds[2], summary.Bounds[1], summary.Bounds[3])
	}
	return nil
}

func convertJSONFGPlace(feature *geodata.Feature, rootCRS string) error {
	if !bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
		return nil
	}
	place := feature.Foreign["place"]
	if place == nil || bytes.Equal(bytes.TrimSpace(place), []byte("null")) {
		return nil
	}
	sourceCRS := rootCRS
	if raw := feature.Foreign["coordRefSys"]; raw != nil {
		var err error
		sourceCRS, err = geodata.ParseCRS(raw)
		if err != nil {
			return err
		}
	}
	var placeMembers map[string]json.RawMessage
	if err := json.Unmarshal(place, &placeMembers); err != nil {
		return err
	}
	if raw := placeMembers["coordRefSys"]; raw != nil {
		var err error
		sourceCRS, err = geodata.ParseCRS(raw)
		if err != nil {
			return err
		}
	}
	delete(placeMembers, "coordRefSys")
	delete(placeMembers, "measures")
	plainPlace, err := json.Marshal(placeMembers)
	if err != nil {
		return err
	}
	geometry, err := geodata.DecodeGeomJSON(plainPlace)
	if err != nil {
		return err
	}
	targetCRS := geodata.CRSCRS84
	if geometry.Stride() == 3 {
		targetCRS = geodata.CRSCRS84h
	}
	if _, err := geodata.TransformJSONFGGeometry(geometry, sourceCRS, targetCRS); err != nil {
		return err
	}
	feature.Geometry, err = geodata.EncodeGeomJSON(geometry)
	return err
}

func rejectJSONFGGeometryExtensions(raw json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var geometry struct {
		CoordRefSys json.RawMessage   `json:"coordRefSys"`
		Measures    json.RawMessage   `json:"measures"`
		Geometries  []json.RawMessage `json:"geometries"`
	}
	if err := json.Unmarshal(raw, &geometry); err != nil {
		return err
	}
	if geometry.CoordRefSys != nil || geometry.Measures != nil {
		return fmt.Errorf("GeoJSON geometry member contains JSON-FG coordRefSys or measures extension")
	}
	for _, child := range geometry.Geometries {
		if err := rejectJSONFGGeometryExtensions(child); err != nil {
			return err
		}
	}
	return nil
}

func normalizeJSONFGTime(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("value is empty")
	}
	if trimmed[0] == '{' {
		if err := validateJSONFGTime(trimmed); err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), trimmed...), nil
	}
	if trimmed[0] == '[' {
		value := map[string]json.RawMessage{"interval": append(json.RawMessage(nil), trimmed...)}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if err := validateJSONFGTime(encoded); err != nil {
			return nil, err
		}
		return encoded, nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, fmt.Errorf("expected a date, UTC timestamp, interval array, or JSON-FG time object: %w", err)
	}
	key := "timestamp"
	if isDate(value) {
		key = "date"
	} else if !isUTCTimestamp(value) {
		return nil, fmt.Errorf("%q is neither an RFC 3339 date nor a UTC timestamp", value)
	}
	encoded, err := json.Marshal(map[string]string{key: value})
	return encoded, err
}

func validateJSONFGTime(raw json.RawMessage) error {
	var value jsonFGTimeValue
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("time must be an object: %w", err)
	}
	if value.Date == "" && value.Timestamp == "" && value.Interval == nil {
		return fmt.Errorf("time has no date, timestamp, or interval")
	}
	if value.Date != "" && !isDate(value.Date) {
		return fmt.Errorf("time date %q is not YYYY-MM-DD", value.Date)
	}
	if value.Timestamp != "" && !isUTCTimestamp(value.Timestamp) {
		return fmt.Errorf("time timestamp %q is not an RFC 3339 UTC timestamp ending in Z", value.Timestamp)
	}
	if value.Date != "" && value.Timestamp != "" && value.Date != value.Timestamp[:len("2006-01-02")] {
		return fmt.Errorf("time date %q differs from timestamp date %q", value.Date, value.Timestamp[:len("2006-01-02")])
	}
	intervalKind, start, end, err := validateJSONFGInterval(value.Interval)
	if err != nil {
		return err
	}
	if value.Date != "" && intervalKind != "" && !jsonFGIntervalContains(intervalKind, start, end, value.Date, "date") {
		return fmt.Errorf("time interval %q..%q does not contain date %q", start, end, value.Date)
	}
	if value.Timestamp != "" && intervalKind != "" && !jsonFGIntervalContains(intervalKind, start, end, value.Timestamp, "timestamp") {
		return fmt.Errorf("time interval %q..%q does not contain timestamp %q", start, end, value.Timestamp)
	}
	return nil
}

func validateJSONFGInterval(interval []json.RawMessage) (string, string, string, error) {
	if interval == nil {
		return "", "", "", nil
	}
	if len(interval) != 2 {
		return "", "", "", fmt.Errorf("time interval has %d endpoints; expected 2", len(interval))
	}
	endpoints := [2]string{}
	kind := ""
	for index, endpointRaw := range interval {
		if err := json.Unmarshal(endpointRaw, &endpoints[index]); err != nil {
			return "", "", "", fmt.Errorf("time interval endpoint %d must be a string", index)
		}
		if endpoints[index] == ".." {
			continue
		}
		endpointKind := ""
		if isDate(endpoints[index]) {
			endpointKind = "date"
		} else if isUTCTimestamp(endpoints[index]) {
			endpointKind = "timestamp"
		} else {
			return "", "", "", fmt.Errorf("time interval endpoint %d value %q is not a date, UTC timestamp, or ..", index, endpoints[index])
		}
		if kind != "" && kind != endpointKind {
			return "", "", "", fmt.Errorf("time interval mixes dates and timestamps")
		}
		kind = endpointKind
	}
	if kind == "" {
		return "", "", "", fmt.Errorf("time interval cannot have two unbounded endpoints")
	}
	if endpoints[0] != ".." && endpoints[1] != ".." {
		reversed := endpoints[0] > endpoints[1]
		if kind == "timestamp" {
			startTime, _ := time.Parse(time.RFC3339Nano, endpoints[0])
			endTime, _ := time.Parse(time.RFC3339Nano, endpoints[1])
			reversed = startTime.After(endTime)
		}
		if reversed {
			return "", "", "", fmt.Errorf("time interval starts at %q after it ends at %q", endpoints[0], endpoints[1])
		}
	}
	return kind, endpoints[0], endpoints[1], nil
}

func jsonFGIntervalContains(intervalKind, start, end, value, valueKind string) bool {
	comparable := value
	if intervalKind == "date" && valueKind == "timestamp" {
		comparable = value[:len("2006-01-02")]
	} else if intervalKind == "timestamp" && valueKind == "timestamp" {
		valueTime, _ := time.Parse(time.RFC3339Nano, value)
		afterStart := true
		beforeEnd := true
		if start != ".." {
			startTime, _ := time.Parse(time.RFC3339Nano, start)
			afterStart = !valueTime.Before(startTime)
		}
		if end != ".." {
			endTime, _ := time.Parse(time.RFC3339Nano, end)
			beforeEnd = !valueTime.After(endTime)
		}
		return afterStart && beforeEnd
	} else if intervalKind == "timestamp" && valueKind == "date" {
		if start != ".." {
			start = start[:len("2006-01-02")]
		}
		if end != ".." {
			end = end[:len("2006-01-02")]
		}
	}
	return (start == ".." || comparable >= start) && (end == ".." || comparable <= end)
}

func equalJSONFGTime(first, second json.RawMessage) (bool, error) {
	canonicalFirst, err := canonicalJSONFGTime(first)
	if err != nil {
		return false, err
	}
	canonicalSecond, err := canonicalJSONFGTime(second)
	if err != nil {
		return false, err
	}
	return bytes.Equal(canonicalFirst, canonicalSecond), nil
}

func canonicalJSONFGTime(raw json.RawMessage) ([]byte, error) {
	if err := validateJSONFGTime(raw); err != nil {
		return nil, err
	}
	var value jsonFGTimeValue
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func isDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func isUTCTimestamp(value string) bool {
	if !strings.HasSuffix(value, "Z") {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}
