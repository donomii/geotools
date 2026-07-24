# Geotools

Geotools is a collection of command-line programs for converting OpenStreetMap and Wikipedia into GeoJSON data.

The repository contains five converters:

| Program | Input | Output |
| --- | --- | --- |
| `pbf2json` | OpenStreetMap PBF | GeoJSON Features |
| `osm2geojson` | OpenStreetMap XML | GeoJSON Features |
| `wikipedia2geojson` | MediaWiki XML dumps | GeoJSON Point Features |
| `wikipedia2doc2vec` | MediaWiki XML dumps | Tokenized document lines |
| `geojson2entirety` | GeoJSON Point Features | Eight binary Entirety map files |

The GeoJSON tools write one Feature per line. This JSONL form is convenient for large datasets because downstream programs can process one line at a time. Adding `--strict` writes a standard GeoJSON `FeatureCollection` instead.

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

## Verification

Each program includes a local built-in check:

```sh
./bin/pbf2json -test
./bin/osm2geojson -test
./bin/wikipedia2geojson -test
./bin/wikipedia2doc2vec -test
./bin/geojson2entirety -test
```

The Go tests can be run without building the shared `bin/` directory:

```sh
go -C pbf2geojson test ./...
go -C osm2geojson/osm2geojson test ./...
go -C wikipedia2geojson test ./...
go -C wikipedia2doc2vec test ./...
go -C geojson2entirety test ./...
```

The PBF end-to-end test additionally requires `pbf2geojson/test/fixtures/vancouver_canada.osm.pbf` with SHA-1 `c033bef77dcb88ceb8e224aa17c6fe388a217c98`. The test does not download this fixture.
