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

Entirety geojson breaks the geojson standard in two small but important ways.  Entirety geojson omits the [] array brackets that usually surround the entire geojson file.  So instead of having one giant JSON array in the file, there are simply the array elements.

In addition, each element (each *feature*) must be on its own line, followed by a newline \n.  This allows the parser to easily resume after an error, and also makes the import (and generation) code much smaller and easier to write.
