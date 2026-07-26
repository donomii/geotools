package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	var singaporeProperties map[string]json.RawMessage
	for index := range encodedFeatures {
		properties, _, _, err := decodeFlatProperties(encodedFeatures[index].PropertiesBytes(), header)
		if err != nil {
			t.Fatal(err)
		}
		if string(properties["name"]) == `"Singapore"` {
			singaporeProperties = properties
			break
		}
	}
	if string(singaporeProperties["capital"]) != "true" || string(singaporeProperties["aliases"]) != `["Singapore", "Singapura"]` {
		t.Fatalf("native FlatGeobuf properties are %v", singaporeProperties)
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
			if err := decodeFlatGeobufWithBBox(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection, &bbox); err != nil {
				t.Fatal(err)
			}
			features := sortedFlatFeatures(t, output.Bytes())
			if len(features) != 2 {
				t.Fatalf("bbox query returned %d Features; expected Singapore point and Marina Bay polygon", len(features))
			}
		})
	}
}

func TestFlatGeobufBBoxQueryUsesProjectedIndex(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	settings := flatEncodeSettings{
		InputMode: geodata.InputAuto, LayerName: "places", Indexed: true,
		CRS: geodata.CRSEPSG3857,
	}
	if err := encodeFlatGeobufWithSettings(bytes.NewReader(data), &encoded, settings); err != nil {
		t.Fatal(err)
	}
	bbox := [4]float64{103, 1, 104, 2}
	var output bytes.Buffer
	if err := decodeFlatGeobufWithBBox(bytes.NewReader(encoded.Bytes()), &output, geodata.OutputCollection, &bbox); err != nil {
		t.Fatal(err)
	}
	features := sortedFlatFeatures(t, output.Bytes())
	if len(features) != 2 {
		t.Fatalf("projected bbox query returned %d Features; expected Singapore point and Marina Bay polygon", len(features))
	}
}

