package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qedus/osmpbf"
	"github.com/qedus/osmpbf/OSMPBF"
	"github.com/syndtr/goleveldb/leveldb"
	"google.golang.org/protobuf/proto"
)

func TestConvertRealVancouverPBFData(t *testing.T) {
	output := convertPBFForTest(t, "building", false, false)
	features := decodePBFJSONL(t, output)
	if len(features) != 3 {
		t.Fatalf("converted %d Features, expected node, way, and relation", len(features))
	}
	entrance := pbfFeatureByID(t, features, "node/971076208")
	assertPBFPoint(t, entrance, -123.2548774, 49.2682544)
	if entrance.Properties.Tags["building"] != "entrance" {
		t.Fatalf("entrance tags are %#v", entrance.Properties.Tags)
	}
	school := pbfFeatureByID(t, features, "way/23254060")
	if school.Geometry.Type != "LineString" || school.Properties.Tags["name"] != "David Thompson Secondary School" {
		t.Fatalf("school Feature is %#v", school)
	}
	if len(school.BBox) != 4 || math.Abs(school.BBox[0]-(-123.0716086)) > 1e-7 {
		t.Fatalf("school bbox is %#v", school.BBox)
	}
	courtyard := pbfFeatureByID(t, features, "relation/6000")
	if courtyard.Geometry.Type != "Polygon" {
		t.Fatalf("courtyard geometry is %q", courtyard.Geometry.Type)
	}
}

func TestConvertPBFExactTagValue(t *testing.T) {
	features := decodePBFJSONL(t, convertPBFForTest(t, "amenity~toilets", false, false))
	if len(features) != 1 || features[0].ID != "node/276800886" {
		t.Fatalf("exact filter emitted %#v", features)
	}
	assertPBFPoint(t, features[0], -123.1685809, 49.2132425)
	if features[0].Properties.Tags["created_by"] != "JOSM" {
		t.Fatalf("toilet tags are %#v", features[0].Properties.Tags)
	}
}

func TestConvertPBFAndOrTagGroups(t *testing.T) {
	features := decodePBFJSONL(t, convertPBFForTest(t, "building+name,amenity~toilets", false, false))
	if len(features) != 3 {
		t.Fatalf("combined filter emitted %d Features", len(features))
	}
	pbfFeatureByID(t, features, "node/276800886")
	pbfFeatureByID(t, features, "way/23254060")
	pbfFeatureByID(t, features, "relation/6000")
	for _, feature := range features {
		if feature.ID == "node/971076208" {
			t.Fatal("AND filter emitted building-only entrance")
		}
	}
}

func TestConvertPBFIncludesWayNodesAndBounds(t *testing.T) {
	features := decodePBFJSONL(t, convertPBFForTest(t, "building+name", false, true))
	school := pbfFeatureByID(t, features, "way/23254060")
	if len(school.Properties.Nodes) != 5 {
		t.Fatalf("way contains %d node positions", len(school.Properties.Nodes))
	}
	if school.Properties.Nodes[0]["lat"] != "49.2204222" || school.Properties.Nodes[0]["lon"] != "-123.0696397" {
		t.Fatalf("first way node is %#v", school.Properties.Nodes[0])
	}
	if school.Properties.Centroid["lat"] == "" || school.Properties.Centroid["lon"] == "" {
		t.Fatalf("way centroid is %#v", school.Properties.Centroid)
	}
}

func TestConvertPBFStrictMultipolygonWithInnerRing(t *testing.T) {
	output := convertPBFForTest(t, "building", true, false)
	var collection struct {
		Type     string           `json:"type"`
		Features []geoJSONFeature `json:"features"`
	}
	if err := json.Unmarshal([]byte(output), &collection); err != nil {
		t.Fatal(err)
	}
	if collection.Type != "FeatureCollection" || len(collection.Features) != 3 {
		t.Fatalf("strict output type=%q features=%d", collection.Type, len(collection.Features))
	}
	relation := pbfFeatureByID(t, collection.Features, "relation/6000")
	var rings [][][]float64
	if err := json.Unmarshal(relation.Geometry.Coordinates, &rings); err != nil {
		t.Fatal(err)
	}
	if len(rings) != 2 || len(rings[0]) != 5 || len(rings[1]) != 5 {
		t.Fatalf("multipolygon rings are %#v", rings)
	}
}

