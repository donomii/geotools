package geodata

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/twpayne/go-geom"
	geomjson "github.com/twpayne/go-geom/encoding/geojson"
)

const (
	CRSCRS84     = "OGC:CRS84"
	CRSCRS84h    = "OGC:CRS84h"
	CRSEPSG4326  = "EPSG:4326"
	CRSEPSG3857  = "EPSG:3857"
	CRSEPSG3395  = "EPSG:3395"
	CRSEPSG27700 = "EPSG:27700"
)

const projJSONWGS84Datum = `"datum_ensemble":{"name":"World Geodetic System 1984 ensemble","members":[{"name":"World Geodetic System 1984 (Transit)","id":{"authority":"EPSG","code":1166}},{"name":"World Geodetic System 1984 (G730)","id":{"authority":"EPSG","code":1152}},{"name":"World Geodetic System 1984 (G873)","id":{"authority":"EPSG","code":1153}},{"name":"World Geodetic System 1984 (G1150)","id":{"authority":"EPSG","code":1154}},{"name":"World Geodetic System 1984 (G1674)","id":{"authority":"EPSG","code":1155}},{"name":"World Geodetic System 1984 (G1762)","id":{"authority":"EPSG","code":1156}},{"name":"World Geodetic System 1984 (G2139)","id":{"authority":"EPSG","code":1309}},{"name":"World Geodetic System 1984 (G2296)","id":{"authority":"EPSG","code":1383}}],"ellipsoid":{"name":"WGS 84","semi_major_axis":6378137,"inverse_flattening":298.257223563},"accuracy":"2.0","id":{"authority":"EPSG","code":6326}}`

const projJSONWGS84Base = `{"type":"GeographicCRS","name":"WGS 84",` + projJSONWGS84Datum + `,"coordinate_system":{"subtype":"ellipsoidal","axis":[{"name":"Geodetic latitude","abbreviation":"Lat","direction":"north","unit":"degree"},{"name":"Geodetic longitude","abbreviation":"Lon","direction":"east","unit":"degree"}]},"id":{"authority":"EPSG","code":4326}}`

const projJSONOSGB36Base = `{"type":"GeographicCRS","name":"OSGB36","datum":{"type":"GeodeticReferenceFrame","name":"OSGB 1936","ellipsoid":{"name":"Airy 1830","semi_major_axis":6377563.396,"inverse_flattening":299.3249646},"id":{"authority":"EPSG","code":6277}},"coordinate_system":{"subtype":"ellipsoidal","axis":[{"name":"Geodetic latitude","abbreviation":"Lat","direction":"north","unit":"degree"},{"name":"Geodetic longitude","abbreviation":"Lon","direction":"east","unit":"degree"}]},"id":{"authority":"EPSG","code":4277}}`

const projJSONProjectedCoordinateSystem = `{"subtype":"Cartesian","axis":[{"name":"Easting","abbreviation":"E","direction":"east","unit":"metre"},{"name":"Northing","abbreviation":"N","direction":"north","unit":"metre"}]}`

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
	}
	code, ok := epsgCode(upper)
	if ok && (code == 3395 || code == 27700 || code >= 32601 && code <= 32660 || code >= 32701 && code <= 32760) {
		return "EPSG:" + strconv.Itoa(code), nil
	}
	return "", fmt.Errorf("coordinate reference system %q is unsupported; expected WGS 84, Web/World Mercator, British National Grid, or WGS 84 UTM EPSG codes", value)
}

