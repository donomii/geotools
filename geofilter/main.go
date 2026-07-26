package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/donomii/geotools/geodata"
)

var errFilterLimitReached = errors.New("geofilter output limit reached")

type filterSettings struct {
	GeometryTypes map[string]bool
	Required      []string
	Expected      map[string]json.RawMessage
	Selected      map[string]bool
	Dropped       map[string]bool
	BBox          [4]float64
	HasBBox       bool
	Limit         int64
	AllowNull     bool
}

func filterGeoJSON(inputMode geodata.InputMode, outputMode geodata.OutputMode, settings filterSettings, input io.Reader, output io.Writer) error {
	writer := geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	readErr := geodata.ReadFeatures(input, inputMode, func(feature geodata.Feature) error {
		if settings.Limit >= 0 && writer.FeatureCount() >= settings.Limit {
			return errFilterLimitReached
		}
		summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{AllowNullGeometry: settings.AllowNull})
		if err != nil {
			return err
		}
		if len(settings.GeometryTypes) > 0 && !settings.GeometryTypes[summary.Type] {
			return nil
		}
		if settings.HasBBox && (!summary.HasBounds || !boundsIntersect(summary.Bounds, settings.BBox)) {
			return nil
		}
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		if !propertiesMatch(properties, settings.Required, settings.Expected) {
			return nil
		}
		if len(settings.Selected) > 0 || len(settings.Dropped) > 0 {
			if len(settings.Selected) > 0 {
				properties = selectProperties(properties, settings.Selected)
			}
			for key := range settings.Dropped {
				delete(properties, key)
			}
			if err := feature.SetPropertyMap(properties); err != nil {
				return err
			}
		}
		return writer.Write(feature)
	})
	if errors.Is(readErr, errFilterLimitReached) {
		readErr = nil
	}
	closeErr := writer.Close()
	return errors.Join(readErr, closeErr)
}

func propertiesMatch(properties map[string]json.RawMessage, required []string, expected map[string]json.RawMessage) bool {
	for _, key := range required {
		if _, exists := properties[key]; !exists {
			return false
		}
	}
	for key, expectedValue := range expected {
		actual, exists := properties[key]
		if !exists || !equalJSONValue(actual, expectedValue) {
			return false
		}
	}
	return true
}

func equalJSONValue(first, second json.RawMessage) bool {
	first = bytes.TrimSpace(first)
	second = bytes.TrimSpace(second)
	if len(first) == 0 || len(second) == 0 || first[0] != second[0] {
		return false
	}
	switch first[0] {
	case '{':
		var firstMembers, secondMembers map[string]json.RawMessage
		if json.Unmarshal(first, &firstMembers) != nil || json.Unmarshal(second, &secondMembers) != nil || len(firstMembers) != len(secondMembers) {
			return false
		}
		for name, firstValue := range firstMembers {
			secondValue, exists := secondMembers[name]
			if !exists || !equalJSONValue(firstValue, secondValue) {
				return false
			}
		}
		return true
	case '[':
		var firstValues, secondValues []json.RawMessage
		if json.Unmarshal(first, &firstValues) != nil || json.Unmarshal(second, &secondValues) != nil || len(firstValues) != len(secondValues) {
			return false
		}
		for index := range firstValues {
			if !equalJSONValue(firstValues[index], secondValues[index]) {
				return false
			}
		}
		return true
	case '"':
		var firstText, secondText string
		return json.Unmarshal(first, &firstText) == nil && json.Unmarshal(second, &secondText) == nil && firstText == secondText
	default:
		return bytes.Equal(first, second)
	}
}

func compactJSON(value json.RawMessage) json.RawMessage {
	var output bytes.Buffer
	if json.Compact(&output, value) != nil {
		return value
	}
	return output.Bytes()
}

func selectProperties(properties map[string]json.RawMessage, selected map[string]bool) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage)
	for key := range selected {
		if value, exists := properties[key]; exists {
			result[key] = value
		}
	}
	return result
}

