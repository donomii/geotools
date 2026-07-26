package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/donomii/geotools/geodata"
)

const (
	jsonFGCoreConformance                  = "http://www.opengis.net/spec/json-fg-1/1.0/conf/core"
	jsonFGMeasuresConformance              = "http://www.opengis.net/spec/json-fg-1/1.0/conf/measures"
	jsonFGTypesSchemaConformance           = "http://www.opengis.net/spec/json-fg-1/1.0/conf/types-schemas"
	defaultJSONFGTimePropertyName          = "jsonfg_time"
	defaultJSONFGMeasuresPropertyName      = "jsonfg_measures"
	defaultJSONFGFeatureTypePropertyName   = "jsonfg_feature_type"
	defaultJSONFGFeatureSchemaPropertyName = "jsonfg_feature_schema"
)

var jsonFGFeatureMembers = []string{"conformsTo", "coordRefSys", "featureSchema", "featureType", "geometryDimension", "measures", "place", "time"}

type jsonFGSettings struct {
	PlaceCRS              string
	TimeProperty          string
	MeasuresProperty      string
	FeatureTypeProperty   string
	FeatureSchemaProperty string
}

type jsonFGRoot struct {
	Type          string            `json:"type"`
	ConformsTo    []string          `json:"conformsTo"`
	CoordRefSys   json.RawMessage   `json:"coordRefSys,omitempty"`
	Measures      json.RawMessage   `json:"measures,omitempty"`
	FeatureType   json.RawMessage   `json:"featureType,omitempty"`
	FeatureSchema json.RawMessage   `json:"featureSchema,omitempty"`
	GeometryDim   *int              `json:"geometryDimension,omitempty"`
	Features      []json.RawMessage `json:"features"`
}

type jsonFGTimeValue struct {
	Date      string            `json:"date,omitempty"`
	Timestamp string            `json:"timestamp,omitempty"`
	Interval  []json.RawMessage `json:"interval,omitempty"`
}

