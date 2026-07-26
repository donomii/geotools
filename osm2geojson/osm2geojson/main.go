package main

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
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
	Tags        map[string]string
}

type osmRelationSegment struct {
	Role        string
	Coordinates [][]float64
}

func convertOSM(input io.Reader, output io.Writer, strictOutput bool) error {
	if seeker, ok := input.(io.ReadSeeker); ok {
		start, err := seeker.Seek(0, io.SeekCurrent)
		if err == nil {
			index, err := indexOSMReferences(seeker)
			if err != nil {
				return err
			}
			if _, err := seeker.Seek(start, io.SeekStart); err != nil {
				return fmt.Errorf("failed to rewind OSM XML stream to byte %d: %w", start, err)
			}
			return convertIndexedOSM(seeker, output, strictOutput, index)
		}
	}
	return convertStreamingOSM(input, output, strictOutput)
}

type osmReferenceIndex struct {
	nodeUses     map[int64]int
	relationWays map[int64]bool
	relations    []osmXMLRelation
	relationByID map[int64]osmXMLRelation
}

func indexOSMReferences(input io.Reader) (*osmReferenceIndex, error) {
	index := &osmReferenceIndex{
		nodeUses:     make(map[int64]int),
		relationWays: make(map[int64]bool),
		relationByID: make(map[int64]osmXMLRelation),
	}
	decoder := xml.NewDecoder(input)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return index, nil
		} else if err != nil {
			return nil, fmt.Errorf("failed to index OSM XML references: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "node":
			if err := decoder.Skip(); err != nil {
				return nil, fmt.Errorf("failed to index OSM node: %w", err)
			}
		case "way":
			var way osmXMLWay
			if err := decoder.DecodeElement(&way, &start); err != nil {
				return nil, fmt.Errorf("failed to index OSM way: %w", err)
			}
			for _, reference := range way.NodeReferences {
				nodeID, err := osmNumericID(reference.Reference, "node reference")
				if err != nil {
					return nil, fmt.Errorf("OSM way %s: %w", way.ID, err)
				}
				index.nodeUses[nodeID]++
			}
		case "relation":
			var relation osmXMLRelation
			if err := decoder.DecodeElement(&relation, &start); err != nil {
				return nil, fmt.Errorf("failed to index OSM relation: %w", err)
			}
			relationID, err := osmNumericID(relation.ID, "relation")
			if err != nil {
				return nil, err
			}
			index.relations = append(index.relations, relation)
			index.relationByID[relationID] = relation
			for _, member := range relation.Members {
				memberID, err := osmNumericID(member.Reference, "relation member "+member.Type)
				if err != nil {
					return nil, fmt.Errorf("OSM relation %s: %w", relation.ID, err)
				}
				switch member.Type {
				case "node":
					index.nodeUses[memberID]++
				case "way":
					index.relationWays[memberID] = true
				case "relation":
				default:
					return nil, fmt.Errorf("OSM relation %s has unknown member type %q", relation.ID, member.Type)
				}
			}
		}
	}
}

func convertIndexedOSM(input io.Reader, output io.Writer, strictOutput bool, index *osmReferenceIndex) error {
	writer := newOSMFeatureWriter(output, strictOutput)
	if err := writer.Start(); err != nil {
		return err
	}
	nodes := make(map[int64][2]float64, len(index.nodeUses))
	ways := make(map[int64]osmStoredWay, len(index.relationWays))
	remainingNodeUses := make(map[int64]int, len(index.nodeUses))
	for nodeID, count := range index.nodeUses {
		remainingNodeUses[nodeID] = count
	}
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
			nodeID, err := osmNumericID(node.ID, "node")
			if err != nil {
				return err
			}
			if remainingNodeUses[nodeID] > 0 {
				nodes[nodeID] = position
			}
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
			wayID, err := osmNumericID(way.ID, "way")
			if err != nil {
				return err
			}
			if index.relationWays[wayID] {
				ways[wayID] = stored
			}
			if err := releaseOSMWayNodes(way, remainingNodeUses, nodes); err != nil {
				return err
			}
			if err := writer.Write(feature); err != nil {
				return err
			}
		case "relation":
			if err := decoder.Skip(); err != nil {
				return fmt.Errorf("failed to decode OSM relation: %w", err)
			}
		}
	}
	for _, relation := range index.relations {
		feature, err := osmRelationFeature(relation, nodes, ways, index.relationByID)
		if err != nil {
			return err
		}
		if err := writer.Write(feature); err != nil {
			return err
		}
	}
	return writer.Close()
}

