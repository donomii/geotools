# geojson2entirety

`geojson2entirety` converts WGS 84 GeoJSON Point Features into the eight binary files consumed by the Entirety map viewer. It reads from standard input and writes files using a configurable prefix.

## Build

From the Geotools repository root:

```sh
./build_all.sh
```

Or build this module directly:

```sh
mkdir -p bin
go -C geojson2entirety build -o ../bin/geojson2entirety .
```

## Use

```sh
./bin/geojson2entirety -outFile=world < places.geojson
./bin/geojson2entirety -outFile=sample -limit=1000 < places.geojsonl
```

Input may be a GeoJSON `FeatureCollection`, an array of Features, GeoJSONL, or consecutive top-level Features. RFC 8142 record separators are not accepted. Every Feature must have a two-dimensional Point with finite longitude from -180 through 180 and latitude from -90 through 90. A non-empty string `properties.name` creates a tag; a missing, null, or empty name creates an ordinary map point.

The command creates or truncates these files immediately:

```text
world.tag_points
world.map_points
world.map_data
world.tag_category
world.pre_offset
world.tag_offset
world.tag_text
world.tag_index
```

## Options

| Option | Meaning |
| --- | --- |
| `-outFile=default_map` | Prefix for all eight output files. |
| `-limit=-1` | Stop after this many Features. `-1` converts all input and `0` converts none. |
| `-points` | Write every Feature as an unnamed map point and omit all tag records. |
| `-tags` | Write only named Features as tags and omit unnamed map points. It cannot be combined with `-points`. |
| `-verbose` | Log every converted point or tag. |
| `-test` | Run the built-in conversion checks and exit without creating map files. |

The converter streams Features and uses fixed-size buffers for the eight outputs. A failed conversion may leave partially written output files.

## Test

```sh
go -C geojson2entirety test ./...
./bin/geojson2entirety -test
```
