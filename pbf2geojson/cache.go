package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/qedus/osmpbf"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

type pbfFeatureCache interface {
	PutNode(node *osmpbf.Node) error
	PutWay(way *osmpbf.Way) error
	Flush() error
	LookupNodeByID(id int64) (map[string]string, error)
	LookupNodes(way *osmpbf.Way) ([]map[string]string, error)
	LookupWayNodes(wayID int64) ([]map[string]string, error)
	LookupWayTags(wayID int64) (map[string]string, error)
	Close() error
}

type memoryNode struct {
	data   [13]byte
	length uint8
}

type memoryFeatureCache struct {
	nodes   map[int64]memoryNode
	ways    map[int64][]int64
	wayTags map[int64]map[string]string
}

func newMemoryFeatureCache() *memoryFeatureCache {
	return &memoryFeatureCache{
		nodes:   make(map[int64]memoryNode),
		ways:    make(map[int64][]int64),
		wayTags: make(map[int64]map[string]string),
	}
}

func openPBFFeatureCache(path string, batchSize int) (pbfFeatureCache, error) {
	if path == "" {
		return newMemoryFeatureCache(), nil
	}
	return openLevelDBFeatureCache(path, batchSize)
}

func (cache *memoryFeatureCache) PutNode(node *osmpbf.Node) error {
	_, encoded := nodeToBytes(node)
	var stored memoryNode
	copy(stored.data[:], encoded)
	stored.length = uint8(len(encoded))
	cache.nodes[node.ID] = stored
	return nil
}

func (cache *memoryFeatureCache) PutWay(way *osmpbf.Way) error {
	cache.ways[way.ID] = append([]int64(nil), way.NodeIDs...)
	cache.wayTags[way.ID] = copyPBFTags(way.Tags)
	return nil
}

func (cache *memoryFeatureCache) Flush() error {
	return nil
}

func (cache *memoryFeatureCache) LookupNodeByID(id int64) (map[string]string, error) {
	node, ok := cache.nodes[id]
	if !ok {
		return nil, fmt.Errorf("node %d is not in the coordinate cache", id)
	}
	decoded, err := bytesToLatLon(node.data[:node.length])
	if err != nil {
		return nil, fmt.Errorf("node %d has invalid in-memory coordinate data: %w", id, err)
	}
	return decoded, nil
}

func (cache *memoryFeatureCache) LookupNodes(way *osmpbf.Way) ([]map[string]string, error) {
	return lookupPBFNodes(cache, way)
}

func (cache *memoryFeatureCache) LookupWayNodes(wayID int64) ([]map[string]string, error) {
	nodeIDs, ok := cache.ways[wayID]
	if !ok {
		return nil, fmt.Errorf("way %d is not in the member cache", wayID)
	}
	return lookupPBFNodes(cache, &osmpbf.Way{ID: wayID, NodeIDs: nodeIDs})
}

func (cache *memoryFeatureCache) LookupWayTags(wayID int64) (map[string]string, error) {
	tags, ok := cache.wayTags[wayID]
	if !ok {
		return nil, fmt.Errorf("way %d is not in the member cache", wayID)
	}
	return copyPBFTags(tags), nil
}

func (cache *memoryFeatureCache) Close() error {
	return nil
}

type levelDBFeatureCache struct {
	database  *leveldb.DB
	batch     *leveldb.Batch
	batchSize int
}

func openLevelDBFeatureCache(path string, batchSize int) (*levelDBFeatureCache, error) {
	database, err := leveldb.OpenFile(path, &opt.Options{ErrorIfExist: true})
	if err != nil {
		return nil, fmt.Errorf("failed to create new LevelDB at %q: %w", path, err)
	}
	return &levelDBFeatureCache{database: database, batch: new(leveldb.Batch), batchSize: batchSize}, nil
}

func (cache *levelDBFeatureCache) PutNode(node *osmpbf.Node) error {
	id, value := nodeToBytes(node)
	cache.batch.Put([]byte(id), value)
	return cache.flushFullBatch()
}

