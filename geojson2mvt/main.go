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

type tileSettings struct {
	Zoom   uint
	X      uint
	Y      uint
	Layer  string
	Extent uint
	Gzip   bool
}

func encodeVectorTile(input io.Reader, output io.Writer, inputMode geodata.InputMode, settings tileSettings) error {
	if settings.Zoom > 30 {
		return fmt.Errorf("tile zoom %d exceeds supported maximum 30", settings.Zoom)
	}
	if settings.X > uint(^uint32(0)) || settings.Y > uint(^uint32(0)) {
		return fmt.Errorf("tile coordinates %d/%d exceed 32-bit MVT limits", settings.X, settings.Y)
	}
	tile := maptile.New(uint32(settings.X), uint32(settings.Y), maptile.Zoom(settings.Zoom))
	if !tile.Valid() {
		return fmt.Errorf("tile %d/%d/%d is invalid; x and y must be below %d", settings.Zoom, settings.X, settings.Y, uint64(1)<<settings.Zoom)
	}
	if settings.Layer == "" {
		return fmt.Errorf("MVT layer name is empty")
	}
	if settings.Extent < 256 || settings.Extent > uint(^uint32(0)) {
		return fmt.Errorf("MVT extent %d is outside supported range 256 through %d", settings.Extent, uint(^uint32(0)))
	}
	collection := geojson.NewFeatureCollection()
	featureNumber := 0
	if err := geodata.ReadFeatures(input, inputMode, func(feature geodata.Feature) error {
		featureNumber++
		summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{})
		if err != nil {
			return fmt.Errorf("MVT input Feature %d with id %s is invalid: %w", featureNumber, feature.EncodedID(), err)
		}
		if summary.CoordinateDimension != 2 {
			return fmt.Errorf("MVT input Feature %d with id %s has %d-coordinate positions; MVT supports 2D positions", featureNumber, feature.EncodedID(), summary.CoordinateDimension)
		}
		if summary.Type == "GeometryCollection" {
			return fmt.Errorf("MVT input Feature %d with id %s has GeometryCollection geometry; MVT supports Point, LineString, Polygon, and their multi-geometry forms", featureNumber, feature.EncodedID())
		}
		if summary.HasBounds && (summary.Bounds[1] < -85.0511287798066 || summary.Bounds[3] > 85.0511287798066) {
			return fmt.Errorf("MVT input Feature %d with id %s has latitude bounds %v..%v outside Web Mercator limits -85.0511287798066..85.0511287798066", featureNumber, feature.EncodedID(), summary.Bounds[1], summary.Bounds[3])
		}
		converted, err := geodata.OrbFeature(feature)
		if err != nil {
			return err
		}
		collection.Append(converted)
		return nil
	}); err != nil {
		return err
	}
	layer := mvt.NewLayer(settings.Layer, collection)
	layer.Extent = uint32(settings.Extent)
	layer.ProjectToTile(tile)
	extent := float64(settings.Extent)
	layer.Clip(orb.Bound{Min: orb.Point{-extent, -extent}, Max: orb.Point{2*extent - 1, 2*extent - 1}})
	layers := mvt.Layers{layer}
	var encoded []byte
	var err error
	if settings.Gzip {
		encoded, err = mvt.MarshalGzipped(layers)
	} else {
		encoded, err = mvt.Marshal(layers)
	}
	if err != nil {
		return fmt.Errorf("failed to encode tile %d/%d/%d layer %q: %w", settings.Zoom, settings.X, settings.Y, settings.Layer, err)
	}
	if _, err := output.Write(encoded); err != nil {
		return fmt.Errorf("failed to write tile %d/%d/%d layer %q: %w", settings.Zoom, settings.X, settings.Y, settings.Layer, err)
	}
	return nil
}

func runBuiltInTest() error {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[{"type":"Feature","id":1,"geometry":{"type":"Point","coordinates":[0,0]},"properties":{"name":"Null Island"}}]}`)
	var output bytes.Buffer
	settings := tileSettings{Zoom: 0, X: 0, Y: 0, Layer: "places", Extent: 4096}
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
	gzipOutput := flag.Bool("gzip", false, "Compress the MVT output with gzip for storage or HTTP transfer")
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
	settings := tileSettings{Zoom: *zoom, X: *x, Y: *y, Layer: *layer, Extent: *extent, Gzip: *gzipOutput}
	if err := encodeVectorTile(os.Stdin, os.Stdout, inputMode, settings); err != nil {
		log.Fatal(err)
	}
}