func boundsIntersect(first, second [4]float64) bool {
	if first[1] > second[3] || first[3] < second[1] {
		return false
	}
	if first[0] > first[2] && second[0] > second[2] {
		return true
	}
	if first[0] > first[2] {
		return second[2] >= first[0] || second[0] <= first[2]
	}
	if second[0] > second[2] {
		return first[2] >= second[0] || first[0] <= second[2]
	}
	return first[0] <= second[2] && first[2] >= second[0]
}

func parseList(value string) (map[string]bool, error) {
	result := make(map[string]bool)
	if value == "" {
		return result, nil
	}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("list %q contains an empty item", value)
		}
		result[item] = true
	}
	return result, nil
}

func validateGeometryTypes(types map[string]bool, allowNull bool) error {
	valid := map[string]bool{
		"Point": true, "MultiPoint": true, "LineString": true, "MultiLineString": true,
		"Polygon": true, "MultiPolygon": true, "GeometryCollection": true,
	}
	if allowNull {
		valid["null"] = true
	}
	expected := "a GeoJSON geometry type"
	if allowNull {
		expected += " or null"
	}
	for geometryType := range types {
		if !valid[geometryType] {
			return fmt.Errorf("geometry type %q is unsupported; expected %s", geometryType, expected)
		}
	}
	return nil
}

func parseWhere(value string) (map[string]json.RawMessage, error) {
	result := make(map[string]json.RawMessage)
	if value == "" {
		return result, nil
	}
	conditions, err := splitWhereConditions(value)
	if err != nil {
		return nil, err
	}
	for _, condition := range conditions {
		parts := strings.SplitN(condition, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("condition %q must have the form property=value", condition)
		}
		key := strings.TrimSpace(parts[0])
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("property %q has more than one condition", key)
		}
		expected := strings.TrimSpace(parts[1])
		if expected == "" {
			return nil, fmt.Errorf("condition %q has an empty expected value", condition)
		}
		raw := json.RawMessage(expected)
		if !json.Valid(raw) {
			encoded, err := json.Marshal(expected)
			if err != nil {
				return nil, err
			}
			raw = encoded
		}
		result[key] = compactJSON(raw)
	}
	return result, nil
}

func splitWhereConditions(value string) ([]string, error) {
	var conditions []string
	var brackets []byte
	start := 0
	inString := false
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if inString {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '[', '{':
			brackets = append(brackets, character)
		case ']', '}':
			if len(brackets) == 0 {
				return nil, fmt.Errorf("conditions %q contain unmatched %q", value, character)
			}
			open := brackets[len(brackets)-1]
			if (open == '[' && character != ']') || (open == '{' && character != '}') {
				return nil, fmt.Errorf("conditions %q close %q with %q", value, open, character)
			}
			brackets = brackets[:len(brackets)-1]
		case ',':
			if len(brackets) == 0 {
				conditions = append(conditions, value[start:index])
				start = index + 1
			}
		}
	}
	if inString {
		return nil, fmt.Errorf("conditions %q contain an unterminated JSON string", value)
	}
	if len(brackets) > 0 {
		return nil, fmt.Errorf("conditions %q contain unclosed %q", value, brackets[len(brackets)-1])
	}
	return append(conditions, value[start:]), nil
}

func parseBBox(value string) ([4]float64, bool, error) {
	var result [4]float64
	if value == "" {
		return result, false, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) != 4 {
		return result, false, fmt.Errorf("bbox %q has %d values; expected minLon,minLat,maxLon,maxLat", value, len(parts))
	}
	for index, part := range parts {
		number, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return result, false, fmt.Errorf("bbox value %d %q is not a number: %w", index+1, part, err)
		}
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return result, false, fmt.Errorf("bbox value %d %q is not finite", index+1, part)
		}
		result[index] = number
	}
	if result[0] < -180 || result[0] > 180 || result[2] < -180 || result[2] > 180 {
		return result, false, fmt.Errorf("bbox %q has longitude outside -180 through 180", value)
	}
	if result[1] < -90 || result[1] > 90 || result[3] < -90 || result[3] > 90 {
		return result, false, fmt.Errorf("bbox %q has latitude outside -90 through 90", value)
	}
	if result[1] > result[3] {
		return result, false, fmt.Errorf("bbox %q has minimum latitude greater than maximum latitude", value)
	}
	return result, true, nil
}

