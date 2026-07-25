package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/donomii/geotools/geodata"
)

func TestJSONFGRealDataRoundTrip(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	var jsonFG bytes.Buffer
	if err := encodeJSONFG(bytes.NewReader(data), &jsonFG, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	if err := requireCoreConformance(jsonFG.Bytes()); err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(jsonFG.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	if string(root["type"]) != `"FeatureCollection"` {
		t.Fatalf("JSON-FG root type is %s", root["type"])
	}
	var output bytes.Buffer
	if err := decodeJSONFG(&jsonFG, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(jsonFGFeatures(t, data), jsonFGFeatures(t, output.Bytes())) {
		t.Fatal("JSON-FG round trip changed Feature content")
	}
}

func TestJSONFGRejectsMissingCoreDeclaration(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[]}`)
	var output bytes.Buffer
	if err := decodeJSONFG(input, &output, geodata.OutputJSONL); err == nil {
		t.Fatal("accepted JSON-FG without the core declaration")
	}
}

func TestJSONFGStreamingRequiresMetadataBeforeFeatures(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}],"conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"]}`)
	var output bytes.Buffer
	err := decodeJSONFG(input, &output, geodata.OutputJSONL)
	if err == nil || !strings.Contains(err.Error(), `requires root member "conformsTo" before features`) {
		t.Fatalf("out-of-order streaming JSON-FG decode returned %v", err)
	}
}

func TestDecodeJSONFGRootFeature(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"coordRefSys":"http://www.opengis.net/def/crs/EPSG/0/4326","geometry":null,"place":{"type":"Point","coordinates":[1.3521,103.8198]},"properties":{"name":"Singapore"}}`)
	var output bytes.Buffer
	if err := decodeJSONFG(input, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, output.Bytes())
	if len(features) != 1 || strings.Contains(features[0], "conformsTo") || strings.Contains(features[0], "coordRefSys") {
		t.Fatalf("root JSON-FG Feature decoded as %s", output.Bytes())
	}
}

func TestJSONFGDefaultEncodingOmitsDuplicatePlace(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"name":"Singapore"}}`)
	var output bytes.Buffer
	if err := encodeJSONFG(input, &output, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(output.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	if containsJSONFGString(root.ConformsTo, jsonFGTypesSchemaConformance) {
		t.Fatalf("untyped JSON-FG declares type/schema conformance: %v", root.ConformsTo)
	}
	var feature geodata.Feature
	if err := json.Unmarshal(root.Features[0], &feature); err != nil {
		t.Fatal(err)
	}
	if feature.Foreign["place"] != nil {
		t.Fatalf("default JSON-FG duplicated simple WGS 84 geometry in place: %s", feature.Foreign["place"])
	}
}

func TestJSONFGStreamingTypeDeclarationRequiresEveryFeatureType(t *testing.T) {
	inputs := []string{
		`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"jsonfg_feature_type":"first"}},{"type":"Feature","geometry":{"type":"Point","coordinates":[3,4]},"properties":{}}]}`,
		`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}},{"type":"Feature","geometry":{"type":"Point","coordinates":[3,4]},"properties":{"jsonfg_feature_type":"second"}}]}`,
		`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"jsonfg_feature_schema":"https://example.test/schema"}}`,
	}
	for _, input := range inputs {
		if err := encodeJSONFG(strings.NewReader(input), &bytes.Buffer{}, geodata.InputAuto); err == nil {
			t.Fatalf("accepted inconsistent type/schema input %s", input)
		}
	}
}

func TestJSONFGEncodeRejectsExistingExtensionMembers(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{},"time":{"date":"2026-01-01"}}`)
	var output bytes.Buffer
	if err := encodeJSONFG(input, &output, geodata.InputAuto); err == nil {
		t.Fatal("accepted a Feature that already contains JSON-FG members")
	}
}

func TestJSONFGConvertsProjectedPlaceAndTime(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"coordRefSys":"http://www.opengis.net/def/crs/EPSG/0/3857","features":[{"type":"Feature","id":"projected","geometry":null,"place":{"type":"Point","coordinates":[11557101.59,150541.27]},"time":{"timestamp":"2026-07-25T04:00:00Z"},"properties":{"name":"Singapore"}}]}`)
	var output bytes.Buffer
	if err := decodeJSONFG(input, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, output.Bytes())
	if len(features) != 1 {
		t.Fatalf("decoded %d Features; expected 1", len(features))
	}
	var feature geodata.Feature
	if err := json.Unmarshal([]byte(features[0]), &feature); err != nil {
		t.Fatal(err)
	}
	summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.HasBounds || summary.Bounds[0] < 103.81 || summary.Bounds[0] > 103.83 {
		t.Fatalf("projected place became bounds %v", summary.Bounds)
	}
	properties, err := feature.PropertyMap()
	if err != nil {
		t.Fatal(err)
	}
	if string(properties[defaultJSONFGTimePropertyName]) != `{"timestamp":"2026-07-25T04:00:00Z"}` {
		t.Fatalf("decoded time property is %s", properties[defaultJSONFGTimePropertyName])
	}
}

