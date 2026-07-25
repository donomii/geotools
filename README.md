# Geotools

Geotools is a collection of small command-line converters for OpenStreetMap, MediaWiki, GeoJSON, GeoJSON Text Sequences, GeoParquet, FlatGeobuf, JSON-FG, Mapbox Vector Tiles, and MBTiles. The tools are designed to compose through standard input and output, work without a network connection, and reject malformed or unsupported data with a useful error.

| Program | Purpose |
| --- | --- |
| `geojsoncheck` | Validate GeoJSON geometry and print a dataset summary |
| `geojsonseq` | Convert among GeoJSONL, FeatureCollections, arrays, and RFC 8142 sequences |
| `geofilter` | Filter Features by geometry, properties, bounding box, and count |
| `geoparquet` | Convert between GeoJSON and GeoParquet |
| `flatgeobuf` | Convert between GeoJSON and FlatGeobuf |
| `jsonfg` | Convert between GeoJSON and JSON-FG |
| `geojson2mvt` / `mvt2geojson` | Encode or decode one Mapbox Vector Tile |
| `geojson2mbtiles` | Build an MBTiles vector-tile archive |
| `pbf2json` | Convert selected OpenStreetMap PBF elements to GeoJSON |
| `osm2geojson` | Convert OpenStreetMap XML to GeoJSON |
| `wikipedia2geojson` | Extract GeoJSON Points from a local MediaWiki XML dump |
| `wikipedia2doc2vec` | Extract tokenized documents from a local MediaWiki XML dump |
| `geojson2entirety` | Build the eight binary map files used by Entirety |

The stream-oriented tools accept compact GeoJSON Features separated by newlines, a JSON array of Features, a standard `FeatureCollection`, or an RFC 8142 GeoJSON Text Sequence. Automatic input mode detects all four; `-input=seq` requires record separators. GeoJSON output defaults to one compact Feature per line; use `-output=collection` for a standard `FeatureCollection` or `-output=seq` for RFC 8142 records.

## Requirements

- Go 1.25.1 or newer
- Enough disk space for the source data and generated files
- Node.js 10 or newer only if using the optional `pbf2geojson` Node stream interface

Go downloads each program's dependencies from its module definition when it is first built.

## Build

Run the repository launcher:

```sh
./build_all.sh
```

The binaries are written to `bin/`:

```text
bin/geojson2entirety
bin/geojson2mbtiles
bin/geojson2mvt
bin/geojsoncheck
bin/geojsonseq
bin/geofilter
bin/geoparquet
bin/flatgeobuf
bin/jsonfg
bin/mvt2geojson
bin/osm2geojson
bin/pbf2json
bin/wikipedia2doc2vec
bin/wikipedia2geojson
```

## GeoJSON output

OpenStreetMap Features use IDs such as `node/123`, `way/456`, and `relation/789`. Their properties contain the original element type, ID, and tags:

```json
{
  "type": "Feature",
  "id": "node/123",
  "geometry": {
    "type": "Point",
    "coordinates": [115.85, -31.95]
  },
  "properties": {
    "osm_type": "node",
    "osm_id": 123,
    "tags": {
      "name": "Example"
    }
  }
}
```

Coordinates follow GeoJSON order: longitude first, then latitude.

Ways become `LineString` or `Polygon` geometries. Multipolygon relations become `Polygon` or `MultiPolygon` geometries, and other relations become `GeometryCollection` geometries. Bounding boxes are included where they can be calculated.

## `pbf2json`

`pbf2json` scans an OpenStreetMap PBF file, selects elements by tag, resolves way nodes and relation members, and writes GeoJSON to standard output. It makes multiple passes over the input, so the input must be a seekable file rather than standard input.

```sh
./bin/pbf2json -tags='amenity' region.osm.pbf > amenities.geojsonl
```

The `-tags` expression is required. Commas separate alternatives, `+` joins conditions that must all match, and `~` requires an exact value:

```sh
# Elements containing either a building tag or a shop tag
./bin/pbf2json -tags='building,shop' region.osm.pbf

# Elements containing both address tags
./bin/pbf2json -tags='addr:housenumber+addr:street' region.osm.pbf

# Named highways or named waterways
./bin/pbf2json -tags='highway+name,waterway+name' region.osm.pbf

# Exact tag values
./bin/pbf2json -tags='cuisine~vegetarian,cuisine~vegan' region.osm.pbf
```

Options:

