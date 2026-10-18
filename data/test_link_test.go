// SPDX-License-Identifier: BSD-3-Clause

package data

import "testing"

func mustLink(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
