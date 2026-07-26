# pbf2json

`pbf2json` selects tagged OpenStreetMap objects from a local PBF extract and writes valid GeoJSON. It resolves way nodes, assembles supported relations, and emits geometries rather than the normalized node references stored in OSM.

The input must be a seekable PBF file because the converter makes multiple streaming passes over it. Standard input is not supported.

## Build

From the repository root:

```sh
./build_all.sh
```

The binary is written to `bin/pbf2json`. To build only this command:

```sh
mkdir -p bin
go -C pbf2geojson build -o ../bin/pbf2json .
```

## Usage

`-tags` is required:

```sh
./bin/pbf2json -tags=amenity wellington.osm.pbf > amenities.geojsonl
```

The default output is one GeoJSON Feature per line:

```json
{"type":"Feature","id":"node/170603342","geometry":{"type":"Point","coordinates":[174.7944402,-41.289843]},"properties":{"osm_type":"node","osm_id":170603342,"tags":{"amenity":"fountain","name":"Oriental Bay Fountain"}}}
```

Use `-strict` to write one GeoJSON FeatureCollection:

```sh
./bin/pbf2json -strict -tags=building,shop city.osm.pbf > selected.geojson
```

## Tag expressions

Comma-separated groups are alternatives:

```text
-tags=building,shop
```

Conditions joined by `+` must all match:

```text
-tags=addr:housenumber+addr:street
```

The two forms can be combined:

```text
-tags=highway+name,waterway+name
```

Use `~` to require a specific value:

```text
-tags=cuisine~vegetarian,cuisine~vegan
```

## Geometries and properties

Nodes become Points. Ways become LineStrings or Polygons according to their tags. Multipolygon and boundary relations are assembled from member ways, including nested relations. Polygon rings are normalized to GeoJSON winding order.

Every Feature has an ID such as `node/123`, `way/456`, or `relation/789`. Properties contain `osm_type`, numeric `osm_id`, and the original OSM tags. Ways also include a centroid and bounding box. `-waynodes` adds the resolved node latitude/longitude list to way properties.

Objects required to construct a selected way or relation are retained even when they do not independently match the requested tags.

## Coordinate storage

Coordinates for every node referenced by a selected way or relation, and every way needed by a selected relation, are stored in memory by default; no temporary database is created. Selection masks and selected or nested relations also remain in memory.

For inputs that do not fit comfortably in memory, provide a path for a new disk-backed LevelDB:

```sh
./bin/pbf2json -tags=building -leveldb=/data/pbf-cache city.osm.pbf > buildings.geojsonl
```

`-batch` controls the number of cached elements per LevelDB write and defaults to 50000. It is ignored when `-leveldb` is empty.

## Options

| Option | Meaning |
| --- | --- |
| `-tags=EXPRESSION` | Required tag selection expression using comma, `+`, and optional `~value` conditions. |
| `-strict` | Emit one GeoJSON FeatureCollection instead of GeoJSON Lines. |
| `-waynodes` | Include resolved way-node coordinates in way properties. |
| `-leveldb=PATH` | Store referenced-node coordinates and relation-member ways in a new LevelDB directory instead of memory. Selection masks and selected or nested relations remain in memory. |
| `-batch=50000` | Set elements per disk-backed LevelDB write; ignored for in-memory storage. |
| `-test` | Run the command's built-in checks and exit. |

## Node module

The Node wrapper exposes `createReadStream(config)` and decodes the command's GeoJSON Lines into an object stream. Install its dependencies with `npm install`, then place a matching binary at `pbf2geojson/build/pbf2json.<platform>-<architecture>`.

```js
const pbf2json = require("./index");

pbf2json.createReadStream({
  file: "/data/city.osm.pbf",
  tags: ["addr:housenumber+addr:street"],
  waynodes: false
}).on("data", feature => {
  console.log(feature);
});
```

The optional `leveldb`, `batch`, and `waynodes` configuration properties map to the corresponding command options.

## Tests

From the repository root:

```sh
go -C pbf2geojson test ./...
./bin/pbf2json -test
```

After installing the optional Node dependencies, run the Node assertions with:

```sh
node pbf2geojson/test/run.js
```

The Node end-to-end suite also requires `pbf2geojson/test/vancouver_canada.osm.pbf` with the hash documented by its pretest script. The suite does not download the fixture.