func TestJSONFGEncodeProjectsPlaceAndMapsTime(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"jsonfg_time":"2026-07-25"}}`)
	var output bytes.Buffer
	settings := jsonFGSettings{PlaceCRS: geodata.CRSEPSG3857, TimeProperty: defaultJSONFGTimePropertyName}
	if err := encodeJSONFGWithSettings(input, &output, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(output.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	var feature geodata.Feature
	if err := json.Unmarshal(root.Features[0], &feature); err != nil {
		t.Fatal(err)
	}
	var place struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Foreign["place"], &place); err != nil {
		t.Fatal(err)
	}
	if place.Coordinates[0] < 11557000 || string(feature.Foreign["time"]) != `{"date":"2026-07-25"}` {
		t.Fatalf("projected place is %v and time is %s", place.Coordinates, feature.Foreign["time"])
	}
	var decoded bytes.Buffer
	if err := decodeJSONFGWithSettings(&output, &decoded, geodata.OutputCollection, settings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(jsonFGFeatures(t, []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"jsonfg_time":"2026-07-25"}}`)), jsonFGFeatures(t, decoded.Bytes())) {
		t.Fatalf("JSON-FG temporal round trip changed the source Feature: %s", decoded.Bytes())
	}
}

func TestJSONFGEPSG4326AxisOrderRoundTrip(t *testing.T) {
	input := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"name":"Singapore"}}`)
	var encoded bytes.Buffer
	settings := jsonFGSettings{PlaceCRS: geodata.CRSEPSG4326, TimeProperty: defaultJSONFGTimePropertyName}
	if err := encodeJSONFGWithSettings(bytes.NewReader(input), &encoded, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(encoded.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	var feature geodata.Feature
	if err := json.Unmarshal(root.Features[0], &feature); err != nil {
		t.Fatal(err)
	}
	var place struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Foreign["place"], &place); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(place.Coordinates, []float64{1.3521, 103.8198}) {
		t.Fatalf("EPSG:4326 place coordinates are %v; expected latitude then longitude", place.Coordinates)
	}
	feature.Geometry = json.RawMessage("null")
	root.Features[0], _ = json.Marshal(feature)
	encoded.Reset()
	if err := json.NewEncoder(&encoded).Encode(root); err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	if err := decodeJSONFGWithSettings(&encoded, &decoded, geodata.OutputCollection, settings); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, decoded.Bytes())
	if len(features) != 1 || !strings.Contains(features[0], `[103.8198,1.3521]`) {
		t.Fatalf("EPSG:4326 place decoded as %s", decoded.Bytes())
	}
}

func TestJSONFGMeasuresAndTypeSchemaRoundTrip(t *testing.T) {
	input := []byte(`{"type":"Feature","id":"station","geometry":{"type":"Point","coordinates":[103.8198,1.3521,12.5]},"properties":{"name":"Station","jsonfg_measures":{"enabled":true,"unit":"m","description":"Distance along platform"},"jsonfg_feature_type":"transit-station","jsonfg_feature_schema":"https://example.test/schemas/station"}}`)
	var encoded bytes.Buffer
	if err := encodeJSONFG(bytes.NewReader(input), &encoded, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(encoded.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	if !containsJSONFGString(root.ConformsTo, jsonFGMeasuresConformance) || !containsJSONFGString(root.ConformsTo, jsonFGTypesSchemaConformance) {
		t.Fatalf("JSON-FG conformance declarations are %v", root.ConformsTo)
	}
	var encodedFeature geodata.Feature
	if err := json.Unmarshal(root.Features[0], &encodedFeature); err != nil {
		t.Fatal(err)
	}
	if encodedFeature.Foreign["featureType"] == nil || encodedFeature.Foreign["featureSchema"] == nil {
		t.Fatalf("encoded type/schema members are %v", encodedFeature.Foreign)
	}
	if !bytes.Equal(encodedFeature.Geometry, []byte("null")) {
		t.Fatalf("measured JSON-FG fallback geometry is %s; expected null", encodedFeature.Geometry)
	}
	if err := validateJSONFGMeasures(encodedFeature.Foreign["measures"]); err != nil {
		t.Fatalf("encoded Feature measures are %s: %v", encodedFeature.Foreign["measures"], err)
	}
	var place struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(encodedFeature.Foreign["place"], &place); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(place.Coordinates, []float64{103.8198, 1.3521, 12.5}) {
		t.Fatalf("encoded measured place coordinates are %v", place.Coordinates)
	}
	var decoded bytes.Buffer
	if err := decodeJSONFG(&encoded, &decoded, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, decoded.Bytes())
	if len(features) != 1 {
		t.Fatalf("JSON-FG measures/type/schema round trip returned %d Features", len(features))
	}
	var decodedFeature geodata.Feature
	if err := json.Unmarshal([]byte(features[0]), &decodedFeature); err != nil {
		t.Fatal(err)
	}
	properties, err := decodedFeature.PropertyMap()
	if err != nil {
		t.Fatal(err)
	}
	if string(properties["name"]) != `"Station"` || string(properties[defaultJSONFGFeatureTypePropertyName]) != `"transit-station"` ||
		string(properties[defaultJSONFGFeatureSchemaPropertyName]) != `"https://example.test/schemas/station"` ||
		validateJSONFGMeasures(properties[defaultJSONFGMeasuresPropertyName]) != nil {
		t.Fatalf("restored JSON-FG properties are %v", properties)
	}
	if !strings.Contains(string(decodedFeature.Geometry), `[103.8198,1.3521,12.5]`) {
		t.Fatalf("restored measured geometry is %s", decodedFeature.Geometry)
	}
}

func TestJSONFGDecodesOfficialStyleMeasuredLineString(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","id":"road","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core","http://www.opengis.net/spec/json-fg-1/1.0/conf/measures"],"coordRefSys":"http://www.opengis.net/def/crs/OGC/0/CRS84","measures":{"enabled":true,"unit":"km","description":"Distance along road"},"geometry":null,"place":{"type":"LineString","coordinates":[[8.1,50.1,0],[8.2,50.2,12.75]]},"properties":{"name":"A road"}}`)
	var output bytes.Buffer
	if err := decodeJSONFG(input, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, output.Bytes())
	if len(features) != 1 || !strings.Contains(features[0], `[[8.1,50.1,0],[8.2,50.2,12.75]]`) ||
		!strings.Contains(features[0], `"jsonfg_measures":{"enabled":true,"unit":"km","description":"Distance along road"}`) {
		t.Fatalf("measured JSON-FG decoded as %s", output.Bytes())
	}
}

