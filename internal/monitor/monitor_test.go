package monitor

import (
	"testing"

	"dmetric.dalang.io/internal/model"
)

func TestAckDoesNotMutateSharedSnapshot(t *testing.T) {
	store := &fakeStore{}
	m := &Monitor{
		engine: NewAlertEngine(store, model.DefaultThresholds()),
		latest: model.Snapshot{Alerts: []model.Alert{{ID: 7}}},
	}
	before := m.Latest()
	if err := m.Ack(7); err != nil {
		t.Fatal(err)
	}
	if before.Alerts[0].Acked {
		t.Fatal("a snapshot taken before Ack was mutated")
	}
	after := m.Latest()
	if !after.Alerts[0].Acked {
		t.Fatal("latest snapshot was not marked acknowledged")
	}
}

func TestApplyNetworkInterfaces(t *testing.T) {
	m := &Monitor{networkInterfaces: []string{"en0"}}
	snap := model.Snapshot{
		Network: model.NetworkStat{
			Interfaces: []model.NetInterfaceStat{
				{Name: "en0", RxBytesPerSec: 100, TxBytesPerSec: 10, RxBytesTotal: 1000, TxBytesTotal: 100},
				{Name: "utun0", RxBytesPerSec: 900, TxBytesPerSec: 90, RxBytesTotal: 9000, TxBytesTotal: 900},
			},
		},
	}
	m.applyNetworkInterfaces(&snap)
	if !snap.Network.Interfaces[0].Selected || snap.Network.Interfaces[1].Selected {
		t.Fatalf("unexpected selection: %+v", snap.Network.Interfaces)
	}
	if snap.Network.RxBytesPerSec != 100 || snap.Network.TxBytesPerSec != 10 {
		t.Fatalf("aggregate not filtered: %+v", snap.Network)
	}
	if snap.Network.RxBytesTotal != 1000 || snap.Network.TxBytesTotal != 100 {
		t.Fatalf("totals not filtered: %+v", snap.Network)
	}

	m.SetNetworkInterfaces(nil)
	m.applyNetworkInterfaces(&snap)
	if !snap.Network.Interfaces[0].Selected || !snap.Network.Interfaces[1].Selected {
		t.Fatalf("empty selection should select all: %+v", snap.Network.Interfaces)
	}
}
