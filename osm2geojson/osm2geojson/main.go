package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/donomii/goof"
)

var strict bool

type osmXMLTag struct {
	Key   string `xml:"k,attr"`
	Value string `xml:"v,attr"`
}

type osmXMLNode struct {
	ID   string      `xml:"id,attr"`
	Lat  string      `xml:"lat,attr"`
	Lon  string      `xml:"lon,attr"`
	Tags []osmXMLTag `xml:"tag"`
}

type osmXMLNodeReference struct {
	Reference string `xml:"ref,attr"`
}

type osmXMLWay struct {
	ID             string                `xml:"id,attr"`
	NodeReferences []osmXMLNodeReference `xml:"nd"`
	Tags           []osmXMLTag           `xml:"tag"`
}

type osmXMLRelationMember struct {
	Type      string `xml:"type,attr"`
	Reference string `xml:"ref,attr"`
	Role      string `xml:"role,attr"`
}

type osmXMLRelation struct {
	ID      string                 `xml:"id,attr"`
	Members []osmXMLRelationMember `xml:"member"`
	Tags    []osmXMLTag            `xml:"tag"`
}

type osmGeometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates,omitempty"`
	Geometries  []osmGeometry   `json:"geometries,omitempty"`
}

type osmProperties struct {
	OSMType string            `json:"osm_type"`
	OSMID   string            `json:"osm_id"`
	Tags    map[string]string `json:"tags"`
}

type osmFeature struct {
	Type       string        `json:"type"`
	ID         string        `json:"id"`
	BBox       []float64     `json:"bbox,omitempty"`
	Geometry   osmGeometry   `json:"geometry"`
	Properties osmProperties `json:"properties"`
}

type osmFeatureWriter struct {
	output *bufio.Writer
	strict bool
	wrote  bool
}

func newOSMFeatureWriter(output io.Writer, strict bool) *osmFeatureWriter {
	return &osmFeatureWriter{output: bufio.NewWriter(output), strict: strict}
}

func (writer *osmFeatureWriter) Start() error {
	if writer.strict {
		_, err := writer.output.WriteString(`{"type":"FeatureCollection","features":[`)
		return err
	}
	return nil
}

func (writer *osmFeatureWriter) Write(feature osmFeature) error {
	encoded, err := json.Marshal(feature)
	if err != nil {
		return fmt.Errorf("failed to encode OSM element %q: %w", feature.ID, err)
	}
	if writer.strict && writer.wrote {
		if err := writer.output.WriteByte(','); err != nil {
			return err
		}
	}
	if _, err := writer.output.Write(encoded); err != nil {
		return err
	}
	if !writer.strict {
		if err := writer.output.WriteByte('\n'); err != nil {
			return err
		}
	}
	writer.wrote = true
	return nil
}

func (writer *osmFeatureWriter) Close() error {
	if writer.strict {
		if _, err := writer.output.WriteString("]}\n"); err != nil {
			return err
		}
	}
	return writer.output.Flush()
}

type osmStoredWay struct {
	Coordinates [][]float64
	Geometry    osmGeometry
}

type osmRelationSegment struct {
	Role        string
	Coordinates [][]float64
}

func convertOSM(input io.Reader, output io.Writer, strictOutput bool) error {
	writer := newOSMFeatureWriter(output, strictOutput)
	if err := writer.Start(); err != nil {
		return err
	}
	nodes := make(map[string][]float64)
	ways := make(map[string]osmStoredWay)
	relations := make([]osmXMLRelation, 0)
	decoder := xml.NewDecoder(input)

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("failed to decode OSM XML: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "node":
			var node osmXMLNode
			if err := decoder.DecodeElement(&node, &start); err != nil {
				return fmt.Errorf("failed to decode OSM node: %w", err)
			}
			feature, position, err := osmNodeFeature(node)
			if err != nil {
				return err
			}
			nodes[node.ID] = position
			if err := writer.Write(feature); err != nil {
				return err
			}
		case "way":
			var way osmXMLWay
			if err := decoder.DecodeElement(&way, &start); err != nil {
				return fmt.Errorf("failed to decode OSM way: %w", err)
			}
			feature, stored, err := osmWayFeature(way, nodes)
			if err != nil {
				return err
			}
			ways[way.ID] = stored
			if err := writer.Write(feature); err != nil {
				return err
			}
		case "relation":
			var relation osmXMLRelation
			if err := decoder.DecodeElement(&relation, &start); err != nil {
				return fmt.Errorf("failed to decode OSM relation: %w", err)
			}
			relations = append(relations, relation)
		}
	}

	relationByID := make(map[string]osmXMLRelation, len(relations))
	for _, relation := range relations {
		relationByID[relation.ID] = relation
	}
	for _, relation := range relations {
		feature, err := osmRelationFeature(relation, nodes, ways, relationByID)
		if err != nil {
			return err
		}
		if err := writer.Write(feature); err != nil {
			return err
		}
	}
	return writer.Close()
}

