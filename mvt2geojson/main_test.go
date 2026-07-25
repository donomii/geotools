package main

import (
	"bytes"
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