func releaseOSMWayNodes(way osmXMLWay, remainingUses map[int64]int, nodes map[int64][2]float64) error {
	for _, reference := range way.NodeReferences {
		nodeID, err := osmNumericID(reference.Reference, "node reference")
		if err != nil {
			return fmt.Errorf("OSM way %s: %w", way.ID, err)
		}
		remainingUses[nodeID]--
		if remainingUses[nodeID] == 0 {
			delete(remainingUses, nodeID)
			delete(nodes, nodeID)
		}
	}
	return nil
}

func convertStreamingOSM(input io.Reader, output io.Writer, strictOutput bool) error {
	writer := newOSMFeatureWriter(output, strictOutput)
	if err := writer.Start(); err != nil {
		return err
	}
	nodes := make(map[int64][2]float64)
	ways := make(map[int64]osmStoredWay)
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
			nodeID, err := osmNumericID(node.ID, "node")
			if err != nil {
				return err
			}
			nodes[nodeID] = position
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
			wayID, err := osmNumericID(way.ID, "way")
			if err != nil {
				return err
			}
			ways[wayID] = stored
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

	relationByID := make(map[int64]osmXMLRelation, len(relations))
	for _, relation := range relations {
		relationID, err := osmNumericID(relation.ID, "relation")
		if err != nil {
			return err
		}
		relationByID[relationID] = relation
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

func osmNodeFeature(node osmXMLNode) (osmFeature, [2]float64, error) {
	if node.ID == "" {
		return osmFeature{}, [2]float64{}, fmt.Errorf("OSM node is missing id")
	}
	lat, err := strconv.ParseFloat(node.Lat, 64)
	if err != nil {
		return osmFeature{}, [2]float64{}, fmt.Errorf("OSM node %s has invalid latitude %q: %w", node.ID, node.Lat, err)
	}
	lon, err := strconv.ParseFloat(node.Lon, 64)
	if err != nil {
		return osmFeature{}, [2]float64{}, fmt.Errorf("OSM node %s has invalid longitude %q: %w", node.ID, node.Lon, err)
	}
	if math.IsNaN(lon) || math.IsInf(lon, 0) || lon < -180 || lon > 180 {
		return osmFeature{}, [2]float64{}, fmt.Errorf("OSM node %s longitude %q is outside -180 through 180", node.ID, node.Lon)
	}
	if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
		return osmFeature{}, [2]float64{}, fmt.Errorf("OSM node %s latitude %q is outside -90 through 90", node.ID, node.Lat)
	}
	position := [2]float64{lon, lat}
	encoded, err := json.Marshal(position)
	if err != nil {
		return osmFeature{}, [2]float64{}, err
	}
	return osmFeature{
		Type:       "Feature",
		ID:         "node/" + node.ID,
		Geometry:   osmGeometry{Type: "Point", Coordinates: encoded},
		Properties: osmProperties{OSMType: "node", OSMID: node.ID, Tags: osmTags(node.Tags)},
	}, position, nil
}

func osmWayFeature(way osmXMLWay, nodes map[int64][2]float64) (osmFeature, osmStoredWay, error) {
	if way.ID == "" {
		return osmFeature{}, osmStoredWay{}, fmt.Errorf("OSM way is missing id")
	}
	if len(way.NodeReferences) < 2 {
		return osmFeature{}, osmStoredWay{}, fmt.Errorf("OSM way %s has fewer than two node references", way.ID)
	}
	coordinates := make([][]float64, 0, len(way.NodeReferences))
	for _, reference := range way.NodeReferences {
		nodeID, err := osmNumericID(reference.Reference, "node reference")
		if err != nil {
			return osmFeature{}, osmStoredWay{}, fmt.Errorf("OSM way %s: %w", way.ID, err)
		}
		position, ok := nodes[nodeID]
		if !ok {
			return osmFeature{}, osmStoredWay{}, fmt.Errorf("OSM way %s references missing node %s", way.ID, reference.Reference)
		}
		coordinates = append(coordinates, []float64{position[0], position[1]})
	}
	tags := osmTags(way.Tags)
	geometry, err := osmGeometryFromCoordinates(coordinates, tags)
	if err != nil {
		return osmFeature{}, osmStoredWay{}, fmt.Errorf("failed to build OSM way %s geometry: %w", way.ID, err)
	}
	stored := osmStoredWay{Coordinates: coordinates, Tags: tags}
	return osmFeature{
		Type:       "Feature",
		ID:         "way/" + way.ID,
		BBox:       osmBounds(coordinates),
		Geometry:   geometry,
		Properties: osmProperties{OSMType: "way", OSMID: way.ID, Tags: tags},
	}, stored, nil
}

func osmRelationFeature(relation osmXMLRelation, nodes map[int64][2]float64, ways map[int64]osmStoredWay, relations map[int64]osmXMLRelation) (osmFeature, error) {
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

func osmRelationCollection(relation osmXMLRelation, nodes map[int64][2]float64, ways map[int64]osmStoredWay, relations map[int64]osmXMLRelation, visiting map[string]bool) (osmGeometry, error) {
	if visiting[relation.ID] {
		return osmGeometry{}, fmt.Errorf("nested relation cycle includes relation %s", relation.ID)
	}
	visiting[relation.ID] = true
	defer delete(visiting, relation.ID)
	geometries := make([]osmGeometry, 0, len(relation.Members))
	for _, member := range relation.Members {
		switch member.Type {
		case "node":
			nodeID, err := osmNumericID(member.Reference, "relation member node")
			if err != nil {
				return osmGeometry{}, err
			}
			position, ok := nodes[nodeID]
			if !ok {
				return osmGeometry{}, fmt.Errorf("member node %s was not found", member.Reference)
			}
			encoded, err := json.Marshal(position)
			if err != nil {
				return osmGeometry{}, err
			}
			geometries = append(geometries, osmGeometry{Type: "Point", Coordinates: encoded})
		case "way":
			wayID, err := osmNumericID(member.Reference, "relation member way")
			if err != nil {
				return osmGeometry{}, err
			}
			way, ok := ways[wayID]
			if !ok {
				return osmGeometry{}, fmt.Errorf("member way %s was not found", member.Reference)
			}
			geometry, err := osmGeometryFromCoordinates(way.Coordinates, way.Tags)
			if err != nil {
				return osmGeometry{}, fmt.Errorf("member way %s has invalid geometry: %w", member.Reference, err)
			}
			geometries = append(geometries, geometry)
		case "relation":
			relationID, err := osmNumericID(member.Reference, "nested relation")
			if err != nil {
				return osmGeometry{}, err
			}
			nested, ok := relations[relationID]
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

func osmCollectRelationWays(relation osmXMLRelation, ways map[int64]osmStoredWay, relations map[int64]osmXMLRelation, inheritedRole string, visiting map[string]bool) ([]osmRelationSegment, error) {
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
			wayID, err := osmNumericID(member.Reference, "relation member way")
			if err != nil {
				return nil, err
			}
			way, ok := ways[wayID]
			if !ok {
				return nil, fmt.Errorf("member way %s was not found", member.Reference)
			}
			segments = append(segments, osmRelationSegment{Role: role, Coordinates: osmCopyCoordinates(way.Coordinates)})
		case "relation":
			relationID, err := osmNumericID(member.Reference, "nested relation")
			if err != nil {
				return nil, err
			}
			nested, ok := relations[relationID]
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

func osmNumericID(value, description string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("OSM %s id %q is not a signed 64-bit integer: %w", description, value, err)
	}
	return id, nil
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
		outerRings[index] = osmNormalizeRing(ring, false)
		polygons[index] = [][][]float64{outerRings[index]}
	}
	for _, inner := range innerRings {
		inner = osmNormalizeRing(inner, true)
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

func osmGeometryFromCoordinates(coordinates [][]float64, tags map[string]string) (osmGeometry, error) {
	if len(coordinates) < 2 {
		return osmGeometry{}, fmt.Errorf("expected at least two positions")
	}
	if len(coordinates) >= 4 && osmPositionsEqual(coordinates[0], coordinates[len(coordinates)-1]) && osmWayIsArea(tags) {
		encoded, err := json.Marshal([][][]float64{osmNormalizeRing(coordinates, false)})
		return osmGeometry{Type: "Polygon", Coordinates: encoded}, err
	}
	encoded, err := json.Marshal(coordinates)
	return osmGeometry{Type: "LineString", Coordinates: encoded}, err
}

func osmWayIsArea(tags map[string]string) bool {
	if area, exists := tags["area"]; exists {
		return area != "no" && area != "false" && area != "0"
	}
	for key, value := range tags {
		switch key {
		case "aeroway", "amenity", "area:highway", "building", "building:part", "craft", "geological", "historic",
			"indoor", "landuse", "leisure", "military", "office", "place", "public_transport", "shop", "sport",
			"tourism", "water", "wetland":
			if value != "" && value != "no" {
				return true
			}
		}
	}
	if natural := tags["natural"]; natural != "" && natural != "no" {
		return natural != "coastline" && natural != "cliff" && natural != "ridge" && natural != "tree_row"
	}
	if manMade := tags["man_made"]; manMade != "" && manMade != "no" {
		return manMade != "cutline" && manMade != "embankment" && manMade != "pipeline"
	}
	if power := tags["power"]; power != "" && power != "no" {
		return power != "line" && power != "minor_line" && power != "cable"
	}
	return false
}

func osmRingSignedArea(ring [][]float64) float64 {
	area := 0.0
	for index := 0; index+1 < len(ring); index++ {
		area += ring[index][0]*ring[index+1][1] - ring[index+1][0]*ring[index][1]
	}
	return area / 2
}

func osmNormalizeRing(ring [][]float64, clockwise bool) [][]float64 {
	if (osmRingSignedArea(ring) < 0) == clockwise {
		return ring
	}
	return osmReverseCoordinates(ring)
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

type osmInput struct {
	io.Reader
	file       *os.File
	compressor *gzip.Reader
	closeFile  bool
}

func openOSMInput(filename, compression string) (*osmInput, error) {
	selectedCompression, err := osmInputCompression(filename, compression)
	if err != nil {
		return nil, err
	}
	file := os.Stdin
	closeFile := false
	if filename != "" {
		file, err = os.Open(filename)
		if err != nil {
			return nil, fmt.Errorf("failed to open OSM input file %q: %w", filename, err)
		}
		closeFile = true
	}
	buffered := bufio.NewReaderSize(file, 64*1024)
	input := &osmInput{Reader: buffered, file: file, closeFile: closeFile}
	switch selectedCompression {
	case "":
	case "gz":
		input.compressor, err = gzip.NewReader(buffered)
		if err != nil {
			openErr := fmt.Errorf("failed to open gzip-compressed OSM input %q: %w", filename, err)
			if closeFile {
				return nil, errors.Join(openErr, file.Close())
			}
			return nil, openErr
		}
		input.Reader = input.compressor
	case "bz2":
		input.Reader = bzip2.NewReader(buffered)
	}
	return input, nil
}

func osmInputCompression(filename, compression string) (string, error) {
	selected := strings.ToLower(strings.TrimSpace(compression))
	if selected == "" {
		lowerName := strings.ToLower(filename)
		if strings.HasSuffix(lowerName, ".gz") {
			selected = "gz"
		} else if strings.HasSuffix(lowerName, ".bz2") {
			selected = "bz2"
		}
	}
	if selected != "" && selected != "gz" && selected != "bz2" {
		return "", fmt.Errorf("OSM input compression %q is unsupported; expected gz, bz2, or empty for filename detection", compression)
	}
	return selected, nil
}

func (input *osmInput) Close() error {
	var compressorErr error
	if input.compressor != nil {
		compressorErr = input.compressor.Close()
	}
	var fileErr error
	if input.closeFile {
		fileErr = input.file.Close()
	}
	return errors.Join(compressorErr, fileErr)
}

func convertOSMFile(filename, compression string, output io.Writer, strictOutput bool) error {
	firstPass, err := openOSMInput(filename, compression)
	if err != nil {
		return err
	}
	index, indexErr := indexOSMReferences(firstPass)
	firstCloseErr := firstPass.Close()
	if indexErr != nil || firstCloseErr != nil {
		return errors.Join(indexErr, firstCloseErr)
	}
	secondPass, err := openOSMInput(filename, compression)
	if err != nil {
		return err
	}
	convertErr := convertIndexedOSM(secondPass, output, strictOutput, index)
	return errors.Join(convertErr, secondPass.Close())
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
	var compressorErr error
	if destination.compressor != nil {
		compressorErr = destination.compressor.Close()
	}
	var fileErr error
	if destination.file != nil {
		fileErr = destination.file.Close()
	}
	return errors.Join(compressorErr, fileErr)
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
	output, err := openOSMOutput(outputFilename)
	if err != nil {
		log.Fatal(err)
	}
	var convertErr error
	if inputFilename == "" {
		input, err := openOSMInput("", compression)
		if err != nil {
			log.Fatal(err)
		}
		convertErr = errors.Join(convertOSM(input, output.writer, strict), input.Close())
	} else {
		convertErr = convertOSMFile(inputFilename, compression, output.writer, strict)
	}
	closeErr := output.Close()
	if err := errors.Join(convertErr, closeErr); err != nil {
		log.Fatal(err)
	}
}