func osmNodeFeature(node osmXMLNode) (osmFeature, []float64, error) {
	if node.ID == "" {
		return osmFeature{}, nil, fmt.Errorf("OSM node is missing id")
	}
	lat, err := strconv.ParseFloat(node.Lat, 64)
	if err != nil {
		return osmFeature{}, nil, fmt.Errorf("OSM node %s has invalid latitude %q: %w", node.ID, node.Lat, err)
	}
	lon, err := strconv.ParseFloat(node.Lon, 64)
	if err != nil {
		return osmFeature{}, nil, fmt.Errorf("OSM node %s has invalid longitude %q: %w", node.ID, node.Lon, err)
	}
	position := []float64{lon, lat}
	encoded, err := json.Marshal(position)
	if err != nil {
		return osmFeature{}, nil, err
	}
	return osmFeature{
		Type:       "Feature",
		ID:         "node/" + node.ID,
		Geometry:   osmGeometry{Type: "Point", Coordinates: encoded},
		Properties: osmProperties{OSMType: "node", OSMID: node.ID, Tags: osmTags(node.Tags)},
	}, position, nil
}

func osmWayFeature(way osmXMLWay, nodes map[string][]float64) (osmFeature, osmStoredWay, error) {
	if way.ID == "" {
		return osmFeature{}, osmStoredWay{}, fmt.Errorf("OSM way is missing id")
	}
	if len(way.NodeReferences) < 2 {
		return osmFeature{}, osmStoredWay{}, fmt.Errorf("OSM way %s has fewer than two node references", way.ID)
	}
	coordinates := make([][]float64, 0, len(way.NodeReferences))
	for _, reference := range way.NodeReferences {
		position, ok := nodes[reference.Reference]
		if !ok {
			return osmFeature{}, osmStoredWay{}, fmt.Errorf("OSM way %s references missing node %s", way.ID, reference.Reference)
		}
		coordinates = append(coordinates, append([]float64(nil), position...))
	}
	geometry, err := osmGeometryFromCoordinates(coordinates)
	if err != nil {
		return osmFeature{}, osmStoredWay{}, fmt.Errorf("failed to build OSM way %s geometry: %w", way.ID, err)
	}
	stored := osmStoredWay{Coordinates: coordinates, Geometry: geometry}
	return osmFeature{
		Type:       "Feature",
		ID:         "way/" + way.ID,
		BBox:       osmBounds(coordinates),
		Geometry:   geometry,
		Properties: osmProperties{OSMType: "way", OSMID: way.ID, Tags: osmTags(way.Tags)},
	}, stored, nil
}

func osmRelationFeature(relation osmXMLRelation, nodes map[string][]float64, ways map[string]osmStoredWay, relations map[string]osmXMLRelation) (osmFeature, error) {
	tags := osmTags(relation.Tags)
	var geometry osmGeometry
	var segments []osmRelationSegment
	var err error
	if tags["type"] == "multipolygon" || tags["type"] == "boundary" {
		segments, err = osmCollectRelationWays(relation, ways, relations, "", make(map[string]bool))
		if err == nil {
			geometry, err = osmMultipolygonGeometry(segments)
		}
	} else {
		geometry, err = osmRelationCollection(relation, nodes, ways, relations, make(map[string]bool))
	}
	if err != nil {
		return osmFeature{}, fmt.Errorf("failed to build OSM relation %s: %w", relation.ID, err)
	}
	return osmFeature{
		Type:       "Feature",
		ID:         "relation/" + relation.ID,
		Geometry:   geometry,
		Properties: osmProperties{OSMType: "relation", OSMID: relation.ID, Tags: tags},
	}, nil
}

