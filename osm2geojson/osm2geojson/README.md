# osm2geojson

`osm2geojson` converts OpenStreetMap XML into WGS 84 GeoJSON. It resolves node references, classifies ways as lines or areas from their tags, assembles multipolygon and boundary relations, expands nested relations, and validates missing or cyclic references.

## Build

From the Geotools repository root:

```sh
./build_all.sh
```

Or build this module directly:

```sh
mkdir -p bin
go -C osm2geojson/osm2geojson build -o ../../bin/osm2geojson .
```

## Use

```sh
./bin/osm2geojson region.osm region.geojsonl
./bin/osm2geojson -strict region.osm region.geojson
./bin/osm2geojson region.osm.gz region.geojson.gz
./bin/osm2geojson -compression=bz2 - converted.geojsonl
```

The positional arguments are `[input|-] [output|-]`. With no paths, the command reads standard input and writes standard output. `-` explicitly selects either stream.

Input filenames ending in `.gz` or `.bz2` are decompressed automatically. For compressed standard input, set `-compression=gz` or `-compression=bz2`. An output filename ending in `.gz` is gzip-compressed.

## Output

The default is one compact Feature per line. `-strict` writes one GeoJSON `FeatureCollection`.

Element IDs use `node/ID`, `way/ID`, and `relation/ID`. Properties retain `osm_type`, numeric `osm_id`, and all tags. Node geometries are Points. Ways become Polygons only when their tags describe an area or explicitly set `area=yes`; other closed ways remain LineStrings. Polygon exterior rings are counterclockwise and holes are clockwise. Multipolygon and boundary relations become Polygon or MultiPolygon geometries; other relations become GeometryCollections.

For a named input, the command makes two streaming passes without a temporary copy. The first pass records references; the second emits Features and releases resolved nodes and ways. Standard input cannot be rewound and therefore uses a one-pass in-memory fallback.

## Options

| Option | Meaning |
| --- | --- |
| `-compression=''` | Detect gzip or bzip2 from the input filename. This is the default. |
| `-compression=gz` | Force gzip decompression. |
| `-compression=bz2` | Force bzip2 decompression. |
| `-strict` | Write one FeatureCollection instead of GeoJSONL. |
| `-test` | Run built-in node, way, relation, geometry, and compression checks. |

Only OpenStreetMap XML is accepted; use `pbf2json` for `.osm.pbf` input.

## Test

```sh
go -C osm2geojson/osm2geojson test ./...
./bin/osm2geojson -test
```
