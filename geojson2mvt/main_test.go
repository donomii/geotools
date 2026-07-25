package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/donomii/geotools/geodata"
	"github.com/paulmach/orb/encoding/mvt"
)

func TestEncodeRealDataVectorTile(t *testing.T) {
	data, err := os.ReadFile("../testdata/real_places.geojson")
	if err != nil {
		t.Fatal(err)
	}
	settings := tileSettings{Zoom: 0, X: 0, Y: 0, Layer: "real_places", Extent: 4096, IDProperty: geodata.DefaultMVTIDProperty}
	var output bytes.Buffer
	if err := encodeVectorTile(bytes.NewReader(data), &output, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	layers, err := mvt.Unmarshal(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 || layers[0].Name != "real_places" || len(layers[0].Features) != 5 {
		t.Fatalf("unexpected MVT layers: %#v", layers)
	}
	if layers[0].Features[0].Properties[geodata.DefaultMVTIDProperty] != `"Q334"` {
		t.Fatalf("real-data string id property is %#v; expected exact JSON id", layers[0].Features[0].Properties[geodata.DefaultMVTIDProperty])
	}
	if layers[0].Features[0].Properties["aliases"] != `["Singapore","Singapura"]` {
		t.Fatalf("real-data array property is %#v; expected compact JSON text", layers[0].Features[0].Properties["aliases"])
	}
}

func TestEncodeGzippedVectorTile(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{}}`)
	settings := tileSettings{Zoom: 0, X: 0, Y: 0, Layer: "features", Extent: 4096, Gzip: true}
	var output bytes.Buffer
	if err := encodeVectorTile(input, &output, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := mvt.UnmarshalGzipped(output.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestEncodeRejectsInvalidTileCoordinates(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{}}`)
	settings := tileSettings{Zoom: 1, X: 2, Y: 0, Layer: "features", Extent: 4096}
	if err := encodeVectorTile(input, &bytes.Buffer{}, geodata.InputAuto, settings); err == nil {
		t.Fatal("accepted x=2 at zoom 1")
	}
}

func TestEncodeRejectsUnsupportedGeometry(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"GeometryCollection","geometries":[{"type":"Point","coordinates":[0,0]}]},"properties":{}}`)
	settings := tileSettings{Zoom: 0, X: 0, Y: 0, Layer: "features", Extent: 4096}
	if err := encodeVectorTile(input, &bytes.Buffer{}, geodata.InputAuto, settings); err == nil {
		t.Fatal("accepted GeometryCollection input")
	}
}

func TestEncodeRejectsLatitudeOutsideWebMercator(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,89]},"properties":{}}`)
	settings := tileSettings{Zoom: 0, X: 0, Y: 0, Layer: "features", Extent: 4096}
	if err := encodeVectorTile(input, &bytes.Buffer{}, geodata.InputAuto, settings); err == nil {
		t.Fatal("accepted latitude outside Web Mercator")
	}
}

func TestEncodeClipsFeatureOutsideTile(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[100,-30]},"properties":{"name":"outside"}}`)
	settings := tileSettings{Zoom: 1, X: 0, Y: 0, Layer: "features", Extent: 4096, Buffer: 0}
	var output bytes.Buffer
	if err := encodeVectorTile(input, &output, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	layers, err := mvt.Unmarshal(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 || len(layers[0].Features) != 0 {
		t.Fatalf("outside Feature survived clipping: %#v", layers)
	}
}

func TestEncodeMultipleLayers(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"layer":"places"}},{"type":"Feature","geometry":{"type":"LineString","coordinates":[[-10,0],[10,0]]},"properties":{"layer":"roads"}}]}`)
	settings := tileSettings{Zoom: 0, X: 0, Y: 0, Layer: "features", Extent: 4096, Buffer: 0, LayerProperty: "layer"}
	var output bytes.Buffer
	if err := encodeVectorTile(input, &output, geodata.InputAuto, settings); err != nil {
		t.Fatal(err)
	}
	layers, err := mvt.Unmarshal(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 2 || layers[0].Name != "places" || layers[1].Name != "roads" {
		t.Fatalf("multiple-layer tile contains %#v", layers)
	}
}

func TestEncodeRejectsReservedIDProperty(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","id":"source","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"__geotools_geojson_id":"occupied"}}`)
	settings := tileSettings{
		Zoom: 0, X: 0, Y: 0, Layer: "features", Extent: 4096, IDProperty: geodata.DefaultMVTIDProperty,
	}
	if err := encodeVectorTile(input, &bytes.Buffer{}, geodata.InputAuto, settings); err == nil {
		t.Fatal("accepted a source Feature using the reserved MVT id property")
	}
}