func encodeJSONFG(input io.Reader, output io.Writer, inputMode geodata.InputMode) error {
	return encodeJSONFGWithSettings(input, output, inputMode, jsonFGSettings{
		PlaceCRS:              geodata.CRSCRS84,
		TimeProperty:          defaultJSONFGTimePropertyName,
		MeasuresProperty:      defaultJSONFGMeasuresPropertyName,
		FeatureTypeProperty:   defaultJSONFGFeatureTypePropertyName,
		FeatureSchemaProperty: defaultJSONFGFeatureSchemaPropertyName,
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
	featureNumber := 0
	prefixWritten := false
	typesDeclared := false
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
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		propertiesChanged := false
		var measures json.RawMessage
		if settings.MeasuresProperty != "" {
			if raw := properties[settings.MeasuresProperty]; raw != nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				if err := validateJSONFGMeasures(raw); err != nil {
					return fmt.Errorf("Feature %d with id %s property %q cannot become JSON-FG measures: %w", featureNumber, feature.EncodedID(), settings.MeasuresProperty, err)
				}
				measures = raw
				delete(properties, settings.MeasuresProperty)
				propertiesChanged = true
			}
		}
		for propertyName, memberName := range map[string]string{
			settings.FeatureTypeProperty: "featureType", settings.FeatureSchemaProperty: "featureSchema",
		} {
			if propertyName == "" {
				continue
			}
			if raw := properties[propertyName]; raw != nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				if err := validateJSONFGClassMember(memberName, raw); err != nil {
					return fmt.Errorf("Feature %d with id %s property %q cannot become JSON-FG %s: %w", featureNumber, feature.EncodedID(), propertyName, memberName, err)
				}
				feature.Foreign[memberName] = raw
				delete(properties, propertyName)
				propertiesChanged = true
			}
		}
		if propertiesChanged {
			if err := feature.SetPropertyMap(properties); err != nil {
				return err
			}
		}
		hasFeatureType := feature.Foreign["featureType"] != nil
		if feature.Foreign["featureSchema"] != nil && !hasFeatureType {
			return fmt.Errorf("Feature %d with id %s has featureSchema but no featureType", featureNumber, feature.EncodedID())
		}
		if !prefixWritten {
			typesDeclared = hasFeatureType
			if err := writeJSONFGRootPrefix(buffered, placeCRSURI, typesDeclared); err != nil {
				return err
			}
			prefixWritten = true
		} else if typesDeclared && !hasFeatureType {
			return fmt.Errorf("Feature %d with id %s has no featureType; every Feature must have one when the root declares type/schema conformance", featureNumber, feature.EncodedID())
		} else if !typesDeclared && (hasFeatureType || feature.Foreign["featureSchema"] != nil) {
			return fmt.Errorf("Feature %d with id %s has type/schema metadata after the first Feature omitted it; streaming JSON-FG encoding requires type metadata on every Feature", featureNumber, feature.EncodedID())
		}
		measuresEnabled, err := jsonFGMeasuresEnabled(measures)
		if err != nil {
			return fmt.Errorf("Feature %d with id %s measures cannot be interpreted: %w", featureNumber, feature.EncodedID(), err)
		}
		if measures != nil {
			feature.Foreign["measures"] = measures
		}
		if measuresEnabled && bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
			return fmt.Errorf("Feature %d with id %s has enabled JSON-FG measures but null geometry", featureNumber, feature.EncodedID())
		}
		if measuresEnabled {
			place, err := encodeJSONFGMeasuredPlace(feature.Geometry, placeCRS)
			if err != nil {
				return fmt.Errorf("Feature %d with id %s measured geometry cannot become JSON-FG place: %w", featureNumber, feature.EncodedID(), err)
			}
			feature.Foreign["place"] = place
			feature.Geometry = json.RawMessage("null")
		} else if !bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) &&
			placeCRS != geodata.CRSCRS84 && placeCRS != geodata.CRSCRS84h {
			place, err := encodeJSONFGPlace(feature.Geometry, placeCRS)
			if err != nil {
				return fmt.Errorf("Feature %d with id %s geometry cannot become JSON-FG place: %w", featureNumber, feature.EncodedID(), err)
			}
			feature.Foreign["place"] = place
		}
		if settings.TimeProperty != "" {
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
	if !prefixWritten {
		if err := writeJSONFGRootPrefix(buffered, placeCRSURI, false); err != nil {
			return errors.Join(err, buffered.Flush())
		}
	}
	if _, err := buffered.WriteString("]}\n"); err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("failed to finish JSON-FG output after %d Features: %w", featureNumber, err)
	}
	return nil
}

func writeJSONFGRootPrefix(buffered *bufio.Writer, placeCRSURI string, includeTypes bool) error {
	conformance := []string{jsonFGCoreConformance, jsonFGMeasuresConformance}
	if includeTypes {
		conformance = append(conformance, jsonFGTypesSchemaConformance)
	}
	rootPrefix, err := json.Marshal(struct {
		Type        string   `json:"type"`
		ConformsTo  []string `json:"conformsTo"`
		CoordRefSys string   `json:"coordRefSys"`
	}{
		Type:        "FeatureCollection",
		ConformsTo:  conformance,
		CoordRefSys: placeCRSURI,
	})
	if err != nil {
		return err
	}
	if _, err := buffered.Write(bytes.TrimSuffix(rootPrefix, []byte("}"))); err != nil {
		return err
	}
	_, err = buffered.WriteString(`,"features":[`)
	return err
}

func decodeJSONFG(input io.Reader, output io.Writer, outputMode geodata.OutputMode) error {
	return decodeJSONFGWithSettings(input, output, outputMode, jsonFGSettings{
		TimeProperty:          defaultJSONFGTimePropertyName,
		MeasuresProperty:      defaultJSONFGMeasuresPropertyName,
		FeatureTypeProperty:   defaultJSONFGFeatureTypePropertyName,
		FeatureSchemaProperty: defaultJSONFGFeatureSchemaPropertyName,
	})
}