| Option | Meaning |
| --- | --- |
| `-tags=EXPRESSION` | Required tag filter. Comma groups are OR conditions; terms joined by `+` are AND conditions; `key~value` requires an exact value. |
| `-leveldb=PATH` | Use `PATH` as a new LevelDB directory for node and relation indexing. If omitted, an isolated temporary directory is created. |
| `-batch=N` | Buffer this many LevelDB writes per batch. The default is `50000`; larger values use more memory and may improve throughput. |
| `-waynodes=true` | Include each way node's latitude and longitude in `properties.nodes`. It is disabled by default because it increases output size. |
| `-strict` | Write one GeoJSON `FeatureCollection` instead of JSONL. |
| `-test` | Run the program's built-in checks and exit without reading a PBF file. |

Only one PBF filename may be supplied. An explicitly selected LevelDB path must not already contain a database.

## `osm2geojson`

`osm2geojson` converts OpenStreetMap XML into GeoJSON. It resolves referenced nodes, closed ways, multipolygons, and nested relation members.

```sh
./bin/osm2geojson region.osm region.geojsonl
./bin/osm2geojson --strict region.osm region.geojson
./bin/osm2geojson region.osm.gz region.geojson.gz
```

Input defaults to standard input and output defaults to standard output. Use `-` explicitly for either stream:

```sh
./bin/osm2geojson --compression=bz2 - converted.geojsonl
```

Options:

| Option | Meaning |
| --- | --- |
| `-compression=bz2` | Decompress bzip2 input. |
| `-compression=gz` | Decompress gzip input. |
| `-compression=''` | Detect compression from the input filename. This is the default. For compressed standard input, specify the format explicitly. |
| `-strict` | Write one GeoJSON `FeatureCollection` instead of JSONL. |
| `-test` | Run the program's built-in checks and exit. |

An output filename ending in `.gz` is gzip-compressed automatically.

## `wikipedia2geojson`

`wikipedia2geojson` reads a MediaWiki XML dump, extracts coordinates from page revisions, and writes Point Features whose `properties.name` is the page title.

```sh
./bin/wikipedia2geojson enwiki-pages.xml.bz2 > wikipedia.geojsonl
./bin/wikipedia2geojson --strict enwiki-pages.xml.bz2 > wikipedia.geojson
```

It also accepts a Wikimedia multistream index and data file:

```sh
./bin/wikipedia2geojson enwiki-index.txt.bz2 enwiki-pages-multistream.xml.bz2 > wikipedia.geojsonl
```

Use `-` to read a single stream from standard input. The output is always written to standard output.

Options:

| Option | Meaning |
| --- | --- |
| `-compression=bz2` | Force bzip2 decompression for single-file or standard-input mode. |
| `-compression=gz` | Force gzip decompression for single-file or standard-input mode. |
| `-workers=N` | Run `N` page-parsing workers. The default is `8`; output remains in source order. |
| `-cpus=N` | Allow Go to execute on `N` logical CPUs. The default is the runtime's current CPU limit. |
| `-strict` | Write one GeoJSON `FeatureCollection` instead of JSONL. |
| `-help` | Print usage examples and exit. |
| `-test` | Run the program's built-in checks and exit. |

Pages without coordinates are skipped. Pages with invalid coordinate data are logged and saved to `errors.gob` in the current directory for later inspection.

## `wikipedia2doc2vec`

`wikipedia2doc2vec` turns a MediaWiki XML dump into one tokenized document per line:

```text
PAGE_ID<TAB>lowercase tokens separated by spaces
```

It removes common MediaWiki markup, comments, references, templates, tables, links, URLs, and HTML tags before tokenizing Unicode letters and numbers.

```sh
./bin/wikipedia2doc2vec enwiki-pages.xml.bz2 > wikipedia-documents.txt
./bin/wikipedia2doc2vec -limit=10000 enwiki-pages.xml.bz2 > sample-documents.txt
```

Multistream dumps use the same two-file form as `wikipedia2geojson`:

```sh
./bin/wikipedia2doc2vec enwiki-index.txt.bz2 enwiki-pages-multistream.xml.bz2 > wikipedia-documents.txt
```

Options:

| Option | Meaning |
| --- | --- |
| `-compression=bz2` | Force bzip2 decompression for single-file or standard-input mode. |
| `-compression=gz` | Force gzip decompression for single-file or standard-input mode. |
| `-workers=N` | Run `N` page-parsing workers. The default is `8`; output remains in source order. |
| `-cpus=N` | Allow Go to execute on `N` logical CPUs. The default is the runtime's current CPU limit. |
| `-limit=N` | Stop after exactly `N` pages. The default `-1` processes the complete input; `0` processes no pages. |
| `-help` | Print usage examples and exit. |
| `-test` | Run the program's built-in checks and exit. |

