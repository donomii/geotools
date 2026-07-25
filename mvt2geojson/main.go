package main

import (
	"bytes"
	"errors"
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

type decodeSettings struct {
	Zoom  uint
	X     uint
	Y     uint
	Layer string
	Gzip  bool
}

func decodeVectorTile(input io.Reader, output io.Writer, outputMode geodata.OutputMode, settings decodeSettings) error {
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
	data, err := io.ReadAll(input)
	if err != nil {
		return fmt.Errorf("failed to read MVT tile %d/%d/%d: %w", settings.Zoom, settings.X, settings.Y, err)
	}
	var layers mvt.Layers
	if settings.Gzip {
		layers, err = mvt.UnmarshalGzipped(data)
	} else {
		layers, err = mvt.Unmarshal(data)
	}
	if err != nil {
		return fmt.Errorf("failed to decode MVT tile %d/%d/%d: %w", settings.Zoom, settings.X, settings.Y, err)
	}
	var selected *mvt.Layer
	for _, layer := range layers {
		if layer.Name == settings.Layer {
			selected = layer
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("MVT tile %d/%d/%d has no layer named %q", settings.Zoom, settings.X, settings.Y, settings.Layer)
	}
	selected.ProjectToWGS84(tile)
	writer := geodata.NewFeatureWriter(output, outputMode)
	if err := writer.Start(); err != nil {
		return err
	}
	var decodeErr error
	for index, feature := range selected.Features {
		converted, err := geodata.FeatureFromOrb(feature)
		if err != nil {
			decodeErr = fmt.Errorf("MVT layer %q Feature %d cannot be converted to GeoJSON: %w", settings.Layer, index+1, err)
			break
		}
		if _, err := geodata.ValidateFeature(converted, geodata.ValidationOptions{}); err != nil {
			decodeErr = fmt.Errorf("MVT layer %q Feature %d produced invalid GeoJSON: %w", settings.Layer, index+1, err)
			break
		}
		if err := writer.Write(converted); err != nil {
			decodeErr = err
			break
		}
	}
	return errors.Join(decodeErr, writer.Close())
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
	settings := decodeSettings{Zoom: *zoom, X: *x, Y: *y, Layer: *layer, Gzip: *gzipInput}
	if err := decodeVectorTile(os.Stdin, os.Stdout, outputMode, settings); err != nil {
		log.Fatal(err)
	}
}