func decodeJSONFGWithSettings(input io.Reader, output io.Writer, outputMode geodata.OutputMode, settings jsonFGSettings) error {
	decoder := json.NewDecoder(input)
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("JSON-FG root must be an object: received %v: %w", token, err)
	}
	rootMembers := make(map[string]json.RawMessage)
	seen := make(map[string]bool)
	var root jsonFGRoot
	var writer *geodata.FeatureWriter
	featuresSeen := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return closeJSONFGWriter(writer, fmt.Errorf("JSON-FG root member name cannot be read: %w", err))
		}
		key, ok := keyToken.(string)
		if !ok {
			return closeJSONFGWriter(writer, fmt.Errorf("JSON-FG root member name is %v; expected a string", keyToken))
		}
		if seen[key] {
			return closeJSONFGWriter(writer, fmt.Errorf("JSON-FG root contains duplicate member %q", key))
		}
		seen[key] = true
		if key == "features" && root.Type == "" {
			return closeJSONFGWriter(writer, fmt.Errorf("streaming JSON-FG decoding requires root member %q before features", "type"))
		}
		if key == "features" && root.Type == "FeatureCollection" {
			if !seen["conformsTo"] {
				return closeJSONFGWriter(writer, fmt.Errorf("streaming JSON-FG decoding requires root member %q before features", "conformsTo"))
			}
			featuresSeen = true
			root.Features = []json.RawMessage{}
			rootCRS, declarations, err := validateJSONFGRoot(root)
			if err != nil {
				return closeJSONFGWriter(writer, err)
			}
			writer = geodata.NewFeatureWriter(output, outputMode)
			if err := writer.Start(); err != nil {
				return err
			}
			if err := decodeJSONFGFeatures(decoder, writer, rootCRS, declarations, root.Measures, root.FeatureType, root.FeatureSchema, root.GeometryDim, settings); err != nil {
				return closeJSONFGWriter(writer, err)
			}
			continue
		}
		if featuresSeen && (key == "type" || key == "conformsTo" || key == "coordRefSys" || key == "measures" || key == "featureType" || key == "featureSchema" || key == "geometryDimension") {
			return closeJSONFGWriter(writer, fmt.Errorf("streaming JSON-FG decoding requires root member %q before features", key))
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return closeJSONFGWriter(writer, fmt.Errorf("JSON-FG root member %q cannot be read: %w", key, err))
		}
		rootMembers[key] = raw
		switch key {
		case "type":
			if err := json.Unmarshal(raw, &root.Type); err != nil {
				return closeJSONFGWriter(writer, fmt.Errorf("JSON-FG root type is invalid: %w", err))
			}
		case "conformsTo":
			if err := json.Unmarshal(raw, &root.ConformsTo); err != nil {
				return closeJSONFGWriter(writer, fmt.Errorf("JSON-FG root conformsTo is invalid: %w", err))
			}
		case "coordRefSys":
			root.CoordRefSys = raw
		case "measures":
			root.Measures = raw
		case "featureType":
			root.FeatureType = raw
		case "featureSchema":
			root.FeatureSchema = raw
		case "geometryDimension":
			if err := json.Unmarshal(raw, &root.GeometryDim); err != nil {
				return closeJSONFGWriter(writer, fmt.Errorf("JSON-FG root geometryDimension is invalid: %w", err))
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return closeJSONFGWriter(writer, fmt.Errorf("JSON-FG root object is not closed: %w", err))
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected value %s", trailing)
		}
		return closeJSONFGWriter(writer, fmt.Errorf("JSON-FG input contains data after the root object: %w", err))
	}
	if root.Type == "FeatureCollection" {
		if !featuresSeen {
			return closeJSONFGWriter(writer, fmt.Errorf("JSON-FG root is missing features"))
		}
		return closeJSONFGWriter(writer, nil)
	}
	rootData, err := json.Marshal(rootMembers)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(rootData, &root); err != nil {
		return fmt.Errorf("JSON-FG root is invalid: %w", err)
	}
	rootCRS, declarations, err := validateJSONFGRoot(root)
	if err != nil {
		return err
	}
	writer = geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	feature, err := decodeJSONFGFeature(rootData, root.Type, 1, rootCRS, declarations, nil, nil, nil, root.GeometryDim, settings)
	if err == nil {
		err = writer.Write(feature)
	}
	return closeJSONFGWriter(writer, err)
}