func osmRelationCollection(relation osmXMLRelation, nodes map[string][]float64, ways map[string]osmStoredWay, relations map[string]osmXMLRelation, visiting map[string]bool) (osmGeometry, error) {
	if visiting[relation.ID] {
		return osmGeometry{}, fmt.Errorf("nested relation cycle includes relation %s", relation.ID)
	}
	visiting[relation.ID] = true
	defer delete(visiting, relation.ID)
	geometries := make([]osmGeometry, 0, len(relation.Members))
	for _, member := range relation.Members {
		switch member.Type {
		case "node":
			position, ok := nodes[member.Reference]
			if !ok {
				return osmGeometry{}, fmt.Errorf("member node %s was not found", member.Reference)
			}
			encoded, err := json.Marshal(position)
			if err != nil {
				return osmGeometry{}, err
			}
			geometries = append(geometries, osmGeometry{Type: "Point", Coordinates: encoded})
		case "way":
			way, ok := ways[member.Reference]
			if !ok {
				return osmGeometry{}, fmt.Errorf("member way %s was not found", member.Reference)
			}
			geometries = append(geometries, way.Geometry)
		case "relation":
			nested, ok := relations[member.Reference]
			if !ok {
				return osmGeometry{}, fmt.Errorf("member relation %s was not found", member.Reference)
			}
			geometry, err := osmRelationCollection(nested, nodes, ways, relations, visiting)
			if err != nil {
				return osmGeometry{}, err
			}
			geometries = append(geometries, geometry)
		default:
			return osmGeometry{}, fmt.Errorf("unknown member type %q", member.Type)
		}
	}
	if len(geometries) == 0 {
		return osmGeometry{}, fmt.Errorf("relation has no members")
	}
	return osmGeometry{Type: "GeometryCollection", Geometries: geometries}, nil
}

func osmCollectRelationWays(relation osmXMLRelation, ways map[string]osmStoredWay, relations map[string]osmXMLRelation, inheritedRole string, visiting map[string]bool) ([]osmRelationSegment, error) {
	if visiting[relation.ID] {
		return nil, fmt.Errorf("nested relation cycle includes relation %s", relation.ID)
	}
	visiting[relation.ID] = true
	defer delete(visiting, relation.ID)
	segments := make([]osmRelationSegment, 0)
	for _, member := range relation.Members {
		role := member.Role
		if inheritedRole == "inner" || inheritedRole == "outer" {
			role = inheritedRole
		}
		switch member.Type {
		case "way":
			way, ok := ways[member.Reference]
			if !ok {
				return nil, fmt.Errorf("member way %s was not found", member.Reference)
			}
			segments = append(segments, osmRelationSegment{Role: role, Coordinates: osmCopyCoordinates(way.Coordinates)})
		case "relation":
			nested, ok := relations[member.Reference]
			if !ok {
				return nil, fmt.Errorf("member relation %s was not found", member.Reference)
			}
			nestedSegments, err := osmCollectRelationWays(nested, ways, relations, role, visiting)
			if err != nil {
				return nil, err
			}
			segments = append(segments, nestedSegments...)
		}
	}
	return segments, nil
}

func osmMultipolygonGeometry(segments []osmRelationSegment) (osmGeometry, error) {
	outerSegments := make([][][]float64, 0)
	innerSegments := make([][][]float64, 0)
	for _, segment := range segments {
		if segment.Role == "inner" {
			innerSegments = append(innerSegments, segment.Coordinates)
		} else {
			outerSegments = append(outerSegments, segment.Coordinates)
		}
	}
	outerRings, err := osmStitchRings(outerSegments)
	if err != nil {
		return osmGeometry{}, fmt.Errorf("outer rings: %w", err)
	}
	if len(outerRings) == 0 {
		return osmGeometry{}, fmt.Errorf("expected at least one outer ring")
	}
	innerRings, err := osmStitchRings(innerSegments)
	if err != nil {
		return osmGeometry{}, fmt.Errorf("inner rings: %w", err)
	}
	polygons := make([][][][]float64, len(outerRings))
	for index, ring := range outerRings {
		polygons[index] = [][][]float64{ring}
	}
	for _, inner := range innerRings {
		assigned := false
		for index, outer := range outerRings {
			if osmPointInRing(inner[0], outer) {
				polygons[index] = append(polygons[index], inner)
				assigned = true
				break
			}
		}
		if !assigned {
			return osmGeometry{}, fmt.Errorf("inner ring is not contained by an outer ring")
		}
	}
	if len(polygons) == 1 {
		encoded, err := json.Marshal(polygons[0])
		return osmGeometry{Type: "Polygon", Coordinates: encoded}, err
	}
	encoded, err := json.Marshal(polygons)
	return osmGeometry{Type: "MultiPolygon", Coordinates: encoded}, err
}

