COPY (
    SELECT
        from_hex('0101000000b1506b9a77f45940e0be0e9c33a2f53f')::BLOB AS geom,
        'Singapore'::VARCHAR AS name,
        5917600::BIGINT AS population,
        {'country': 'Singapore', 'rank': 1::BIGINT} AS profile,
        ['city', 'capital']::VARCHAR[] AS tags
) TO 'testdata/external/duckdb_geoparquet.parquet' (
    FORMAT parquet,
    KV_METADATA {
        geo: '{"version":"1.1.0","primary_column":"geom","columns":{"geom":{"encoding":"WKB","geometry_types":["Point"]}}}'
    }
);
