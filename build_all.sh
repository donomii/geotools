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

echo "=== Build Complete! Binaries are in ./bin ==="
ls -l bin
