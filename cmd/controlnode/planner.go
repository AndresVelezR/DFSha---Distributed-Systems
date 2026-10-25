package main

import (
	"math/bits"
	"sort"
)

// Este archivo contiene las decisiones de particionamiento y ubicación.
// Son funciones puras: no tocan HTTP ni el estado del Server, así que se
// pueden probar de forma aislada.

// chooseBlockSize aplica la fórmula del Hito 1 [N3]:
// B = clamp(Min, size / (BlocksPerNode · nodes), Max),
// redondeado a la potencia de dos inferior.
func chooseBlockSize(size int64, nodes int, p BlockSizing) int64 {
	if size <= 0 || nodes <= 0 {
		return p.Min
	}
	proposed := size / (p.BlocksPerNode * int64(nodes))
	if proposed < p.Min {
		return p.Min
	}
	if proposed > p.Max {
		return p.Max
	}
	powerOfTwo := int64(1) << (63 - bits.LeadingZeros64(uint64(proposed)))
	return max(powerOfTwo, p.Min)
}

// numberOfBlocks es cuántos bloques de blockSize hacen falta para size bytes.
// Un archivo vacío ocupa un bloque vacío, para que igual tenga metadatos.
func numberOfBlocks(size, blockSize int64) int {
	if size == 0 {
		return 1
	}
	return int((size + blockSize - 1) / blockSize)
}

// blockSizes devuelve el tamaño de cada bloque: todos miden blockSize salvo
// el último, que lleva el resto.
func blockSizes(size, blockSize int64) []int64 {
	sizes := make([]int64, numberOfBlocks(size, blockSize))
	for i := range sizes {
		sizes[i] = min(blockSize, size-int64(i)*blockSize)
	}
	return sizes
}

// copiesPerBlock decide cuántas copias lleva cada bloque, según el Hito 1 [N4]:
//   - target: el factor configurado, pero nunca mayor que registered-1 (N-1),
//     para que siempre exista un DataNode donde rehacer una copia perdida.
//   - copies: las copias que realmente se pueden guardar ahora. Si hay menos
//     DataNodes vivos que target, se guardan las posibles en vez de rechazar
//     la escritura ("Qué pasa al quitar un nodo").
//
// Nunca devuelve menos de 1 copia mientras haya al menos un nodo vivo.
func copiesPerBlock(configured, registered, alive int) (target, copies int) {
	target = min(configured, registered-1)
	target = max(target, 1)
	copies = min(target, alive)
	return target, copies
}

// placeBlocks asigna destinos a cada bloque con la heurística voraz del
// Hito 1 [N5]: cada copia va al DataNode con menor ocupación relativa que
// todavía quede por debajo de fullThreshold después de recibirla.
//
// La ocupación se actualiza mientras se arma el plan ("carga virtual") para
// que los bloques de una misma subida no caigan todos en los mismos nodos.
// Las copias de un mismo bloque siempre quedan en DataNodes distintos y los
// empates se resuelven por node_id para que el plan sea reproducible.
//
// Devuelve false si algún bloque no consigue todas sus copias: el archivo no
// cabe y se rechaza antes de empezar a subirlo.
func placeBlocks(nodes []*NodeInfo, sizes []int64, copies int, fullThreshold float64) ([]BlockPlan, bool) {
	used := make(map[string]int64, len(nodes))
	for _, n := range nodes {
		used[n.NodeID] = n.Used
	}
	occupancy := func(n *NodeInfo) float64 {
		return float64(used[n.NodeID]) / float64(max(n.Capacity, 1))
	}

	plans := make([]BlockPlan, len(sizes))
	for i, size := range sizes {
		candidates := append([]*NodeInfo(nil), nodes...)
		sort.Slice(candidates, func(a, b int) bool {
			oa, ob := occupancy(candidates[a]), occupancy(candidates[b])
			if oa != ob {
				return oa < ob
			}
			return candidates[a].NodeID < candidates[b].NodeID
		})
		targets := make([]Target, 0, copies)
		for _, n := range candidates {
			if float64(used[n.NodeID]+size) > float64(n.Capacity)*fullThreshold {
				continue
			}
			targets = append(targets, Target{Node: n.NodeID, URL: n.URL})
			used[n.NodeID] += size
			if len(targets) == copies {
				break
			}
		}
		if len(targets) < copies {
			return nil, false
		}
		plans[i] = BlockPlan{Index: i, Targets: targets}
	}
	return plans, true
}