func TestJSONFGCollectionMeasuresApplyToFeatures(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core","http://www.opengis.net/spec/json-fg-1/1.0/conf/measures"],"coordRefSys":"http://www.opengis.net/def/crs/OGC/0/CRS84","measures":{"enabled":true,"unit":"m"},"features":[{"type":"Feature","geometry":null,"place":{"type":"Point","coordinates":[1,2,3]},"properties":{}},{"type":"Feature","measures":{"enabled":false},"geometry":{"type":"Point","coordinates":[4,5]},"properties":{}}]}`)
	var output bytes.Buffer
	if err := decodeJSONFG(input, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, output.Bytes())
	if len(features) != 2 || !strings.Contains(features[0], `[1,2,3]`) ||
		!strings.Contains(features[0], `"jsonfg_measures":{"enabled":true,"unit":"m"}`) ||
		!strings.Contains(features[1], `"jsonfg_measures":{"enabled":false}`) {
		t.Fatalf("collection measures decoded as %s", output.Bytes())
	}
}

func TestJSONFGMeasuredGeometryCollectionRoundTrip(t *testing.T) {
	input := []byte(`{"type":"Feature","geometry":{"type":"GeometryCollection","geometries":[{"type":"Point","coordinates":[1,2,10]},{"type":"LineString","coordinates":[[3,4,20],[5,6,30]]}]},"properties":{"jsonfg_measures":{"enabled":true}}}`)
	var encoded bytes.Buffer
	if err := encodeJSONFG(bytes.NewReader(input), &encoded, geodata.InputAuto); err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	if err := decodeJSONFG(&encoded, &decoded, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, decoded.Bytes())
	if len(features) != 1 || !strings.Contains(features[0], `[1,2,10]`) ||
		!strings.Contains(features[0], `[[3,4,20],[5,6,30]]`) ||
		!strings.Contains(features[0], `"jsonfg_measures":{"enabled":true}`) {
		t.Fatalf("measured GeometryCollection round trip changed Feature content: %s", decoded.Bytes())
	}
}