func decodeJSONFGFeatures(decoder *json.Decoder, writer *geodata.FeatureWriter, rootCRS string, declarations map[string]bool, rootMeasures, rootFeatureType, rootFeatureSchema json.RawMessage, rootGeometryDimension *int, settings jsonFGSettings) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("JSON-FG features member cannot be read: %w", err)
	}
	if token != json.Delim('[') {
		return fmt.Errorf("JSON-FG features member begins with %v; expected an array", token)
	}
	singleSchemaType := ""
	rootHasSingleSchema := jsonFGSchemaIsSingleURI(rootFeatureSchema)
	for featureNumber := 1; decoder.More(); featureNumber++ {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("JSON-FG Feature %d cannot be read: %w", featureNumber, err)
		}
		if rootHasSingleSchema {
			featureType, err := jsonFGEffectiveFeatureType(raw, rootFeatureType)
			if err != nil {
				return fmt.Errorf("JSON-FG Feature %d featureType is invalid: %w", featureNumber, err)
			}
			if singleSchemaType == "" {
				singleSchemaType = featureType
			} else if featureType != singleSchemaType {
				return fmt.Errorf("JSON-FG Feature %d has featureType %q; root featureSchema is a single URI and earlier Features use type %q", featureNumber, featureType, singleSchemaType)
			}
		}
		feature, err := decodeJSONFGFeature(raw, "FeatureCollection", featureNumber, rootCRS, declarations, rootMeasures, rootFeatureType, rootFeatureSchema, rootGeometryDimension, settings)
		if err != nil {
			return err
		}
		if err := writer.Write(feature); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("JSON-FG features array is not closed: %w", err)
	}
	return nil
}

func closeJSONFGWriter(writer *geodata.FeatureWriter, decodeErr error) error {
	if writer == nil {
		return decodeErr
	}
	return errors.Join(decodeErr, writer.Close())
}

