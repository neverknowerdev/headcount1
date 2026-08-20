package engine

import (
	"context"
	"testing"
	"time"
)

func TestRunRegistryContextWithDrainCancelsSidecar(t *testing.T) {
	registry := newRunRegistry()
	ctx, cancel := registry.contextWithDrain(context.Background())
	defer cancel()

	registry.beginDrain()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("sidecar context was not canceled when drain began")
	}
}

func TestRunRegistryContextWithDrainHonorsParentCancellation(t *testing.T) {
	registry := newRunRegistry()
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel := registry.contextWithDrain(parent)
	defer cancel()

	cancelParent()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("sidecar context did not honor parent cancellation")
	}
}
