package main

import (
	"math/bits"
	"sort"
)

// Este archivo contiene las decisiones de particionamiento y ubicación.
// Son funciones puras: no tocan HTTP ni el estado del Server, así que se
// pueden probar de forma aislada.

// chooseBlockSize aplica la fórmula del Hito 1 [N3]:
// B = clamp(blockMin, size / (blocksPerNode · nodes), blockMax),
// redondeado a la potencia de dos inferior.
func chooseBlockSize(size int64, nodes int) int64 {
	if size <= 0 || nodes <= 0 {
		return blockMin
	}
	proposed := size / (blocksPerNode * int64(nodes))
	if proposed < blockMin {
		return blockMin
	}
	if proposed > blockMax {
		return blockMax
	}
	powerOfTwo := int64(1) << (63 - bits.LeadingZeros64(uint64(proposed)))
	return max(powerOfTwo, blockMin)
}

// numberOfBlocks es cuántos bloques de blockSize hacen falta para size bytes.
// Un archivo vacío ocupa un bloque vacío, para que igual tenga metadatos.
func numberOfBlocks(size, blockSize int64) int {
	if size == 0 {
		return 1
	}
	return int((size + blockSize - 1) / blockSize)
}

// placeBlocks asigna destinos a cada bloque con la heurística voraz del
// Hito 1 [N5]: cada copia va al DataNode con menor ocupación relativa.
// La ocupación se actualiza mientras se arma el plan ("carga virtual") para
// que los bloques de una misma subida no caigan todos en los mismos nodos.
// Las copias de un mismo bloque siempre quedan en DataNodes distintos.
// Los empates se resuelven por node_id para que el plan sea reproducible.
func placeBlocks(nodes []*NodeInfo, nBlocks int, blockSize int64, copies int) []BlockPlan {
	used := make(map[string]int64, len(nodes))
	for _, n := range nodes {
		used[n.NodeID] = n.Used
	}
	occupancy := func(n *NodeInfo) float64 {
		return float64(used[n.NodeID]) / float64(max(n.Capacity, 1))
	}

	plans := make([]BlockPlan, nBlocks)
	for i := range plans {
		candidates := append([]*NodeInfo(nil), nodes...)
		sort.Slice(candidates, func(a, b int) bool {
			oa, ob := occupancy(candidates[a]), occupancy(candidates[b])
			if oa != ob {
				return oa < ob
			}
			return candidates[a].NodeID < candidates[b].NodeID
		})
		targets := make([]Target, 0, copies)
		for _, n := range candidates[:copies] {
			targets = append(targets, Target{Node: n.NodeID, Host: n.Host, Port: n.Port})
			used[n.NodeID] += blockSize
		}
		plans[i] = BlockPlan{Index: i, Targets: targets}
	}
	return plans
}