func TestJSONFGThreeDimensionalMeasuresRoundTrip(t *testing.T) {
	input := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521,18.25,44]},"properties":{"jsonfg_measures":{"enabled":true,"unit":"m"}}}`)
	settings := jsonFGSettings{PlaceCRS: geodata.CRSCRS84h, MeasuresProperty: defaultJSONFGMeasuresPropertyName}
	var encoded bytes.Buffer
	if err := encodeJSONFGWithSettings(bytes.NewReader(input), &encoded, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(encoded.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	var feature geodata.Feature
	if err := json.Unmarshal(root.Features[0], &feature); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(feature.Foreign["place"]), `[103.8198,1.3521,18.25,44]`) {
		t.Fatalf("3D measured place is %s", feature.Foreign["place"])
	}
	var decoded bytes.Buffer
	if err := decodeJSONFGWithSettings(&encoded, &decoded, geodata.OutputCollection, settings); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded.String(), `[103.8198,1.3521,18.25,44]`) {
		t.Fatalf("3D measured round trip returned %s", decoded.Bytes())
	}
}

func TestJSONFGMeasuredEPSG4326UsesLatitudeLongitudeMeasureOrder(t *testing.T) {
	input := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521,7]},"properties":{"jsonfg_measures":{"enabled":true}}}`)
	settings := jsonFGSettings{PlaceCRS: geodata.CRSEPSG4326, MeasuresProperty: defaultJSONFGMeasuresPropertyName}
	var encoded bytes.Buffer
	if err := encodeJSONFGWithSettings(bytes.NewReader(input), &encoded, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(encoded.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	var feature geodata.Feature
	if err := json.Unmarshal(root.Features[0], &feature); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(feature.Foreign["place"]), `[1.3521,103.8198,7]`) {
		t.Fatalf("measured EPSG:4326 place is %s", feature.Foreign["place"])
	}
	var decoded bytes.Buffer
	if err := decodeJSONFGWithSettings(&encoded, &decoded, geodata.OutputCollection, settings); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded.String(), `[103.8198,1.3521,7]`) {
		t.Fatalf("measured EPSG:4326 round trip returned %s", decoded.Bytes())
	}
}

