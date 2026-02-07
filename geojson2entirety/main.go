package main

import (
	"log"

	//"github.com/tchap/go-patricia/patricia"

	//"strings"
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"

	"flag"

	"github.com/jharlap/geojson"
)

func writeBytes(n float64, f *bufio.Writer) {
	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.LittleEndian, n)
	if err != nil {
		log.Println("binary.Write failed:", err)
	}
	_, err = f.Write(buf.Bytes())
	//fmt.Printf("%s", buf.Bytes())
}

func writeBytesInt(n int64, f *bufio.Writer) {
	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.LittleEndian, n)
	if err != nil {
		log.Println("binary.Write failed:", err)
	}
	_, err = f.Write(buf.Bytes())
	fmt.Printf("%s", buf.Bytes())
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

func checkJSONerr(err error, js string) {
	if err != nil {
		log.Println(err)
		log.Println(string(js))
	}
}

func unpackJSON(accum string) (geojson.Container, error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Caught error in unpackJSON ", r)
			//return geojson.Container{}, nil
		}
	}()
	result := geojson.Container{}
	err := json.Unmarshal([]byte(accum), &result)
	checkJSONerr(err, accum)
	if err != nil {
		return geojson.Container{}, err
	}
	return result, nil
}

func writeTag(str string, long, lat float64, tagpointsFile, offsetFile, indexFile, tagcatFile, stringsFile, preoffsetFile *bufio.Writer, indexCount, offset int64) int64 {
	//treeIndexAdd2(str, long, lat)
	//fmt.Println("Parsed: ", string2Bytes(result.Properties["name"].(string)))
	//fmt.Printf("%s ", string2Bytes(result.Properties["name"].(string)))

	//str = strings.Replace(str, "\"", "\\\"", -1)
	if verbose {
		// log.Println("Adding tag ", indexCount, ": ", str, " at ", lat, ",", long, " at offset ", offset)
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

func openFile(mapname string) (*os.File, *bufio.Writer) {
	f, err := os.Create(mapname)
	check(err)
	//defer tagpointsFile.Close()
	w := bufio.NewWriterSize(f, 10*1024*1024)
	return f, w
}

func main() {
	var mapName = flag.String("outFile", "default_map", "Name for map file")
	var limit = flag.Int64("limit", -1, "Limit the number of records imported")
	var pointsOnly = flag.Bool("points", false, "Only save data point")
	var tagsOnly = flag.Bool("tags", false, "Only save tags(named points)")
	flag.BoolVar(&verbose, "verbose", false, "Print progress")
	//var skip = flag.Int("skip", -1, "Skip every nth record")

	flag.Parse()

	if *tagsOnly {
		log.Println("Not writing points")
	}
	if *pointsOnly {
		log.Println("Not writing tags")
	}

	log.Println("Reading from stdin")
	scanner := bufio.NewScanner(os.Stdin)

	tp_handle, tagpointsFile := openFile(*mapName + ".tag_points")

	pf_handle, pointsFile := openFile(*mapName + ".map_points")

	pd_handle, pointdataFile := openFile(*mapName + ".map_data")

	tg_handle, tagcatFile := openFile(*mapName + ".tag_category")

	po_handle, preoffsetFile := openFile(*mapName + ".pre_offset")

	of_handle, offsetFile := openFile(*mapName + ".tag_offset")

	str_handle, stringsFile := openFile(*mapName + ".tag_text")

	in_handle, indexFile := openFile(*mapName + ".tag_index")

	offset := int64(0)
	count := int64(0)
	indexCount := int64(0)
	offset += writeTag("FAIL", -60000, -6000, tagpointsFile, offsetFile, indexFile, tagcatFile, stringsFile, preoffsetFile, indexCount, offset)
	for scanner.Scan() {

		line := scanner.Text()
		if *limit > -1 && count > *limit {
			log.Printf("Finishing import early after %d records for %v", count, *mapName)
			//buildFinal()
			break
		}
		//fmt.Println("Parsing line: ", line)
		result, err := unpackJSON(line)
		if err != nil {
			log.Println("Error unpacking JSON: ", err)
			log.Printf("Invalid json '%v'\n", string(line))
		}
		if err == nil {

			//Dump result struct
			//fmt.Printf("%+v\n", result)

			if result.Properties["name"] != nil && len(result.Properties["name"].(string)) > 1 {
				count = count + 1
				str := result.Properties["name"].(string)
				if !*pointsOnly {
					indexCount = indexCount + 1
					if verbose {
						// fmt.Printf("Adding tag %d: %s at %v, %v at offset %v\n", indexCount, str, result.Geometry.Point[0]*-60, result.Geometry.Point[1]*60, offset)
					}
					offset += writeTag(str, result.Geometry.Point[0]*-60, result.Geometry.Point[1]*60, tagpointsFile, offsetFile, indexFile, tagcatFile, stringsFile, preoffsetFile, indexCount, offset)
				}
			} else {
				if !*tagsOnly {
					if verbose {
						// fmt.Println("Adding point without tag at ", result.Geometry.Point)
					}
					//treeIndexAdd2("", result.Geometry.Point[1]*-60, result.Geometry.Point[0]*60)
					writeBytes(result.Geometry.Point[1]*60, pointsFile)
					writeBytes(result.Geometry.Point[0]*-60, pointsFile)
					writeBytes(0, pointdataFile)
					writeBytes(0, pointdataFile)
					writeBytes(0, pointdataFile)
				}
			}
		}
	}
	tagpointsFile.Flush()
	tp_handle.Close()
	pointsFile.Flush()
	pf_handle.Close()
	pointdataFile.Flush()
	pd_handle.Close()
	tagcatFile.Flush()
	tg_handle.Close()
	preoffsetFile.Flush()
	po_handle.Close()
	offsetFile.Flush()
	of_handle.Close()
	stringsFile.Flush()
	str_handle.Close()
	indexFile.Flush()
	in_handle.Close()
	//buildFinal()
	log.Println("Job's a good'un, boss!")
}