func TestFlatGeobufBBoxQueryUsesFeatureOffsets(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[
		{"type":"Feature","geometry":{"type":"Point","coordinates":[-150,-60]},"properties":{"name":"southwest"}},
		{"type":"Feature","geometry":{"type":"Point","coordinates":[150,-60]},"properties":{"name":"southeast"}},
		{"type":"Feature","geometry":{"type":"Point","coordinates":[-150,60]},"properties":{"name":"northwest"}},
		{"type":"Feature","geometry":{"type":"Point","coordinates":[150,60]},"properties":{"name":"northeast"}}
	]}`)
	var sources []flatSourceFeature
	if err := geodata.ReadFeatures(bytes.NewReader(input), geodata.InputAuto, func(feature geodata.Feature) error {
		source, err := prepareFlatSourceFeature(feature, geodata.CRSCRS84)
		if err == nil {
			sources = append(sources, source)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	columns, err := inferFlatColumns(sources)
	if err != nil {
		t.Fatal(err)
	}
	encodedFeatures := make([]flat.Feature, 0, len(sources))
	for _, source := range sources {
		encoded, err := buildFlatFeature(source, columns)
		if err != nil {
			t.Fatal(err)
		}
		encodedFeatures = append(encodedFeatures, encoded)
	}
	encodedFeatures = sortFlatFeaturesForIndex(sources, encodedFeatures)
	for left, right := 0, len(encodedFeatures)-1; left < right; left, right = left+1, right-1 {
		encodedFeatures[left], encodedFeatures[right] = encodedFeatures[right], encodedFeatures[left]
	}
	header, err := buildFlatHeader("unsorted", columns, sources, true, geodata.CRSCRS84, 2)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	fileWriter := gogamafgb.NewFileWriter(writerWithoutClose{&encoded})
	if _, err := fileWriter.Header(header); err != nil {
		t.Fatal(err)
	}
	if _, err := fileWriter.IndexData(encodedFeatures); err != nil {
		t.Fatal(err)
	}
	if err := fileWriter.Close(); err != nil {
		t.Fatal(err)
	}
	queries := []struct {
		name string
		bbox [4]float64
	}{
		{name: "southwest", bbox: [4]float64{-151, -61, -149, -59}},
		{name: "southeast", bbox: [4]float64{149, -61, 151, -59}},
		{name: "northwest", bbox: [4]float64{-151, 59, -149, 61}},
		{name: "northeast", bbox: [4]float64{149, 59, 151, 61}},
	}
	for _, query := range queries {
		for _, seekable := range []bool{true, false} {
			mode := map[bool]string{true: "seekable", false: "stream"}[seekable]
			t.Run(query.name+"/"+mode, func(t *testing.T) {
				var input io.Reader = struct{ io.Reader }{bytes.NewReader(encoded.Bytes())}
				if seekable {
					input = bytes.NewReader(encoded.Bytes())
				}
				var output bytes.Buffer
				if err := decodeFlatGeobufWithBBox(input, &output, geodata.OutputCollection, &query.bbox); err != nil {
					t.Fatal(err)
				}
				var names []string
				if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
					properties, err := feature.PropertyMap()
					if err != nil {
						return err
					}
					var name string
					if err := json.Unmarshal(properties["name"], &name); err != nil {
						return err
					}
					names = append(names, name)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if len(names) != 1 || names[0] != query.name {
					t.Fatalf("bbox query returned names %v; expected %s", names, query.name)
				}
			})
		}
	}
}

func TestFlatGeobufBBoxQueryCrossesAntimeridian(t *testing.T) {
	input := []byte(`{"type":"FeatureCollection","features":[
		{"type":"Feature","geometry":{"type":"Point","coordinates":[175,1]},"properties":{"name":"east"}},
		{"type":"Feature","geometry":{"type":"Point","coordinates":[-175,1]},"properties":{"name":"west"}},
		{"type":"Feature","geometry":{"type":"Point","coordinates":[0,1]},"properties":{"name":"middle"}}
	]}`)
	tests := []struct {
		name      string
		indexed   bool
		seekable  bool
		sourceCRS string
	}{
		{name: "indexed seekable", indexed: true, seekable: true, sourceCRS: geodata.CRSCRS84},
		{name: "indexed stream", indexed: true, seekable: false, sourceCRS: geodata.CRSCRS84},
		{name: "indexed projected", indexed: true, seekable: true, sourceCRS: geodata.CRSEPSG3857},
		{name: "unindexed", indexed: false, seekable: false, sourceCRS: geodata.CRSCRS84},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var encoded bytes.Buffer
			settings := flatEncodeSettings{
				InputMode: geodata.InputAuto, LayerName: "places", Indexed: test.indexed,
				CRS: test.sourceCRS, Dimension: 2,
			}
			if err := encodeFlatGeobufWithSettings(bytes.NewReader(input), &encoded, settings); err != nil {
				t.Fatal(err)
			}
			var reader io.Reader = bytes.NewReader(encoded.Bytes())
			if !test.seekable {
				reader = struct{ io.Reader }{reader}
			}
			bbox := [4]float64{170, -10, -170, 10}
			var output bytes.Buffer
			if err := decodeFlatGeobufWithBBox(reader, &output, geodata.OutputCollection, &bbox); err != nil {
				t.Fatal(err)
			}
			features := sortedFlatFeatures(t, output.Bytes())
			if len(features) != 2 {
				t.Fatalf("antimeridian bbox query returned %d Features; expected east and west", len(features))
			}
		})
	}
}

func TestParseFlatBBoxRejectsCoordinatesOutsideWGS84(t *testing.T) {
	for _, value := range []string{"-181,0,1,1", "0,-91,1,1", "0,0,181,1", "0,0,1,91"} {
		if _, err := parseFlatBBox(value); err == nil {
			t.Fatalf("accepted bbox %q outside WGS84", value)
		}
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
