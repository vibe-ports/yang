// SPDX-License-Identifier: BSD-3-Clause

package opidx

import (
	"slices"
	"testing"
)

func FuzzSequence(f *testing.F) {
	f.Add([]byte{1, 0, 1, 0, 0, 0, 1, 0}) // insert, remove, reinsert at the same position
	f.Add([]byte{1, 1, 1, 2, 0, 1, 1, 3, 0, 0})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 512 {
			input = input[:512]
		}
		var s Seq[int]
		var want []*Handle[int]
		for i := 0; i+1 < len(input); i += 2 {
			op, key := input[i], input[i+1]
			if len(want) != 0 && op&1 == 0 {
				at := int(key) % len(want)
				h := want[at]
				if !s.Remove(h) {
					t.Fatal("remove failed")
				}
				want = slices.Delete(want, at, at+1)
			} else {
				at := int(key) % (len(want) + 1)
				h := s.InsertBefore(valueAt(want, at), i)
				want = slices.Insert(want, at, h)
			}
			checkSequence(t, &s, want)
		}
	})
}

func FuzzRanks(f *testing.F) {
	f.Add([]byte{1, 128, 0, 128, 1, 128}) // insert, remove, reinsert the same key
	f.Add([]byte{1, 0, 1, 255, 0, 0, 0, 255, 1, 0})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 1024 {
			input = input[:1024]
		}
		var r Ranks
		var model []int
		for i := 0; i+1 < len(input); i += 2 {
			op, key := input[i], input[i+1]
			v := int(key) - 128
			at := slices.Index(model, v)
			if op&1 != 0 {
				if r.Insert(v) != (at < 0) {
					t.Fatal("insert status differs")
				}
				if at < 0 {
					model = append(model, v)
				}
			} else {
				if r.Remove(v) != (at >= 0) {
					t.Fatal("remove status differs")
				}
				if at >= 0 {
					model = slices.Delete(model, at, at+1)
				}
			}
			if r.Len() != len(model) {
				t.Fatal("length differs")
			}
			for _, query := range []int{v - 1, v, v + 1, -129, 128} {
				var pred, succ int
				pok, sok := false, false
				for _, item := range model {
					if item < query && (!pok || item > pred) {
						pred, pok = item, true
					}
					if item > query && (!sok || item < succ) {
						succ, sok = item, true
					}
				}
				gotP, gotPOK := r.Predecessor(query)
				gotS, gotSOK := r.Successor(query)
				if gotPOK != pok || gotSOK != sok || pok && gotP != pred || sok && gotS != succ {
					t.Fatalf("neighbors differ for %d", query)
				}
			}
		}
	})
}

func FuzzOccurrences(f *testing.F) {
	f.Add([]byte{1, 0, 0, 0, 1, 0}) // insert, remove, reinsert the same class and position
	f.Add([]byte{1, 255, 1, 127, 0, 255, 1, 255, 0, 127})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 256 {
			input = input[:256]
		}
		var seq Seq[int]
		idx := NewOccurrences[int, int, int](&seq)
		var model []*Handle[int]
		classes := map[*Handle[int]][2]int{}
		for i := 0; i+1 < len(input); i += 2 {
			op, key := input[i], input[i+1]
			if len(model) > 0 && op&1 == 0 {
				at := int(key) % len(model)
				h := model[at]
				if !idx.Remove(h) || !seq.Remove(h) {
					t.Fatal("remove failed")
				}
				delete(classes, h)
				model = slices.Delete(model, at, at+1)
			} else {
				at := int(key) % (len(model) + 1)
				h := seq.InsertBefore(valueAt(model, at), i)
				if !idx.Insert(int(key%3), int(key%5), h) {
					t.Fatal("register failed")
				}
				classes[h] = [2]int{int(key % 3), int(key % 5)}
				model = slices.Insert(model, at, h)
			}
			for schema := range 3 {
				for key := range 5 {
					var want []*Handle[int]
					for _, h := range model {
						if classes[h] == [2]int{schema, key} {
							want = append(want, h)
						}
					}
					if idx.Len(schema, key) != len(want) || idx.First(schema, key) != valueAt(want, 0) || idx.Last(schema, key) != valueAt(want, len(want)-1) {
						t.Fatal("class endpoints differ")
					}
					for _, anchor := range []*Handle[int]{valueAt(model, 0), valueAt(model, len(model)/2), valueAt(model, len(model)-1)} {
						if anchor == nil {
							continue
						}
						var next *Handle[int]
						for _, h := range want {
							if pos, _ := seq.Compare(h, anchor); pos > 0 {
								next = h
								break
							}
						}
						if idx.Next(schema, key, anchor) != next {
							t.Fatal("next differs")
						}
					}
				}
			}
		}
	})
}
