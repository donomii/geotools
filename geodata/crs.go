package geodata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/twpayne/go-geom"
	geomjson "github.com/twpayne/go-geom/encoding/geojson"
)

const (
	CRSCRS84    = "OGC:CRS84"
	CRSCRS84h   = "OGC:CRS84h"
	CRSEPSG4326 = "EPSG:4326"
	CRSEPSG3857 = "EPSG:3857"
)

const projJSONWGS84Datum = `"datum_ensemble":{"name":"World Geodetic System 1984 ensemble","members":[{"name":"World Geodetic System 1984 (Transit)","id":{"authority":"EPSG","code":1166}},{"name":"World Geodetic System 1984 (G730)","id":{"authority":"EPSG","code":1152}},{"name":"World Geodetic System 1984 (G873)","id":{"authority":"EPSG","code":1153}},{"name":"World Geodetic System 1984 (G1150)","id":{"authority":"EPSG","code":1154}},{"name":"World Geodetic System 1984 (G1674)","id":{"authority":"EPSG","code":1155}},{"name":"World Geodetic System 1984 (G1762)","id":{"authority":"EPSG","code":1156}},{"name":"World Geodetic System 1984 (G2139)","id":{"authority":"EPSG","code":1309}},{"name":"World Geodetic System 1984 (G2296)","id":{"authority":"EPSG","code":1383}}],"ellipsoid":{"name":"WGS 84","semi_major_axis":6378137,"inverse_flattening":298.257223563},"accuracy":"2.0","id":{"authority":"EPSG","code":6326}}`

const projJSONEPSG4326 = `{"$schema":"https://proj.org/schemas/v0.7/projjson.schema.json","type":"GeographicCRS","name":"WGS 84",` + projJSONWGS84Datum + `,"coordinate_system":{"subtype":"ellipsoidal","axis":[{"name":"Geodetic latitude","abbreviation":"Lat","direction":"north","unit":"degree"},{"name":"Geodetic longitude","abbreviation":"Lon","direction":"east","unit":"degree"}]},"scope":"Horizontal component of 3D system.","area":"World.","bbox":{"south_latitude":-90,"west_longitude":-180,"north_latitude":90,"east_longitude":180},"id":{"authority":"EPSG","code":4326}}`

const projJSONEPSG4979 = `{"$schema":"https://proj.org/schemas/v0.7/projjson.schema.json","type":"GeographicCRS","name":"WGS 84",` + projJSONWGS84Datum + `,"coordinate_system":{"subtype":"ellipsoidal","axis":[{"name":"Geodetic latitude","abbreviation":"Lat","direction":"north","unit":"degree"},{"name":"Geodetic longitude","abbreviation":"Lon","direction":"east","unit":"degree"},{"name":"Ellipsoidal height","abbreviation":"h","direction":"up","unit":"metre"}]},"scope":"Geodesy. Navigation and positioning using GPS satellite system.","area":"World.","bbox":{"south_latitude":-90,"west_longitude":-180,"north_latitude":90,"east_longitude":180},"id":{"authority":"EPSG","code":4979}}`

const projJSONEPSG3857 = `{"$schema":"https://proj.org/schemas/v0.7/projjson.schema.json","type":"ProjectedCRS","name":"WGS 84 / Pseudo-Mercator","base_crs":{"type":"GeographicCRS","name":"WGS 84",` + projJSONWGS84Datum + `,"coordinate_system":{"subtype":"ellipsoidal","axis":[{"name":"Geodetic latitude","abbreviation":"Lat","direction":"north","unit":"degree"},{"name":"Geodetic longitude","abbreviation":"Lon","direction":"east","unit":"degree"}]},"id":{"authority":"EPSG","code":4326}},"conversion":{"name":"Popular Visualisation Pseudo-Mercator","method":{"name":"Popular Visualisation Pseudo Mercator","id":{"authority":"EPSG","code":1024}},"parameters":[{"name":"Latitude of natural origin","value":0,"unit":"degree","id":{"authority":"EPSG","code":8801}},{"name":"Longitude of natural origin","value":0,"unit":"degree","id":{"authority":"EPSG","code":8802}},{"name":"False easting","value":0,"unit":"metre","id":{"authority":"EPSG","code":8806}},{"name":"False northing","value":0,"unit":"metre","id":{"authority":"EPSG","code":8807}}]},"coordinate_system":{"subtype":"Cartesian","axis":[{"name":"Easting","abbreviation":"X","direction":"east","unit":"metre"},{"name":"Northing","abbreviation":"Y","direction":"north","unit":"metre"}]},"scope":"Web mapping and visualisation.","area":"World between 85.06°S and 85.06°N.","bbox":{"south_latitude":-85.06,"west_longitude":-180,"north_latitude":85.06,"east_longitude":180},"id":{"authority":"EPSG","code":3857}}`

