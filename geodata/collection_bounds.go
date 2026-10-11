package geodata

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

const collectionLongitudeBufferSize = 8192

type collectionBounds struct {
	summary    GeometrySummary
	dimension  int
	bbox       []float64
	longitudes []float64
	file       *os.File
}

func (bounds *collectionBounds) setBBox(raw json.RawMessage) error {
	dimension, values, err := validateBBox(raw)
	if err != nil {
		return fmt.Errorf("FeatureCollection bbox is invalid: %w", err)
	}
	bounds.dimension = dimension
	bounds.bbox = values
	return nil
}

func (bounds *collectionBounds) add(feature Feature) error {
	options := ValidationOptions{AllowNullGeometry: true, AllowOutOfRange: true, visitLongitude: bounds.addLongitude}
	summary, err := ValidateFeature(feature, options)
	if err != nil {
		return err
	}
	mergeGeometrySummary(&bounds.summary, summary)
	return nil
}

func (bounds *collectionBounds) addLongitude(longitude float64) error {
	if bounds.bbox != nil {
		return bounds.checkLongitude(longitude)
	}
	// A trailing bbox may wrap at any longitude; min/max alone cannot validate it.
	bounds.longitudes = append(bounds.longitudes, longitude)
	if len(bounds.longitudes) < collectionLongitudeBufferSize {
		return nil
	}
	if bounds.file == nil {
		file, err := os.CreateTemp("", "geotools-collection-longitudes-*")
		if err != nil {
			return fmt.Errorf("failed to retain longitudes for a possible trailing collection bbox: %w", err)
		}
		bounds.file = file
	}
	if err := binary.Write(bounds.file, binary.LittleEndian, bounds.longitudes); err != nil {
		return fmt.Errorf("failed to retain collection longitudes in %q: %w", bounds.file.Name(), err)
	}
	bounds.longitudes = bounds.longitudes[:0]
	return nil
}

func (bounds *collectionBounds) checkLongitude(longitude float64) error {
	if bounds.dimension == 0 || bounds.bbox[0] <= bounds.bbox[bounds.dimension] {
		return nil
	}
	west, east := bounds.bbox[0], bounds.bbox[bounds.dimension]
	if longitude < west && longitude > east {
		return fmt.Errorf("longitude %v is outside FeatureCollection bbox %v; expected %v through 180 or -180 through %v",
			longitude, bounds.bbox, west, east)
	}
	return nil
}

func (bounds *collectionBounds) finish(raw json.RawMessage) error {
	if err := bounds.setBBox(raw); err != nil {
		return err
	}
	if bounds.bbox == nil || !bounds.summary.HasBounds {
		return nil
	}
	if err := validateSummaryBBox(bounds.summary, bounds.dimension, bounds.bbox); err != nil {
		return fmt.Errorf("FeatureCollection bbox is invalid: %w", err)
	}
	if bounds.bbox[0] <= bounds.bbox[bounds.dimension] {
		return nil
	}
	for _, longitude := range bounds.longitudes {
		if err := bounds.checkLongitude(longitude); err != nil {
			return err
		}
	}
	if bounds.file == nil {
		return nil
	}
	if _, err := bounds.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to rewind collection longitude file %q: %w", bounds.file.Name(), err)
	}
	reader := bufio.NewReader(bounds.file)
	var encoded [8]byte
	for {
		_, err := io.ReadFull(reader, encoded[:])
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to read retained collection longitude from %q: %w", bounds.file.Name(), err)
		}
		if err := bounds.checkLongitude(math.Float64frombits(binary.LittleEndian.Uint64(encoded[:]))); err != nil {
			return err
		}
	}
}

func (bounds *collectionBounds) close() error {
	if bounds.file == nil {
		return nil
	}
	path := bounds.file.Name()
	if err := errors.Join(bounds.file.Close(), os.Remove(path)); err != nil {
		return fmt.Errorf("failed to clean up collection longitude file %q: %w", path, err)
	}
	return nil
}
