#!/bin/bash
set -e

echo "=== Building Geotools ==="
mkdir -p bin

echo "Building geojson2entirety..."
(cd geojson2entirety && go build -o ../bin/geojson2entirety main.go treeindex.go)

echo "Building osm2geojson..."
(cd osm2geojson/osm2geojson && go build -o ../../bin/osm2geojson main.go)

echo "Building pbf2json..."
# Building from the directory to handle dependencies/imports correctly
(cd pbf2geojson && go build -o ../bin/pbf2json pbf2json.go bitmask.go bitmaskmap.go cache.go line_centroid.go poly_centroid.go)

echo "Building wikipedia2doc2vec..."
(cd wikipedia2doc2vec && go build -o ../bin/wikipedia2doc2vec docvecextract.go)

echo "Building wikipedia2geojson..."
(cd wikipedia2geojson && go build -o ../bin/wikipedia2geojson geoextract.go)

echo "Building geojsoncheck..."
go build -o bin/geojsoncheck ./geojsoncheck

echo "Building geojsonseq..."
go build -o bin/geojsonseq ./geojsonseq

echo "Building geofilter..."
go build -o bin/geofilter ./geofilter

echo "Building geoparquet..."
go build -o bin/geoparquet ./geoparquet

echo "Building mbtiles..."
go build -o bin/mbtiles ./mbtiles

echo "Building flatgeobuf..."
go build -o bin/flatgeobuf ./flatgeobuf

echo "Building jsonfg..."
go build -o bin/jsonfg ./jsonfg

echo "Building geojson2mvt..."
go build -o bin/geojson2mvt ./geojson2mvt

echo "Building mvt2geojson..."
go build -o bin/mvt2geojson ./mvt2geojson

echo "Building geojson2mbtiles..."
go build -o bin/geojson2mbtiles ./geojson2mbtiles

echo "=== Build Complete! Binaries are in ./bin ==="
ls -l bin
