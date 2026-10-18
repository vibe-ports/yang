// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/{date,time,xpath1.0,
// node_instanceid,instanceid_keys}.c (LY_VALUE_LYB print callbacks)
// (BSD-3-Clause, © CESNET).

package types

import "encoding/binary"

// remainingPluginLYB returns the LY_VALUE_LYB payloads of the registered plugins whose
// representations are not shared with a built-in type or an inet plugin.
func remainingPluginLYB(v Value, id string) (lybRepresentation, bool) {
	switch id {
	case "xpath1.0", "node-instance-identifier", "instance-identifier-keys":
		// These callbacks explicitly return the canonical cache for LYB, including
		// text retained by cross-context duplication; they do not regenerate it.
		return stringLYB(v.canon), true
	case "date":
		dv, ok := v.ext.(*dateVal)
		if !ok {
			return lybRepresentation{}, false
		}
		r := uintLYB(uint64(dv.unix), 64) //nolint:gosec // signed time_t's LYB bit pattern
		if dv.unknownTZ {
			r.data = append(r.data, 1)
			r.bits = 72
		}
		return r, true
	case "time":
		tv, ok := v.ext.(*timeVal)
		if !ok {
			return lybRepresentation{}, false
		}
		var seconds [4]byte
		binary.LittleEndian.PutUint32(seconds[:], tv.seconds)
		r := lybRepresentation{data: seconds[:], bits: 32}
		if tv.unknownTZ || tv.hasFrac {
			// D-0130: time.c:387-399 leaves byte 4 uninitialized for
			// fractional time-no-zone. Keep its hash deterministic with zero.
			r.data = append(r.data, byte(0))
			if tv.unknownTZ {
				r.data[4] = 1
			}
			r.data = append(r.data, tv.frac...)
			r.bits = byteCount(len(r.data)) * 8
		}
		return r, true
	default:
		// Every plugin registered by this package is handled here or by pluginLYB. Keep an
		// unknown future registration unequal until its exact callback is ported.
		return lybRepresentation{}, false
	}
}