func TestJSONFGMeasuredUTMRoundTrip(t *testing.T) {
	input := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[103.8198,1.3521,7]},"properties":{"jsonfg_measures":{"enabled":true,"unit":"km"}}}`)
	settings := jsonFGSettings{PlaceCRS: "EPSG:32648", MeasuresProperty: defaultJSONFGMeasuresPropertyName}
	var encoded bytes.Buffer
	if err := encodeJSONFGWithSettings(bytes.NewReader(input), &encoded, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	var root jsonFGRoot
	if err := json.Unmarshal(encoded.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	var feature geodata.Feature
	if err := json.Unmarshal(root.Features[0], &feature); err != nil {
		t.Fatal(err)
	}
	var place struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(feature.Foreign["place"], &place); err != nil {
		t.Fatal(err)
	}
	if len(place.Coordinates) != 3 || place.Coordinates[0] < 360000 || place.Coordinates[0] > 380000 ||
		place.Coordinates[1] < 140000 || place.Coordinates[1] > 160000 || place.Coordinates[2] != 7 {
		t.Fatalf("measured UTM place coordinates are %v", place.Coordinates)
	}
	var decoded bytes.Buffer
	if err := decodeJSONFGWithSettings(&encoded, &decoded, geodata.OutputCollection, settings); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, decoded.Bytes())
	if len(features) != 1 {
		t.Fatalf("measured UTM round trip returned %d Features: %s", len(features), decoded.Bytes())
	}
	var roundTripped geodata.Feature
	if err := json.Unmarshal([]byte(features[0]), &roundTripped); err != nil {
		t.Fatal(err)
	}
	var geometry struct {
		Coordinates []float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(roundTripped.Geometry, &geometry); err != nil {
		t.Fatal(err)
	}
	if len(geometry.Coordinates) != 3 || math.Abs(geometry.Coordinates[0]-103.8198) > 1e-8 ||
		math.Abs(geometry.Coordinates[1]-1.3521) > 1e-8 || geometry.Coordinates[2] != 7 {
		t.Fatalf("measured UTM round trip coordinates are %v", geometry.Coordinates)
	}
}

func TestJSONFGRestoresRootTypeAndSchemaClasses(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core","http://www.opengis.net/spec/json-fg-1/1.0/conf/types-schemas"],"featureType":"building","featureSchema":{"building":"https://example.test/schemas/building"},"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}]}`)
	var output bytes.Buffer
	if err := decodeJSONFG(input, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := jsonFGFeatures(t, output.Bytes())
	if len(features) != 1 || !strings.Contains(features[0], `"jsonfg_feature_type":"building"`) ||
		!strings.Contains(features[0], `"jsonfg_feature_schema":{"building":"https://example.test/schemas/building"}`) {
		t.Fatalf("root type/schema classes decoded as %s", output.Bytes())
	}
}

func TestJSONFGValidatesRootGeometryDimension(t *testing.T) {
	valid := bytes.NewBufferString(`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"geometryDimension":1,"features":[{"type":"Feature","geometry":{"type":"LineString","coordinates":[[1,2],[3,4]]},"properties":{}}]}`)
	if err := decodeJSONFG(valid, &bytes.Buffer{}, geodata.OutputJSONL); err != nil {
		t.Fatal(err)
	}
	invalid := bytes.NewBufferString(`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"geometryDimension":2,"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}]}`)
	if err := decodeJSONFG(invalid, &bytes.Buffer{}, geodata.OutputJSONL); err == nil {
		t.Fatal("accepted a Point in a collection declaring surface geometryDimension 2")
	}
}

