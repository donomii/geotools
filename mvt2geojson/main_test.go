package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/donomii/geotools/geodata"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/maptile"
)

func TestDecodeVectorTileToGeoJSON(t *testing.T) {
	collection := geojson.NewFeatureCollection()
	feature := geojson.NewFeature(orb.Point{103.8198, 1.3521})
	feature.ID = uint64(334)
	feature.Properties["name"] = "Singapore"
	collection.Append(feature)
	layer := mvt.NewLayer("places", collection)
	tile := maptile.New(0, 0, 0)
	layer.ProjectToTile(tile)
	data, err := mvt.Marshal(mvt.Layers{layer})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	settings := decodeSettings{Zoom: 0, X: 0, Y: 0, Layer: "places"}
	if err := decodeVectorTile(bytes.NewReader(data), &output, geodata.OutputCollection, settings); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
		count++
		if feature.EncodedID() != "334" {
			t.Fatalf("decoded Feature id is %s; expected 334", feature.EncodedID())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("decoded %d Features; expected 1", count)
	}
}

func TestDecodeRejectsUnknownLayer(t *testing.T) {
	data, err := mvt.Marshal(mvt.Layers{mvt.NewLayer("present", geojson.NewFeatureCollection())})
	if err != nil {
		t.Fatal(err)
	}
	settings := decodeSettings{Zoom: 0, X: 0, Y: 0, Layer: "missing"}
	if err := decodeVectorTile(bytes.NewReader(data), &bytes.Buffer{}, geodata.OutputJSONL, settings); err == nil {
		t.Fatal("accepted a tile without the requested layer")
	}
}

func TestDecodeAllLayersDoesNotRequireOneLayerName(t *testing.T) {
	collection := geojson.NewFeatureCollection()
	collection.Append(geojson.NewFeature(orb.Point{0, 0}))
	data, err := mvt.Marshal(mvt.Layers{mvt.NewLayer("present", collection)})
	if err != nil {
		t.Fatal(err)
	}
	settings := decodeSettings{Zoom: 0, X: 0, Y: 0, AllLayers: true, LayerProperty: "mvt_layer"}
	if err := decodeVectorTile(bytes.NewReader(data), &bytes.Buffer{}, geodata.OutputJSONL, settings); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeExternalRealWorldVectorTile(t *testing.T) {
	data, err := os.ReadFile("../testdata/external/map_tile_16_17896_24449.mvt")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	settings := decodeSettings{Zoom: 16, X: 17896, Y: 24449, Layer: "features", Gzip: true, AllLayers: true, LayerProperty: "mvt_layer"}
	if err := decodeVectorTile(bytes.NewReader(data), &output, geodata.OutputCollection, settings); err != nil {
		t.Fatal(err)
	}
	count := 0
	layers := make(map[string]bool)
	geometryTypes := make(map[string]bool)
	if err := geodata.ReadFeatures(&output, geodata.InputAuto, func(feature geodata.Feature) error {
		count++
		properties, err := feature.PropertyMap()
		if err != nil {
			return err
		}
		var layer string
		if err := json.Unmarshal(properties["mvt_layer"], &layer); err != nil {
			return err
		}
		layers[layer] = true
		var geometry struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(feature.Geometry, &geometry); err != nil {
			return err
		}
		geometryTypes[geometry.Type] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 282 {
		t.Fatalf("decoded %d Features from the external vector tile; expected 282", count)
	}
	if len(layers) != 7 {
		t.Fatalf("decoded layer names %v; expected 7 layers", layers)
	}
	if len(geometryTypes) < 3 {
		t.Fatalf("decoded geometry types %v; expected at least three types", geometryTypes)
	}
}
