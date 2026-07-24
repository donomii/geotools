package main

import (
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"reflect"
)

// BitmaskMap - struct to hold common masks
type BitmaskMap struct {
	Nodes       *Bitmask
	Ways        *Bitmask
	Relations   *Bitmask
	WayRefs     *Bitmask
	RelNodes    *Bitmask
	RelWays     *Bitmask
	RelRelation *Bitmask
}

type byteCountingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *byteCountingWriter) Write(data []byte) (int, error) {
	written, err := writer.writer.Write(data)
	writer.count += int64(written)
	return written, err
}

type byteCountingReader struct {
	reader io.Reader
	count  int64
}

func (reader *byteCountingReader) Read(data []byte) (int, error) {
	read, err := reader.reader.Read(data)
	reader.count += int64(read)
	return read, err
}

// NewBitmaskMap - constructor
func NewBitmaskMap() *BitmaskMap {
	return &BitmaskMap{
		Nodes:       NewBitMask(),
		Ways:        NewBitMask(),
		Relations:   NewBitMask(),
		WayRefs:     NewBitMask(),
		RelNodes:    NewBitMask(),
		RelWays:     NewBitMask(),
		RelRelation: NewBitMask(),
	}
}

// WriteTo - write to destination
func (m *BitmaskMap) WriteTo(sink io.Writer) (int64, error) {
	countingSink := &byteCountingWriter{writer: sink}
	encoder := gob.NewEncoder(countingSink)
	err := encoder.Encode(m)
	return countingSink.count, err
}

// ReadFrom - read from destination
func (m *BitmaskMap) ReadFrom(tap io.Reader) (int64, error) {
	countingTap := &byteCountingReader{reader: tap}
	decoder := gob.NewDecoder(countingTap)
	err := decoder.Decode(m)
	if err == nil {
		m.initialize()
	}
	return countingTap.count, err
}

func (m *BitmaskMap) initialize() {
	masks := []*Bitmask{m.Nodes, m.Ways, m.Relations, m.WayRefs, m.RelNodes, m.RelWays, m.RelRelation}
	for _, mask := range masks {
		if mask != nil && mask.mutex == nil {
			replacement := NewBitMask()
			replacement.I = mask.I
			mask.mutex = replacement.mutex
		}
	}
}

// WriteToFile - write to disk
func (m *BitmaskMap) WriteToFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create bitmask file %q: %w", path, err)
	}
	if _, err := m.WriteTo(file); err != nil {
		closeErr := file.Close()
		return errors.Join(fmt.Errorf("failed to encode bitmask file %q: %w", path, err), closeErr)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close bitmask file %q: %w", path, err)
	}
	log.Println("wrote bitmask:", path)
	return nil
}

// ReadFromFile - read from disk
func (m *BitmaskMap) ReadFromFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open bitmask file %q: %w", path, err)
	}
	if _, err := m.ReadFrom(file); err != nil {
		closeErr := file.Close()
		return errors.Join(fmt.Errorf("failed to decode bitmask file %q: %w", path, err), closeErr)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close bitmask file %q: %w", path, err)
	}
	log.Println("read bitmask:", path)
	return nil
}

// Print -- print debug stats
func (m BitmaskMap) Print() {
	k := reflect.TypeOf(m)
	v := reflect.ValueOf(m)
	for i := 0; i < k.NumField(); i++ {
		key := k.Field(i).Name
		val := v.Field(i).Interface()
		fmt.Printf("%s: %v\n", key, (val.(*Bitmask)).Len())
	}
}
