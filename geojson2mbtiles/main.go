package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/donomii/geotools/geodata"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/maptile"
	_ "modernc.org/sqlite"
)

type archiveSettings struct {
	InputMode         geodata.InputMode
	Name              string
	Layer             string
	LayerProperty     string
	DropLayerProperty bool
	IDProperty        string
	MinZoom           uint
	MaxZoom           uint
	Extent            uint
	Buffer            uint
	Simplify          float64
	Gzip              bool
	MaxTiles          int
}

type archiveTile struct {
	Z uint
	X uint
	Y uint
}

type archiveSource struct {
	Data         []byte
	Tiles        []archiveTile
	Layers       []string
	Bounds       [4]float64
	HasBounds    bool
	FeatureCount int
}

func writeMBTiles(input io.Reader, outputPath string, settings archiveSettings) error {
	if outputPath == "" {
		return fmt.Errorf("MBTiles output path is empty")
	}
	if _, err := os.Stat(outputPath); err == nil {
		return fmt.Errorf("MBTiles output %q already exists; choose a new path so existing data is not overwritten", outputPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot inspect MBTiles output %q: %w", outputPath, err)
	}
	source, err := readArchiveSource(input, settings)
	if err != nil {
		return err
	}
	database, err := sql.Open("sqlite", outputPath)
	if err != nil {
		return fmt.Errorf("failed to create MBTiles output %q: %w", outputPath, err)
	}
	writeErr := writeMBTilesDatabase(database, source, settings)
	closeErr := database.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close MBTiles output %q: %w", outputPath, closeErr)
	}
	return nil
}

