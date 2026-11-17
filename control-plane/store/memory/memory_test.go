package memory_test

import (
	"testing"

	"nexostream/control-plane/store/memory"
	"nexostream/control-plane/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, memory.New())
}
