package main

import (
	"fmt"
	"log"
	"sort"
)

var tree map[int]interface{}
var mp mapPack

func treeIndexAdd(str string, long, lat float64) {

	if tree == nil {
		tree = make(map[int]interface{})
	}

	l1 := int(long)
	l2 := int((long - float64(l1)) * 1000000)

	l3 := int(lat)
	l4 := int((lat - float64(l3)) * 1000000)

	m2, ok := tree[l1]
	if !ok {
		m2 = make(map[int]interface{})
		tree[l1] = m2
	}

	m3, ok := m2.(map[int]interface{})[l2]
	if !ok {
		m3 = make(map[int]interface{})
		m2.(map[int]interface{})[l2] = m3
	}

	m4, ok := m3.(map[int]interface{})[l3]
	if !ok {
		m4 = make(map[int]interface{})
		m3.(map[int]interface{})[l3] = m4
	}

	m4.(map[int]interface{})[l4] = leaf{Pos: int32(l4), Text: str, Latitude: lat, Longitude: long}

	log.Println("Added ", str)

	//log.Printf("%+v\n", tree)
	//jsonString, err := json.MarshalIndent(tree, "", "  ")
	//fmt.Println(err)
	//log.Println(string(jsonString))

}

func dumpMap(mp mapPack) {
	for i1 := 0; i1 < len(mp.L1)-1; i1 += 1 {
		m2start := mp.L1[i1].Index
		m2end := mp.L1[i1].Index + 1
		//log.Printf("1. Dumping from %v to %v\n", m2start, m2end)
		for i2 := m2start; i2 < m2end; i2 += 1 {
			m3start := mp.L2[i2].Index
			m3end := mp.L2[i2].Index + 1
			//			log.Printf("2. Dumping from %v to %v\n", m3start, m3end)
			for i3 := m3start; i3 < m3end; i3 += 1 {
				m4start := mp.L3[i3].Index
				m4end := mp.L3[i3].Index + 1
				for i4 := m4start; i4 < m4end; i4 += 1 {
					fmt.Printf("%v,%v - %v\n", float64(mp.L1[i1].Key)+float64(mp.L2[i2].Key)/1000000, float64(mp.L3[i3].Key)+float64(mp.L4[i4].Pos)/1000000, mp.L4[i4])
				}

			}

		}
	}
}

func IterateMp(mp mapPack, f func(float64, float64, leaf)) {
	for i1 := 0; i1 < len(mp.L1)-1; i1 += 1 {
		m2start := mp.L1[i1].Index
		m2end := mp.L1[i1].Index + 1
		//log.Printf("1. Dumping from %v to %v\n", m2start, m2end)
		for i2 := m2start; i2 < m2end; i2 += 1 {
			m3start := mp.L2[i2].Index
			m3end := mp.L2[i2].Index + 1
			//			log.Printf("2. Dumping from %v to %v\n", m3start, m3end)
			for i3 := m3start; i3 < m3end; i3 += 1 {
				m4start := mp.L3[i3].Index
				m4end := mp.L3[i3].Index + 1
				for i4 := m4start; i4 < m4end; i4 += 1 {
					f(float64(mp.L1[i1].Key)+float64(mp.L2[i2].Key)/1000000, float64(mp.L3[i3].Key)+float64(mp.L4[i4].Pos)/1000000, mp.L4[i4])
					//fmt.Printf(mp.l1[i1].key, mp.l2[i2].key, mp.l3[i3].key, mp.l4[i4].pos, mp.l4[i4])
				}

			}

		}
	}
}

type mapPack struct {
	L1, L2, L3 []node
	L4         []leaf
}
type node struct {
	Key   int32
	Index int32
}

type leaf struct {
	Pos       int32
	Text      string
	Latitude  float64
	Longitude float64
}

func buildFinal() {

	m1i := []node{}
	m2i := []node{}
	m3i := []node{}
	m4i := []leaf{}

	//m1last := 0
	m2last := 0
	m3last := 0
	m4last := 0

	for _, k1 := range mapKeys(tree) {
		m1i = append(m1i, node{int32(k1), int32(m2last)})

		m2 := tree[k1].(map[int]interface{})
		m1keys := mapKeys(m2)
		for _, k2 := range m1keys {
			m2last += 1
			m2i = append(m2i, node{int32(k2), int32(m3last)})

			m3 := m2[k2].(map[int]interface{})
			m2keys := mapKeys(m3)
			for _, k3 := range m2keys {
				m3last += 1
				m3i = append(m3i, node{int32(k3), int32(m4last)})

				m4 := m3[k3].(map[int]interface{})
				m3keys := mapKeys(m4)
				for _, k4 := range m3keys {
					m4last += 1
					m4i = append(m4i, m4[k4].(leaf))

				}

			}

		}

	}
	m3i = append(m3i, node{int32(0), int32(m4last)})
	m2i = append(m2i, node{int32(0), int32(m3last)})
	m1i = append(m1i, node{int32(0), int32(m2last)})
	/*log.Printf("Keys %v\n", m1i)
	log.Printf("2Keys %v\n", m2i)
	log.Printf("3Keys %v\n", m3i)
	log.Printf("leafs %v\n", m4i)
	*/

	mp.L1 = m1i
	mp.L2 = m2i
	mp.L3 = m3i
	mp.L4 = m4i

	dumpMap(mp)
}
func mapKeys(mymap map[int]interface{}) []int {
	keys := make([]int, len(mymap))

	i := 0
	for k := range mymap {
		keys[i] = k
		i++
	}

	sort.Ints(keys)
	return keys
}