Pages that cannot be converted are logged and saved to `errors.gob` in the current directory.

## `geojson2entirety`

`geojson2entirety` reads GeoJSON Point Features from standard input and creates the binary files used by the Entirety map viewer. It accepts JSONL Features, a JSON array of Features, or a standard `FeatureCollection`.

```sh
./bin/geojson2entirety -outFile=world < places.geojson
```

The command creates:

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

Features must have Point geometry with finite coordinates. A string `properties.name` creates a named tag; unnamed points are stored as map points.

Options:

| Option | Meaning |
| --- | --- |
| `-outFile=PREFIX` | Prefix for all eight output files. The default is `default_map`. |
| `-limit=N` | Stop after exactly `N` input Features. The default `-1` processes the complete input; `0` processes none. |
| `-points` | Store every input Feature as an unnamed map point and omit tag records. |
| `-tags` | Store only named Features as tags and omit unnamed map points. This cannot be combined with `-points`. |
| `-verbose` | Log every converted point or tag. It is disabled by default. |
| `-test` | Run the program's built-in checks and exit without creating map files. |

## `geojsoncheck`

`geojsoncheck` validates every Feature and writes a JSON report containing the Feature count, position count, geometry-type counts, and combined bounding box. It checks geometry structure, finite numbers, polygon ring closure, coordinate dimensions, bounding boxes, longitude and latitude ranges, and required Feature members.

```sh
./bin/geojsoncheck < places.geojson
```

Options:

| Option | Meaning |
| --- | --- |
| `-input=auto` | Detect GeoJSONL, a Feature array, or a FeatureCollection. Use `seq` for an RFC 8142 sequence. |
| `-allow-null-geometry` | Accept Features whose geometry is `null`. Null geometry is rejected by default. |
| `-allow-out-of-range` | Accept longitude outside -180..180 and latitude outside -90..90. This is disabled by default. |
| `-test` | Run built-in valid- and invalid-geometry checks and exit. |

## `geojsonseq`

`geojsonseq` changes the framing of a GeoJSON Feature stream without changing its Features.

```sh
./bin/geojsonseq -output=seq < places.geojson > places.geojson-seq
./bin/geojsonseq -input=seq -output=collection < places.geojson-seq > places.geojson
```

Options:

| Option | Meaning |
| --- | --- |
| `-input=auto` | Detect GeoJSONL, a Feature array, or a FeatureCollection. Use `seq` for RFC 8142 input. |
| `-output=seq` | Write `seq`, `jsonl`, or `collection` output. The default is `seq`. |
| `-test` | Run a FeatureCollection-to-sequence round-trip check and exit. |

## `geofilter`

`geofilter` selects Features and properties while streaming. Filters are combined: a Feature must satisfy every supplied geometry, property, and bounding-box condition.

```sh
./bin/geofilter -geometry=Point -has=name -bbox=103,1,104,2 -select=name,population -output=collection < places.geojson
./bin/geofilter -where='capital=true,kind="city"' < places.geojsonl
```

Options:

| Option | Meaning |
| --- | --- |
| `-input=auto` | Detect GeoJSONL, a Feature array, or a FeatureCollection. Use `seq` for RFC 8142 input. |
| `-output=jsonl` | Write `jsonl`, `collection`, or `seq` output. |
| `-geometry=TYPES` | Retain the comma-separated geometry types, such as `Point,Polygon`. Empty retains every type. |
| `-has=NAMES` | Require every comma-separated property name to exist. Empty adds no property requirement. |
| `-where=CONDITIONS` | Require comma-separated exact values such as `kind=city,population=42`. JSON numbers, booleans, strings, and `null` retain their types. |
| `-select=NAMES` | Keep only the named properties. Empty keeps every property. |
| `-drop=NAMES` | Remove the named properties. It cannot be combined with `-select`. |
| `-bbox=MINLON,MINLAT,MAXLON,MAXLAT` | Retain geometries intersecting the WGS 84 bounding box. Empty disables spatial filtering. |
| `-limit=N` | Stop after writing `N` Features. The default `-1` has no limit; `0` writes none. |
| `-allow-null-geometry` | Accept null geometries. They are retained only when no geometry or bounding-box filter excludes them. |
| `-test` | Run a built-in property and geometry filter check and exit. |

