package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/donomii/geotools/geodata"
	gogamafgb "github.com/gogama/flatgeobuf/flatgeobuf"
	"github.com/gogama/flatgeobuf/flatgeobuf/flat"
)

func TestFlatGeobufRealDataRoundTrip(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	var flatData bytes.Buffer
	if err := encodeFlatGeobuf(bytes.NewReader(data), &flatData, geodata.InputAuto, "real_places"); err != nil {
		t.Fatal(err)
	}
	reader := gogamafgb.NewFileReader(bytes.NewReader(flatData.Bytes()))
	header, err := reader.Header()
	if err != nil {
		t.Fatal(err)
	}
	if string(header.Name()) != "real_places" || header.IndexNodeSize() == 0 || header.FeaturesCount() != 5 {
		t.Fatalf("FlatGeobuf header has name %q, index node size %d, and %d Features", header.Name(), header.IndexNodeSize(), header.FeaturesCount())
	}
	var crs flat.Crs
	if header.Crs(&crs) == nil || string(crs.Org()) != "EPSG" || crs.Code() != 4326 {
		t.Fatalf("FlatGeobuf CRS is organization %q code %d; expected EPSG:4326", crs.Org(), crs.Code())
	}
	if header.ColumnsLength() < 6 {
		t.Fatalf("FlatGeobuf header has %d property columns; expected native fixture properties plus preservation data", header.ColumnsLength())
	}
	if _, err := reader.Index(); err != nil {
		t.Fatal(err)
	}
	encodedFeatures, err := reader.DataRem()
	if err != nil {
		t.Fatal(err)
	}
	properties, _, _, err := decodeFlatProperties(encodedFeatures[0].PropertiesBytes(), header)
	if err != nil {
		t.Fatal(err)
	}
	if string(properties["name"]) != `"Singapore"` || string(properties["capital"]) != "true" ||
		string(properties["aliases"]) != `["Singapore", "Singapura"]` {
		t.Fatalf("native FlatGeobuf properties are %v", properties)
	}
	var output bytes.Buffer
	if err := decodeFlatGeobuf(&flatData, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	expected := sortedFlatFeatures(t, data)
	actual := sortedFlatFeatures(t, output.Bytes())
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("FlatGeobuf round trip changed Feature content:\nexpected %v\nactual   %v", expected, actual)
	}
}

func TestFlatGeobufRejectsReservedProperty(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[1,2]},"properties":{"__geotools_feature_json":"occupied"}}`)
	var output bytes.Buffer
	if err := encodeFlatGeobuf(input, &output, geodata.InputAuto, "features"); err == nil {
		t.Fatal("accepted the reserved preservation property")
	}
}

func TestFlatGeobufEmptyCollection(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[]}`)
	var encoded bytes.Buffer
	if err := encodeFlatGeobuf(input, &encoded, geodata.InputAuto, "empty"); err != nil {
		t.Fatal(err)
	}
	reader := gogamafgb.NewFileReader(bytes.NewReader(encoded.Bytes()))
	header, err := reader.Header()
	if err != nil {
		t.Fatal(err)
	}
	if header.FeaturesCount() != 0 || header.IndexNodeSize() != 0 {
		t.Fatalf("empty header has %d Features and index node size %d", header.FeaturesCount(), header.IndexNodeSize())
	}
	var output bytes.Buffer
	if err := decodeFlatGeobuf(&encoded, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if string(output.Bytes()) != `{"type":"FeatureCollection","features":[]}`+"\n" {
		t.Fatalf("empty FlatGeobuf decoded as %s", output.Bytes())
	}
}

func TestStreamingFlatGeobufRoundTrip(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	settings := flatEncodeSettings{InputMode: geodata.InputAuto, LayerName: "streamed", Indexed: false}
	if err := encodeFlatGeobufWithSettings(bytes.NewReader(data), &encoded, settings); err != nil {
		t.Fatal(err)
	}
	reader := gogamafgb.NewFileReader(bytes.NewReader(encoded.Bytes()))
	header, err := reader.Header()
	if err != nil {
		t.Fatal(err)
	}
	if header.IndexNodeSize() != 0 || header.FeaturesCount() != 0 {
		t.Fatalf("streaming header has index node size %d and declared Feature count %d", header.IndexNodeSize(), header.FeaturesCount())
	}
	var output bytes.Buffer
	if err := decodeFlatGeobuf(&encoded, &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFlatFeatures(t, data), sortedFlatFeatures(t, output.Bytes())) {
		t.Fatal("streaming FlatGeobuf round trip changed Features")
	}
}

