// SPDX-License-Identifier: BSD-3-Clause

// Command zgen enumerates the zero-byte permutation from libyang v5.8.6
// src/hash_table.c:53-57 and writes zcycles.bin (go generate ./internal/types/).
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
)

const stride = uint32(65536)

type cycle struct {
	length uint64
	states []uint32
}

type counts struct {
	cycles, states, checkpoints uint64
}

// enumerate visits starts in increasing order; each recorded cycle starts at its
// smallest state. Multiplication by odd 1025 and x ^= x >> 6 are both invertible.
// bits < 32 uses the same algorithm on the smaller permutation for self-checks.
func enumerate(bits uint, block uint32) ([]cycle, counts) {
	size := uint64(1) << bits
	mask := uint32(size - 1) //nolint:gosec // bits is in [1, 32]
	visited := make([]uint64, (size+63)/64)
	var cycles []cycle
	var stats counts
	for s := uint64(0); s < size; s++ {
		if visited[s/64]&(uint64(1)<<(s%64)) != 0 {
			continue
		}
		c := cycle{}
		h := uint32(s)
		for {
			visited[h/64] |= uint64(1) << (h % 64)
			if c.length%uint64(block) == 0 {
				c.states = append(c.states, h)
			}
			c.length++
			t := (1025 * h) & mask
			h = t ^ (t >> 6)
			if uint64(h) == s {
				break
			}
		}
		stats.cycles++
		stats.states += c.length
		if c.length >= uint64(block) {
			cycles = append(cycles, c)
			stats.checkpoints += uint64(len(c.states))
		}
	}
	return cycles, stats
}

func encode(cycles []cycle, block uint32) []byte {
	data := []byte("ZCY1")
	data = binary.LittleEndian.AppendUint32(data, block)
	data = binary.LittleEndian.AppendUint32(data, uint32(len(cycles))) //nolint:gosec // at most 2^32 / B cycles
	for _, c := range cycles {
		data = binary.LittleEndian.AppendUint64(data, c.length)
		data = binary.LittleEndian.AppendUint32(data, uint32(len(c.states))) //nolint:gosec // at most 2^32 / B checkpoints
		for _, h := range c.states {
			data = binary.LittleEndian.AppendUint32(data, h)
		}
	}
	return data
}

func run(bits uint, output string) error {
	if bits < 1 || bits > 32 {
		return fmt.Errorf("bits must be in [1, 32]")
	}
	cycles, stats := enumerate(bits, stride)
	data := encode(cycles, stride)
	if bits < 32 {
		// Exercise the same enumeration twice on small state spaces to check determinism.
		again, _ := enumerate(bits, stride)
		if stats.states != uint64(1)<<bits || !bytes.Equal(data, encode(again, stride)) {
			return fmt.Errorf("cycle self-check failed")
		}
	}
	if err := os.WriteFile(output, data, 0o644); err != nil { //nolint:gosec // generated public lookup table
		return err
	}
	fmt.Fprintf(os.Stderr, "sha256 %x; states %d; cycles %d; recorded %d; checkpoints %d\n",
		sha256.Sum256(data), stats.states, stats.cycles, len(cycles), stats.checkpoints)
	return nil
}

func main() {
	output := flag.String("o", "zcycles.bin", "output file")
	bits := flag.Uint("bits", 32, "state width (16 for a small self-check)")
	flag.Parse()
	if err := run(*bits, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