func decodeJSONFGFeature(raw json.RawMessage, rootType string, featureNumber int, rootCRS string, declarations map[string]bool, rootMeasures, rootFeatureType, rootFeatureSchema json.RawMessage, rootGeometryDimension *int, settings jsonFGSettings) (geodata.Feature, error) {
	var feature geodata.Feature
	if err := json.Unmarshal(raw, &feature); err != nil {
		return geodata.Feature{}, fmt.Errorf("JSON-FG Feature %d is invalid: %w", featureNumber, err)
	}
	if rootType == "Feature" {
		delete(feature.Foreign, "conformsTo")
		delete(feature.Foreign, "coordRefSys")
	} else {
		if feature.Foreign["geometryDimension"] != nil {
			return geodata.Feature{}, fmt.Errorf("JSON-FG Feature %d with id %s includes geometryDimension; only the FeatureCollection root may declare it", featureNumber, feature.EncodedID())
		}
		if feature.Foreign["measures"] == nil && rootMeasures != nil {
			feature.Foreign["measures"] = rootMeasures
		}
		if feature.Foreign["featureType"] == nil && rootFeatureType != nil {
			feature.Foreign["featureType"] = rootFeatureType
		}
		if feature.Foreign["featureSchema"] == nil && rootFeatureSchema != nil {
			feature.Foreign["featureSchema"] = rootFeatureSchema
		}
	}
	if err := validateJSONFGFeature(feature, rootCRS, declarations); err != nil {
		return geodata.Feature{}, fmt.Errorf("JSON-FG Feature %d with id %s is invalid: %w", featureNumber, feature.EncodedID(), err)
	}
	if rootGeometryDimension != nil {
		if err := validateJSONFGGeometryDimension(feature, *rootGeometryDimension); err != nil {
			return geodata.Feature{}, fmt.Errorf("JSON-FG Feature %d with id %s conflicts with root geometryDimension %d: %w", featureNumber, feature.EncodedID(), *rootGeometryDimension, err)
		}
	}
	measures, err := jsonFGFeatureMeasures(feature)
	if err != nil {
		return geodata.Feature{}, fmt.Errorf("JSON-FG Feature %d with id %s measures are invalid: %w", featureNumber, feature.EncodedID(), err)
	}
	for propertyName, raw := range map[string]json.RawMessage{
		settings.MeasuresProperty: measures, settings.FeatureTypeProperty: feature.Foreign["featureType"],
		settings.FeatureSchemaProperty: feature.Foreign["featureSchema"],
	} {
		if propertyName != "" && raw != nil {
			if err := restoreJSONFGProperty(&feature, propertyName, raw); err != nil {
				return geodata.Feature{}, fmt.Errorf("JSON-FG Feature %d with id %s cannot restore property %q: %w", featureNumber, feature.EncodedID(), propertyName, err)
			}
		}
	}
	if err := convertJSONFGPlace(&feature, rootCRS, measures); err != nil {
		return geodata.Feature{}, fmt.Errorf("JSON-FG Feature %d with id %s cannot become GeoJSON: %w", featureNumber, feature.EncodedID(), err)
	}
	if settings.TimeProperty != "" {
		if timeValue := feature.Foreign["time"]; timeValue != nil {
			properties, err := feature.PropertyMap()
			if err != nil {
				return geodata.Feature{}, err
			}
			if existing := properties[settings.TimeProperty]; existing != nil {
				normalizedExisting, err := normalizeJSONFGTime(existing)
				if err != nil {
					return geodata.Feature{}, fmt.Errorf("JSON-FG Feature %d with id %s property %q cannot be compared with its time member: %w", featureNumber, feature.EncodedID(), settings.TimeProperty, err)
				}
				equal, err := equalJSONFGTime(normalizedExisting, timeValue)
				if err != nil {
					return geodata.Feature{}, err
				}
				if !equal {
					return geodata.Feature{}, fmt.Errorf("JSON-FG Feature %d with id %s property %q conflicts with its time member", featureNumber, feature.EncodedID(), settings.TimeProperty)
				}
			} else {
				properties[settings.TimeProperty] = timeValue
			}
			if err := feature.SetPropertyMap(properties); err != nil {
				return geodata.Feature{}, err
			}
		}
	}
	for _, member := range []string{"coordRefSys", "featureSchema", "featureType", "geometryDimension", "measures", "place", "time"} {
		delete(feature.Foreign, member)
	}
	if _, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{AllowNullGeometry: true}); err != nil {
		return geodata.Feature{}, err
	}
	return feature, nil
}