func TestFlatGeobufBBoxQueryIndexedAndStreaming(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	for _, indexed := range []bool{true, false} {
		t.Run(fmt.Sprintf("indexed=%t", indexed), func(t *testing.T) {
			var encoded bytes.Buffer
			settings := flatEncodeSettings{InputMode: geodata.InputAuto, LayerName: "places", Indexed: indexed}
			if err := encodeFlatGeobufWithSettings(bytes.NewReader(data), &encoded, settings); err != nil {
				t.Fatal(err)
			}
			bbox := [4]float64{103, 1, 104, 2}
			var output bytes.Buffer
			if err := decodeFlatGeobufWithBBox(&encoded, &output, geodata.OutputCollection, &bbox); err != nil {
				t.Fatal(err)
			}
			features := sortedFlatFeatures(t, output.Bytes())
			if len(features) != 2 {
				t.Fatalf("bbox query returned %d Features; expected Singapore point and Marina Bay polygon", len(features))
			}
		})
	}
}

func TestDecodeFlatGeobufProjectFixture(t *testing.T) {
	data, err := os.ReadFile("../testdata/external/flatgeobuf_countries.fgb")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeFlatGeobuf(bytes.NewReader(data), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	features := sortedFlatFeatures(t, output.Bytes())
	if len(features) != 179 {
		t.Fatalf("decoded %d countries from the FlatGeobuf project fixture; expected 179", len(features))
	}
}

func TestRejectsExternalFlatGeobufCoordinatesOutsideDeclaredCRS(t *testing.T) {
	data, err := os.ReadFile("../testdata/external/flatgeobuf_poly00.fgb")
	if err != nil {
		t.Fatal(err)
	}
	err = decodeFlatGeobuf(bytes.NewReader(data), &bytes.Buffer{}, geodata.OutputCollection)
	if err == nil {
		t.Fatal("accepted FlatGeobuf coordinates outside the declared EPSG:27700 grid")
	}
	expected := `British National Grid coordinate`
	if !bytes.Contains([]byte(err.Error()), []byte(expected)) {
		t.Fatalf("invalid coordinate error is %q; expected it to contain %q", err, expected)
	}
}

func TestFlatGeobufThreeDimensionalProjectedRoundTrip(t *testing.T) {
	input := []byte(`{"type":"Feature","id":"three-dimensional","geometry":{"type":"LineString","coordinates":[[103.8,1.3,4],[103.9,1.4,5]]},"properties":{"name":"route"}}`)
	for _, indexed := range []bool{true, false} {
		t.Run(fmt.Sprintf("indexed=%t", indexed), func(t *testing.T) {
			var encoded bytes.Buffer
			settings := flatEncodeSettings{
				InputMode: geodata.InputAuto, LayerName: "projected", Indexed: indexed,
				CRS: "EPSG:32648", Dimension: 3,
			}
			if err := encodeFlatGeobufWithSettings(bytes.NewReader(input), &encoded, settings); err != nil {
				t.Fatal(err)
			}
			reader := gogamafgb.NewFileReader(bytes.NewReader(encoded.Bytes()))
			header, err := reader.Header()
			if err != nil {
				t.Fatal(err)
			}
			if !header.HasZ() {
				t.Fatal("3D FlatGeobuf header does not declare Z coordinates")
			}
			var output bytes.Buffer
			if err := decodeFlatGeobuf(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sortedFlatFeatures(t, input), sortedFlatFeatures(t, output.Bytes())) {
				t.Fatalf("projected 3D FlatGeobuf changed Feature content: %s", output.Bytes())
			}
		})
	}
}

func TestFlatGeobufBritishNationalGridRoundTrip(t *testing.T) {
	input := []byte(`{"type":"Feature","geometry":{"type":"Point","coordinates":[-0.1246,51.5007]},"properties":{"name":"Westminster"}}`)
	var encoded bytes.Buffer
	settings := flatEncodeSettings{InputMode: geodata.InputAuto, LayerName: "bng", Indexed: true, CRS: geodata.CRSEPSG27700}
	if err := encodeFlatGeobufWithSettings(bytes.NewReader(input), &encoded, settings); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := decodeFlatGeobuf(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFlatFeatures(t, input), sortedFlatFeatures(t, output.Bytes())) {
		t.Fatalf("British National Grid round trip changed Feature content: %s", output.Bytes())
	}
}

func sortedFlatFeatures(t *testing.T, data []byte) []string {
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
	sort.Strings(result)
	return result
}
