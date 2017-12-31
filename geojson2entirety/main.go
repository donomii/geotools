package main

import (
	"log"
	//"strings"
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jharlap/geojson"
)
import "flag"

func writeBytes(n float64, f *os.File) {
	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.LittleEndian, n)
	if err != nil {
		log.Println("binary.Write failed:", err)
	}
	_, err = f.Write(buf.Bytes())
	//fmt.Printf("%s", buf.Bytes())
}

func writeBytesInt(n int64, f *os.File) {
	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.LittleEndian, n)
	if err != nil {
		log.Println("binary.Write failed:", err)
	}
	_, err = f.Write(buf.Bytes())
	//fmt.Printf("%s", buf.Bytes())
}

func string2Bytes(s string) ([]byte, int) {
	b := []byte(s)
	b = append(b, []byte{0}...)
	l := len(b)
	return b, l
}

func check(err error) {
	if err != nil {
		log.Println(err)
	}
}

func checkJSONerr(err error, js []byte) {
	if err != nil {
		log.Println(err)
		log.Println(string(js))
	}
}

func unpackJSON(accum []byte) (geojson.Container, error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Caught error in unpackJSON ", r)
			//return geojson.Container{}, nil
		}
	}()
	result := geojson.Container{}
	err := json.Unmarshal(accum, &result)
	checkJSONerr(err, accum)
	if err != nil {
		return geojson.Container{}, err
	}
	return result, nil
}

func writeTag(str string, long, lat float64, tagpointsFile, offsetFile, indexFile, tagcatFile, stringsFile, preoffsetFile *os.File, indexCount, offset int64) int64 {

	//fmt.Println("Parsed: ", string2Bytes(result.Properties["name"].(string)))
	//fmt.Printf("%s ", string2Bytes(result.Properties["name"].(string)))

	//str = strings.Replace(str, "\"", "\\\"", -1)
	if verbose {
		log.Println("Adding tag ", indexCount, ": ", str, " at ", lat, ",", long, " at offset ", offset)
	}
	outBytes, blength := string2Bytes(str)
	wrote, err := stringsFile.Write(outBytes)
	if wrote != blength {
		panic("Written length is different from string length!")
	}
	check(err)
	writeBytesInt(int64(offset), offsetFile)
	_, err = preoffsetFile.Write([]byte(fmt.Sprintf("%v\n", offset)))

	writeBytes(lat, tagpointsFile)
	writeBytes(long, tagpointsFile)
	writeBytesInt(indexCount-1, indexFile)
	writeBytesInt(0, tagcatFile)
	return int64(blength)
}

var verbose bool

func main() {
	var mapName = flag.String("outFile", "default_map", "Name for map file")
	var limit = flag.Int64("limit", -1, "Limit the number of records imported")
	verbose = *flag.Bool("verbose", false, "Print progress")
	//var skip = flag.Int("skip", -1, "Skip every nth record")

	flag.Parse()
	var err error
	scanner := bufio.NewReader(os.Stdin)
	tagpointsFile, err := os.Create(*mapName + ".tag_points")
	check(err)
	defer tagpointsFile.Close()
	pointsFile, err := os.Create(*mapName + ".map_points")
	check(err)
	defer pointsFile.Close()
	pointdataFile, err := os.Create(*mapName + ".map_data")
	check(err)
	defer pointdataFile.Close()
	tagcatFile, err := os.Create(*mapName + ".tag_category")
	check(err)
	defer tagcatFile.Close()
	preoffsetFile, err := os.Create(*mapName + ".pre_offset")
	check(err)
	defer preoffsetFile.Close()
	offsetFile, err := os.Create(*mapName + ".tag_offset")
	check(err)
	defer offsetFile.Close()
	stringsFile, err := os.Create(*mapName + ".tag_text")
	check(err)
	defer stringsFile.Close()
	indexFile, err := os.Create(*mapName + ".tag_index")
	check(err)
	defer indexFile.Close()
	line := []byte{}
	more := false
	accum := []byte{}
	offset := int64(0)
	count := int64(0)
	indexCount := int64(0)
	offset += writeTag("FAIL", -60000, -6000, tagpointsFile, offsetFile, indexFile, tagcatFile, stringsFile, preoffsetFile, indexCount, offset)
	for {

		line, more, err = scanner.ReadLine()
		if err != nil {
			log.Printf("Imported %d records\n", count)
			log.Println("Done.  Finished map: ", *mapName)
			os.Exit(0)
		}
		accum = append(accum, line...)
		if more {
			//accum = append(accum, line...)
		} else {
			if *limit > -1 && count > *limit {
				log.Printf("Finishing import early after %d records for %v", count, *mapName)
				os.Exit(0)
			}

			//accum = append(accum, line...)
			//fmt.Printf("Line: %v\n", string(accum[:len(accum)]))
			result, err := unpackJSON(accum)
			check(err)
			if err == nil {

				/*if *skip > -1 {
				if count >= *skip {
					count = 0
				} else {*/
				if result.Properties["name"] != nil && len(result.Properties["name"].(string)) > 1 {
					count = count + 1
					indexCount += 1
					str := result.Properties["name"].(string)
					offset += writeTag(str, result.Geometry.Point[1]*-60, result.Geometry.Point[0]*60, tagpointsFile, offsetFile, indexFile, tagcatFile, stringsFile, preoffsetFile, indexCount, offset)
				} else {
					if verbose {
						fmt.Println("Adding point without tag at ", result.Geometry.Point)
					}
					writeBytes(result.Geometry.Point[0]*60, pointsFile)
					writeBytes(result.Geometry.Point[1]*-60, pointsFile)
					writeBytes(0, pointdataFile)
					writeBytes(0, pointdataFile)
					writeBytes(0, pointdataFile)
				}
				accum = []byte{}
			} else {
				accum = []byte{}
			}
		}
	}

	log.Println("Job's a good'un, boss!")
}