func validateJSONFGRoot(root jsonFGRoot) (string, map[string]bool, error) {
	if root.Type != "FeatureCollection" && root.Type != "Feature" {
		return "", nil, fmt.Errorf("JSON-FG root type is %q; expected FeatureCollection or Feature", root.Type)
	}
	if root.Type == "FeatureCollection" && root.Features == nil {
		return "", nil, fmt.Errorf("JSON-FG root is missing features")
	}
	if root.GeometryDim != nil && (*root.GeometryDim < 0 || *root.GeometryDim > 3) {
		return "", nil, fmt.Errorf("JSON-FG root geometryDimension is %d; expected 0 through 3", *root.GeometryDim)
	}
	declarations := make(map[string]bool)
	for _, value := range root.ConformsTo {
		declarations[value] = true
	}
	if !declarations[jsonFGCoreConformance] {
		return "", nil, fmt.Errorf("JSON-FG root conformsTo does not include %q", jsonFGCoreConformance)
	}
	if root.Measures != nil {
		if !declarations[jsonFGMeasuresConformance] {
			return "", nil, fmt.Errorf("JSON-FG root uses measures but conformsTo omits %q", jsonFGMeasuresConformance)
		}
		if err := validateJSONFGMeasures(root.Measures); err != nil {
			return "", nil, fmt.Errorf("JSON-FG root measures are invalid: %w", err)
		}
	}
	if root.FeatureType != nil || root.FeatureSchema != nil {
		if !declarations[jsonFGTypesSchemaConformance] {
			return "", nil, fmt.Errorf("JSON-FG root uses featureType or featureSchema but conformsTo omits %q", jsonFGTypesSchemaConformance)
		}
		if root.FeatureType != nil {
			if err := validateJSONFGClassMember("featureType", root.FeatureType); err != nil {
				return "", nil, fmt.Errorf("JSON-FG root featureType is invalid: %w", err)
			}
		}
		if root.FeatureSchema != nil {
			if err := validateJSONFGClassMember("featureSchema", root.FeatureSchema); err != nil {
				return "", nil, fmt.Errorf("JSON-FG root featureSchema is invalid: %w", err)
			}
		}
	}
	if root.Type == "Feature" && declarations[jsonFGTypesSchemaConformance] && root.FeatureType == nil {
		return "", nil, fmt.Errorf("JSON-FG root declares type/schema conformance but the Feature has no featureType")
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
	if raw := feature.Foreign["measures"]; raw != nil {
		if err := validateJSONFGMeasures(raw); err != nil {
			return err
		}
	}
	featureMeasuresEnabled, err := jsonFGMeasuresEnabled(feature.Foreign["measures"])
	if err != nil {
		return err
	}
	if featureMeasuresEnabled && !bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
		if err := validateJSONFGMeasuredGeoJSONGeometry(feature.Geometry); err != nil {
			return fmt.Errorf("geometry measure coordinates are invalid: %w", err)
		}
	}
	if (feature.Foreign["featureType"] != nil || feature.Foreign["featureSchema"] != nil) && !declarations[jsonFGTypesSchemaConformance] {
		return fmt.Errorf("Feature uses featureType or featureSchema but root conformsTo omits %q", jsonFGTypesSchemaConformance)
	}
	if declarations[jsonFGTypesSchemaConformance] && feature.Foreign["featureType"] == nil {
		return fmt.Errorf("root declares type/schema conformance but Feature has no featureType")
	}
	for _, member := range []string{"featureType", "featureSchema"} {
		if raw := feature.Foreign[member]; raw != nil {
			if err := validateJSONFGClassMember(member, raw); err != nil {
				return err
			}
		}
	}
	place := feature.Foreign["place"]
	if place == nil || bytes.Equal(bytes.TrimSpace(place), []byte("null")) {
		return nil
	}
	var placeMembers map[string]json.RawMessage
	if err := json.Unmarshal(place, &placeMembers); err != nil {
		return fmt.Errorf("place is not a geometry object: %w", err)
	}
	placeCRS := rootCRS
	if raw := placeMembers["coordRefSys"]; raw != nil {
		placeCRS, err = geodata.ParseCRS(raw)
		if err != nil {
			return fmt.Errorf("place coordRefSys is invalid: %w", err)
		}
	}
	if raw := placeMembers["measures"]; raw != nil && !declarations[jsonFGMeasuresConformance] {
		return fmt.Errorf("place uses measures but root conformsTo omits %q", jsonFGMeasuresConformance)
	}
	if raw := placeMembers["measures"]; raw != nil {
		if err := validateJSONFGMeasures(raw); err != nil {
			return fmt.Errorf("place measures are invalid: %w", err)
		}
	}
	placeMeasures := feature.Foreign["measures"]
	if placeMembers["measures"] != nil {
		placeMeasures = placeMembers["measures"]
	}
	placeMeasuresEnabled, err := jsonFGMeasuresEnabled(placeMeasures)
	if err != nil {
		return err
	}
	expectedDimension := jsonFGCRSDimension(placeCRS)
	if placeMeasuresEnabled {
		expectedDimension++
	}
	if err := validateJSONFGGeometryDimensions(place, expectedDimension, true); err != nil {
		return fmt.Errorf("place coordinates are invalid for %s: %w", placeCRS, err)
	}
	if (placeCRS == geodata.CRSCRS84 || placeCRS == geodata.CRSCRS84h) && !placeMeasuresEnabled {
		return fmt.Errorf("place uses %s without measures; simple WGS 84 geometry belongs only in the GeoJSON geometry member", placeCRS)
	}
	if !placeMeasuresEnabled && !bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
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
		AllowOutOfRange:   placeCRS != geodata.CRSCRS84 && placeCRS != geodata.CRSCRS84h,
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

func convertJSONFGPlace(feature *geodata.Feature, rootCRS string, measures json.RawMessage) error {
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
	effectiveMeasures := measures
	if placeMembers["measures"] != nil {
		effectiveMeasures = placeMembers["measures"]
	}
	measuresEnabled, err := jsonFGMeasuresEnabled(effectiveMeasures)
	if err != nil {
		return err
	}
	if !measuresEnabled && !bytes.Equal(bytes.TrimSpace(feature.Geometry), []byte("null")) {
		return nil
	}
	delete(placeMembers, "coordRefSys")
	delete(placeMembers, "measures")
	plainPlace, err := json.Marshal(placeMembers)
	if err != nil {
		return err
	}
	var measureValues []json.RawMessage
	if measuresEnabled {
		plainPlace, measureValues, err = splitJSONFGMeasureGeometry(plainPlace, jsonFGCRSDimension(sourceCRS))
		if err != nil {
			return err
		}
	}
	geometry, err := geodata.DecodeGeomJSON(plainPlace)
	if err != nil {
		return err
	}
	targetCRS := geodata.CRSCRS84
	if jsonFGCRSDimension(sourceCRS) == 3 {
		targetCRS = geodata.CRSCRS84h
	}
	if _, err := geodata.TransformJSONFGGeometry(geometry, sourceCRS, targetCRS); err != nil {
		return err
	}
	feature.Geometry, err = geodata.EncodeGeomJSON(geometry)
	if err == nil && measuresEnabled {
		feature.Geometry, err = attachJSONFGMeasures(feature.Geometry, measureValues)
	}
	return err
}

func encodeJSONFGPlace(raw json.RawMessage, targetCRS string) (json.RawMessage, error) {
	geometry, err := geodata.DecodeGeomJSON(raw)
	if err != nil {
		return nil, err
	}
	sourceCRS := geodata.CRSCRS84
	if geometry.Stride() == 3 {
		sourceCRS = geodata.CRSCRS84h
	}
	if _, err := geodata.TransformJSONFGGeometry(geometry, sourceCRS, targetCRS); err != nil {
		return nil, fmt.Errorf("place reprojection from %s to %s failed: %w", sourceCRS, targetCRS, err)
	}
	encoded, err := geodata.EncodeGeomJSON(geometry)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func validateJSONFGMeasures(raw json.RawMessage) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return fmt.Errorf("measures must be an object: %w", err)
	}
	enabled, exists := members["enabled"]
	if !exists {
		return fmt.Errorf("measures is missing required enabled boolean")
	}
	var enabledValue bool
	if err := json.Unmarshal(enabled, &enabledValue); err != nil {
		return fmt.Errorf("measures enabled value %s is not a boolean", enabled)
	}
	for name, value := range members {
		switch name {
		case "enabled":
		case "unit", "description":
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return fmt.Errorf("measures %s value %s is not a string", name, value)
			}
		default:
			return fmt.Errorf("measures contains unknown member %q", name)
		}
	}
	return nil
}