func TestJSONFGRejectsMultipleTypesForRootSingleSchema(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core","http://www.opengis.net/spec/json-fg-1/1.0/conf/types-schemas"],"featureSchema":"https://example.test/schema","features":[{"type":"Feature","featureType":"road","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}},{"type":"Feature","featureType":"bridge","geometry":{"type":"Point","coordinates":[3,4]},"properties":{}}]}`)
	if err := decodeJSONFG(input, &bytes.Buffer{}, geodata.OutputJSONL); err == nil {
		t.Fatal("accepted multiple feature types with one root featureSchema URI")
	}
}

func TestJSONFGPlaceCanOverrideRootCRS(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"coordRefSys":"http://www.opengis.net/def/crs/EPSG/0/3857","features":[{"type":"Feature","geometry":null,"place":{"type":"Point","coordRefSys":"http://www.opengis.net/def/crs/EPSG/0/4326","coordinates":[1.3521,103.8198]},"properties":{}}]}`)
	var output bytes.Buffer
	if err := decodeJSONFG(input, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `[103.8198,1.3521]`) {
		t.Fatalf("place-level CRS decoded as %s", output.Bytes())
	}
}

func containsJSONFGString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestJSONFGRejectsInvalidTemporalAndConformanceData(t *testing.T) {
	cases := []string{
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"time":{"timestamp":"2026-07-25T04:00:00+08:00"},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"measures":{"enabled":true},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"coordRefSys":"http://www.opengis.net/def/crs/OGC/0/CRS84","features":[{"type":"Feature","geometry":null,"place":{"type":"Point","coordinates":[1,2]},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"time":{"date":"2026-07-25","timestamp":"2026-07-26T04:00:00Z"},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"time":{"interval":["2026-07-26","2026-07-25"]},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"time":{"date":"2026-07-25","interval":["2026-07-26","2026-07-27"]},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core"],"coordRefSys":"http://www.opengis.net/def/crs/EPSG/0/4326","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,1]},"place":{"type":"Point","coordinates":[1,1]},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core","http://www.opengis.net/spec/json-fg-1/1.0/conf/measures"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"measures":{"unit":"m"},"properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core","http://www.opengis.net/spec/json-fg-1/1.0/conf/types-schemas"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"featureType":"building","featureSchema":"relative/schema.json","properties":{}}]}`,
		`{"type":"FeatureCollection","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core","http://www.opengis.net/spec/json-fg-1/1.0/conf/types-schemas"],"features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}]}`,
		`{"type":"Feature","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core","http://www.opengis.net/spec/json-fg-1/1.0/conf/types-schemas"],"geometry":{"type":"Point","coordinates":[1,2]},"properties":{}}`,
		`{"type":"Feature","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core","http://www.opengis.net/spec/json-fg-1/1.0/conf/measures"],"coordRefSys":"http://www.opengis.net/def/crs/OGC/0/CRS84","measures":{"enabled":true},"geometry":null,"place":{"type":"Point","coordinates":[1,2]},"properties":{}}`,
		`{"type":"Feature","conformsTo":["http://www.opengis.net/spec/json-fg-1/1.0/conf/core","http://www.opengis.net/spec/json-fg-1/1.0/conf/measures"],"coordRefSys":"http://www.opengis.net/def/crs/OGC/0/CRS84","measures":{"enabled":false},"geometry":null,"place":{"type":"Point","coordinates":[1,2,3]},"properties":{}}`,
	}
	for _, input := range cases {
		if err := decodeJSONFG(strings.NewReader(input), &bytes.Buffer{}, geodata.OutputJSONL); err == nil {
			t.Fatalf("accepted invalid JSON-FG %s", input)
		}
	}
}

func jsonFGFeatures(t *testing.T, data []byte) []string {
	t.Helper()
	var result []string
	if err := geodata.ReadFeatures(bytes.NewReader(data), geodata.InputAuto, func(feature geodata.Feature) error {
		encoded, err := json.Marshal(feature)
		if err != nil {
			return err
		}
		result = append(result, string(encoded))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
