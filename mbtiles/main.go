package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type mbtilesInspection struct {
	File      string            `json:"file"`
	Metadata  map[string]string `json:"metadata"`
	TileCount int64             `json:"tile_count"`
	MinZoom   *int              `json:"min_zoom"`
	MaxZoom   *int              `json:"max_zoom"`
}

func openMBTiles(path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("MBTiles file path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve MBTiles path %q: %w", path, err)
	}
	location := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}
	database, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, fmt.Errorf("cannot open MBTiles file %q: %w", absolute, err)
	}
	if err := database.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("cannot read MBTiles file %q: %w", absolute, err), database.Close())
	}
	return database, nil
}

func inspectMBTiles(database *sql.DB, path string) (mbtilesInspection, error) {
	inspection := mbtilesInspection{File: path, Metadata: make(map[string]string)}
	rows, err := database.Query(`SELECT name, value FROM metadata ORDER BY name`)
	if err != nil {
		return inspection, fmt.Errorf("MBTiles metadata table cannot be read: %w", err)
	}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return inspection, errors.Join(fmt.Errorf("MBTiles metadata row cannot be read: %w", err), rows.Close())
		}
		inspection.Metadata[name] = value
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return inspection, fmt.Errorf("MBTiles metadata query failed: %w", err)
	}
	var minimum, maximum sql.NullInt64
	if err := database.QueryRow(`SELECT count(*), min(zoom_level), max(zoom_level) FROM tiles`).Scan(&inspection.TileCount, &minimum, &maximum); err != nil {
		return inspection, fmt.Errorf("MBTiles tiles table cannot be inspected: %w", err)
	}
	if minimum.Valid {
		value := int(minimum.Int64)
		inspection.MinZoom = &value
	}
	if maximum.Valid {
		value := int(maximum.Int64)
		inspection.MaxZoom = &value
	}
	return inspection, nil
}

func extractMBTile(database *sql.DB, output io.Writer, zoom, x, y uint) error {
	if zoom > 30 {
		return fmt.Errorf("MBTiles extraction zoom %d exceeds supported maximum 30", zoom)
	}
	width := uint64(1) << zoom
	if uint64(x) >= width || uint64(y) >= width {
		return fmt.Errorf("MBTiles XYZ tile %d/%d/%d is outside zoom %d coordinate range 0 through %d", zoom, x, y, zoom, width-1)
	}
	tmsY := width - 1 - uint64(y)
	var data []byte
	err := database.QueryRow(`SELECT tile_data FROM tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?`, zoom, x, tmsY).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("MBTiles archive has no XYZ tile %d/%d/%d (TMS row %d)", zoom, x, y, tmsY)
	}
	if err != nil {
		return fmt.Errorf("MBTiles XYZ tile %d/%d/%d cannot be read: %w", zoom, x, y, err)
	}
	if _, err := output.Write(data); err != nil {
		return fmt.Errorf("MBTiles XYZ tile %d/%d/%d containing %d bytes cannot be written: %w", zoom, x, y, len(data), err)
	}
	return nil
}

func runBuiltInTest() error {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE metadata (name TEXT PRIMARY KEY, value TEXT NOT NULL); CREATE TABLE tiles (zoom_level INTEGER, tile_column INTEGER, tile_row INTEGER, tile_data BLOB); INSERT INTO metadata VALUES ('name', 'test'); INSERT INTO tiles VALUES (1, 0, 1, x'010203')`); err != nil {
		return err
	}
	inspection, err := inspectMBTiles(database, ":memory:")
	if err != nil {
		return err
	}
	if inspection.TileCount != 1 || inspection.Metadata["name"] != "test" {
		return fmt.Errorf("MBTiles built-in inspection returned %d tiles and metadata %v", inspection.TileCount, inspection.Metadata)
	}
	var output byteWriter
	if err := extractMBTile(database, &output, 1, 0, 0); err != nil {
		return err
	}
	if string(output.data) != string([]byte{1, 2, 3}) {
		return fmt.Errorf("MBTiles built-in extraction returned %v; expected [1 2 3]", output.data)
	}
	return nil
}

type byteWriter struct {
	data []byte
}

func (writer *byteWriter) Write(data []byte) (int, error) {
	writer.data = append(writer.data, data...)
	return len(data), nil
}

func main() {
	mode := flag.String("mode", "inspect", "Operation: inspect writes archive metadata and tile statistics as JSON; extract writes one raw tile_data record")
	file := flag.String("file", "tiles.mbtiles", "MBTiles archive to inspect or extract; the file is opened read-only")
	zoom := flag.Uint("z", 0, "XYZ zoom level of the tile extracted by -mode=extract")
	x := flag.Uint("x", 0, "XYZ column of the tile extracted by -mode=extract")
	y := flag.Uint("y", 0, "XYZ row of the tile extracted by -mode=extract; it is converted to the archive's TMS row")
	runTest := flag.Bool("test", false, "Run an in-memory MBTiles inspection and extraction check and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("mbtiles accepts options only; positional arguments are not accepted")
	}
	if *runTest {
		if err := runBuiltInTest(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("mbtiles built-in test passed")
		return
	}
	database, err := openMBTiles(*file)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	switch *mode {
	case "inspect":
		inspection, err := inspectMBTiles(database, *file)
		if err != nil {
			log.Fatal(err)
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(inspection); err != nil {
			log.Fatal(err)
		}
	case "extract":
		if err := extractMBTile(database, os.Stdout, *zoom, *x, *y); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("invalid mode %q; expected inspect or extract", *mode)
	}
}