func runBuiltInTest() error {
	expected, _ := parseWhere("kind=city")
	settings := filterSettings{Required: []string{"name"}, Expected: expected, GeometryTypes: map[string]bool{"Point": true}, Limit: -1}
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"name":"A","kind":"city"}},{"type":"Feature","geometry":{"type":"Point","coordinates":[3,4]},"properties":{"kind":"town"}}]}`)
	var output bytes.Buffer
	if err := filterGeoJSON(geodata.InputAuto, geodata.OutputJSONL, settings, input, &output); err != nil {
		return err
	}
	count := 0
	if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
		count++
		return nil
	}); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("filter emitted %d Features; expected 1", count)
	}
	return nil
}

func main() {
	inputName := flag.String("input", "auto", "Input format: auto detects JSONL, arrays, FeatureCollections, and RFC 8142 sequences; seq requires record separators")
	outputName := flag.String("output", "jsonl", "Output format: jsonl writes one Feature per line, collection writes one FeatureCollection, and seq writes RFC 8142 records")
	geometryValue := flag.String("geometry", "", "Comma-separated geometry types to retain, such as Point,LineString; empty retains every type")
	hasValue := flag.String("has", "", "Comma-separated property names that must all exist; empty does not require properties")
	whereValue := flag.String("where", "", "Comma-separated exact property conditions such as kind=city,population=42; JSON numbers, booleans, and null retain their types")
	selectValue := flag.String("select", "", "Comma-separated properties to keep in emitted Features; empty keeps every property")
	dropValue := flag.String("drop", "", "Comma-separated properties to remove from emitted Features; cannot be combined with -select")
	bboxValue := flag.String("bbox", "", "Retain geometries intersecting minLon,minLat,maxLon,maxLat; minLon greater than maxLon crosses the antimeridian, and empty disables spatial filtering")
	limit := flag.Int64("limit", -1, "Stop after emitting exactly this many Features; -1 has no limit and 0 emits none")
	allowNull := flag.Bool("allow-null-geometry", false, "Accept null geometries; they are retained only when no geometry or bbox filter excludes them")
	runTest := flag.Bool("test", false, "Run a built-in property and geometry filter check and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("geofilter reads standard input and writes standard output; positional arguments are not accepted")
	}
	if *runTest {
		if err := runBuiltInTest(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("geofilter built-in test passed")
		return
	}
	if *limit < -1 {
		log.Fatal("invalid limit: expected -1 or a non-negative integer")
	}
	inputMode, err := geodata.ParseInputMode(*inputName)
	if err != nil {
		log.Fatal(err)
	}
	outputMode, err := geodata.ParseOutputMode(*outputName)
	if err != nil {
		log.Fatal(err)
	}
	geometryTypes, err := parseList(*geometryValue)
	if err != nil {
		log.Fatal(err)
	}
	if err := validateGeometryTypes(geometryTypes, *allowNull); err != nil {
		log.Fatal(err)
	}
	requiredMap, err := parseList(*hasValue)
	if err != nil {
		log.Fatal(err)
	}
	selected, err := parseList(*selectValue)
	if err != nil {
		log.Fatal(err)
	}
	dropped, err := parseList(*dropValue)
	if err != nil {
		log.Fatal(err)
	}
	if len(selected) > 0 && len(dropped) > 0 {
		log.Fatal("-select and -drop cannot be combined because one keeps a property set while the other removes a property set")
	}
	expected, err := parseWhere(*whereValue)
	if err != nil {
		log.Fatal(err)
	}
	bbox, hasBBox, err := parseBBox(*bboxValue)
	if err != nil {
		log.Fatal(err)
	}
	required := make([]string, 0, len(requiredMap))
	for key := range requiredMap {
		required = append(required, key)
	}
	settings := filterSettings{
		GeometryTypes: geometryTypes,
		Required:      required,
		Expected:      expected,
		Selected:      selected,
		Dropped:       dropped,
		BBox:          bbox,
		HasBBox:       hasBBox,
		Limit:         *limit,
		AllowNull:     *allowNull,
	}
	if err := filterGeoJSON(inputMode, outputMode, settings, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