func osmStitchRings(segments [][][]float64) ([][][]float64, error) {
	remaining := make([][][]float64, len(segments))
	for index, segment := range segments {
		if len(segment) < 2 {
			return nil, fmt.Errorf("ring segment has fewer than two positions")
		}
		remaining[index] = osmCopyCoordinates(segment)
	}
	rings := make([][][]float64, 0)
	for len(remaining) > 0 {
		ring := remaining[0]
		remaining = remaining[1:]
		for !osmPositionsEqual(ring[0], ring[len(ring)-1]) {
			joined := false
			for index, segment := range remaining {
				if combined, ok := osmJoinCoordinates(ring, segment); ok {
					ring = combined
					remaining = append(remaining[:index], remaining[index+1:]...)
					joined = true
					break
				}
			}
			if !joined {
				return nil, fmt.Errorf("could not close ring beginning at %v", ring[0])
			}
		}
		if len(ring) < 4 {
			return nil, fmt.Errorf("closed ring has fewer than four positions")
		}
		rings = append(rings, ring)
	}
	return rings, nil
}

func osmJoinCoordinates(ring, segment [][]float64) ([][]float64, bool) {
	if osmPositionsEqual(ring[len(ring)-1], segment[0]) {
		return append(ring, segment[1:]...), true
	}
	if osmPositionsEqual(ring[len(ring)-1], segment[len(segment)-1]) {
		reversed := osmReverseCoordinates(segment)
		return append(ring, reversed[1:]...), true
	}
	if osmPositionsEqual(ring[0], segment[len(segment)-1]) {
		return append(osmCopyCoordinates(segment[:len(segment)-1]), ring...), true
	}
	if osmPositionsEqual(ring[0], segment[0]) {
		reversed := osmReverseCoordinates(segment)
		return append(reversed[:len(reversed)-1], ring...), true
	}
	return nil, false
}

func osmReverseCoordinates(coordinates [][]float64) [][]float64 {
	reversed := make([][]float64, len(coordinates))
	for index := range coordinates {
		reversed[len(coordinates)-1-index] = append([]float64(nil), coordinates[index]...)
	}
	return reversed
}

func osmCopyCoordinates(coordinates [][]float64) [][]float64 {
	copied := make([][]float64, len(coordinates))
	for index, coordinate := range coordinates {
		copied[index] = append([]float64(nil), coordinate...)
	}
	return copied
}

func osmPositionsEqual(left, right []float64) bool {
	return len(left) >= 2 && len(right) >= 2 && left[0] == right[0] && left[1] == right[1]
}

func osmPointInRing(point []float64, ring [][]float64) bool {
	inside := false
	for current, previous := 0, len(ring)-1; current < len(ring); previous, current = current, current+1 {
		currentPoint := ring[current]
		previousPoint := ring[previous]
		crossesLatitude := (currentPoint[1] > point[1]) != (previousPoint[1] > point[1])
		if crossesLatitude {
			crossingLongitude := (previousPoint[0]-currentPoint[0])*(point[1]-currentPoint[1])/(previousPoint[1]-currentPoint[1]) + currentPoint[0]
			if point[0] < crossingLongitude {
				inside = !inside
			}
		}
	}
	return inside
}

func osmGeometryFromCoordinates(coordinates [][]float64) (osmGeometry, error) {
	if len(coordinates) < 2 {
		return osmGeometry{}, fmt.Errorf("expected at least two positions")
	}
	if len(coordinates) >= 4 && osmPositionsEqual(coordinates[0], coordinates[len(coordinates)-1]) {
		encoded, err := json.Marshal([][][]float64{coordinates})
		return osmGeometry{Type: "Polygon", Coordinates: encoded}, err
	}
	encoded, err := json.Marshal(coordinates)
	return osmGeometry{Type: "LineString", Coordinates: encoded}, err
}