func epsgCode(value string) (int, bool) {
	codeText := ""
	if strings.HasPrefix(value, "EPSG:") {
		codeText = strings.TrimPrefix(value, "EPSG:")
	} else if marker := strings.LastIndex(value, "/EPSG/0/"); marker >= 0 {
		codeText = value[marker+len("/EPSG/0/"):]
	}
	code, err := strconv.Atoi(codeText)
	return code, err == nil
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
	}
	if code, ok := epsgCode(normalized); ok {
		return "http://www.opengis.net/def/crs/EPSG/0/" + strconv.Itoa(code), nil
	}
	return "", fmt.Errorf("coordinate reference system %q has no URI", crs)
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
	}
	code, ok := epsgCode(normalized)
	if !ok {
		return nil, fmt.Errorf("coordinate reference system %q cannot be represented as GeoParquet PROJJSON", crs)
	}
	return projectedGeoParquetCRS(code)
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
	if collection, ok := geometry.(*geom.GeometryCollection); ok {
		for index := 0; index < collection.NumGeoms(); index++ {
			if _, err := TransformGeometry(collection.Geom(index), source, target); err != nil {
				return nil, err
			}
		}
		return collection, nil
	}
	var transformErr error
	transform := func(coordinate geom.Coord) {
		if transformErr != nil {
			return
		}
		longitude, latitude, err := projectedCoordinateToWGS84(coordinate[0], coordinate[1], source)
		if err != nil {
			transformErr = err
			return
		}
		x, y, err := wgs84ToProjectedCoordinate(longitude, latitude, target)
		if err != nil {
			transformErr = err
			return
		}
		coordinate[0], coordinate[1] = x, y
	}
	transformed := geom.TransformInPlace(geometry, transform)
	if transformErr != nil {
		return nil, fmt.Errorf("cannot transform coordinate from %s to %s: %w", source, target, transformErr)
	}
	return transformed, nil
}

func TransformJSONFGGeometry(geometry geom.T, sourceCRS, targetCRS string) (geom.T, error) {
	return TransformGeometryWithCRSAxisOrder(geometry, sourceCRS, targetCRS)
}

