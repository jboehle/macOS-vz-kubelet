package resourcemanager

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	vmdata "github.com/agoda-com/macOS-vz-kubelet/internal/data/vm"

	"github.com/virtual-kubelet/virtual-kubelet/errdefs"
	"github.com/virtual-kubelet/virtual-kubelet/log"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// slotPool hands out the host's VM slots, numbered 1 to MaxVirtualMachines. A VM
// holds one slot from just before it is built until it is deleted, so two running
// VMs never share a number. The number lets a VM use per-slot host resources, such
// as its own persistent cache disk, without any locking between the VMs.
type slotPool struct {
	free chan int

	mu   sync.Mutex
	held map[types.NamespacedName]int
}

func newSlotPool(size int) *slotPool {
	p := &slotPool{
		free: make(chan int, size),
		held: make(map[types.NamespacedName]int, size),
	}
	for slot := 1; slot <= size; slot++ {
		p.free <- slot
	}
	return p
}

// acquire blocks until a slot is free or ctx is done. claim runs with the pool
// locked, so a concurrent release for the same key waits until claim has returned;
// when claim reports false (the VM was deleted while waiting) the slot goes back
// to the pool and acquire fails with NotFound.
func (p *slotPool) acquire(ctx context.Context, key types.NamespacedName, claim func(slot int) bool) (int, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case slot := <-p.free:
			p.mu.Lock()
			if _, ok := p.held[key]; ok {
				p.mu.Unlock()
				p.free <- slot
				return 0, errdefs.AsInvalidInput(fmt.Errorf("virtual machine already holds a slot"))
			}
			if claim != nil && !claim(slot) {
				p.mu.Unlock()
				p.free <- slot
				return 0, errdefs.NotFound("virtual machine info expired")
			}
			p.held[key] = slot
			p.mu.Unlock()
			return slot, nil
		case <-ticker.C:
			log.G(ctx).Debug("waiting for a free VM slot")
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// release returns the key's slot to the pool. It is a no-op when the key holds no
// slot, so calling it twice for the same VM is safe.
func (p *slotPool) release(key types.NamespacedName) (int, bool) {
	p.mu.Lock()
	slot, ok := p.held[key]
	if ok {
		delete(p.held, key)
	}
	p.mu.Unlock()
	if ok {
		p.free <- slot
	}
	return slot, ok
}

// slotGuard releases a slot acquired for a VM that never started. Commit marks the
// VM as started; from then on the slot is released by DeleteVirtualMachine.
type slotGuard struct {
	client    *MacOSClient
	key       types.NamespacedName
	committed atomic.Bool
	release   sync.Once
}

func (g *slotGuard) Commit() {
	g.committed.Store(true)
}

func (g *slotGuard) Release(ctx context.Context) {
	g.release.Do(func() {
		if g.committed.Load() {
			return
		}
		g.client.releaseSlot(ctx, g.key)
	})
}

// SlotEnvVar is set in every exec session of a VM to the VM's slot number.
const SlotEnvVar = "MACOS_VZ_SLOT"

// acquireSlot blocks until a VM slot is free, records it in the VM's info and adds
// SlotEnvVar to the VM's environment.
func (c *MacOSClient) acquireSlot(ctx context.Context, key types.NamespacedName) (int, *slotGuard, error) {
	slot, err := c.vmSlots.acquire(ctx, key, func(slot int) bool {
		_, found := c.vms.Update(key.Namespace, key.Name, func(info vmdata.VirtualMachineInfo) vmdata.VirtualMachineInfo {
			info.Slot = slot
			info.Resource.AddEnv(corev1.EnvVar{Name: SlotEnvVar, Value: strconv.Itoa(slot)})
			return info
		})
		return found
	})
	if err != nil {
		return 0, nil, err
	}
	log.G(ctx).WithField("slot", slot).Info("acquired VM slot")
	return slot, &slotGuard{client: c, key: key}, nil
}

func (c *MacOSClient) releaseSlot(ctx context.Context, key types.NamespacedName) {
	if slot, ok := c.vmSlots.release(key); ok {
		log.G(ctx).WithField("slot", slot).Info("released VM slot")
	}
}
