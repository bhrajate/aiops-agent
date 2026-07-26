package bus

import (
	"testing"
	"time"
)

func TestBackoffFor(t *testing.T) {
	base := 500 * time.Millisecond
	max := 10 * time.Second
	// attempt 1 -> base;attempt 2 -> 2x;指数增长但封顶 max
	if d := backoffFor(1, base, max); d != base {
		t.Errorf("attempt1 want %v got %v", base, d)
	}
	if d := backoffFor(2, base, max); d != 2*base {
		t.Errorf("attempt2 want %v got %v", 2*base, d)
	}
	if d := backoffFor(3, base, max); d != 4*base {
		t.Errorf("attempt3 want %v got %v", 4*base, d)
	}
	// 大 attempt 必须封顶,不得溢出
	if d := backoffFor(100, base, max); d != max {
		t.Errorf("large attempt should cap at %v got %v", max, d)
	}
}