func TransformGeometryWithCRSAxisOrder(geometry geom.T, sourceCRS, targetCRS string) (geom.T, error) {
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

func projectedCoordinateToWGS84(x, y float64, crs string) (float64, float64, error) {
	if !isFiniteCoordinate(x, y) {
		return 0, 0, fmt.Errorf("coordinate [%v,%v] is not finite", x, y)
	}
	if crs == CRSCRS84 || crs == CRSCRS84h || crs == CRSEPSG4326 {
		return x, y, nil
	}
	switch crs {
	case CRSEPSG3857:
		return x * 180 / (math.Pi * 6378137), (2*math.Atan(math.Exp(y/6378137)) - math.Pi/2) * 180 / math.Pi, nil
	case CRSEPSG3395:
		return inverseWorldMercator(x, y)
	case CRSEPSG27700:
		return britishNationalGridToWGS84(x, y)
	}
	if zone, north, ok := utmCRS(crs); ok {
		return inverseUTM(x, y, zone, north)
	}
	return 0, 0, fmt.Errorf("coordinate reference system %s has no inverse transform", crs)
}

func wgs84ToProjectedCoordinate(longitude, latitude float64, crs string) (float64, float64, error) {
	if !isFiniteCoordinate(longitude, latitude) {
		return 0, 0, fmt.Errorf("coordinate [%v,%v] is not finite", longitude, latitude)
	}
	if latitude < -90 || latitude > 90 {
		return 0, 0, fmt.Errorf("latitude %v is outside -90 through 90", latitude)
	}
	if crs == CRSCRS84 || crs == CRSCRS84h || crs == CRSEPSG4326 {
		return longitude, latitude, nil
	}
	switch crs {
	case CRSEPSG3857:
		latitude = math.Max(-85.0511287798066, math.Min(85.0511287798066, latitude))
		return longitude * math.Pi * 6378137 / 180, math.Log(math.Tan((90+latitude)*math.Pi/360)) * 6378137, nil
	case CRSEPSG3395:
		return worldMercator(longitude, latitude)
	case CRSEPSG27700:
		return wgs84ToBritishNationalGrid(longitude, latitude)
	}
	if zone, north, ok := utmCRS(crs); ok {
		return forwardUTM(longitude, latitude, zone, north)
	}
	return 0, 0, fmt.Errorf("coordinate reference system %s has no forward transform", crs)
}

func isFiniteCoordinate(first, second float64) bool {
	return !math.IsNaN(first) && !math.IsInf(first, 0) && !math.IsNaN(second) && !math.IsInf(second, 0)
}

func utmCRS(crs string) (int, bool, bool) {
	code, ok := epsgCode(crs)
	if !ok {
		return 0, false, false
	}
	if code >= 32601 && code <= 32660 {
		return code - 32600, true, true
	}
	if code >= 32701 && code <= 32760 {
		return code - 32700, false, true
	}
	return 0, false, false
}

func forwardUTM(longitude, latitude float64, zone int, north bool) (float64, float64, error) {
	if latitude < -80 || latitude > 84 {
		return 0, 0, fmt.Errorf("UTM latitude %v is outside -80 through 84", latitude)
	}
	if north != (latitude >= 0) {
		return 0, 0, fmt.Errorf("latitude %v is outside the EPSG UTM zone hemisphere", latitude)
	}
	const (
		major = 6378137.0
		e2    = 0.0066943799901413165
		k0    = 0.9996
	)
	ep2 := e2 / (1 - e2)
	phi := latitude * math.Pi / 180
	lambda := longitude * math.Pi / 180
	central := float64(zone*6-183) * math.Pi / 180
	n := major / math.Sqrt(1-e2*math.Sin(phi)*math.Sin(phi))
	t := math.Tan(phi) * math.Tan(phi)
	c := ep2 * math.Cos(phi) * math.Cos(phi)
	a := math.Cos(phi) * (lambda - central)
	m := major * ((1-e2/4-3*e2*e2/64-5*e2*e2*e2/256)*phi -
		(3*e2/8+3*e2*e2/32+45*e2*e2*e2/1024)*math.Sin(2*phi) +
		(15*e2*e2/256+45*e2*e2*e2/1024)*math.Sin(4*phi) -
		(35*e2*e2*e2/3072)*math.Sin(6*phi))
	easting := 500000 + k0*n*(a+(1-t+c)*math.Pow(a, 3)/6+(5-18*t+t*t+72*c-58*ep2)*math.Pow(a, 5)/120)
	northing := k0 * (m + n*math.Tan(phi)*(a*a/2+(5-t+9*c+4*c*c)*math.Pow(a, 4)/24+
		(61-58*t+t*t+600*c-330*ep2)*math.Pow(a, 6)/720))
	if !north {
		northing += 10000000
	}
	return easting, northing, nil
}

func inverseUTM(easting, northing float64, zone int, north bool) (float64, float64, error) {
	const (
		major = 6378137.0
		e2    = 0.0066943799901413165
		k0    = 0.9996
	)
	if easting < 0 || easting > 1000000 || northing < 0 || northing > 10000000 {
		return 0, 0, fmt.Errorf("UTM coordinate [%v,%v] is outside the supported grid", easting, northing)
	}
	x := easting - 500000
	y := northing
	if !north {
		y -= 10000000
	}
	ep2 := e2 / (1 - e2)
	m := y / k0
	mu := m / (major * (1 - e2/4 - 3*e2*e2/64 - 5*e2*e2*e2/256))
	e1 := (1 - math.Sqrt(1-e2)) / (1 + math.Sqrt(1-e2))
	phi1 := mu + (3*e1/2-27*math.Pow(e1, 3)/32)*math.Sin(2*mu) +
		(21*e1*e1/16-55*math.Pow(e1, 4)/32)*math.Sin(4*mu) +
		151*math.Pow(e1, 3)*math.Sin(6*mu)/96 + 1097*math.Pow(e1, 4)*math.Sin(8*mu)/512
	n1 := major / math.Sqrt(1-e2*math.Sin(phi1)*math.Sin(phi1))
	r1 := major * (1 - e2) / math.Pow(1-e2*math.Sin(phi1)*math.Sin(phi1), 1.5)
	t1 := math.Tan(phi1) * math.Tan(phi1)
	c1 := ep2 * math.Cos(phi1) * math.Cos(phi1)
	d := x / (n1 * k0)
	latitude := phi1 - n1*math.Tan(phi1)/r1*(d*d/2-(5+3*t1+10*c1-4*c1*c1-9*ep2)*math.Pow(d, 4)/24+
		(61+90*t1+298*c1+45*t1*t1-252*ep2-3*c1*c1)*math.Pow(d, 6)/720)
	longitude := float64(zone*6-183)*math.Pi/180 + (d-(1+2*t1+c1)*math.Pow(d, 3)/6+
		(5-2*c1+28*t1-3*c1*c1+8*ep2+24*t1*t1)*math.Pow(d, 5)/120)/math.Cos(phi1)
	return longitude * 180 / math.Pi, latitude * 180 / math.Pi, nil
}

func worldMercator(longitude, latitude float64) (float64, float64, error) {
	if latitude <= -90 || latitude >= 90 {
		return 0, 0, fmt.Errorf("World Mercator latitude %v must be between -90 and 90", latitude)
	}
	const major = 6378137.0
	const eccentricity = 0.08181919084262149
	phi := latitude * math.Pi / 180
	sine := math.Sin(phi)
	y := major * math.Log(math.Tan(math.Pi/4+phi/2)*math.Pow((1-eccentricity*sine)/(1+eccentricity*sine), eccentricity/2))
	return major * longitude * math.Pi / 180, y, nil
}

func inverseWorldMercator(x, y float64) (float64, float64, error) {
	const major = 6378137.0
	const eccentricity = 0.08181919084262149
	t := math.Exp(-y / major)
	phi := math.Pi/2 - 2*math.Atan(t)
	for iteration := 0; iteration < 12; iteration++ {
		sine := math.Sin(phi)
		next := math.Pi/2 - 2*math.Atan(t*math.Pow((1-eccentricity*sine)/(1+eccentricity*sine), eccentricity/2))
		if math.Abs(next-phi) < 1e-13 {
			phi = next
			break
		}
		phi = next
	}
	return x * 180 / (math.Pi * major), phi * 180 / math.Pi, nil
}

func britishNationalGridToWGS84(easting, northing float64) (float64, float64, error) {
	if easting < -100000 || easting > 800000 || northing < -200000 || northing > 1400000 {
		return 0, 0, fmt.Errorf("British National Grid coordinate [%v,%v] is outside the supported grid", easting, northing)
	}
	latitude, longitude := inverseBritishNationalGrid(easting, northing)
	x, y, z := geodeticToCartesian(longitude, latitude, 6377563.396, 6356256.909)
	x, y, z = helmertTransform(x, y, z, 446.448, -125.157, 542.060, 0.1502, 0.2470, 0.8421, -20.4894)
	longitude, latitude = cartesianToGeodetic(x, y, z, 6378137, 6356752.314245)
	return longitude, latitude, nil
}

func wgs84ToBritishNationalGrid(longitude, latitude float64) (float64, float64, error) {
	x, y, z := geodeticToCartesian(longitude, latitude, 6378137, 6356752.314245)
	x, y, z = helmertTransform(x, y, z, -446.448, 125.157, -542.060, -0.1502, -0.2470, -0.8421, 20.4894)
	osgbLongitude, osgbLatitude := cartesianToGeodetic(x, y, z, 6377563.396, 6356256.909)
	return forwardBritishNationalGrid(osgbLongitude, osgbLatitude)
}

func inverseBritishNationalGrid(easting, northing float64) (float64, float64) {
	const (
		major = 6377563.396
		minor = 6356256.909
		scale = 0.9996012717
		lat0  = 49 * math.Pi / 180
		lon0  = -2 * math.Pi / 180
		e0    = 400000.0
		n0    = -100000.0
	)
	latitude := lat0
	arc := 0.0
	for math.Abs(northing-n0-arc) >= 0.00001 {
		latitude += (northing - n0 - arc) / (major * scale)
		arc = britishMeridionalArc(latitude)
	}
	e2 := 1 - minor*minor/(major*major)
	sine := math.Sin(latitude)
	nu := major * scale / math.Sqrt(1-e2*sine*sine)
	rho := major * scale * (1 - e2) / math.Pow(1-e2*sine*sine, 1.5)
	eta2 := nu/rho - 1
	tangent := math.Tan(latitude)
	secant := 1 / math.Cos(latitude)
	vii := tangent / (2 * rho * nu)
	viii := tangent / (24 * rho * math.Pow(nu, 3)) * (5 + 3*tangent*tangent + eta2 - 9*tangent*tangent*eta2)
	ix := tangent / (720 * rho * math.Pow(nu, 5)) * (61 + 90*tangent*tangent + 45*math.Pow(tangent, 4))
	x := secant / nu
	xi := secant / (6 * math.Pow(nu, 3)) * (nu/rho + 2*tangent*tangent)
	xii := secant / (120 * math.Pow(nu, 5)) * (5 + 28*tangent*tangent + 24*math.Pow(tangent, 4))
	xiia := secant / (5040 * math.Pow(nu, 7)) * (61 + 662*tangent*tangent + 1320*math.Pow(tangent, 4) + 720*math.Pow(tangent, 6))
	delta := easting - e0
	latitude = latitude - vii*delta*delta + viii*math.Pow(delta, 4) - ix*math.Pow(delta, 6)
	longitude := lon0 + x*delta - xi*math.Pow(delta, 3) + xii*math.Pow(delta, 5) - xiia*math.Pow(delta, 7)
	return latitude * 180 / math.Pi, longitude * 180 / math.Pi
}

func forwardBritishNationalGrid(longitude, latitude float64) (float64, float64, error) {
	const (
		major = 6377563.396
		minor = 6356256.909
		scale = 0.9996012717
		lon0  = -2 * math.Pi / 180
		e0    = 400000.0
		n0    = -100000.0
	)
	phi := latitude * math.Pi / 180
	lambda := longitude * math.Pi / 180
	e2 := 1 - minor*minor/(major*major)
	sine := math.Sin(phi)
	cosine := math.Cos(phi)
	tangent := math.Tan(phi)
	nu := major * scale / math.Sqrt(1-e2*sine*sine)
	rho := major * scale * (1 - e2) / math.Pow(1-e2*sine*sine, 1.5)
	eta2 := nu/rho - 1
	arc := britishMeridionalArc(phi)
	i := arc + n0
	ii := nu / 2 * sine * cosine
	iii := nu / 24 * sine * math.Pow(cosine, 3) * (5 - tangent*tangent + 9*eta2)
	iiia := nu / 720 * sine * math.Pow(cosine, 5) * (61 - 58*tangent*tangent + math.Pow(tangent, 4))
	iv := nu * cosine
	v := nu / 6 * math.Pow(cosine, 3) * (nu/rho - tangent*tangent)
	vi := nu / 120 * math.Pow(cosine, 5) * (5 - 18*tangent*tangent + math.Pow(tangent, 4) + 14*eta2 - 58*tangent*tangent*eta2)
	delta := lambda - lon0
	northing := i + ii*delta*delta + iii*math.Pow(delta, 4) + iiia*math.Pow(delta, 6)
	easting := e0 + iv*delta + v*math.Pow(delta, 3) + vi*math.Pow(delta, 5)
	return easting, northing, nil
}

func britishMeridionalArc(latitude float64) float64 {
	const (
		major = 6377563.396
		minor = 6356256.909
		scale = 0.9996012717
		lat0  = 49 * math.Pi / 180
	)
	n := (major - minor) / (major + minor)
	return minor * scale * ((1+n+5*n*n/4+5*n*n*n/4)*(latitude-lat0) -
		(3*n+3*n*n+21*n*n*n/8)*math.Sin(latitude-lat0)*math.Cos(latitude+lat0) +
		(15*n*n/8+15*n*n*n/8)*math.Sin(2*(latitude-lat0))*math.Cos(2*(latitude+lat0)) -
		35*n*n*n/24*math.Sin(3*(latitude-lat0))*math.Cos(3*(latitude+lat0)))
}

func geodeticToCartesian(longitude, latitude, major, minor float64) (float64, float64, float64) {
	phi := latitude * math.Pi / 180
	lambda := longitude * math.Pi / 180
	e2 := 1 - minor*minor/(major*major)
	nu := major / math.Sqrt(1-e2*math.Sin(phi)*math.Sin(phi))
	return nu * math.Cos(phi) * math.Cos(lambda), nu * math.Cos(phi) * math.Sin(lambda), nu * (1 - e2) * math.Sin(phi)
}

func cartesianToGeodetic(x, y, z, major, minor float64) (float64, float64) {
	e2 := 1 - minor*minor/(major*major)
	longitude := math.Atan2(y, x)
	distance := math.Hypot(x, y)
	latitude := math.Atan2(z, distance*(1-e2))
	for iteration := 0; iteration < 12; iteration++ {
		nu := major / math.Sqrt(1-e2*math.Sin(latitude)*math.Sin(latitude))
		next := math.Atan2(z+e2*nu*math.Sin(latitude), distance)
		if math.Abs(next-latitude) < 1e-13 {
			latitude = next
			break
		}
		latitude = next
	}
	return longitude * 180 / math.Pi, latitude * 180 / math.Pi
}

func helmertTransform(x, y, z, translateX, translateY, translateZ, rotateX, rotateY, rotateZ, scalePPM float64) (float64, float64, float64) {
	arcseconds := math.Pi / (180 * 3600)
	rx := rotateX * arcseconds
	ry := rotateY * arcseconds
	rz := rotateZ * arcseconds
	scale := 1 + scalePPM*1e-6
	return translateX + scale*x - rz*y + ry*z,
		translateY + rz*x + scale*y - rx*z,
		translateZ - ry*x + rx*y + scale*z
}

func projectedGeoParquetCRS(code int) (json.RawMessage, error) {
	name := "EPSG:" + strconv.Itoa(code)
	baseCRS := projJSONWGS84Base
	conversion := ""
	if code == 3395 {
		name = "WGS 84 / World Mercator"
		conversion = projJSONConversion("World Mercator", "Mercator (variant A)", 9804, 0, 0, 1, 0, 0)
	} else if code == 27700 {
		name = "OSGB36 / British National Grid"
		baseCRS = projJSONOSGB36Base
		conversion = projJSONConversion("British National Grid", "Transverse Mercator", 9807, 49, -2, 0.9996012717, 400000, -100000)
	} else if zone, north, ok := utmCRS("EPSG:" + strconv.Itoa(code)); ok {
		hemisphere := "N"
		falseNorthing := 0.0
		if !north {
			hemisphere = "S"
			falseNorthing = 10000000
		}
		name = fmt.Sprintf("WGS 84 / UTM zone %d%s", zone, hemisphere)
		conversion = projJSONConversion("UTM zone "+strconv.Itoa(zone)+hemisphere, "Transverse Mercator", 9807, 0, float64(zone*6-183), 0.9996, 500000, falseNorthing)
	}
	if conversion == "" {
		return nil, fmt.Errorf("EPSG:%d has no GeoParquet projection definition", code)
	}
	encodedName, err := json.Marshal(name)
	if err != nil {
		return nil, err
	}
	value := fmt.Sprintf(`{"$schema":"https://proj.org/schemas/v0.7/projjson.schema.json","type":"ProjectedCRS","name":%s,"base_crs":%s,"conversion":%s,"coordinate_system":%s,"id":{"authority":"EPSG","code":%d}}`, encodedName, baseCRS, conversion, projJSONProjectedCoordinateSystem, code)
	if !json.Valid([]byte(value)) {
		return nil, fmt.Errorf("failed to construct PROJJSON for EPSG:%d", code)
	}
	return json.RawMessage(value), nil
}

func projJSONConversion(name, method string, methodCode int, latitude, longitude, scale, falseEasting, falseNorthing float64) string {
	encodedName, _ := json.Marshal(name)
	encodedMethod, _ := json.Marshal(method)
	return fmt.Sprintf(`{"name":%s,"method":{"name":%s,"id":{"authority":"EPSG","code":%d}},"parameters":[{"name":"Latitude of natural origin","value":%s,"unit":"degree","id":{"authority":"EPSG","code":8801}},{"name":"Longitude of natural origin","value":%s,"unit":"degree","id":{"authority":"EPSG","code":8802}},{"name":"Scale factor at natural origin","value":%s,"unit":"unity","id":{"authority":"EPSG","code":8805}},{"name":"False easting","value":%s,"unit":"metre","id":{"authority":"EPSG","code":8806}},{"name":"False northing","value":%s,"unit":"metre","id":{"authority":"EPSG","code":8807}}]}`,
		encodedName, encodedMethod, methodCode,
		strconv.FormatFloat(latitude, 'g', -1, 64), strconv.FormatFloat(longitude, 'g', -1, 64),
		strconv.FormatFloat(scale, 'g', -1, 64), strconv.FormatFloat(falseEasting, 'g', -1, 64),
		strconv.FormatFloat(falseNorthing, 'g', -1, 64))
}
