package resourcemanager

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtual-kubelet/virtual-kubelet/errdefs"
	"k8s.io/apimachinery/pkg/types"
)

func key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: "default", Name: name}
}

func TestSlotPoolHandsOutDistinctNumberedSlots(t *testing.T) {
	p := newSlotPool(2)
	ctx := context.Background()

	a, err := p.acquire(ctx, key("a"), nil)
	require.NoError(t, err)
	b, err := p.acquire(ctx, key("b"), nil)
	require.NoError(t, err)

	assert.ElementsMatch(t, []int{1, 2}, []int{a, b})
}

func TestSlotPoolBlocksWhenFullAndReusesReleasedSlot(t *testing.T) {
	p := newSlotPool(2)
	ctx := context.Background()
	a, _ := p.acquire(ctx, key("a"), nil)
	_, _ = p.acquire(ctx, key("b"), nil)

	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err := p.acquire(waitCtx, key("c"), nil)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	released, ok := p.release(key("a"))
	require.True(t, ok)
	assert.Equal(t, a, released)

	c, err := p.acquire(ctx, key("c"), nil)
	require.NoError(t, err)
	assert.Equal(t, a, c)
}

func TestSlotPoolReleaseIsIdempotent(t *testing.T) {
	p := newSlotPool(2)
	ctx := context.Background()
	_, _ = p.acquire(ctx, key("a"), nil)
	b, _ := p.acquire(ctx, key("b"), nil)

	_, ok := p.release(key("a"))
	require.True(t, ok)
	_, ok = p.release(key("a"))
	assert.False(t, ok, "second release of the same VM must not free another slot")
	_, ok = p.release(key("never"))
	assert.False(t, ok)

	// Exactly one slot is free: b's slot must still be held.
	_, err := p.acquire(ctx, key("c"), nil)
	require.NoError(t, err)
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = p.acquire(waitCtx, key("d"), nil)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	released, ok := p.release(key("b"))
	require.True(t, ok)
	assert.Equal(t, b, released)
}

func TestSlotPoolFailedClaimReturnsSlot(t *testing.T) {
	p := newSlotPool(1)
	ctx := context.Background()

	_, err := p.acquire(ctx, key("gone"), func(int) bool { return false })
	assert.True(t, errdefs.IsNotFound(err))

	slot, err := p.acquire(ctx, key("a"), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, slot)
}

func TestSlotPoolRejectsSecondAcquireForSameVM(t *testing.T) {
	p := newSlotPool(2)
	ctx := context.Background()
	_, err := p.acquire(ctx, key("a"), nil)
	require.NoError(t, err)

	_, err = p.acquire(ctx, key("a"), nil)
	assert.True(t, errdefs.IsInvalidInput(err))

	// The rejected attempt must not have kept the second slot.
	_, err = p.acquire(ctx, key("b"), nil)
	require.NoError(t, err)
}

func TestSlotPoolConcurrentUseNeverSharesASlot(t *testing.T) {
	p := newSlotPool(2)
	ctx := context.Background()

	var mu sync.Mutex
	inUse := map[int]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := key(string(rune('a'+i%26)) + string(rune('0'+i/26)))
			slot, err := p.acquire(ctx, k, nil)
			if !assert.NoError(t, err) {
				return
			}
			mu.Lock()
			assert.False(t, inUse[slot], "slot %d handed out twice", slot)
			inUse[slot] = true
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			inUse[slot] = false
			mu.Unlock()
			p.release(k)
		}(i)
	}
	wg.Wait()
}

func TestSlotDiskPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "slot-2.img"), nil, 0o600))

	assert.Equal(t, "", slotDiskPath(ctx, "", 2), "no annotation, no disk")
	assert.Equal(t, filepath.Join(dir, "slot-2.img"), slotDiskPath(ctx, dir, 2))
	assert.Equal(t, "", slotDiskPath(ctx, dir, 1), "missing image starts the VM without it")
}
