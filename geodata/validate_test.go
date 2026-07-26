package geodata

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateFeatureRejectsInvalidData(t *testing.T) {
	cases := map[string]string{
		"null id":        `{"type":"Feature","id":null,"geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}`,
		"missing props":  `{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]}}`,
		"bad longitude":  `{"type":"Feature","geometry":{"type":"Point","coordinates":[181,2]},"properties":{}}`,
		"short line":     `{"type":"Feature","geometry":{"type":"LineString","coordinates":[[1,2]]},"properties":{}}`,
		"open polygon":   `{"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1]]]},"properties":{}}`,
		"unknown shape":  `{"type":"Feature","geometry":{"type":"Circle","coordinates":[1,2]},"properties":{}}`,
		"bad properties": `{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":[]}`,
		"bbox dimension": `{"type":"Feature","bbox":[1,2,3,4,5,6],"geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}`,
		"bbox latitude":  `{"type":"Feature","bbox":[1,5,3,4],"geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}`,
		"bbox excludes":  `{"type":"Feature","bbox":[0,0,1,1],"geometry":{"type":"Point","coordinates":[2,2]},"properties":{}}`,
		"wrapped bbox":   `{"type":"Feature","bbox":[170,-10,-170,10],"geometry":{"type":"Point","coordinates":[0,0]},"properties":{}}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			var feature Feature
			if err := json.Unmarshal([]byte(input), &feature); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateFeature(feature, ValidationOptions{}); err == nil {
				t.Fatalf("accepted invalid Feature %s", input)
			}
		})
	}
}

func TestValidateFeatureOptions(t *testing.T) {
	var nullGeometry Feature
	if err := json.Unmarshal([]byte(`{"type":"Feature","geometry":null,"properties":{}}`), &nullGeometry); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFeature(nullGeometry, ValidationOptions{}); err == nil {
		t.Fatal("accepted null geometry without the option")
	}
	nullSummary, err := ValidateFeature(nullGeometry, ValidationOptions{AllowNullGeometry: true})
	if err != nil {
		t.Fatal(err)
	}
	if nullSummary.Type != "null" {
		t.Fatalf("null geometry summary type is %q; expected null", nullSummary.Type)
	}
	var projected Feature
	if err := json.Unmarshal([]byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[500000,4500000]},"properties":{}}`), &projected); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFeature(projected, ValidationOptions{AllowOutOfRange: true}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateFeatureAcceptsArbitrarilyLargeNumericID(t *testing.T) {
	var feature Feature
	if err := json.Unmarshal([]byte(`{"type":"Feature","id":1e400,"geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}`), &feature); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFeature(feature, ValidationOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateCoordinateDimensions(t *testing.T) {
	var threeDimensional Feature
	if err := json.Unmarshal([]byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2,3]},"properties":{}}`), &threeDimensional); err != nil {
		t.Fatal(err)
	}
	summary, err := ValidateFeature(threeDimensional, ValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.CoordinateDimension != 3 {
		t.Fatalf("coordinate dimension is %d; expected 3", summary.CoordinateDimension)
	}
	var mixed Feature
	if err := json.Unmarshal([]byte(`{"type":"Feature","geometry":{"type":"LineString","coordinates":[[1,2],[3,4,5]]},"properties":{}}`), &mixed); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateFeature(mixed, ValidationOptions{}); err == nil {
		t.Fatal("accepted geometry with mixed coordinate dimensions")
	}
}

func TestValidateHigherDimensionalAndAntimeridianBBoxes(t *testing.T) {
	for _, input := range []string{
		`{"type":"Feature","bbox":[1,2,3,4,1,2,3,4],"geometry":{"type":"Point","coordinates":[1,2,3,4]},"properties":{}}`,
		`{"type":"Feature","bbox":[177,-20,-178,-16],"geometry":{"type":"LineString","coordinates":[[179,-18],[-179,-17]]},"properties":{}}`,
	} {
		var feature Feature
		if err := json.Unmarshal([]byte(input), &feature); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateFeature(feature, ValidationOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidateGeometryCollectionSummary(t *testing.T) {
	input := `{"type":"Feature","geometry":{"type":"GeometryCollection","geometries":[{"type":"Point","coordinates":[1,2]},{"type":"LineString","coordinates":[[3,4],[5,6]]}]},"properties":{}}`
	var feature Feature
	if err := json.Unmarshal([]byte(input), &feature); err != nil {
		t.Fatal(err)
	}
	summary, err := ValidateFeature(feature, ValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Type != "GeometryCollection" || summary.PositionCount != 3 || summary.Bounds != [4]float64{1, 2, 5, 6} {
		t.Fatalf("unexpected GeometryCollection summary: %#v", summary)
	}
}

func TestPropertyMapPreservesJSONTypes(t *testing.T) {
	input := `{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"number":42,"boolean":true,"null":null,"object":{"x":1},"array":[1,"two"]}}`
	var feature Feature
	if err := json.Unmarshal([]byte(input), &feature); err != nil {
		t.Fatal(err)
	}
	properties, err := feature.PropertyMap()
	if err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{"number": "42", "boolean": "true", "null": "null", "object": `{"x":1}`, "array": `[1,"two"]`} {
		if strings.TrimSpace(string(properties[key])) != expected {
			t.Fatalf("property %q is %s; expected %s", key, properties[key], expected)
		}
	}
}