func TestPBFDecoderReportsTruncatedData(t *testing.T) {
	data := realVancouverPBF(t)
	err := validatePBFFraming(bytes.NewReader(data[:len(data)-7]))
	if err == nil || !strings.Contains(err.Error(), "blob is truncated") {
		t.Fatalf("received error %v", err)
	}
	if err := validatePBFFraming(bytes.NewReader(data)); err != nil {
		t.Fatalf("valid PBF failed framing validation: %v", err)
	}
}

func convertPBFForTest(t *testing.T, tagExpression string, strictOutput, includeWayNodes bool) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "vancouver.osm.pbf")
	if err := os.WriteFile(filename, realVancouverPBF(t), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	config := settings{
		PbfPath:   filename,
		Tags:      pbfTagGroups(tagExpression),
		BatchSize: 2,
		WayNodes:  includeWayNodes,
		Strict:    strictOutput,
	}
	masks := NewBitmaskMap()
	decoder := startTestPBFDecoder(t, file)
	if err := index(decoder, masks, config); err != nil {
		t.Fatal(err)
	}
	if !masks.RelWays.Empty() || !masks.RelRelation.Empty() {
		for {
			relationCount := masks.RelRelation.Len()
			seekTestPBF(t, file)
			relationDecoder := startTestPBFDecoder(t, file)
			if err := indexRelationMembers(relationDecoder, masks, config); err != nil {
				t.Fatal(err)
			}
			if masks.RelRelation.Len() == relationCount {
				break
			}
		}
	}
	database, err := leveldb.OpenFile(filepath.Join(t.TempDir(), "leveldb"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	seekTestPBF(t, file)
	outputDecoder := startTestPBFDecoder(t, file)
	var output bytes.Buffer
	writer := newFeatureWriter(&output, strictOutput)
	if err := writer.Start(); err != nil {
		t.Fatal(err)
	}
	if err := print(outputDecoder, masks, database, config, writer); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func startTestPBFDecoder(t *testing.T, input io.Reader) *osmpbf.Decoder {
	t.Helper()
	decoder := osmpbf.NewDecoder(input)
	if err := decoder.Start(1); err != nil {
		t.Fatal(err)
	}
	return decoder
}

func seekTestPBF(t *testing.T, file *os.File) {
	t.Helper()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
}

func pbfTagGroups(expression string) map[string][]string {
	groups := make(map[string][]string)
	for _, group := range strings.Split(expression, ",") {
		groups[group] = strings.Split(group, "+")
	}
	return groups
}

func decodePBFJSONL(t *testing.T, output string) []geoJSONFeature {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	features := make([]geoJSONFeature, 0, len(lines))
	for lineNumber, line := range lines {
		var feature geoJSONFeature
		if err := json.Unmarshal([]byte(line), &feature); err != nil {
			t.Fatalf("line %d is invalid JSON: %v", lineNumber+1, err)
		}
		features = append(features, feature)
	}
	return features
}

func pbfFeatureByID(t *testing.T, features []geoJSONFeature, id string) geoJSONFeature {
	t.Helper()
	for _, feature := range features {
		if feature.ID == id {
			return feature
		}
	}
	t.Fatalf("Feature %q was not emitted", id)
	return geoJSONFeature{}
}

func assertPBFPoint(t *testing.T, feature geoJSONFeature, longitude, latitude float64) {
	t.Helper()
	if feature.Geometry.Type != "Point" {
		t.Fatalf("geometry is %q", feature.Geometry.Type)
	}
	var coordinates []float64
	if err := json.Unmarshal(feature.Geometry.Coordinates, &coordinates); err != nil {
		t.Fatal(err)
	}
	if len(coordinates) != 2 || math.Abs(coordinates[0]-longitude) > 1e-7 || math.Abs(coordinates[1]-latitude) > 1e-7 {
		t.Fatalf("coordinates are %#v, expected [%v, %v]", coordinates, longitude, latitude)
	}
}

type testPBFStringTable struct {
	values  []string
	indexes map[string]uint32
}

func newTestPBFStringTable() *testPBFStringTable {
	return &testPBFStringTable{values: []string{""}, indexes: map[string]uint32{"": 0}}
}

func (table *testPBFStringTable) index(value string) uint32 {
	if index, exists := table.indexes[value]; exists {
		return index
	}
	index := uint32(len(table.values))
	table.values = append(table.values, value)
	table.indexes[value] = index
	return index
}

func (table *testPBFStringTable) tags(pairs ...string) ([]uint32, []uint32) {
	keys := make([]uint32, 0, len(pairs)/2)
	values := make([]uint32, 0, len(pairs)/2)
	for index := 0; index < len(pairs); index += 2 {
		keys = append(keys, table.index(pairs[index]))
		values = append(values, table.index(pairs[index+1]))
	}
	return keys, values
}

func realVancouverPBF(t *testing.T) []byte {
	t.Helper()
	table := newTestPBFStringTable()
	node := func(id int64, latitude, longitude float64, tags ...string) *OSMPBF.Node {
		keys, values := table.tags(tags...)
		return &OSMPBF.Node{
			Id:   proto.Int64(id),
			Lat:  proto.Int64(int64(math.Round(latitude * 1e7))),
			Lon:  proto.Int64(int64(math.Round(longitude * 1e7))),
			Keys: keys,
			Vals: values,
		}
	}
	nodes := []*OSMPBF.Node{
		node(971076208, 49.2682544, -123.2548774, "building", "entrance"),
		node(276800886, 49.2132425, -123.1685809, "amenity", "toilets", "created_by", "JOSM"),
		node(251630268, 49.2204222, -123.0696397),
		node(3945155393, 49.2204318, -123.0702035),
		node(3945155394, 49.2204831, -123.0702015),
		node(4577742541, 49.2204838, -123.0702460, "entrance", "yes"),
		node(251630270, 49.2205069, -123.0716086),
		node(1, 0, 0),
		node(2, 0, 4),
		node(3, 4, 4),
		node(4, 4, 0),
		node(5, 1, 1),
		node(6, 1, 2),
		node(7, 2, 2),
		node(8, 2, 1),
	}
	schoolKeys, schoolValues := table.tags("building", "school", "name", "David Thompson Secondary School")
	outerKeys, outerValues := table.tags()
	innerKeys, innerValues := table.tags()
	ways := []*OSMPBF.Way{
		{Id: proto.Int64(23254060), Keys: schoolKeys, Vals: schoolValues, Refs: deltaEncodeIDs([]int64{251630268, 3945155393, 3945155394, 4577742541, 251630270})},
		{Id: proto.Int64(5000), Keys: outerKeys, Vals: outerValues, Refs: deltaEncodeIDs([]int64{1, 2, 3, 4, 1})},
		{Id: proto.Int64(5001), Keys: innerKeys, Vals: innerValues, Refs: deltaEncodeIDs([]int64{5, 6, 7, 8, 5})},
	}
	relationKeys, relationValues := table.tags("type", "multipolygon", "building", "yes", "name", "Courtyard")
	relation := &OSMPBF.Relation{
		Id:       proto.Int64(6000),
		Keys:     relationKeys,
		Vals:     relationValues,
		RolesSid: []int32{int32(table.index("outer")), int32(table.index("inner"))},
		Memids:   deltaEncodeIDs([]int64{5000, 5001}),
		Types:    []OSMPBF.Relation_MemberType{OSMPBF.Relation_WAY, OSMPBF.Relation_WAY},
	}
	block := &OSMPBF.PrimitiveBlock{
		Stringtable: &OSMPBF.StringTable{S: table.values},
		Primitivegroup: []*OSMPBF.PrimitiveGroup{
			{Nodes: nodes},
			{Ways: ways},
			{Relations: []*OSMPBF.Relation{relation}},
		},
	}
	header := &OSMPBF.HeaderBlock{
		RequiredFeatures: []string{"OsmSchema-V0.6"},
		Writingprogram:   proto.String("geotools test encoder"),
		Source:           proto.String("OpenStreetMap Vancouver extract records"),
	}
	var output bytes.Buffer
	if err := appendPBFBlock(&output, "OSMHeader", header); err != nil {
		t.Fatal(err)
	}
	if err := appendPBFBlock(&output, "OSMData", block); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func deltaEncodeIDs(ids []int64) []int64 {
	deltas := make([]int64, len(ids))
	previous := int64(0)
	for index, id := range ids {
		deltas[index] = id - previous
		previous = id
	}
	return deltas
}

func appendPBFBlock(output *bytes.Buffer, blockType string, message proto.Message) error {
	payload, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	blobBytes, err := proto.Marshal(&OSMPBF.Blob{Data: &OSMPBF.Blob_Raw{Raw: payload}})
	if err != nil {
		return err
	}
	headerBytes, err := proto.Marshal(&OSMPBF.BlobHeader{Type: proto.String(blockType), Datasize: proto.Int32(int32(len(blobBytes)))})
	if err != nil {
		return err
	}
	if err := binary.Write(output, binary.BigEndian, uint32(len(headerBytes))); err != nil {
		return err
	}
	if _, err := output.Write(headerBytes); err != nil {
		return err
	}
	_, err = output.Write(blobBytes)
	return err
}
