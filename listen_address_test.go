package main

import (
	"testing"
)

func TestResolveListenAddressesUsesConfigByDefault(t *testing.T) {
	addrs := resolveListenAddresses([]string{"127.0.0.1:9000"}, nil)
	if len(addrs) != 1 || addrs[0] != "127.0.0.1:9000" {
		t.Fatalf("expected config address, got %#v", addrs)
	}

	addrs = resolveListenAddresses(nil, nil)
	if len(addrs) != 1 || addrs[0] != ":28088" {
		t.Fatalf("expected fallback address, got %#v", addrs)
	}
}

func TestResolveListenAddressesUsesRepeatedFlags(t *testing.T) {
	addrs := resolveListenAddresses([]string{":28088"}, []string{"127.0.0.1:28088", "0.0.0.0:28088", "127.0.0.1:28088"})
	want := []string{"127.0.0.1:28088", "0.0.0.0:28088"}
	if len(addrs) != len(want) {
		t.Fatalf("expected %d addresses, got %#v", len(want), addrs)
	}
	for i := range want {
		if addrs[i] != want[i] {
			t.Fatalf("expected addresses %#v, got %#v", want, addrs)
		}
	}
}
