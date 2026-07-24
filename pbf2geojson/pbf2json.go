package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	geo "github.com/paulmach/go.geo"
	"github.com/qedus/osmpbf"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

type settings struct {
	PbfPath         string
	LevedbPath      string
	Tags            map[string][]string
	BatchSize       int
	WayNodes        bool
	Strict          bool
	RunBuiltInTests bool
}

func getSettings() settings {

	// command line flags
	leveldbPath := flag.String("leveldb", "", "path to a new LevelDB directory; empty uses an isolated temporary directory")
	tagList := flag.String("tags", "", "comma-separated list of valid tags, group AND conditions with a +")
	batchSize := flag.Int("batch", 50000, "batch leveldb writes in batches of this size")
	wayNodes := flag.Bool("waynodes", false, "should the lat/lons of nodes belonging to ways be printed")
	strict := flag.Bool("strict", false, "emit one GeoJSON FeatureCollection instead of one Feature per line")
	runBuiltInTests := flag.Bool("test", false, "run built-in tests and exit")

	flag.Parse()
	args := flag.Args()

	if *runBuiltInTests {
		return settings{RunBuiltInTests: true}
	}

	if len(args) < 1 {
		log.Fatal("invalid args, you must specify a PBF file")
	}
	if len(args) > 1 {
		log.Fatal("invalid args, expected exactly one PBF file")
	}

	// invalid tags
	if len(*tagList) < 1 {
		log.Fatal("Nothing to do, you must specify tags to match against")
	}
	if *batchSize < 1 {
		log.Fatal("invalid batch size: expected an integer greater than zero")
	}

	// parse tag conditions
	conditions := make(map[string][]string)
	for _, group := range strings.Split(*tagList, ",") {
		group = strings.TrimSpace(group)
		if group == "" {
			log.Fatal("invalid tags: empty OR group")
		}
		conditions[group] = strings.Split(group, "+")
	}

	if *leveldbPath == "" {
		tempRoot, err := os.MkdirTemp("", "pbf2geojson-leveldb-")
		if err != nil {
			log.Fatalf("failed to create temporary LevelDB directory: %v", err)
		}
		*leveldbPath = filepath.Join(tempRoot, "db")
	}

	return settings{
		PbfPath:    args[0],
		LevedbPath: *leveldbPath,
		Tags:       conditions,
		BatchSize:  *batchSize,
		WayNodes:   *wayNodes,
		Strict:     *strict,
	}
}

func main() {

	// configuration
	config := getSettings()
	if config.RunBuiltInTests {
		if err := runBuiltInTests(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("pbf2geojson built-in tests passed")
		return
	}
	// open pbf file
	file := openFile(config.PbfPath)

	// perform two passes over the file, on the first pass
	// we record a bitmask of the interesting elements in the
	// file, on the second pass we extract the data

	// set up bimasks
	var masks = NewBitmaskMap()

	// set up leveldb connection
	var db = openLevelDB(config.LevedbPath)

	// === first pass (indexing) ===
	idxDecoder := osmpbf.NewDecoder(file)
	err := idxDecoder.Start(runtime.GOMAXPROCS(-1)) // use several goroutines for faster decoding
	if err != nil {
		log.Fatal(err)
	}

	// index target IDs in bitmasks
	if err := index(idxDecoder, masks, config); err != nil {
		log.Fatal(err)
	}

	// Expand nested relations and index the nodes used by their member ways.
	if !masks.RelWays.Empty() || !masks.RelRelation.Empty() {
		for {
			relationCount := masks.RelRelation.Len()
			rewind(file)
			idxRelationsDecoder := osmpbf.NewDecoder(file)
			err = idxRelationsDecoder.Start(runtime.GOMAXPROCS(-1)) // use several goroutines for faster decoding
			if err != nil {
				log.Fatal(err)
			}
			if err := indexRelationMembers(idxRelationsDecoder, masks, config); err != nil {
				log.Fatal(err)
			}
			if masks.RelRelation.Len() == relationCount {
				break
			}
		}
	}

	// === final pass (printing json) ===
	rewind(file)
	decoder := osmpbf.NewDecoder(file)
	err = decoder.Start(runtime.GOMAXPROCS(-1)) // use several goroutines for faster decoding
	if err != nil {
		log.Fatal(err)
	}

	writer := newFeatureWriter(os.Stdout, config.Strict)
	if err := writer.Start(); err != nil {
		log.Fatal(err)
	}
	if err := print(decoder, masks, db, config, writer); err != nil {
		log.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		log.Fatal(err)
	}
	if err := db.Close(); err != nil {
		log.Fatalf("failed to close LevelDB %q: %v", config.LevedbPath, err)
	}
	if err := file.Close(); err != nil {
		log.Fatalf("failed to close PBF file %q: %v", config.PbfPath, err)
	}
}

func rewind(file *os.File) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		log.Fatalf("failed to rewind PBF file %q: %v", file.Name(), err)
	}
}