func validateJSONFGClassMember(member string, raw json.RawMessage) error {
	if member == "featureType" {
		var featureType string
		if err := json.Unmarshal(raw, &featureType); err != nil || featureType == "" {
			return fmt.Errorf("featureType must be a non-empty string")
		}
		return nil
	}
	var schemaURI string
	if json.Unmarshal(raw, &schemaURI) == nil {
		return validateJSONFGSchemaURI(schemaURI)
	}
	var schemas map[string]string
	if err := json.Unmarshal(raw, &schemas); err != nil || len(schemas) == 0 {
		return fmt.Errorf("featureSchema must be an absolute URI or a non-empty object mapping feature types to absolute URIs")
	}
	for featureType, schema := range schemas {
		if featureType == "" {
			return fmt.Errorf("featureSchema contains an empty feature type")
		}
		if err := validateJSONFGSchemaURI(schema); err != nil {
			return fmt.Errorf("featureSchema type %q: %w", featureType, err)
		}
	}
	return nil
}

func validateJSONFGSchemaURI(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() {
		return fmt.Errorf("schema %q is not an absolute URI", value)
	}
	return nil
}

func jsonFGSchemaIsSingleURI(raw json.RawMessage) bool {
	var schemaURI string
	return raw != nil && json.Unmarshal(raw, &schemaURI) == nil
}