func readArchiveSource(input io.Reader, settings archiveSettings) (archiveSource, error) {
	if settings.Name == "" {
		return archiveSource{}, fmt.Errorf("MBTiles tileset name is empty")
	}
	if settings.Layer == "" {
		return archiveSource{}, fmt.Errorf("MBTiles default layer name is empty")
	}
	if settings.Extent < 256 || settings.Extent > uint(^uint32(0)) {
		return archiveSource{}, fmt.Errorf("MBTiles MVT extent %d is outside supported range 256 through %d", settings.Extent, uint(^uint32(0)))
	}
	if settings.Buffer > settings.Extent {
		return archiveSource{}, fmt.Errorf("MBTiles MVT buffer %d exceeds extent %d", settings.Buffer, settings.Extent)
	}
	if settings.Simplify < 0 {
		return archiveSource{}, fmt.Errorf("MBTiles MVT simplification tolerance %v is negative", settings.Simplify)
	}
	if settings.IDProperty != "" && settings.IDProperty == settings.LayerProperty {
		return archiveSource{}, fmt.Errorf("MBTiles id property %q is also the layer-selection property; use distinct property names", settings.IDProperty)
	}
	if settings.MinZoom > settings.MaxZoom {
		return archiveSource{}, fmt.Errorf("minimum zoom %d exceeds maximum zoom %d", settings.MinZoom, settings.MaxZoom)
	}
	if settings.MaxZoom > 30 {
		return archiveSource{}, fmt.Errorf("maximum zoom %d exceeds supported maximum 30", settings.MaxZoom)
	}
	if settings.MaxTiles <= 0 {
		return archiveSource{}, fmt.Errorf("maximum tile count %d must be positive", settings.MaxTiles)
	}
	data, err := io.ReadAll(input)
	if err != nil {
		return archiveSource{}, fmt.Errorf("failed to read GeoJSON input for MBTiles: %w", err)
	}
	tileSet := make(map[archiveTile]bool)
	layerSet := make(map[string]bool)
	source := archiveSource{Data: data}
	err = geodata.ReadFeatures(bytes.NewReader(data), settings.InputMode, func(feature geodata.Feature) error {
		summary, err := geodata.ValidateFeature(feature, geodata.ValidationOptions{})
		if err != nil {
			return fmt.Errorf("MBTiles input Feature %d with id %s is invalid: %w", source.FeatureCount+1, feature.EncodedID(), err)
		}
		if summary.CoordinateDimension != 2 || summary.Type == "GeometryCollection" {
			return fmt.Errorf("MBTiles input Feature %d with id %s has %s %dD geometry; MVT supports 2D simple and multi-geometries", source.FeatureCount+1, feature.EncodedID(), summary.Type, summary.CoordinateDimension)
		}
		if summary.Bounds[1] < -85.0511287798066 || summary.Bounds[3] > 85.0511287798066 {
			return fmt.Errorf("MBTiles input Feature %d with id %s has latitude bounds %v..%v outside Web Mercator limits", source.FeatureCount+1, feature.EncodedID(), summary.Bounds[1], summary.Bounds[3])
		}
		layerName := settings.Layer
		if settings.LayerProperty != "" {
			properties, err := feature.PropertyMap()
			if err != nil {
				return err
			}
			raw := properties[settings.LayerProperty]
			if raw == nil || json.Unmarshal(raw, &layerName) != nil || layerName == "" {
				return fmt.Errorf("MBTiles input Feature %d with id %s property %q is %s; expected a non-empty layer name", source.FeatureCount+1, feature.EncodedID(), settings.LayerProperty, raw)
			}
		}
		layerSet[layerName] = true
		source.FeatureCount++
		if !source.HasBounds {
			source.Bounds = summary.Bounds
			source.HasBounds = true
		} else {
			source.Bounds[0] = math.Min(source.Bounds[0], summary.Bounds[0])
			source.Bounds[1] = math.Min(source.Bounds[1], summary.Bounds[1])
			source.Bounds[2] = math.Max(source.Bounds[2], summary.Bounds[2])
			source.Bounds[3] = math.Max(source.Bounds[3], summary.Bounds[3])
		}
		for zoom := settings.MinZoom; zoom <= settings.MaxZoom; zoom++ {
			northwest := maptile.At(orb.Point{summary.Bounds[0], summary.Bounds[3]}, maptile.Zoom(zoom))
			southeast := maptile.At(orb.Point{summary.Bounds[2], summary.Bounds[1]}, maptile.Zoom(zoom))
			for x := northwest.X; x <= southeast.X; x++ {
				for y := northwest.Y; y <= southeast.Y; y++ {
					tileSet[archiveTile{Z: zoom, X: uint(x), Y: uint(y)}] = true
					if len(tileSet) > settings.MaxTiles {
						return fmt.Errorf("MBTiles input covers more than %d tiles from zoom %d through %d; increase -max-tiles or reduce the zoom range", settings.MaxTiles, settings.MinZoom, settings.MaxZoom)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return archiveSource{}, err
	}
	for tile := range tileSet {
		source.Tiles = append(source.Tiles, tile)
	}
	sort.Slice(source.Tiles, func(first, second int) bool {
		if source.Tiles[first].Z != source.Tiles[second].Z {
			return source.Tiles[first].Z < source.Tiles[second].Z
		}
		if source.Tiles[first].X != source.Tiles[second].X {
			return source.Tiles[first].X < source.Tiles[second].X
		}
		return source.Tiles[first].Y < source.Tiles[second].Y
	})
	for layer := range layerSet {
		source.Layers = append(source.Layers, layer)
	}
	sort.Strings(source.Layers)
	if len(source.Layers) == 0 {
		source.Layers = []string{settings.Layer}
	}
	return source, nil
}

func writeMBTilesDatabase(database *sql.DB, source archiveSource, settings archiveSettings) error {
	transaction, err := database.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin MBTiles transaction: %w", err)
	}
	statements := []string{
		`CREATE TABLE metadata (name TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE tiles (zoom_level INTEGER NOT NULL, tile_column INTEGER NOT NULL, tile_row INTEGER NOT NULL, tile_data BLOB NOT NULL, PRIMARY KEY (zoom_level, tile_column, tile_row))`,
	}
	for _, statement := range statements {
		if _, err := transaction.Exec(statement); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("failed to initialize MBTiles schema with %q: %w", statement, err)
		}
	}
	metadata, err := archiveMetadata(source, settings)
	if err != nil {
		_ = transaction.Rollback()
		return err
	}
	for name, value := range metadata {
		if _, err := transaction.Exec(`INSERT INTO metadata(name, value) VALUES (?, ?)`, name, value); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("failed to write MBTiles metadata %q=%q: %w", name, value, err)
		}
	}
	insert, err := transaction.Prepare(`INSERT INTO tiles(zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)`)
	if err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("failed to prepare MBTiles tile insertion: %w", err)
	}
	for index, tile := range source.Tiles {
		var encoded bytes.Buffer
		tileSettings := geodata.MVTEncodeSettings{
			Zoom: tile.Z, X: tile.X, Y: tile.Y, Layer: settings.Layer, Extent: settings.Extent, Buffer: settings.Buffer,
			Simplify: settings.Simplify, Gzip: settings.Gzip, LayerProperty: settings.LayerProperty, DropLayerProperty: settings.DropLayerProperty,
			IDProperty: settings.IDProperty,
		}
		if err := geodata.EncodeMVT(bytes.NewReader(source.Data), &encoded, settings.InputMode, tileSettings); err != nil {
			_ = insert.Close()
			_ = transaction.Rollback()
			return fmt.Errorf("failed to encode MBTiles tile %d/%d/%d (%d of %d): %w", tile.Z, tile.X, tile.Y, index+1, len(source.Tiles), err)
		}
		tmsY := (uint64(1) << tile.Z) - 1 - uint64(tile.Y)
		if _, err := insert.Exec(tile.Z, tile.X, tmsY, encoded.Bytes()); err != nil {
			_ = insert.Close()
			_ = transaction.Rollback()
			return fmt.Errorf("failed to store MBTiles tile %d/%d/%d with TMS row %d: %w", tile.Z, tile.X, tile.Y, tmsY, err)
		}
	}
	if err := insert.Close(); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("failed to close MBTiles tile insertion after %d tiles: %w", len(source.Tiles), err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("failed to commit MBTiles archive containing %d tiles: %w", len(source.Tiles), err)
	}
	return nil
}

func archiveMetadata(source archiveSource, settings archiveSettings) (map[string]string, error) {
	type vectorLayer struct {
		ID      string            `json:"id"`
		Fields  map[string]string `json:"fields"`
		MinZoom uint              `json:"minzoom"`
		MaxZoom uint              `json:"maxzoom"`
	}
	description := struct {
		VectorLayers []vectorLayer `json:"vector_layers"`
	}{}
	for _, name := range source.Layers {
		description.VectorLayers = append(description.VectorLayers, vectorLayer{
			ID: name, Fields: map[string]string{}, MinZoom: settings.MinZoom, MaxZoom: settings.MaxZoom,
		})
	}
	encodedDescription, err := json.Marshal(description)
	if err != nil {
		return nil, err
	}
	bounds := "-180,-85.0511287798066,180,85.0511287798066"
	if source.HasBounds {
		values := make([]string, len(source.Bounds))
		for index, value := range source.Bounds {
			values[index] = strconv.FormatFloat(value, 'g', -1, 64)
		}
		bounds = strings.Join(values, ",")
	}
	return map[string]string{
		"name": settings.Name, "type": "overlay", "version": "1", "description": settings.Name,
		"format": "pbf", "minzoom": strconv.FormatUint(uint64(settings.MinZoom), 10),
		"maxzoom": strconv.FormatUint(uint64(settings.MaxZoom), 10), "bounds": bounds, "json": string(encodedDescription),
	}, nil
}

func runBuiltInTest() error {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"name":"Null Island"}}`)
	settings := archiveSettings{
		InputMode: geodata.InputAuto, Name: "test", Layer: "places", MinZoom: 0, MaxZoom: 1,
		Extent: 4096, Buffer: 64, Simplify: 1, Gzip: true, MaxTiles: 10,
	}
	source, err := readArchiveSource(input, settings)
	if err != nil {
		return err
	}
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	if err := writeMBTilesDatabase(database, source, settings); err != nil {
		return err
	}
	var tileCount int
	if err := database.QueryRow(`SELECT count(*) FROM tiles`).Scan(&tileCount); err != nil {
		return err
	}
	if tileCount != 2 {
		return fmt.Errorf("MBTiles built-in check wrote %d tiles; expected 2", tileCount)
	}
	return database.Close()
}

func main() {
	outputPath := flag.String("output", "tiles.mbtiles", "New MBTiles archive path; the command refuses to overwrite an existing file")
	name := flag.String("name", "geotools", "Tileset name stored in MBTiles metadata")
	layer := flag.String("layer", "features", "Default MVT layer name")
	layerProperty := flag.String("layer-property", "", "GeoJSON string property selecting each Feature's MVT layer; empty uses -layer")
	dropLayerProperty := flag.Bool("drop-layer-property", false, "Remove the property named by -layer-property from tile attributes after layer selection")
	idProperty := flag.String("id-property", geodata.DefaultMVTIDProperty, "MVT string property used to preserve exact GeoJSON Feature ids; empty disables preservation")
	minZoom := flag.Uint("min-z", 0, "Lowest Web Mercator zoom included in the archive")
	maxZoom := flag.Uint("max-z", 5, "Highest Web Mercator zoom included in the archive")
	extent := flag.Uint("extent", 4096, "Integer coordinate extent inside each vector tile")
	buffer := flag.Uint("buffer", 64, "Clipping buffer in tile coordinate units")
	simplifyTolerance := flag.Float64("simplify", 1, "Geometry simplification tolerance in tile coordinate units; 0 disables simplification")
	gzipTiles := flag.Bool("gzip", true, "Gzip-compress tile_data records, as expected by most MBTiles vector-tile readers")
	maxTiles := flag.Int("max-tiles", 100000, "Maximum archive tile count; protects against accidentally enormous zoom ranges")
	inputName := flag.String("input", "auto", "GeoJSON input format: auto detects JSONL, arrays, FeatureCollections, and RFC 8142 sequences; seq requires record separators")
	runTest := flag.Bool("test", false, "Run an in-memory GeoJSON-to-MBTiles check and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("geojson2mbtiles reads GeoJSON from standard input; positional arguments are not accepted")
	}
	if *runTest {
		if err := runBuiltInTest(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("geojson2mbtiles built-in test passed")
		return
	}
	inputMode, err := geodata.ParseInputMode(*inputName)
	if err != nil {
		log.Fatal(err)
	}
	settings := archiveSettings{
		InputMode: inputMode, Name: *name, Layer: *layer, LayerProperty: *layerProperty, DropLayerProperty: *dropLayerProperty, IDProperty: *idProperty,
		MinZoom: *minZoom, MaxZoom: *maxZoom, Extent: *extent, Buffer: *buffer, Simplify: *simplifyTolerance,
		Gzip: *gzipTiles, MaxTiles: *maxTiles,
	}
	if err := writeMBTiles(os.Stdin, *outputPath, settings); err != nil {
		log.Fatal(err)
	}
}