func index(d *osmpbf.Decoder, masks *BitmaskMap, config settings) error {
	for {
		if v, err := d.Decode(); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("failed to decode PBF during output pass: %w", err)
		} else {
			switch v := v.(type) {

			case *osmpbf.Node:
				if hasTags(v.Tags) && containsValidTags(v.Tags, config.Tags) {
					masks.Nodes.Insert(v.ID)
				}

			case *osmpbf.Way:
				if hasTags(v.Tags) && containsValidTags(v.Tags, config.Tags) {
					masks.Ways.Insert(v.ID)
					for _, nodeid := range v.NodeIDs {
						masks.WayRefs.Insert(nodeid)
					}
				}

			case *osmpbf.Relation:
				if hasTags(v.Tags) && containsValidTags(v.Tags, config.Tags) {
					masks.Relations.Insert(v.ID)
					indexRelation(v, masks)
				}
			}
		}
	}
	return nil
}

func indexRelationMembers(d *osmpbf.Decoder, masks *BitmaskMap, config settings) error {
	for {
		if v, err := d.Decode(); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("failed to decode PBF while indexing relation members: %w", err)
		} else {
			switch v := v.(type) {
			case *osmpbf.Way:
				if masks.RelWays.Has(v.ID) {
					for _, nodeid := range v.NodeIDs {
						masks.RelNodes.Insert(nodeid)
					}
				}
			case *osmpbf.Relation:
				if masks.RelRelation.Has(v.ID) {
					indexRelation(v, masks)
				}
			}
		}
	}
	return nil
}

func indexRelation(relation *osmpbf.Relation, masks *BitmaskMap) {
	for _, member := range relation.Members {
		switch member.Type {
		case 0:
			masks.RelNodes.Insert(member.ID)
		case 1:
			masks.RelWays.Insert(member.ID)
		case 2:
			masks.RelRelation.Insert(member.ID)
		}
	}
}

func print(d *osmpbf.Decoder, masks *BitmaskMap, db *leveldb.DB, config settings, writer *featureWriter) error {

	batch := new(leveldb.Batch)
	finishedNodes := false
	finishedWays := false
	relations := make(map[int64]*osmpbf.Relation)
	selectedRelationIDs := make([]int64, 0)

	for {
		if v, err := d.Decode(); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("failed to decode PBF during output pass: %w", err)
		} else {
			switch v := v.(type) {

			case *osmpbf.Node:

				// ----------------
				// write to leveldb
				// note: only write way refs and relation member nodes
				// ----------------
				if masks.WayRefs.Has(v.ID) || masks.RelNodes.Has(v.ID) {

					// write in batches
					cacheQueueNode(batch, v)
					if batch.Len() >= config.BatchSize {
						cacheFlush(db, batch, true)
					}
				}

				// bitmask indicates if this is a node of interest
				// if so, print it
				if masks.Nodes.Has(v.ID) {

					// trim tags
					v.Tags = trimTags(v.Tags)
					feature, err := newNodeFeature(v)
					if err != nil {
						return err
					}
					if err := writer.Write(feature); err != nil {
						return err
					}
				}

			case *osmpbf.Way:

				// ----------------
				// write to leveldb
				// flush outstanding node batches
				// before processing any ways
				// ----------------
				if !finishedNodes {
					finishedNodes = true
					if batch.Len() > 0 {
						cacheFlush(db, batch, true)
					}
				}

				// ----------------
				// write to leveldb
				// note: only write relation member ways
				// ----------------
				if masks.RelWays.Has(v.ID) {

					// write in batches
					cacheQueueWay(batch, v)
					if batch.Len() >= config.BatchSize {
						cacheFlush(db, batch, true)
					}
				}

				// bitmask indicates if this is a way of interest
				// if so, print it
				if masks.Ways.Has(v.ID) {

					// lookup from leveldb
					latlons, err := cacheLookupNodes(db, v)

					// skip ways which fail to denormalize
					if err != nil {
						return fmt.Errorf("failed to denormalize way %d: %w", v.ID, err)
					}
					if len(latlons) == 0 {
						return fmt.Errorf("failed to denormalize way %d: way has no node coordinates", v.ID)
					}

					// compute centroid
					centroid, bounds := computeCentroidAndBounds(latlons)

					// trim tags
					v.Tags = trimTags(v.Tags)

					feature, err := newWayFeature(v, latlons, centroid, bounds, config.WayNodes)
					if err != nil {
						return err
					}
					if err := writer.Write(feature); err != nil {
						return err
					}
				}

			case *osmpbf.Relation:

				// ----------------
				// write to leveldb
				// flush outstanding way batches
				// before processing any relation
				// ----------------
				if !finishedWays {
					finishedWays = true
					if batch.Len() > 0 {
						cacheFlush(db, batch, true)
					}
				}

				if masks.Relations.Has(v.ID) || masks.RelRelation.Has(v.ID) {
					relations[v.ID] = v
					if masks.Relations.Has(v.ID) {
						selectedRelationIDs = append(selectedRelationIDs, v.ID)
					}
				}

			default:

				return fmt.Errorf("unknown decoded PBF type %T", v)

			}
		}
	}
	if batch.Len() > 0 {
		cacheFlush(db, batch, true)
	}
	for _, relationID := range selectedRelationIDs {
		feature, err := newRelationFeature(db, relations[relationID], relations)
		if err != nil {
			return err
		}
		if err := writer.Write(feature); err != nil {
			return err
		}
	}
	return nil
}