func ParseCRS(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return CRSCRS84, nil
	}
	var reference string
	if json.Unmarshal(raw, &reference) == nil {
		return NormalizeCRS(reference)
	}
	var object struct {
		Type  string          `json:"type"`
		Href  string          `json:"href"`
		Epoch json.RawMessage `json:"epoch"`
		ID    *struct {
			Authority string          `json:"authority"`
			Code      json.RawMessage `json:"code"`
		} `json:"id"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", fmt.Errorf("coordinate reference system must be a URI or reference object: %w", err)
	}
	if object.Href != "" {
		if object.Type != "" && object.Type != "Reference" {
			return "", fmt.Errorf("coordinate reference system reference type is %q; expected Reference", object.Type)
		}
		return NormalizeCRS(object.Href)
	}
	if object.ID == nil {
		return "", fmt.Errorf("coordinate reference system object has neither href nor id")
	}
	var code string
	if err := json.Unmarshal(object.ID.Code, &code); err != nil {
		var numeric json.Number
		decoder := json.NewDecoder(bytes.NewReader(object.ID.Code))
		decoder.UseNumber()
		if err := decoder.Decode(&numeric); err != nil {
			return "", fmt.Errorf("coordinate reference system id code is invalid: %w", err)
		}
		code = numeric.String()
	}
	return NormalizeCRS(object.ID.Authority + ":" + code)
}

func NormalizeCRS(value string) (string, error) {
	normalized := strings.TrimSpace(value)
	upper := strings.ToUpper(normalized)
	switch {
	case upper == "OGC:CRS84", strings.HasSuffix(upper, "/OGC/0/CRS84"):
		return CRSCRS84, nil
	case upper == "OGC:CRS84H", upper == "EPSG:4979", strings.HasSuffix(upper, "/OGC/0/CRS84H"), strings.HasSuffix(upper, "/EPSG/0/4979"):
		return CRSCRS84h, nil
	case upper == "EPSG:4326", strings.HasSuffix(upper, "/EPSG/0/4326"):
		return CRSEPSG4326, nil
	case upper == "EPSG:3857", strings.HasSuffix(upper, "/EPSG/0/3857"):
		return CRSEPSG3857, nil
	default:
		return "", fmt.Errorf("coordinate reference system %q is unsupported; expected OGC:CRS84, OGC:CRS84h, EPSG:4326, or EPSG:3857", value)
	}
}

func CRSURI(crs string) (string, error) {
	normalized, err := NormalizeCRS(crs)
	if err != nil {
		return "", err
	}
	switch normalized {
	case CRSCRS84:
		return "http://www.opengis.net/def/crs/OGC/0/CRS84", nil
	case CRSCRS84h:
		return "http://www.opengis.net/def/crs/OGC/0/CRS84h", nil
	case CRSEPSG4326:
		return "http://www.opengis.net/def/crs/EPSG/0/4326", nil
	case CRSEPSG3857:
		return "http://www.opengis.net/def/crs/EPSG/0/3857", nil
	default:
		return "", fmt.Errorf("coordinate reference system %q has no URI", crs)
	}
}

func GeoParquetCRS(crs string) (json.RawMessage, error) {
	normalized, err := NormalizeCRS(crs)
	if err != nil {
		return nil, err
	}
	if normalized == CRSCRS84 {
		return nil, nil
	}
	switch normalized {
	case CRSCRS84h:
		return json.RawMessage(projJSONEPSG4979), nil
	case CRSEPSG4326:
		return json.RawMessage(projJSONEPSG4326), nil
	case CRSEPSG3857:
		return json.RawMessage(projJSONEPSG3857), nil
	default:
		return nil, fmt.Errorf("coordinate reference system %q cannot be represented as GeoParquet PROJJSON", crs)
	}
}

func DecodeGeomJSON(raw json.RawMessage) (geom.T, error) {
	var geometry geom.T
	if err := geomjson.Unmarshal(raw, &geometry); err != nil {
		return nil, fmt.Errorf("geometry is not supported GeoJSON: %w", err)
	}
	return geometry, nil
}

func EncodeGeomJSON(geometry geom.T) (json.RawMessage, error) {
	encoded, err := geomjson.Marshal(geometry)
	if err != nil {
		return nil, fmt.Errorf("failed to encode geometry as GeoJSON: %w", err)
	}
	return encoded, nil
}

func TransformGeometry(geometry geom.T, sourceCRS, targetCRS string) (geom.T, error) {
	source, err := NormalizeCRS(sourceCRS)
	if err != nil {
		return nil, err
	}
	target, err := NormalizeCRS(targetCRS)
	if err != nil {
		return nil, err
	}
	if equivalentGeographicCRS(source, target) {
		return geometry, nil
	}
	transform := func(coordinate geom.Coord) {
		if source == CRSEPSG3857 {
			coordinate[0] = coordinate[0] * 180 / (math.Pi * 6378137)
			coordinate[1] = (2*math.Atan(math.Exp(coordinate[1]/6378137)) - math.Pi/2) * 180 / math.Pi
		}
		if target == CRSEPSG3857 {
			latitude := math.Max(-85.0511287798066, math.Min(85.0511287798066, coordinate[1]))
			coordinate[0] = coordinate[0] * math.Pi * 6378137 / 180
			coordinate[1] = math.Log(math.Tan((90+latitude)*math.Pi/360)) * 6378137
		}
	}
	if collection, ok := geometry.(*geom.GeometryCollection); ok {
		for index := 0; index < collection.NumGeoms(); index++ {
			if _, err := TransformGeometry(collection.Geom(index), source, target); err != nil {
				return nil, err
			}
		}
		return collection, nil
	}
	return geom.TransformInPlace(geometry, transform), nil
}

func TransformJSONFGGeometry(geometry geom.T, sourceCRS, targetCRS string) (geom.T, error) {
	source, err := NormalizeCRS(sourceCRS)
	if err != nil {
		return nil, err
	}
	target, err := NormalizeCRS(targetCRS)
	if err != nil {
		return nil, err
	}
	if source == CRSEPSG4326 {
		if _, err := swapGeometryXY(geometry); err != nil {
			return nil, err
		}
		source = CRSCRS84
	}
	swapTarget := target == CRSEPSG4326
	if swapTarget {
		target = CRSCRS84
	}
	if _, err := TransformGeometry(geometry, source, target); err != nil {
		return nil, err
	}
	if swapTarget {
		return swapGeometryXY(geometry)
	}
	return geometry, nil
}

func swapGeometryXY(geometry geom.T) (geom.T, error) {
	if collection, ok := geometry.(*geom.GeometryCollection); ok {
		for index := 0; index < collection.NumGeoms(); index++ {
			if _, err := swapGeometryXY(collection.Geom(index)); err != nil {
				return nil, err
			}
		}
		return collection, nil
	}
	return geom.TransformInPlace(geometry, func(coordinate geom.Coord) {
		coordinate[0], coordinate[1] = coordinate[1], coordinate[0]
	}), nil
}

func equivalentGeographicCRS(first, second string) bool {
	firstGeographic := first == CRSCRS84 || first == CRSCRS84h || first == CRSEPSG4326
	secondGeographic := second == CRSCRS84 || second == CRSCRS84h || second == CRSEPSG4326
	return firstGeographic && secondGeographic
}
