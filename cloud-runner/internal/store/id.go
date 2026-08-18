package store

import (
	"crypto/rand"
	"encoding/hex"
)

func newID(prefix string) string {
	var value [10]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(value[:])
}
