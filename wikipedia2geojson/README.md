# wikipedia2geojson

`wikipedia2geojson` extracts coordinates from local Wikipedia XML dumps and writes a GeoJSON Point for each page containing a supported `Coord` template. It makes no network requests.

## Build

From the repository root:

```sh
./build_all.sh
```

The binary is written to `bin/wikipedia2geojson`. To build only this command:

```sh
mkdir -p bin
go -C wikipedia2geojson build -o ../bin/wikipedia2geojson .
```

## Usage

Plain XML, gzip, and bzip2 inputs are detected from the filename:

```sh
./bin/wikipedia2geojson enwiki-pages.xml.bz2 > places.geojsonl
./bin/wikipedia2geojson -strict enwiki-pages.xml.gz > places.geojson
```

Use `-` to read a plain or explicitly compressed stream from standard input:

```sh
./bin/wikipedia2geojson -compression=bz2 - < enwiki-pages.xml.bz2 > places.geojsonl
```

Wikipedia multistream dumps can be processed from their local index and data files:

```sh
./bin/wikipedia2geojson enwiki-index.txt.bz2 enwiki-pages-multistream.xml.bz2 > places.geojsonl
```

The converter examines only the first revision present for each page. The default output is one valid GeoJSON Feature per line. `-strict` writes one GeoJSON FeatureCollection instead. Coordinates and output records retain source-page order even when multiple parsing workers are used.

## Options

| Option | Meaning |
| --- | --- |
| `-compression=''` | Detect plain, gzip, or bzip2 input from the filename. This is the default. Use `gz` or `bz2` for compressed standard input. |
| `-cpus=N` | Set the number of Go execution threads. The default is the runtime CPU count. |
| `-workers=8` | Set concurrent page parsing workers. Accepted values are 1 through 1000. |
| `-strict` | Emit one GeoJSON FeatureCollection instead of GeoJSON Lines. |
| `-help` | Print input examples and exit. |
| `-test` | Run the command's built-in checks and exit. |

Pages without coordinates are skipped. Pages without revisions or with invalid or non-finite coordinates are reported on standard error and recorded in `errors.gob`; that file is created when the first page-level error is encountered. XML parser failures abort conversion and are not added to that file.

Processing is streaming and ordered. The parser and workers use bounded queues, so memory use does not grow with the number of pages in a dump.

## Tests

From the repository root:

```sh
go -C wikipedia2geojson test ./...
./bin/wikipedia2geojson -test
```