type geoJSONGeometry struct {
	Type        string            `json:"type"`
	Coordinates json.RawMessage   `json:"coordinates,omitempty"`
	Geometries  []geoJSONGeometry `json:"geometries,omitempty"`
}

type geoJSONProperties struct {
	OSMType  string              `json:"osm_type"`
	OSMID    int64               `json:"osm_id"`
	Tags     map[string]string   `json:"tags"`
	Centroid map[string]string   `json:"centroid,omitempty"`
	Nodes    []map[string]string `json:"nodes,omitempty"`
}

type geoJSONFeature struct {
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	BBox       []float64         `json:"bbox,omitempty"`
	Geometry   geoJSONGeometry   `json:"geometry"`
	Properties geoJSONProperties `json:"properties"`
}

type featureWriter struct {
	output *bufio.Writer
	strict bool
	wrote  bool
}

func newFeatureWriter(output io.Writer, strict bool) *featureWriter {
	return &featureWriter{output: bufio.NewWriter(output), strict: strict}
}

func (writer *featureWriter) Start() error {
	if writer.strict {
		_, err := writer.output.WriteString(`{"type":"FeatureCollection","features":[`)
		return err
	}
	return nil
}

func (writer *featureWriter) Write(feature geoJSONFeature) error {
	encoded, err := json.Marshal(feature)
	if err != nil {
		return fmt.Errorf("failed to encode GeoJSON Feature %q: %w", feature.ID, err)
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

func (writer *featureWriter) Close() error {
	if writer.strict {
		if _, err := writer.output.WriteString("]}\n"); err != nil {
			return err
		}
	}
	return writer.output.Flush()
}

func newNodeFeature(node *osmpbf.Node) (geoJSONFeature, error) {
	coordinates, err := json.Marshal([]float64{node.Lon, node.Lat})
	if err != nil {
		return geoJSONFeature{}, fmt.Errorf("failed to encode node %d coordinates: %w", node.ID, err)
	}
	return geoJSONFeature{
		Type:     "Feature",
		ID:       fmt.Sprintf("node/%d", node.ID),
		Geometry: geoJSONGeometry{Type: "Point", Coordinates: coordinates},
		Properties: geoJSONProperties{
			OSMType: "node",
			OSMID:   node.ID,
			Tags:    node.Tags,
		},
	}, nil
}

func newWayFeature(way *osmpbf.Way, latlons []map[string]string, centroid map[string]string, bounds *geo.Bound, includeNodes bool) (geoJSONFeature, error) {
	coordinates, err := coordinatesFromLatLons(latlons)
	if err != nil {
		return geoJSONFeature{}, fmt.Errorf("failed to build way %d geometry: %w", way.ID, err)
	}
	geometry, err := geometryFromCoordinates(coordinates)
	if err != nil {
		return geoJSONFeature{}, fmt.Errorf("failed to build way %d geometry: %w", way.ID, err)
	}
	properties := geoJSONProperties{OSMType: "way", OSMID: way.ID, Tags: way.Tags, Centroid: centroid}
	if includeNodes {
		properties.Nodes = latlons
	}
	return geoJSONFeature{
		Type:       "Feature",
		ID:         fmt.Sprintf("way/%d", way.ID),
		BBox:       geoJSONBbox(bounds),
		Geometry:   geometry,
		Properties: properties,
	}, nil
}

func coordinatesFromLatLons(latlons []map[string]string) ([][]float64, error) {
	if len(latlons) == 0 {
		return nil, fmt.Errorf("expected at least one coordinate")
	}
	coordinates := make([][]float64, 0, len(latlons))
	for index, latlon := range latlons {
		lon, err := strconv.ParseFloat(latlon["lon"], 64)
		if err != nil {
			return nil, fmt.Errorf("coordinate %d has invalid longitude %q: %w", index, latlon["lon"], err)
		}
		lat, err := strconv.ParseFloat(latlon["lat"], 64)
		if err != nil {
			return nil, fmt.Errorf("coordinate %d has invalid latitude %q: %w", index, latlon["lat"], err)
		}
		coordinates = append(coordinates, []float64{lon, lat})
	}
	return coordinates, nil
}

func geometryFromCoordinates(coordinates [][]float64) (geoJSONGeometry, error) {
	geometryType := "LineString"
	geometryCoordinates := coordinates
	if len(coordinates) >= 4 && positionsEqual(coordinates[0], coordinates[len(coordinates)-1]) {
		geometryType = "Polygon"
		encoded, err := json.Marshal([][][]float64{coordinates})
		return geoJSONGeometry{Type: geometryType, Coordinates: encoded}, err
	}
	encoded, err := json.Marshal(geometryCoordinates)
	return geoJSONGeometry{Type: geometryType, Coordinates: encoded}, err
}

func geoJSONBbox(bounds *geo.Bound) []float64 {
	if bounds == nil {
		return nil
	}
	return []float64{bounds.West(), bounds.South(), bounds.East(), bounds.North()}
}

type relationWay struct {
	Role        string
	WayID       int64
	Coordinates [][]float64
}

func newRelationFeature(db *leveldb.DB, relation *osmpbf.Relation, relations map[int64]*osmpbf.Relation) (geoJSONFeature, error) {
	if relation == nil {
		return geoJSONFeature{}, fmt.Errorf("selected relation was not present in the output pass")
	}
	ways := make([]relationWay, 0)
	nodeIDs := make([]int64, 0)
	if err := collectRelationMembers(relation, relations, "", make(map[int64]bool), &ways, &nodeIDs); err != nil {
		return geoJSONFeature{}, fmt.Errorf("failed to expand relation %d: %w", relation.ID, err)
	}
	for index := range ways {
		latlons, err := cacheLookupWayNodes(db, ways[index].WayID)
		if err != nil {
			return geoJSONFeature{}, fmt.Errorf("failed to denormalize relation %d member way %d: %w", relation.ID, ways[index].WayID, err)
		}
		coordinates, err := coordinatesFromLatLons(latlons)
		if err != nil {
			return geoJSONFeature{}, fmt.Errorf("failed to decode relation %d member way %d: %w", relation.ID, ways[index].WayID, err)
		}
		ways[index].Coordinates = coordinates
	}

	var geometry geoJSONGeometry
	var err error
	if relation.Tags["type"] == "multipolygon" || relation.Tags["type"] == "boundary" {
		geometry, err = multipolygonGeometry(ways)
	} else {
		geometry, err = relationGeometry(db, ways, nodeIDs)
	}
	if err != nil {
		return geoJSONFeature{}, fmt.Errorf("failed to build relation %d geometry: %w", relation.ID, err)
	}
	return geoJSONFeature{
		Type:       "Feature",
		ID:         fmt.Sprintf("relation/%d", relation.ID),
		BBox:       geometryBounds(ways, db, nodeIDs),
		Geometry:   geometry,
		Properties: geoJSONProperties{OSMType: "relation", OSMID: relation.ID, Tags: trimTags(relation.Tags)},
	}, nil
}

func collectRelationMembers(relation *osmpbf.Relation, relations map[int64]*osmpbf.Relation, inheritedRole string, visiting map[int64]bool, ways *[]relationWay, nodeIDs *[]int64) error {
	if visiting[relation.ID] {
		return fmt.Errorf("nested relation cycle includes relation %d", relation.ID)
	}
	visiting[relation.ID] = true
	defer delete(visiting, relation.ID)

	for _, member := range relation.Members {
		role := member.Role
		if inheritedRole == "outer" || inheritedRole == "inner" {
			role = inheritedRole
		}
		switch member.Type {
		case 0:
			*nodeIDs = append(*nodeIDs, member.ID)
		case 1:
			*ways = append(*ways, relationWay{Role: role, WayID: member.ID})
		case 2:
			nested, ok := relations[member.ID]
			if !ok {
				return fmt.Errorf("nested relation %d was not indexed", member.ID)
			}
			if err := collectRelationMembers(nested, relations, role, visiting, ways, nodeIDs); err != nil {
				return err
			}
		default:
			return fmt.Errorf("relation %d has unknown member type %d", relation.ID, member.Type)
		}
	}
	return nil
}

func multipolygonGeometry(ways []relationWay) (geoJSONGeometry, error) {
	outerSegments := make([][][]float64, 0)
	innerSegments := make([][][]float64, 0)
	for _, way := range ways {
		if way.Role == "inner" {
			innerSegments = append(innerSegments, way.Coordinates)
		} else {
			outerSegments = append(outerSegments, way.Coordinates)
		}
	}
	outerRings, err := stitchRings(outerSegments)
	if err != nil {
		return geoJSONGeometry{}, fmt.Errorf("outer rings: %w", err)
	}
	if len(outerRings) == 0 {
		return geoJSONGeometry{}, fmt.Errorf("expected at least one outer ring")
	}
	innerRings, err := stitchRings(innerSegments)
	if err != nil {
		return geoJSONGeometry{}, fmt.Errorf("inner rings: %w", err)
	}

	polygons := make([][][][]float64, len(outerRings))
	for index, ring := range outerRings {
		polygons[index] = [][][]float64{ring}
	}
	for _, inner := range innerRings {
		assigned := false
		for outerIndex, outer := range outerRings {
			if pointInRing(inner[0], outer) {
				polygons[outerIndex] = append(polygons[outerIndex], inner)
				assigned = true
				break
			}
		}
		if !assigned {
			return geoJSONGeometry{}, fmt.Errorf("inner ring is not contained by an outer ring")
		}
	}

	if len(polygons) == 1 {
		encoded, err := json.Marshal(polygons[0])
		return geoJSONGeometry{Type: "Polygon", Coordinates: encoded}, err
	}
	encoded, err := json.Marshal(polygons)
	return geoJSONGeometry{Type: "MultiPolygon", Coordinates: encoded}, err
}

func relationGeometry(db *leveldb.DB, ways []relationWay, nodeIDs []int64) (geoJSONGeometry, error) {
	geometries := make([]geoJSONGeometry, 0, len(ways)+len(nodeIDs))
	for _, way := range ways {
		geometry, err := geometryFromCoordinates(way.Coordinates)
		if err != nil {
			return geoJSONGeometry{}, err
		}
		geometries = append(geometries, geometry)
	}
	for _, nodeID := range nodeIDs {
		latlon, err := cacheLookupNodeByID(db, nodeID)
		if err != nil {
			return geoJSONGeometry{}, fmt.Errorf("failed to find member node %d: %w", nodeID, err)
		}
		coordinates, err := coordinatesFromLatLons([]map[string]string{latlon})
		if err != nil {
			return geoJSONGeometry{}, err
		}
		encoded, err := json.Marshal(coordinates[0])
		if err != nil {
			return geoJSONGeometry{}, err
		}
		geometries = append(geometries, geoJSONGeometry{Type: "Point", Coordinates: encoded})
	}
	if len(geometries) == 0 {
		return geoJSONGeometry{}, fmt.Errorf("relation has no resolvable members")
	}
	return geoJSONGeometry{Type: "GeometryCollection", Geometries: geometries}, nil
}

func stitchRings(segments [][][]float64) ([][][]float64, error) {
	remaining := make([][][]float64, 0, len(segments))
	for _, segment := range segments {
		if len(segment) < 2 {
			return nil, fmt.Errorf("ring segment has fewer than two positions")
		}
		remaining = append(remaining, copyCoordinates(segment))
	}
	rings := make([][][]float64, 0)
	for len(remaining) > 0 {
		ring := remaining[0]
		remaining = remaining[1:]
		for !positionsEqual(ring[0], ring[len(ring)-1]) {
			joined := false
			for index, segment := range remaining {
				combined, ok := joinCoordinates(ring, segment)
				if ok {
					ring = combined
					remaining = append(remaining[:index], remaining[index+1:]...)
					joined = true
					break
				}
			}
			if !joined {
				return nil, fmt.Errorf("could not close a ring beginning at %v", ring[0])
			}
		}
		if len(ring) < 4 {
			return nil, fmt.Errorf("closed ring has fewer than four positions")
		}
		rings = append(rings, ring)
	}
	return rings, nil
}

func joinCoordinates(ring, segment [][]float64) ([][]float64, bool) {
	ringStart := ring[0]
	ringEnd := ring[len(ring)-1]
	segmentStart := segment[0]
	segmentEnd := segment[len(segment)-1]
	if positionsEqual(ringEnd, segmentStart) {
		return append(ring, segment[1:]...), true
	}
	if positionsEqual(ringEnd, segmentEnd) {
		reversed := reverseCoordinates(segment)
		return append(ring, reversed[1:]...), true
	}
	if positionsEqual(ringStart, segmentEnd) {
		return append(copyCoordinates(segment[:len(segment)-1]), ring...), true
	}
	if positionsEqual(ringStart, segmentStart) {
		reversed := reverseCoordinates(segment)
		return append(reversed[:len(reversed)-1], ring...), true
	}
	return nil, false
}

func reverseCoordinates(coordinates [][]float64) [][]float64 {
	reversed := make([][]float64, len(coordinates))
	for index := range coordinates {
		reversed[len(coordinates)-1-index] = append([]float64(nil), coordinates[index]...)
	}
	return reversed
}

func copyCoordinates(coordinates [][]float64) [][]float64 {
	copied := make([][]float64, len(coordinates))
	for index, coordinate := range coordinates {
		copied[index] = append([]float64(nil), coordinate...)
	}
	return copied
}

func positionsEqual(left, right []float64) bool {
	return len(left) >= 2 && len(right) >= 2 && left[0] == right[0] && left[1] == right[1]
}

func pointInRing(point []float64, ring [][]float64) bool {
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

func geometryBounds(ways []relationWay, db *leveldb.DB, nodeIDs []int64) []float64 {
	bounds := []float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, way := range ways {
		for _, coordinate := range way.Coordinates {
			extendBounds(bounds, coordinate)
		}
	}
	for _, nodeID := range nodeIDs {
		if latlon, err := cacheLookupNodeByID(db, nodeID); err == nil {
			if coordinates, err := coordinatesFromLatLons([]map[string]string{latlon}); err == nil {
				extendBounds(bounds, coordinates[0])
			}
		}
	}
	if math.IsInf(bounds[0], 1) {
		return nil
	}
	return bounds
}

func extendBounds(bounds, coordinate []float64) {
	bounds[0] = math.Min(bounds[0], coordinate[0])
	bounds[1] = math.Min(bounds[1], coordinate[1])
	bounds[2] = math.Max(bounds[2], coordinate[0])
	bounds[3] = math.Max(bounds[3], coordinate[1])
}

// determine if the node is for an entrance
// https://wiki.openstreetmap.org/wiki/Key:entrance
func isEntranceNode(node *osmpbf.Node) uint8 {
	if val, ok := node.Tags["entrance"]; ok {
		var norm = strings.ToLower(val)
		switch norm {
		case "main":
			return 2
		case "yes", "home", "staircase":
			return 1
		}
	}
	return 0
}

// determine if the node is accessible for wheelchair users
// https://wiki.openstreetmap.org/wiki/Key:entrance
func isWheelchairAccessibleNode(node *osmpbf.Node) uint8 {
	if val, ok := node.Tags["wheelchair"]; ok {
		var norm = strings.ToLower(val)
		switch norm {
		case "yes":
			return 2
		case "no":
			return 0
		default:
			return 1
		}
	}
	return 0
}

// decode bytes to a 'latlon' type object
func bytesToLatLon(data []byte) map[string]string {
	buf := make([]byte, 8)
	latlon := make(map[string]string, 4)

	// first 6 bytes are the latitude
	// buf = append(buf, data[0:6]...)
	copy(buf, data[:6])
	lat64 := math.Float64frombits(binary.BigEndian.Uint64(buf[:8]))
	latlon["lat"] = strconv.FormatFloat(lat64, 'f', 7, 64)

	// next 6 bytes are the longitude
	// buf = append(buf[:0], data[6:12]...)
	copy(buf, data[6:12])
	lon64 := math.Float64frombits(binary.BigEndian.Uint64(buf[:8]))
	latlon["lon"] = strconv.FormatFloat(lon64, 'f', 7, 64)

	// check for the bitmask byte which indicates things like an
	// entrance and the level of wheelchair accessibility
	if len(data) > 12 {
		latlon["entrance"] = fmt.Sprintf("%d", (data[12]&0xC0)>>6)
		latlon["wheelchair"] = fmt.Sprintf("%d", (data[12]&0x30)>>4)
	}

	return latlon
}

// encode a node as bytes (between 12 & 13 bytes used)
func nodeToBytes(node *osmpbf.Node) (string, []byte) {
	stringid := strconv.FormatInt(node.ID, 10)

	buf := make([]byte, 14)
	// encode lat/lon as 64 bit floats packed in to 8 bytes,
	// each float is then truncated to 6 bytes because we don't
	// need the additional precision (> 8 decimal places)

	binary.BigEndian.PutUint64(buf, math.Float64bits(node.Lat))
	binary.BigEndian.PutUint64(buf[6:], math.Float64bits(node.Lon))

	// generate a bitmask for relevant tag features
	isEntrance := isEntranceNode(node)
	if isEntrance == 0 {
		return stringid, buf[:12]
	}

	// leftmost two bits are for the entrance, next two bits are accessibility
	// remaining 4 rightmost bits are reserved for future use.
	bitmask := isEntrance << 6
	bitmask |= isWheelchairAccessibleNode(node) << 4
	buf[12] = bitmask

	return stringid, buf[:13]
}

func idSliceToBytes(ids []int64) []byte {
	buf := make([]byte, 8*len(ids))
	for i, id := range ids {
		binary.BigEndian.PutUint64(buf[8*i:], uint64(id))
	}
	return buf
}

func bytesToIDSlice(bytes []byte) []int64 {
	if len(bytes)%8 != 0 {
		log.Fatal("invalid byte slice length: not divisible by 8")
	}

	ids := make([]int64, len(bytes)/8)
	for i := 0; i < len(bytes)/8; i++ {
		ids[i] = int64(binary.BigEndian.Uint64(bytes[8*i:]))
	}
	return ids
}

// encode a way as bytes (repeated int64 numbers)
func wayToBytes(way *osmpbf.Way) (string, []byte) {
	// prefix the key with 'W' to differentiate it from node ids
	stringid := "W" + strconv.FormatInt(way.ID, 10)
	return stringid, idSliceToBytes(way.NodeIDs)
}

func openFile(filename string) *os.File {
	// no file specified
	if len(filename) < 1 {
		log.Fatal("invalid file: you must specify a pbf path as arg[1]")
	}
	// try to open the file
	file, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}
	return file
}

func openLevelDB(path string) *leveldb.DB {
	// try to open the db
	db, err := leveldb.OpenFile(path, &opt.Options{ErrorIfExist: true})
	if err != nil {
		log.Fatalf("failed to create new LevelDB at %q: %v", path, err)
	}
	return db
}

// extract all keys to array
// keys := []string{}
// for k := range v.Tags {
//     keys = append(keys, k)
// }

// check tags contain features from a whitelist
func matchTagsAgainstCompulsoryTagList(tags map[string]string, tagList []string) bool {
	for _, condition := range tagList {
		condition = strings.TrimSpace(condition)
		if condition == "" {
			return false
		}
		keyAndValue := strings.SplitN(condition, "~", 2)
		tagValue, ok := tags[keyAndValue[0]]
		if !ok {
			return false
		}
		if len(keyAndValue) == 2 && tagValue != keyAndValue[1] {
			return false
		}
	}
	return true
}

// check tags contain features from a groups of whitelists
func containsValidTags(tags map[string]string, group map[string][]string) bool {
	for _, list := range group {
		if matchTagsAgainstCompulsoryTagList(tags, list) {
			return true
		}
	}
	return false
}

// trim leading/trailing spaces from keys and values
func trimTags(tags map[string]string) map[string]string {
	trimmed := make(map[string]string)
	for k, v := range tags {
		trimmed[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return trimmed
}

// check if a tag list is empty or not
func hasTags(tags map[string]string) bool {
	return len(tags) > 0
}

func runBuiltInTests() error {
	tags := map[string]string{"amenity": "toilets", "name": "Station"}
	conditions := map[string][]string{
		"amenity~toilets+name": {"amenity~toilets", "name"},
	}
	if !hasTags(tags) || !containsValidTags(tags, conditions) {
		return fmt.Errorf("tag matching rejected a valid AND/value condition")
	}
	if containsValidTags(tags, map[string][]string{"shop": {"shop"}}) {
		return fmt.Errorf("tag matching accepted a missing tag")
	}
	segments := [][][]float64{
		{{0, 0}, {1, 0}},
		{{1, 1}, {0, 1}, {0, 0}},
		{{1, 0}, {1, 1}},
	}
	rings, err := stitchRings(segments)
	if err != nil {
		return fmt.Errorf("ring stitching failed: %w", err)
	}
	if len(rings) != 1 || len(rings[0]) != 5 {
		return fmt.Errorf("ring stitching returned %d rings with %d positions, expected one ring with five positions", len(rings), len(rings[0]))
	}
	feature, err := newNodeFeature(&osmpbf.Node{ID: 7, Lat: 1, Lon: 2, Tags: map[string]string{"path": `C:\data`}})
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(feature)
	if err != nil || !json.Valid(encoded) {
		return fmt.Errorf("node GeoJSON encoding failed: %v", err)
	}
	var collection bytes.Buffer
	writer := newFeatureWriter(&collection, true)
	if err := writer.Start(); err != nil {
		return err
	}
	if err := writer.Write(feature); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if !json.Valid(collection.Bytes()) {
		return fmt.Errorf("strict PBF output is not valid JSON: %s", collection.String())
	}
	masks := NewBitmaskMap()
	masks.Nodes.Insert(42)
	var serialized bytes.Buffer
	written, err := masks.WriteTo(&serialized)
	if err != nil || written != int64(serialized.Len()) {
		return fmt.Errorf("bitmask serialization wrote %d bytes into a %d-byte buffer: %v", written, serialized.Len(), err)
	}
	var decoded BitmaskMap
	read, err := decoded.ReadFrom(bytes.NewReader(serialized.Bytes()))
	if err != nil || read != written || !decoded.Nodes.Has(42) {
		return fmt.Errorf("bitmask deserialization read %d of %d bytes or lost node 42: %v", read, written, err)
	}
	return nil
}

// select which entrance is preferable
func selectEntrance(entrances []map[string]string) map[string]string {

	// use the mapped entrance location where available
	var centroid = make(map[string]string)
	centroid["type"] = "entrance"

	// prefer the first 'main' entrance we find (should usually only be one).
	for _, entrance := range entrances {
		if val, ok := entrance["entrance"]; ok && val == "2" {
			centroid["lat"] = entrance["lat"]
			centroid["lon"] = entrance["lon"]
			return centroid
		}
	}

	// else prefer the first wheelchair accessible entrance we find
	for _, entrance := range entrances {
		if val, ok := entrance["wheelchair"]; ok && val != "0" {
			centroid["lat"] = entrance["lat"]
			centroid["lon"] = entrance["lon"]
			return centroid
		}
	}

	// otherwise just take the first entrance in the list
	centroid["lat"] = entrances[0]["lat"]
	centroid["lon"] = entrances[0]["lon"]
	return centroid
}

// compute the centroid of a way and its bbox
func computeCentroidAndBounds(latlons []map[string]string) (map[string]string, *geo.Bound) {

	// check to see if there is a tagged entrance we can use.
	var entrances []map[string]string
	for _, latlon := range latlons {
		if _, ok := latlon["entrance"]; ok {
			entrances = append(entrances, latlon)
		}
	}

	// convert lat/lon map to geo.PointSet
	points := geo.NewPointSet()
	for index, each := range latlons {
		lon, err := strconv.ParseFloat(each["lon"], 64)
		if err != nil {
			panic(fmt.Sprintf("cached coordinate %d has invalid longitude %q: %v", index, each["lon"], err))
		}
		lat, err := strconv.ParseFloat(each["lat"], 64)
		if err != nil {
			panic(fmt.Sprintf("cached coordinate %d has invalid latitude %q: %v", index, each["lat"], err))
		}
		points.Push(geo.NewPoint(lon, lat))
	}

	// use the mapped entrance location where available
	if len(entrances) > 0 {
		return selectEntrance(entrances), points.Bound()
	}

	// determine if the way is a closed centroid or a linestring
	// by comparing first and last coordinates.
	isClosed := false
	if points.Length() > 2 {
		isClosed = points.First().Equals(points.Last())
	}

	// compute the centroid using one of two different algorithms
	var compute *geo.Point
	if isClosed {
		compute = GetPolygonCentroid(points)
	} else {
		compute = GetLineCentroid(points)
	}

	// return point as lat/lon map
	var centroid = make(map[string]string)
	centroid["lat"] = formatCoordinate(compute.Lat())
	centroid["lon"] = formatCoordinate(compute.Lng())

	return centroid, points.Bound()
}

func formatCoordinate(value float64) string {
	if math.Abs(value) < 0.00000005 {
		value = 0
	}
	return strconv.FormatFloat(value, 'f', 7, 64)
}
