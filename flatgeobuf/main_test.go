package main

import (
	"bytes"
	"encoding/json"
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