func jsonFGEffectiveFeatureType(raw, rootFeatureType json.RawMessage) (string, error) {
	var metadata struct {
		FeatureType json.RawMessage `json:"featureType"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return "", err
	}
	featureType := metadata.FeatureType
	if featureType == nil {
		featureType = rootFeatureType
	}
	if err := validateJSONFGClassMember("featureType", featureType); err != nil {
		return "", err
	}
	var value string
	if err := json.Unmarshal(featureType, &value); err != nil {
		return "", err
	}
	return value, nil
}

func validateJSONFGGeometryDimension(feature geodata.Feature, expected int) error {
	geometry := feature.Geometry
	if place := feature.Foreign["place"]; place != nil && !bytes.Equal(bytes.TrimSpace(place), []byte("null")) {
		geometry = place
	}
	if geometry == nil || bytes.Equal(bytes.TrimSpace(geometry), []byte("null")) {
		return nil
	}
	var metadata struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(geometry, &metadata); err != nil {
		return err
	}
	dimensions := map[string]int{
		"Point": 0, "MultiPoint": 0,
		"LineString": 1, "MultiLineString": 1,
		"Polygon": 2, "MultiPolygon": 2,
	}
	actual, exists := dimensions[metadata.Type]
	if !exists {
		return fmt.Errorf("primary geometry type %q has no supported geometry-dimension classification", metadata.Type)
	}
	if actual != expected {
		return fmt.Errorf("primary geometry type %q has dimension %d", metadata.Type, actual)
	}
	return nil
}

func jsonFGFeatureMeasures(feature geodata.Feature) (json.RawMessage, error) {
	measures := feature.Foreign["measures"]
	place := feature.Foreign["place"]
	if place == nil || bytes.Equal(bytes.TrimSpace(place), []byte("null")) {
		return measures, nil
	}
	var placeMembers map[string]json.RawMessage
	if err := json.Unmarshal(place, &placeMembers); err != nil {
		return nil, err
	}
	placeMeasures := placeMembers["measures"]
	if placeMeasures != nil {
		return placeMeasures, nil
	}
	return measures, nil
}

func restoreJSONFGProperty(feature *geodata.Feature, name string, raw json.RawMessage) error {
	properties, err := feature.PropertyMap()
	if err != nil {
		return err
	}
	if existing := properties[name]; existing != nil && !equalCompactJSON(existing, raw) {
		return fmt.Errorf("existing value %s conflicts with JSON-FG value %s", existing, raw)
	}
	properties[name] = raw
	return feature.SetPropertyMap(properties)
}

func equalCompactJSON(first, second json.RawMessage) bool {
	var firstBuffer, secondBuffer bytes.Buffer
	if json.Compact(&firstBuffer, first) != nil || json.Compact(&secondBuffer, second) != nil {
		return false
	}
	return bytes.Equal(firstBuffer.Bytes(), secondBuffer.Bytes())
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
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return fmt.Errorf("time must be an object: %w", err)
	}
	if members == nil {
		return fmt.Errorf("time must be an object, received null")
	}
	for name := range members {
		if name != "date" && name != "timestamp" && name != "interval" {
			return fmt.Errorf("time contains unknown member %q", name)
		}
	}
	var value jsonFGTimeValue
	if rawDate, exists := members["date"]; exists {
		if err := json.Unmarshal(rawDate, &value.Date); err != nil || value.Date == "" {
			return fmt.Errorf("time date %s is not a non-empty string", rawDate)
		}
	}
	if rawTimestamp, exists := members["timestamp"]; exists {
		if err := json.Unmarshal(rawTimestamp, &value.Timestamp); err != nil || value.Timestamp == "" {
			return fmt.Errorf("time timestamp %s is not a non-empty string", rawTimestamp)
		}
	}
	if rawInterval, exists := members["interval"]; exists {
		if err := json.Unmarshal(rawInterval, &value.Interval); err != nil || value.Interval == nil {
			return fmt.Errorf("time interval %s is not an array", rawInterval)
		}
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
