package main

import (
	"testing"
	"time"
)

func TestThird_StopAndWaitStartedAndDoubleStop(t *testing.T) {
	w := NewWorker(DefaultConfig(), nil, nil, time.Now)
	go w.Run()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		started := w.started
		w.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { w.StopAndWait(); w.StopAndWait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("started worker/double stop hung")
	}
	select {
	case <-w.done:
	default:
		t.Fatal("StopAndWait returned before Run exited")
	}
}