func osmBounds(coordinates [][]float64) []float64 {
	bounds := []float64{coordinates[0][0], coordinates[0][1], coordinates[0][0], coordinates[0][1]}
	for _, coordinate := range coordinates[1:] {
		if coordinate[0] < bounds[0] {
			bounds[0] = coordinate[0]
		}
		if coordinate[1] < bounds[1] {
			bounds[1] = coordinate[1]
		}
		if coordinate[0] > bounds[2] {
			bounds[2] = coordinate[0]
		}
		if coordinate[1] > bounds[3] {
			bounds[3] = coordinate[1]
		}
	}
	return bounds
}

func osmTags(tags []osmXMLTag) map[string]string {
	properties := make(map[string]string, len(tags))
	for _, tag := range tags {
		if tag.Key != "" {
			properties[tag.Key] = tag.Value
		}
	}
	return properties
}

type osmOutputDestination struct {
	writer     io.Writer
	file       *os.File
	compressor *gzip.Writer
}

func openOSMOutput(filename string) (*osmOutputDestination, error) {
	if filename == "" || filename == "-" {
		return &osmOutputDestination{writer: os.Stdout}, nil
	}
	file, err := os.Create(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to create output file %q: %w", filename, err)
	}
	destination := &osmOutputDestination{writer: file, file: file}
	if strings.HasSuffix(strings.ToLower(filename), ".gz") {
		destination.compressor = gzip.NewWriter(file)
		destination.writer = destination.compressor
	}
	return destination, nil
}

func (destination *osmOutputDestination) Close() error {
	if destination.compressor != nil {
		if err := destination.compressor.Close(); err != nil {
			return err
		}
	}
	if destination.file != nil {
		return destination.file.Close()
	}
	return nil
}

func runOSMBuiltInTests() error {
	input := `<osm>
<node id="1" lat="0" lon="0"><tag k="name" v="A"/></node>
<node id="2" lat="0" lon="1"/>
<node id="3" lat="1" lon="1"/>
<node id="4" lat="1" lon="0"/>
<way id="10"><nd ref="1"/><nd ref="2"/><nd ref="3"/><nd ref="4"/><nd ref="1"/><tag k="building" v="yes"/></way>
<relation id="20"><member type="way" ref="10" role="outer"/><tag k="type" v="multipolygon"/></relation>
</osm>`
	var output bytes.Buffer
	if err := convertOSM(strings.NewReader(input), &output, true); err != nil {
		return err
	}
	if !json.Valid(output.Bytes()) {
		return fmt.Errorf("strict output is not valid JSON: %s", output.String())
	}
	var collection struct {
		Type     string            `json:"type"`
		Features []json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal(output.Bytes(), &collection); err != nil {
		return err
	}
	if collection.Type != "FeatureCollection" || len(collection.Features) != 6 {
		return fmt.Errorf("expected a FeatureCollection with 6 Features, got type %q and %d Features", collection.Type, len(collection.Features))
	}
	return nil
}

func main() {
	var compression string
	var runBuiltInTests bool
	flag.StringVar(&compression, "compression", "", "input compression: bz2 or gz; empty detects compression from the filename")
	flag.BoolVar(&strict, "strict", false, "emit one GeoJSON FeatureCollection instead of one Feature per line")
	flag.BoolVar(&runBuiltInTests, "test", false, "run built-in tests and exit")
	flag.Parse()
	if runBuiltInTests {
		if err := runOSMBuiltInTests(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("osm2geojson built-in tests passed")
		return
	}

	args := flag.Args()
	if len(args) > 2 {
		log.Fatal("usage: osm2geojson [--compression=bz2|gz] [--strict] [input|-] [output|-]")
	}
	inputFilename := ""
	if len(args) > 0 && args[0] != "-" {
		inputFilename = args[0]
	}
	outputFilename := ""
	if len(args) > 1 {
		outputFilename = args[1]
	}
	input := goof.OpenBufferedInput(inputFilename, compression)
	output, err := openOSMOutput(outputFilename)
	if err != nil {
		log.Fatal(err)
	}
	convertErr := convertOSM(input, output.writer, strict)
	closeErr := output.Close()
	if convertErr != nil {
		log.Fatal(convertErr)
	}
	if closeErr != nil {
		log.Fatalf("failed to close OSM output: %v", closeErr)
	}
}