## `geoparquet`

`geoparquet` encodes GeoJSON as GeoParquet or decodes GeoParquet to GeoJSON. Encoded files contain typed scalar property columns, JSON logical columns for arrays and objects, per-row bounding boxes, and GeoParquet metadata. The decoder also reads externally produced nested columns.

```sh
./bin/geoparquet < places.geojson > places.parquet
./bin/geoparquet -mode=decode -output=collection < places.parquet > places.geojson
./bin/geoparquet -crs=EPSG:3857 -geometry-encoding=native < points.geojson > points.parquet
```

Options:

| Option | Meaning |
| --- | --- |
| `-mode=encode` | Use `encode` for GeoJSON to GeoParquet or `decode` for GeoParquet to GeoJSON. |
| `-input=auto` | GeoJSON framing used while encoding. Use `seq` for RFC 8142 input. |
| `-output=jsonl` | GeoJSON framing used while decoding: `jsonl`, `collection`, or `seq`. |
| `-crs=OGC:CRS84` | Geometry CRS for encoded data. Supported values are `OGC:CRS84`, `OGC:CRS84h`, `EPSG:4326`, and `EPSG:3857`; WGS 84 GeoJSON is reprojected when necessary. |
| `-geometry-encoding=wkb` | Use `wkb` for mixed geometry types or `native` for a GeoArrow geometry column. Native encoding requires a non-empty input containing one geometry type. |
| `-test` | Run an in-memory GeoJSON-to-GeoParquet-to-GeoJSON check and exit. |

GeoJSON output is always WGS 84 longitude and latitude. The decoder rejects an unsupported source CRS instead of returning mislabeled coordinates. WKB preserves two- and three-dimensional coordinates; native encoding currently supports Point, LineString, Polygon, and their multi-geometry forms.

## `flatgeobuf`

`flatgeobuf` converts GeoJSON to or from FlatGeobuf. Indexed encoding writes native property columns and a packed spatial index. Streaming encoding begins writing immediately and uses one JSON column to preserve each Feature exactly.

```sh
./bin/flatgeobuf -layer=places < places.geojson > places.fgb
./bin/flatgeobuf -mode=decode -bbox=103,1,104,2 -output=collection < places.fgb > nearby.geojson
./bin/flatgeobuf -index=false < large.geojsonl > large.fgb
```

Options:

| Option | Meaning |
| --- | --- |
| `-mode=encode` | Use `encode` for GeoJSON to FlatGeobuf or `decode` for FlatGeobuf to GeoJSON. |
| `-input=auto` | GeoJSON framing used while encoding. Use `seq` for RFC 8142 input. |
| `-output=jsonl` | GeoJSON framing used while decoding: `jsonl`, `collection`, or `seq`. |
| `-layer=features` | Layer name stored in FlatGeobuf metadata. |
| `-index=true` | Buffer the input, write native property columns, and build a packed index. Set `false` to stream an unindexed file through one lossless JSON property column. |
| `-bbox=MINLON,MINLAT,MAXLON,MAXLAT` | During decoding, return only Features intersecting this box. Indexed input uses the index; unindexed input is scanned. Empty returns every Feature. |
| `-test` | Run an in-memory GeoJSON-to-FlatGeobuf-to-GeoJSON check and exit. |

The decoder accepts EPSG:4326, OGC:CRS84, and OGC:CRS84h input. Other coordinate reference systems are rejected because the command cannot safely label their coordinates as GeoJSON.

## `jsonfg`

`jsonfg` adds or removes JSON-FG 1.0 declarations and accepts either a JSON-FG Feature or FeatureCollection while decoding. Encoding retains the WGS 84 GeoJSON `geometry` fallback and writes a `place` geometry when a non-GeoJSON CRS is selected. A configurable property maps to JSON-FG temporal data.

```sh
./bin/jsonfg -place-crs=EPSG:3857 < places.geojson > places.jsonfg
./bin/jsonfg -mode=decode -output=collection < places.jsonfg > places.geojson
```

Options:

