package main

import (
    //"strings"
    "encoding/json"
    "bufio"
    "fmt"
    "os"
    "github.com/jharlap/geojson"
    "bytes"
    "encoding/binary"
)
import "flag"

func writeBytes(n float64, f *os.File ) {
    buf := new(bytes.Buffer)
    err := binary.Write(buf, binary.LittleEndian, n)
    if err != nil {
        fmt.Println("binary.Write failed:", err)
    }
    _, err = f.Write(buf.Bytes())
    //fmt.Printf("%s", buf.Bytes())
}

func writeBytesInt(n int64, f *os.File ) {
    buf := new(bytes.Buffer)
    err := binary.Write(buf, binary.LittleEndian, n)
    if err != nil {
        fmt.Println("binary.Write failed:", err)
    }
    _, err = f.Write(buf.Bytes())
    //fmt.Printf("%s", buf.Bytes())
}

func string2Bytes(s string) ([]byte, int) {
    return []byte(s), len([]byte(s))
}

func check(err error) {
    if err != nil {
        fmt.Println(err)
        os.Exit(1)
    }
}

func main() {
    var mapName = flag.String("outFile", "default_map", "Name for map file")
    var err error
    scanner := bufio.NewReader(os.Stdin)
    tagpointsFile, err := os.Create(*mapName+".tag_points")
    check(err)
    defer tagpointsFile.Close()
    pointsFile, err := os.Create(*mapName+".map_points")
    check(err)
    defer pointsFile.Close()
    pointdataFile, err := os.Create(*mapName+".map_data")
    check(err)
    defer pointdataFile.Close()
    tagcatFile, err := os.Create(*mapName+".tag_category")
    check(err)
    defer tagcatFile.Close()
    preoffsetFile, err := os.Create(*mapName+".pre_offset")
    check(err)
    defer preoffsetFile.Close()
    offsetFile, err := os.Create(*mapName+".tag_offset")
    check(err)
    defer offsetFile.Close()
    stringsFile, err := os.Create(*mapName+".tag_text")
    check(err)
    defer stringsFile.Close()
    indexFile, err := os.Create(*mapName+".tag_index")
    check(err)
    defer indexFile.Close()
    line := []byte{}
    more := false
    accum := []byte{}
    offset :=0
    for {
        line, more, err = scanner.ReadLine()
        if err != nil {
            fmt.Fprintln(os.Stderr, "Done:", err)
        }
        if more {
            accum = append(accum, line...)
        } else {
            accum = append(accum, line...)
            fmt.Printf("Line: %v\n", string(accum[:len(accum)]))
            result := geojson.Container{}
            err = json.Unmarshal(accum, &result)
            check(err)
            if result.Properties["name"] != nil {
                //fmt.Println("Parsed: ", string2Bytes(result.Properties["name"].(string)))
                //fmt.Printf("%s ", string2Bytes(result.Properties["name"].(string)))
                str := result.Properties["name"].(string)
                //str = strings.Replace(str, "\"", "\\\"", -1)
                outBytes, blength := string2Bytes(str)
                _, err = stringsFile.Write(outBytes)
                check(err)
                writeBytesInt(int64(offset), offsetFile)
                _, err = preoffsetFile.Write([]byte(fmt.Sprintf("%v\n", offset)))
                offset+=blength

                writeBytes(result.Geometry.Point[0], tagpointsFile)
                writeBytes(result.Geometry.Point[1], tagpointsFile)
                writeBytesInt(0, indexFile)
                writeBytesInt(0, tagcatFile)
            } else {
                writeBytes(result.Geometry.Point[0], pointsFile)
                writeBytes(result.Geometry.Point[1], pointsFile)
                writeBytes(0, pointdataFile)
                writeBytes(0, pointdataFile)
                writeBytes(0, pointdataFile)
            }
            accum = []byte{}
        }
    }
}