func (cache *levelDBFeatureCache) PutWay(way *osmpbf.Way) error {
	id, value := wayToBytes(way)
	cache.batch.Put([]byte(id), value)
	tags, err := json.Marshal(way.Tags)
	if err != nil {
		return fmt.Errorf("failed to encode way %d tags for the LevelDB member cache: %w", way.ID, err)
	}
	cache.batch.Put([]byte("T"+strconv.FormatInt(way.ID, 10)), tags)
	return cache.flushFullBatch()
}

func (cache *levelDBFeatureCache) flushFullBatch() error {
	if cache.batch.Len() < cache.batchSize {
		return nil
	}
	return cache.Flush()
}

func (cache *levelDBFeatureCache) Flush() error {
	if cache.batch.Len() == 0 {
		return nil
	}
	options := &opt.WriteOptions{NoWriteMerge: true, Sync: true}
	if err := cache.database.Write(cache.batch, options); err != nil {
		return fmt.Errorf("failed to write %d cached PBF elements to LevelDB: %w", cache.batch.Len(), err)
	}
	cache.batch.Reset()
	return nil
}

func (cache *levelDBFeatureCache) LookupNodeByID(id int64) (map[string]string, error) {
	key := strconv.FormatInt(id, 10)
	data, err := cache.database.Get([]byte(key), nil)
	if err != nil {
		return nil, fmt.Errorf("node %d is not in the LevelDB coordinate cache: %w", id, err)
	}
	decoded, err := bytesToLatLon(data)
	if err != nil {
		return nil, fmt.Errorf("node %d has invalid LevelDB coordinate data: %w", id, err)
	}
	return decoded, nil
}

func (cache *levelDBFeatureCache) LookupNodes(way *osmpbf.Way) ([]map[string]string, error) {
	return lookupPBFNodes(cache, way)
}

func (cache *levelDBFeatureCache) LookupWayNodes(wayID int64) ([]map[string]string, error) {
	key := "W" + strconv.FormatInt(wayID, 10)
	data, err := cache.database.Get([]byte(key), nil)
	if err != nil {
		return nil, fmt.Errorf("way %d is not in the LevelDB member cache: %w", wayID, err)
	}
	nodeIDs, err := bytesToIDSlice(data)
	if err != nil {
		return nil, fmt.Errorf("way %d has invalid LevelDB member data: %w", wayID, err)
	}
	return lookupPBFNodes(cache, &osmpbf.Way{ID: wayID, NodeIDs: nodeIDs})
}

func (cache *levelDBFeatureCache) LookupWayTags(wayID int64) (map[string]string, error) {
	key := "T" + strconv.FormatInt(wayID, 10)
	data, err := cache.database.Get([]byte(key), nil)
	if err != nil {
		return nil, fmt.Errorf("way %d tags are not in the LevelDB member cache: %w", wayID, err)
	}
	var tags map[string]string
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, fmt.Errorf("way %d has invalid LevelDB member tag data: %w", wayID, err)
	}
	return tags, nil
}

func (cache *levelDBFeatureCache) Close() error {
	if err := cache.Flush(); err != nil {
		closeErr := cache.database.Close()
		if closeErr != nil {
			return fmt.Errorf("%v; LevelDB close also failed: %w", err, closeErr)
		}
		return err
	}
	if err := cache.database.Close(); err != nil {
		return fmt.Errorf("failed to close the LevelDB PBF feature cache: %w", err)
	}
	return nil
}

func lookupPBFNodes(cache pbfFeatureCache, way *osmpbf.Way) ([]map[string]string, error) {
	nodes := make([]map[string]string, 0, len(way.NodeIDs))
	for _, nodeID := range way.NodeIDs {
		node, err := cache.LookupNodeByID(nodeID)
		if err != nil {
			return nil, fmt.Errorf("way %d node %d cannot be resolved: %w", way.ID, nodeID, err)
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func copyPBFTags(tags map[string]string) map[string]string {
	copied := make(map[string]string, len(tags))
	for key, value := range tags {
		copied[key] = value
	}
	return copied
}
