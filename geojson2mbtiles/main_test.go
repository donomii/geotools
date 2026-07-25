package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/donomii/geotools/geodata"
)

func TestMBTilesArchiveContainsDecodableTilesAndMetadata(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"FeatureCollection","features":[{"type":"Feature","id":"Q334","geometry":{"type":"Point","coordinates":[103.8198,1.3521]},"properties":{"name":"Singapore","layer":"places"}},{"type":"Feature","id":2,"geometry":{"type":"LineString","coordinates":[[103.8,1.3],[103.9,1.4]]},"properties":{"name":"route","layer":"roads"}}]}`)
	settings := archiveSettings{
		InputMode: geodata.InputAuto, Name: "real archive", Layer: "features", LayerProperty: "layer",
		IDProperty: geodata.DefaultMVTIDProperty, MinZoom: 0, MaxZoom: 2, Extent: 4096, Buffer: 64, Simplify: 0, Gzip: true, MaxTiles: 100,
	}
	source, err := readArchiveSource(input, settings)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMBTilesDatabase(database, source, settings); err != nil {
		t.Fatal(err)
	}
	var name, metadataJSON string
	if err := database.QueryRow(`SELECT value FROM metadata WHERE name='name'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT value FROM metadata WHERE name='json'`).Scan(&metadataJSON); err != nil {
		t.Fatal(err)
	}
	if name != "real archive" || !bytes.Contains([]byte(metadataJSON), []byte(`"id":"places"`)) || !bytes.Contains([]byte(metadataJSON), []byte(`"id":"roads"`)) {
		t.Fatalf("MBTiles metadata name=%q json=%s", name, metadataJSON)
	}
	var zoom, column, row uint
	var tileData []byte
	if err := database.QueryRow(`SELECT zoom_level, tile_column, tile_row, tile_data FROM tiles ORDER BY zoom_level LIMIT 1`).Scan(&zoom, &column, &row, &tileData); err != nil {
		t.Fatal(err)
	}
	xyzY := (uint(1) << zoom) - 1 - row
	var decoded bytes.Buffer
	decodeSettings := geodata.MVTDecodeSettings{
		Zoom: zoom, X: column, Y: xyzY, Layer: "features", Gzip: true, AllLayers: true,
		LayerProperty: "layer", IDProperty: geodata.DefaultMVTIDProperty,
	}
	if err := geodata.DecodeMVT(bytes.NewReader(tileData), &decoded, geodata.OutputCollection, decodeSettings); err != nil {
		t.Fatal(err)
	}
	count := 0
	ids := make(map[string]bool)
	if err := geodata.ReadFeatures(&decoded, geodata.InputAuto, func(feature geodata.Feature) error {
		count++
		ids[feature.EncodedID()] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("decoded MBTiles tile contains %d Features; expected 2", count)
	}
	if !ids[`"Q334"`] {
		t.Fatalf("decoded MBTiles ids are %v; expected exact string id Q334", ids)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMBTilesTileLimit(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"LineString","coordinates":[[-170,-80],[170,80]]},"properties":{}}`)
	settings := archiveSettings{
		InputMode: geodata.InputAuto, Name: "limited", Layer: "features", MinZoom: 4, MaxZoom: 4,
		Extent: 4096, Buffer: 64, Simplify: 1, Gzip: true, MaxTiles: 2,
	}
	if _, err := readArchiveSource(input, settings); err == nil {
		t.Fatal("accepted an archive exceeding its tile limit")
	}
}

func TestMBTilesRejectsConflictingReservedPropertiesBeforeWriting(t *testing.T) {
	input := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"routing":"places"}}`)
	settings := archiveSettings{
		InputMode: geodata.InputAuto, Name: "conflict", Layer: "features", LayerProperty: "routing",
		IDProperty: "routing", MinZoom: 0, MaxZoom: 0, Extent: 4096, Buffer: 64, Simplify: 1, Gzip: true, MaxTiles: 10,
	}
	if _, err := readArchiveSource(input, settings); err == nil {
		t.Fatal("accepted one property for both layer selection and exact id preservation")
	}
}

func TestWriteMBTilesCreatesArchiveAndRefusesOverwrite(t *testing.T) {
	data := []byte(`{"type":"Feature","id":"origin","geometry":{"type":"Point","coordinates":[0,0]},"properties":{"name":"Null Island"}}`)
	settings := archiveSettings{
		InputMode: geodata.InputAuto, Name: "disk archive", Layer: "places", IDProperty: geodata.DefaultMVTIDProperty,
		MinZoom: 0, MaxZoom: 0, Extent: 4096, Buffer: 64, Simplify: 1, Gzip: true, MaxTiles: 10,
	}
	path := filepath.Join(t.TempDir(), "places.mbtiles")
	if err := writeMBTiles(bytes.NewReader(data), path, settings); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("disk MBTiles archive is empty")
	}
	if err := writeMBTiles(bytes.NewReader(data), path, settings); err == nil {
		t.Fatal("overwrote an existing MBTiles archive")
	}
}
