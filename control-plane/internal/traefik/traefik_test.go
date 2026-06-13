package traefik

import (
	"reflect"
	"strings"
	"testing"
)

func TestLabels_NoPorts(t *testing.T) {
	if got := Labels("01HXANYZ", nil, "localhost", "public", "web", false, nil); got != nil {
		t.Fatalf("want nil for empty ports, got %v", got)
	}
	if got := Labels("01HXANYZ", []int{}, "localhost", "private", "web", false, nil); got != nil {
		t.Fatalf("want nil for empty ports, got %v", got)
	}
}

// OSS default: plain HTTP on the `web` entrypoint, no TLS label.
func TestLabels_SinglePort_HTTP(t *testing.T) {
	got := Labels("nx", []int{3000}, "localhost", "public", "web", false, nil)
	want := []string{
		"traefik.enable=true",
		"sandboxd.managed=true",
		"traefik.http.routers.s-nx-3000.rule=Host(`s-nx-3000.preview.localhost`)",
		"traefik.http.routers.s-nx-3000.entrypoints=web",
		"traefik.http.routers.s-nx-3000.priority=100",
		"traefik.http.routers.s-nx-3000.service=s-nx-3000",
		"traefik.http.services.s-nx-3000.loadbalancer.server.port=3000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected label set\ngot:  %#v\nwant: %#v", got, want)
	}
}

// Production-style: websecure entrypoint + tls=true.
func TestLabels_SinglePort_TLS(t *testing.T) {
	got := Labels("nx", []int{3000}, "example.com", "public", "websecure", true, nil)
	want := []string{
		"traefik.enable=true",
		"sandboxd.managed=true",
		"traefik.http.routers.s-nx-3000.rule=Host(`s-nx-3000.preview.example.com`)",
		"traefik.http.routers.s-nx-3000.entrypoints=websecure",
		"traefik.http.routers.s-nx-3000.priority=100",
		"traefik.http.routers.s-nx-3000.service=s-nx-3000",
		"traefik.http.services.s-nx-3000.loadbalancer.server.port=3000",
		"traefik.http.routers.s-nx-3000.tls=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected label set\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestLabels_MultiPort(t *testing.T) {
	got := Labels("01HX", []int{3000, 3001}, "example.com", "public", "web", false, nil)
	if got[0] != "traefik.enable=true" {
		t.Fatalf("first label must be enable; got %q", got[0])
	}
	// Two fixed lines (enable + managed), then 5 lines per port
	// (rule, entrypoints, priority, service, loadBalancer.server.port)
	// when TLS is off. The `service=` line pins each router to its
	// own load balancer — Traefik v3's auto-link fails when multiple
	// services live on the same container.
	if len(got) != 2+5*2 {
		t.Fatalf("want 12 labels for 2 ports (no TLS); got %d (%v)", len(got), got)
	}
	gotMap := map[string]bool{}
	for _, l := range got {
		gotMap[l] = true
	}
	for _, must := range []string{
		"traefik.http.routers.s-01HX-3000.rule=Host(`s-01HX-3000.preview.example.com`)",
		"traefik.http.routers.s-01HX-3001.rule=Host(`s-01HX-3001.preview.example.com`)",
		"traefik.http.routers.s-01HX-3000.service=s-01HX-3000",
		"traefik.http.routers.s-01HX-3001.service=s-01HX-3001",
		"traefik.http.services.s-01HX-3000.loadbalancer.server.port=3000",
		"traefik.http.services.s-01HX-3001.loadbalancer.server.port=3001",
	} {
		if !gotMap[must] {
			t.Errorf("missing expected label: %s", must)
		}
	}
}

// A private sandbox additionally references the sandbox-preview-auth@file
// forward-auth middleware on every router; a public sandbox must NOT.
func TestLabels_Private(t *testing.T) {
	priv := Labels("nx", []int{3000}, "localhost", "private", "web", false, nil)
	wantMW := "traefik.http.routers.s-nx-3000.middlewares=sandbox-preview-auth@file"
	found := false
	for _, l := range priv {
		if l == wantMW {
			found = true
		}
	}
	if !found {
		t.Fatalf("private sandbox must carry the forward-auth middleware label; got %#v", priv)
	}

	pub := Labels("nx", []int{3000}, "localhost", "public", "web", false, nil)
	for _, l := range pub {
		if l == wantMW {
			t.Fatalf("public sandbox must NOT carry the forward-auth middleware label; got %#v", pub)
		}
	}
}

func TestLabels_PerPortAuthOverridesVisibility(t *testing.T) {
	// Public sandbox + auth_ports=[3001] -> 3000 unauthenticated, 3001 gated.
	got := Labels("nx", []int{3000, 3001}, "localhost", "public", "web", false,
		map[int]bool{3001: true})
	want := []string{
		"traefik.enable=true",
		"sandboxd.managed=true",
		"traefik.http.routers.s-nx-3000.rule=Host(`s-nx-3000.preview.localhost`)",
		"traefik.http.routers.s-nx-3000.entrypoints=web",
		"traefik.http.routers.s-nx-3000.priority=100",
		"traefik.http.routers.s-nx-3000.service=s-nx-3000",
		"traefik.http.services.s-nx-3000.loadbalancer.server.port=3000",
		"traefik.http.routers.s-nx-3001.rule=Host(`s-nx-3001.preview.localhost`)",
		"traefik.http.routers.s-nx-3001.entrypoints=web",
		"traefik.http.routers.s-nx-3001.priority=100",
		"traefik.http.routers.s-nx-3001.service=s-nx-3001",
		"traefik.http.services.s-nx-3001.loadbalancer.server.port=3001",
		"traefik.http.routers.s-nx-3001.middlewares=sandbox-preview-auth@file",
	}
	assertLabelsEqual(t, got, want)
}

func TestLabels_PrivateVisibilityStillAuthenticatesAllPorts(t *testing.T) {
	// Backward compat: private visibility + nil authPorts = all ports authed.
	got := Labels("nx", []int{3000, 3001}, "localhost", "private", "web", false, nil)
	for _, want := range []string{
		"traefik.http.routers.s-nx-3000.middlewares=sandbox-preview-auth@file",
		"traefik.http.routers.s-nx-3001.middlewares=sandbox-preview-auth@file",
	} {
		found := false
		for _, l := range got {
			if l == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing label %q in %v", want, got)
		}
	}
}

func TestLabels_NilAuthPortsIsBackwardCompatible(t *testing.T) {
	// nil authPorts + public visibility = no auth labels at all.
	got := Labels("nx", []int{3000}, "localhost", "public", "web", false, nil)
	for _, l := range got {
		if strings.Contains(l, "middlewares=") {
			t.Errorf("unexpected middleware label in public sandbox: %s", l)
		}
	}
}

func assertLabelsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("label count mismatch: got %d, want %d\n got=%v\nwant=%v",
			len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("label[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