| Option | Meaning |
| --- | --- |
| `-mode=encode` | Use `encode` for GeoJSON to JSON-FG or `decode` for JSON-FG to GeoJSON. |
| `-input=auto` | GeoJSON framing used while encoding. Use `seq` for RFC 8142 input. |
| `-output=jsonl` | GeoJSON framing used while decoding: `jsonl`, `collection`, or `seq`. |
| `-place-crs=OGC:CRS84` | CRS for `place`: `OGC:CRS84`, `OGC:CRS84h`, `EPSG:4326`, or `EPSG:3857`. The default omits a redundant simple WGS 84 `place`; the GeoJSON fallback remains WGS 84. |
| `-time-property=jsonfg_time` | Property mapped to JSON-FG `time` while encoding and restored while decoding. Use an empty value to disable temporal mapping. |
| `-test` | Run an in-memory GeoJSON-to-JSON-FG-to-GeoJSON check and exit. |

JSON-FG `place` coordinates honor the declared CRS axis order, including latitude-longitude for EPSG:4326. GeoParquet deliberately uses its specified x-y override, so EPSG:4326 remains longitude-latitude there.

## `geojson2mvt` and `mvt2geojson`

`geojson2mvt` converts WGS 84 GeoJSON into one Mapbox Vector Tile. It projects coordinates to the specified XYZ tile, clips geometry to the tile plus a buffer, simplifies it, and quantizes coordinates to the tile extent. `mvt2geojson` reverses the projection when the same tile coordinates are supplied.

```sh
./bin/geojson2mvt -z=12 -x=3229 -y=2046 -layer=places < places.geojson > 12-3229-2046.mvt
./bin/mvt2geojson -z=12 -x=3229 -y=2046 -layer=places -output=collection < 12-3229-2046.mvt > tile.geojson
```

`geojson2mvt` options:

| Option | Meaning |
| --- | --- |
| `-z=0`, `-x=0`, `-y=0` | XYZ tile coordinates. Zoom must be 0 through 30, and x and y must exist at that zoom. |
| `-layer=features` | Default output layer name. |
| `-extent=4096` | Integer coordinate extent used inside the tile. |
| `-buffer=64` | Clipping buffer in tile-coordinate units. Use `0` to clip exactly at the tile edge. |
| `-simplify=1` | Douglas-Peucker tolerance in tile-coordinate units. Use `0` to disable simplification. |
| `-gzip=false` | Gzip-compress the output tile when enabled. |
| `-layer-property=NAME` | Read each Feature's layer name from this string property. Empty puts every Feature in `-layer`. |
| `-drop-layer-property=false` | Remove the layer-selection property after selecting a layer. It is retained by default. |
| `-id-property=__geotools_geojson_id` | Store each exact GeoJSON string or numeric Feature id in this MVT string property. Empty disables exact preservation and leaves only native non-negative integer MVT ids. |
| `-input=auto` | Detect GeoJSONL, a Feature array, or a FeatureCollection. Use `seq` for RFC 8142 input. |
| `-test` | Run an in-memory GeoJSON-to-MVT check and exit. |

`mvt2geojson` options:

| Option | Meaning |
| --- | --- |
| `-z=0`, `-x=0`, `-y=0` | XYZ coordinates of the source tile. They must match the values used to create it. |
| `-layer=features` | Decode this layer unless `-all-layers` is enabled. |
| `-all-layers=false` | Decode every layer in name order. |
| `-layer-property=mvt_layer` | With `-all-layers`, write the source layer name to this property. Use an empty value to omit layer identity. |
| `-id-property=__geotools_geojson_id` | Restore and remove an exact GeoJSON Feature id stored by `geojson2mvt`. Empty leaves the property and native MVT id unchanged. |
| `-gzip=false` | Read a gzip-compressed tile when enabled. |
| `-output=jsonl` | Write `jsonl`, `collection`, or `seq` GeoJSON. |
| `-test` | Run an in-memory MVT-to-GeoJSON check and exit. |

Vector tiles are intentionally lossy: coordinates are clipped, simplified, and quantized, and unsupported GeoJSON geometry or coordinate dimensions are rejected. MVT has no array, object, or null attribute type, so those property values are stored as compact JSON strings.

## `geojson2mbtiles`

`geojson2mbtiles` builds a new SQLite MBTiles vector-tile archive from WGS 84 GeoJSON. It determines which XYZ tiles intersect the input at every requested zoom, encodes those tiles, stores TMS row numbers, and writes bounds, zoom, layer, and vector-layer metadata.

```sh
./bin/geojson2mbtiles -output=places.mbtiles -name='Places' -min-z=0 -max-z=8 < places.geojson
./bin/geojson2mbtiles -output=map.mbtiles -layer-property=layer -drop-layer-property < multilayer.geojson
```

