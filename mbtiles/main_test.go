package main

import (
	"bytes"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInspectAndExtractMBTiles(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE metadata (name TEXT PRIMARY KEY, value TEXT NOT NULL); CREATE TABLE tiles (zoom_level INTEGER, tile_column INTEGER, tile_row INTEGER, tile_data BLOB); INSERT INTO metadata VALUES ('name', 'fixture'), ('format', 'pbf'); INSERT INTO tiles VALUES (2, 1, 2, x'001122'), (2, 2, 1, x'334455')`); err != nil {
		t.Fatal(err)
	}
	inspection, err := inspectMBTiles(database, "fixture.mbtiles")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.TileCount != 2 || inspection.MinZoom == nil || *inspection.MinZoom != 2 ||
		inspection.MaxZoom == nil || *inspection.MaxZoom != 2 || inspection.Metadata["format"] != "pbf" {
		t.Fatalf("MBTiles inspection is %#v", inspection)
	}
	var output bytes.Buffer
	if err := extractMBTile(database, &output, 2, 1, 1); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), []byte{0, 0x11, 0x22}) {
		t.Fatalf("extracted tile bytes are %v", output.Bytes())
	}
}

func TestExtractMBTilesRejectsMissingAndOutOfRangeTiles(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE tiles (zoom_level INTEGER, tile_column INTEGER, tile_row INTEGER, tile_data BLOB)`); err != nil {
		t.Fatal(err)
	}
	if err := extractMBTile(database, &bytes.Buffer{}, 2, 4, 0); err == nil {
		t.Fatal("accepted an out-of-range XYZ column")
	}
	if err := extractMBTile(database, &bytes.Buffer{}, 2, 1, 1); err == nil {
		t.Fatal("accepted a missing tile")
	}
}
