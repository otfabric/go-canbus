// SPDX-License-Identifier: MIT

package dbc

import "os"

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }
