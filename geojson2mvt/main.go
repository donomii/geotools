package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/donomii/geotools/geodata"
	"github.com/paulmach/orb/encoding/mvt"
)

type tileSettings = geodata.MVTEncodeSettings

func encodeVectorTile(input io.Reader, output io.Writer, inputMode geodata.InputMode, settings tileSettings) error {
	return geodata.EncodeMVT(input, output, inputMode, settings)
}

func runBuiltInTest() error {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[{"type":"Feature","id":1,"geometry":{"type":"Point","coordinates":[0,0]},"properties":{"name":"Null Island"}}]}`)
	var output bytes.Buffer
	settings := tileSettings{Zoom: 0, X: 0, Y: 0, Layer: "places", Extent: 4096, Buffer: 64, Simplify: 1}
	if err := encodeVectorTile(input, &output, geodata.InputAuto, settings); err != nil {
		return err
	}
	layers, err := mvt.Unmarshal(output.Bytes())
	if err != nil {
		return fmt.Errorf("encoded vector tile cannot be decoded: %w", err)
	}
	if len(layers) != 1 || layers[0].Name != "places" || len(layers[0].Features) != 1 {
		return fmt.Errorf("encoded vector tile has %d layers and %d Features in the first layer; expected places with one Feature", len(layers), firstLayerFeatureCount(layers))
	}
	return nil
}

func firstLayerFeatureCount(layers mvt.Layers) int {
	if len(layers) == 0 {
		return 0
	}
	return len(layers[0].Features)
}

func main() {
	zoom := flag.Uint("z", 0, "Web Mercator tile zoom level from 0 through 30")
	x := flag.Uint("x", 0, "Web Mercator tile x coordinate, from 0 through 2^z-1")
	y := flag.Uint("y", 0, "Web Mercator tile y coordinate, from 0 through 2^z-1")
	layer := flag.String("layer", "features", "MVT layer name stored in the output tile")
	extent := flag.Uint("extent", 4096, "Integer coordinate extent used inside the tile; 4096 is the interoperable default")
	buffer := flag.Uint("buffer", 64, "Clipping buffer in tile coordinate units; 64 retains geometry just beyond an edge and 0 clips exactly at the edge")
	simplifyTolerance := flag.Float64("simplify", 1, "Geometry simplification tolerance in tile coordinate units; 1 removes sub-pixel detail and 0 disables simplification")
	gzipOutput := flag.Bool("gzip", false, "Compress the MVT output with gzip for storage or HTTP transfer")
	layerProperty := flag.String("layer-property", "", "GeoJSON string property selecting the output layer for each Feature; empty puts every Feature in -layer")
	dropLayerProperty := flag.Bool("drop-layer-property", false, "Remove the property named by -layer-property after choosing a layer; disabled by default so source attributes are retained")
	idProperty := flag.String("id-property", geodata.DefaultMVTIDProperty, "MVT string property used to preserve exact GeoJSON Feature ids; empty disables preservation and leaves only non-negative integer MVT ids")
	inputName := flag.String("input", "auto", "GeoJSON input format: auto detects JSONL, arrays, FeatureCollections, and RFC 8142 sequences; seq requires record separators")
	runTest := flag.Bool("test", false, "Run an in-memory GeoJSON-to-MVT encode-and-decode check and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("geojson2mvt reads GeoJSON from standard input and writes one MVT tile to standard output; positional arguments are not accepted")
	}
	if *runTest {
		if err := runBuiltInTest(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("geojson2mvt built-in test passed")
		return
	}
	inputMode, err := geodata.ParseInputMode(*inputName)
	if err != nil {
		log.Fatal(err)
	}
	settings := tileSettings{
		Zoom: *zoom, X: *x, Y: *y, Layer: *layer, Extent: *extent, Buffer: *buffer, Simplify: *simplifyTolerance,
		Gzip: *gzipOutput, LayerProperty: *layerProperty, DropLayerProperty: *dropLayerProperty, IDProperty: *idProperty,
	}
	if err := encodeVectorTile(os.Stdin, os.Stdout, inputMode, settings); err != nil {
		log.Fatal(err)
	}
}
