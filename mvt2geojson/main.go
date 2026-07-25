package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/donomii/geotools/geodata"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/mvt"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/maptile"
)

type decodeSettings = geodata.MVTDecodeSettings

func decodeVectorTile(input io.Reader, output io.Writer, outputMode geodata.OutputMode, settings decodeSettings) error {
	return geodata.DecodeMVT(input, output, outputMode, settings)
}

func runBuiltInTest() error {
	collection := geojson.NewFeatureCollection()
	feature := geojson.NewFeature(orb.Point{0, 0})
	feature.ID = uint64(1)
	feature.Properties["name"] = "Null Island"
	collection.Append(feature)
	layer := mvt.NewLayer("places", collection)
	tile := maptile.New(0, 0, 0)
	layer.ProjectToTile(tile)
	encoded, err := mvt.Marshal(mvt.Layers{layer})
	if err != nil {
		return err
	}
	tileData := bytes.NewReader(encoded)
	var output bytes.Buffer
	settings := decodeSettings{Zoom: 0, X: 0, Y: 0, Layer: "places"}
	if err := decodeVectorTile(tileData, &output, geodata.OutputCollection, settings); err != nil {
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
		return fmt.Errorf("MVT decode returned %d Features; expected 1", count)
	}
	return nil
}

func main() {
	zoom := flag.Uint("z", 0, "Web Mercator tile zoom level from 0 through 30; it must match the source tile")
	x := flag.Uint("x", 0, "Web Mercator tile x coordinate; it must match the source tile")
	y := flag.Uint("y", 0, "Web Mercator tile y coordinate; it must match the source tile")
	layer := flag.String("layer", "features", "Name of the MVT layer to convert to GeoJSON")
	allLayers := flag.Bool("all-layers", false, "Decode every MVT layer in name order instead of only -layer")
	layerProperty := flag.String("layer-property", "mvt_layer", "Property receiving each source layer name with -all-layers; empty omits layer identity")
	idProperty := flag.String("id-property", geodata.DefaultMVTIDProperty, "MVT string property containing an exact GeoJSON Feature id; restored and removed when present, empty disables restoration")
	gzipInput := flag.Bool("gzip", false, "Read a gzip-compressed MVT tile instead of an uncompressed tile")
	outputName := flag.String("output", "jsonl", "GeoJSON output format: jsonl writes one Feature per line, collection writes a FeatureCollection, and seq writes RFC 8142 records")
	runTest := flag.Bool("test", false, "Run an in-memory MVT-to-GeoJSON decode check and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("mvt2geojson reads one MVT tile from standard input and writes GeoJSON to standard output; positional arguments are not accepted")
	}
	if *runTest {
		if err := runBuiltInTest(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("mvt2geojson built-in test passed")
		return
	}
	outputMode, err := geodata.ParseOutputMode(*outputName)
	if err != nil {
		log.Fatal(err)
	}
	settings := decodeSettings{Zoom: *zoom, X: *x, Y: *y, Layer: *layer, Gzip: *gzipInput, AllLayers: *allLayers, LayerProperty: *layerProperty, IDProperty: *idProperty}
	if err := decodeVectorTile(os.Stdin, os.Stdout, outputMode, settings); err != nil {
		log.Fatal(err)
	}
}
