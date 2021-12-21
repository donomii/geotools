[![Build Status](https://travis-ci.org/donomii/geojson2entirety.svg?branch=master)](https://travis-ci.org/donomii/geojson2entirety)
[![GoDoc](https://godoc.org/github.com/donomii/geojson2entirety?status.svg)](https://godoc.org/github.com/donomii/geojson2entirety)

# geojson2entirety
Importer for the entirety maptool

## Use

    cat data.geojson | geojson2entirety --outFile myMap --limit 1000

Will create a set of entirety map files called "myMap", using the first 1000 points from data.geojson

## Options

    --outFile mapName   Will create binary map files named mapName
    --limit n           Will stop after converting n points.  Omit --limit to convert the entire file

## Entirety

The Entirety maptool displays the planet, in its entirety.  Using data from multiple sources, we display every known location on the planet, in a responsive interface.  *geojson2entirety* converts slightly non-standard geojson into the Entirety map format, ready for display.

## Format

Entirety geojson expects input in a slightly non-standard format.  Each element (each *feature*) must be on its own line, followed by a newline \n.  This allows the parser to easily resume after an error, and also makes the import (and generation) code much smaller and easier to write.

I plan to fix this in the future.
