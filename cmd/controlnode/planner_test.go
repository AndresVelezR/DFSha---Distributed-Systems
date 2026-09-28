package main

import "testing"

var defaultSizing = BlockSizing{Min: 8 * megabyte, Max: 64 * megabyte, BlocksPerNode: 4}

func TestChooseBlockSize(t *testing.T) {
	cases := []struct {
		name  string
		size  int64
		nodes int
		want  int64
	}{
		{"archivo pequeño usa el mínimo", 1 * megabyte, 3, 8 * megabyte},
		{"archivo vacío usa el mínimo", 0, 3, 8 * megabyte},
		{"1 GB con 3 nodos usa el máximo", 1024 * megabyte, 3, 64 * megabyte},
		{"redondea a la potencia de dos inferior", 300 * megabyte, 3, 16 * megabyte}, // 300/12 = 25 MB -> 16 MB
		{"sin nodos usa el mínimo", 1024 * megabyte, 0, 8 * megabyte},
	}
	for _, c := range cases {
		if got := chooseBlockSize(c.size, c.nodes, defaultSizing); got != c.want {
			t.Errorf("%s: chooseBlockSize(%d, %d) = %d, se esperaba %d", c.name, c.size, c.nodes, got, c.want)
		}
	}
}

func TestNumberOfBlocks(t *testing.T) {
	cases := []struct {
		size, blockSize int64
		want            int
	}{
		{0, 8, 1},
		{1, 8, 1},
		{8, 8, 1},
		{9, 8, 2},
		{20000000, 8 * megabyte, 3},
	}
	for _, c := range cases {
		if got := numberOfBlocks(c.size, c.blockSize); got != c.want {
			t.Errorf("numberOfBlocks(%d, %d) = %d, se esperaba %d", c.size, c.blockSize, got, c.want)
		}
	}
}

func TestCopiesPerBlock(t *testing.T) {
	cases := []struct {
		name                          string
		configured, registered, alive int
		wantTarget, wantCopies        int
	}{
		{"clúster normal de 3 nodos", 2, 3, 3, 2, 2},
		{"3 registrados y 1 caído: guarda las 2 que puede", 2, 3, 2, 2, 2},
		{"3 registrados y 2 caídos: guarda 1 y queda degradado", 2, 3, 1, 2, 1},
		{"clúster de 2 nodos: N-1 limita a 1", 2, 2, 2, 1, 1},
		{"factor igual a N se recorta a N-1", 3, 3, 3, 2, 2},
		{"clúster grande respeta el factor configurado", 3, 10, 10, 3, 3},
		{"un solo nodo guarda 1 copia", 2, 1, 1, 1, 1},
	}
	for _, c := range cases {
		target, copies := copiesPerBlock(c.configured, c.registered, c.alive)
		if target != c.wantTarget || copies != c.wantCopies {
			t.Errorf("%s: copiesPerBlock(%d, %d, %d) = (%d, %d), se esperaba (%d, %d)",
				c.name, c.configured, c.registered, c.alive, target, copies, c.wantTarget, c.wantCopies)
		}
	}
}

func TestPlaceBlocksUsesDistinctNodesAndBalances(t *testing.T) {
	nodes := []*NodeInfo{
		{NodeID: "dn-01", Capacity: 1000 * megabyte},
		{NodeID: "dn-02", Capacity: 1000 * megabyte},
		{NodeID: "dn-03", Capacity: 1000 * megabyte},
	}
	plans := placeBlocks(nodes, 6, 8*megabyte, 2)

	perNode := map[string]int{}
	for _, p := range plans {
		if len(p.Targets) != 2 {
			t.Fatalf("bloque %d: %d destinos, se esperaban 2", p.Index, len(p.Targets))
		}
		if p.Targets[0].Node == p.Targets[1].Node {
			t.Errorf("bloque %d: las dos copias quedaron en %s", p.Index, p.Targets[0].Node)
		}
		for _, target := range p.Targets {
			perNode[target.Node]++
		}
	}
	// 6 bloques x 2 copias = 12 copias entre 3 nodos iguales: 4 por nodo.
	for id, count := range perNode {
		if count != 4 {
			t.Errorf("%s recibió %d copias, se esperaban 4 (reparto parejo)", id, count)
		}
	}
}

func TestPlaceBlocksPrefersEmptierNode(t *testing.T) {
	nodes := []*NodeInfo{
		{NodeID: "dn-01", Capacity: 100, Used: 90},
		{NodeID: "dn-02", Capacity: 100, Used: 10},
		{NodeID: "dn-03", Capacity: 100, Used: 50},
	}
	plans := placeBlocks(nodes, 1, 1, 1)
	if got := plans[0].Targets[0].Node; got != "dn-02" {
		t.Errorf("el bloque fue a %s, se esperaba el nodo más vacío dn-02", got)
	}
}
