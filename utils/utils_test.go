package utils

import (
	"fmt"
	"testing"
)

func TestRandStr(t *testing.T) {
	fmt.Println(RandStringRunes(32))
}
