# wikipedia2geojson

`wikipedia2geojson` extracts coordinate-bearing pages from local Wikipedia XML dumps as GeoJSON Points. It reads plain XML, gzip, bzip2, standard input, and Wikipedia multistream index/data pairs without making network requests.

Build all commands from the repository root:

```sh
./build_all.sh
```

Process a compressed dump as GeoJSON Lines:

```sh
./bin/wikipedia2geojson enwiki-pages.xml.bz2 > places.geojsonl
```

Write one GeoJSON FeatureCollection:

```sh
./bin/wikipedia2geojson -strict enwiki-pages.xml.gz > places.geojson
```

Read compressed standard input:

```sh
./bin/wikipedia2geojson -compression=bz2 - < enwiki-pages.xml.bz2 > places.geojsonl
```

Read a multistream index and data file:

```sh
./bin/wikipedia2geojson enwiki-index.txt.bz2 enwiki-pages-multistream.xml.bz2 > places.geojsonl
```

Useful options are `-compression`, `-cpus=N`, `-workers=8`, `-strict`, `-help`, and `-test`. An empty compression setting detects the format from the filename; use `gz` or `bz2` for compressed standard input. Worker counts may range from 1 through 1000.

The converter examines only the first revision present for each page and preserves source-page order with bounded queues. Pages without coordinates are skipped. Pages without revisions or with invalid or non-finite coordinates are written to standard error and `errors.gob`. XML parser failures abort conversion and are not added to that file.

The complete guide is in the parent [`README.md`](../README.md).