The output path must not already exist.

Options:

| Option | Meaning |
| --- | --- |
| `-output=tiles.mbtiles` | Path of the new archive. Existing files are never overwritten. |
| `-name=geotools` | Tileset name stored in MBTiles metadata. |
| `-layer=features` | Default MVT layer name. |
| `-layer-property=NAME` | Read each Feature's MVT layer from this string property. Empty uses `-layer`. |
| `-drop-layer-property=false` | Remove the layer-selection property from tile attributes when enabled. |
| `-id-property=__geotools_geojson_id` | Store exact GeoJSON Feature ids in this MVT string property. Empty leaves only native non-negative integer MVT ids. |
| `-min-z=0` | Lowest included zoom level. |
| `-max-z=5` | Highest included zoom level. |
| `-extent=4096` | Integer coordinate extent used inside every vector tile. |
| `-buffer=64` | Clipping buffer in tile-coordinate units. |
| `-simplify=1` | Geometry simplification tolerance in tile-coordinate units. Use `0` to disable it. |
| `-gzip=true` | Gzip-compress `tile_data` records. Disable only for readers that require raw vector tiles. |
| `-max-tiles=100000` | Abort before creating an archive larger than this tile count. Increase deliberately for a larger zoom range. |
| `-input=auto` | Detect GeoJSONL, a Feature array, or a FeatureCollection. Use `seq` for RFC 8142 input. |
| `-test` | Run an in-memory GeoJSON-to-MBTiles check and exit. |

## Verification

Run the complete offline test suite:

```sh
./test_all.sh
```

The suite exercises the real conversion paths and checks:

- PBF framing, tag expressions, LevelDB indexing, way denormalization, optional way nodes, strict output, and multipolygons with inner rings
- OSM XML nodes, ways, multipolygons, nested relations, empty tags, missing references, strict output, and gzip output
- MediaWiki XML parsing, real coordinate templates, worker ordering, malformed input, page limits, markup removal, and Unicode tokens
- GeoJSONL, arrays, and FeatureCollections converted into all eight Entirety files, including byte-level float, integer, offset, string, point, and tag validation
- GeoJSON structural and geometry validation, RFC 8142 framing, filtering, foreign members, null values, bounding boxes, and collection-member ordering
- GeoParquet WKB, native GeoArrow geometry, typed and nested columns, CRS reprojection, 3D coordinates, and lossless round trips
- Indexed, unindexed, empty, and bounding-box-selected FlatGeobuf files
- JSON-FG place, CRS, time, conformance, fallback geometry, and round-trip behavior
- Vector-tile clipping, simplification, multilayer identity, GeoJSON round trips, and MBTiles archive metadata
- Offline interoperability with files written by DuckDB, the FlatGeobuf project, and a real seven-layer vector tile

The reduced Vancouver records come from the repository's archived 2015 OpenStreetMap PBF fixture and the [OpenStreetMap API representation of way 23254060](https://api.openstreetmap.org/api/0.6/way/23254060/full). The Wikipedia samples use the page IDs, revision IDs, text, and coordinate templates from [Singapore](https://en.wikipedia.org/w/index.php?title=Singapore&action=raw) and the [Eiffel Tower](https://en.wikipedia.org/w/index.php?title=Eiffel_Tower&action=raw). These records are embedded in the tests, so the suite does not contact either service.

Each program also includes a local built-in check:

```sh
./bin/pbf2json -test
./bin/osm2geojson -test
./bin/wikipedia2geojson -test
./bin/wikipedia2doc2vec -test
./bin/geojson2entirety -test
./bin/geojsoncheck -test
./bin/geojsonseq -test
./bin/geofilter -test
./bin/geoparquet -test
./bin/flatgeobuf -test
./bin/jsonfg -test
./bin/geojson2mvt -test
./bin/mvt2geojson -test
./bin/geojson2mbtiles -test
```

The Go tests can be run without building the shared `bin/` directory:

```sh
go test ./...
go -C pbf2geojson test ./...
go -C osm2geojson/osm2geojson test ./...
go -C wikipedia2geojson test ./...
go -C wikipedia2doc2vec test ./...
go -C geojson2entirety test ./...
```

The separate legacy Node PBF end-to-end test requires `pbf2geojson/test/vancouver_canada.osm.pbf` with SHA-1 `c033bef77dcb88ceb8e224aa17c6fe388a217c98`. That optional test does not download the fixture.
