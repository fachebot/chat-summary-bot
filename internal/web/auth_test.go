package web

import (
	"testing"
	"time"
)

func TestAuthInputSetBeforeWait(t *testing.T) {
	in := newAuthInput()
	in.Set("123")
	v, ok := in.Wait(make(chan struct{}))
	if !ok || v != "123" {
		t.Errorf("Wait after Set = (%q, %v), want (\"123\", true)", v, ok)
	}
}

func TestAuthInputWaitThenSet(t *testing.T) {
	in := newAuthInput()
	done := make(chan struct{})
	got := make(chan string, 1)
	go func() {
		v, _ := in.Wait(done)
		got <- v
	}()

	select {
	case <-got:
		t.Fatalf("Wait returned before Set")
	case <-time.After(20 * time.Millisecond):
	}

	in.Set("abc")
	select {
	case v := <-got:
		if v != "abc" {
			t.Errorf("got %q, want \"abc\"", v)
		}
	case <-time.After(time.Second):
		t.Fatalf("Wait did not return after Set")
	}
}

func TestAuthInputWaitCancelledByDone(t *testing.T) {
	in := newAuthInput()
	done := make(chan struct{})
	closed := make(chan bool, 1)
	go func() {
		_, ok := in.Wait(done)
		closed <- ok
	}()

	close(done)
	select {
	case ok := <-closed:
		if ok {
			t.Errorf("Wait should return false when done closed")
		}
	case <-time.After(time.Second):
		t.Fatalf("Wait did not return after done closed")
	}
}

func TestAuthInputSetNonBlocking(t *testing.T) {
	in := newAuthInput()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			in.Set("x")
		}
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Set should never block")
	}

	v, ok := in.Wait(make(chan struct{}))
	if !ok || v != "x" {
		t.Errorf("Wait after Sets = (%q, %v), want (\"x\", true)", v, ok)
	}
}
