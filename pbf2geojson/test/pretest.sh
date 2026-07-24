
#!/bin/bash
# @ref: https://gist.github.com/missinglink/352f5be805395babada0

dirname=$(dirname $0);
cd "$dirname";
file=vancouver_canada.osm.pbf;

if [ ! -e "$file" ]; then
    echo "missing local fixture: $dirname/$file" >&2
    exit 1
fi

hash=`shasum "$file" | awk '{ print $1 }'`;
if test "$hash" != c033bef77dcb88ceb8e224aa17c6fe388a217c98; then
    echo "invalid fixture hash for: $dirname/$file" >&2
    exit 1
fi
