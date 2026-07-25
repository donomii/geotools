package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"

	"github.com/donomii/geotools/geodata"
)

type checkReport struct {
	Features      int64            `json:"features"`
	Positions     int64            `json:"positions"`
	GeometryTypes map[string]int64 `json:"geometry_types"`
	BBox          []float64        `json:"bbox,omitempty"`
}

func checkGeoJSON(inputMode geodata.InputMode, options geodata.ValidationOptions, input io.Reader) (checkReport, error) {
	report := checkReport{GeometryTypes: make(map[string]int64)}
	hasBounds := false
	err := geodata.ReadFeatures(input, inputMode, func(feature geodata.Feature) error {
		featureNumber := report.Features + 1
		summary, err := geodata.ValidateFeature(feature, options)
		if err != nil {
			return fmt.Errorf("Feature %d with id %s failed validation: %w", featureNumber, feature.EncodedID(), err)
		}
		report.Features++
		report.Positions += summary.PositionCount
		report.GeometryTypes[summary.Type]++
		if summary.HasBounds {
			if !hasBounds {
				report.BBox = append([]float64(nil), summary.Bounds[:]...)
				hasBounds = true
			} else {
				report.BBox[0] = math.Min(report.BBox[0], summary.Bounds[0])
				report.BBox[1] = math.Min(report.BBox[1], summary.Bounds[1])
				report.BBox[2] = math.Max(report.BBox[2], summary.Bounds[2])
				report.BBox[3] = math.Max(report.BBox[3], summary.Bounds[3])
			}
		}
		return nil
	})
	return report, err
}

func runBuiltInTest() error {
	valid := bytes.NewBufferString(`{"type":"Feature","id":7,"geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]},"properties":{}}`)
	report, err := checkGeoJSON(geodata.InputAuto, geodata.ValidationOptions{}, valid)
	if err != nil {
		return err
	}
	if report.Features != 1 || report.Positions != 4 || report.GeometryTypes["Polygon"] != 1 {
		return fmt.Errorf("validator report is %#v", report)
	}
	invalid := bytes.NewBufferString(`{"type":"Feature","geometry":{"type":"Point","coordinates":[181,0]},"properties":{}}`)
	if _, err := checkGeoJSON(geodata.InputAuto, geodata.ValidationOptions{}, invalid); err == nil {
		return fmt.Errorf("validator accepted longitude 181")
	}
	return nil
}

func main() {
	inputName := flag.String("input", "auto", "Input format: auto detects JSONL, arrays, FeatureCollections, and RFC 8142 sequences; seq requires record separators")
	allowNullGeometry := flag.Bool("allow-null-geometry", false, "Accept Features whose geometry is null; disabled by default so missing spatial data is reported")
	allowOutOfRange := flag.Bool("allow-out-of-range", false, "Accept longitude outside -180..180 or latitude outside -90..90; use only for data that is not RFC 7946 WGS 84")
	runTest := flag.Bool("test", false, "Run built-in valid and invalid geometry checks and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("geojsoncheck reads standard input and writes its JSON report to standard output; positional arguments are not accepted")
	}
	if *runTest {
		if err := runBuiltInTest(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("geojsoncheck built-in test passed")
		return
	}
	inputMode, err := geodata.ParseInputMode(*inputName)
	if err != nil {
		log.Fatal(err)
	}
	report, err := checkGeoJSON(inputMode, geodata.ValidationOptions{
		AllowNullGeometry: *allowNullGeometry,
		AllowOutOfRange:   *allowOutOfRange,
	}, os.Stdin)
	if err != nil {
		log.Fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		log.Fatalf("failed to write validation report for %d Features: %v", report.Features, err)
	}
}
