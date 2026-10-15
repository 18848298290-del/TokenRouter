package moderation

import (
	"strings"
)

// keyword_matcher.go builds and searches a keyword automaton that selects
// matches by configured order.
// Keyword blocking returns the first configured keyword present anywhere in the
// lowered text, even when a later configured keyword occurs earlier in the
// prompt. The automaton therefore combines matches by configuration index
// rather than by encounter position. Failure links also carry earlier suffix
// matches so overlapping keywords retain that ordering rule.
//
// Construction uses linked outgoing edges and one-based head storage while the
// trie grows. After breadth-first recovery links are ready, packing converts
// those heads into zero-based sorted edge ranges for runtime searches. The
// published matcher keeps copied original spellings for its result and
// immutable lowered-byte transitions for concurrent snapshot readers.

// contentModerationKeywordMatcher is an immutable byte automaton retaining
// configured keyword order for case-insensitive substring checks.
type contentModerationKeywordMatcher struct {
	// nodes, edges, rootTransitions, and keywords are published together after
	// construction. Match reads their shared storage, so callers keep the
	// completed matcher immutable during concurrent snapshot use.
	// packed automaton state records
	nodes           []contentModerationKeywordNode
	edges           []contentModerationKeywordEdge // sorted packed outgoing edges
	rootTransitions [256]int32                     // first-byte target state table
	keywords        []string                       // original configured keyword spellings
}

// contentModerationKeywordNode holds failure recovery, best configured match,
// and outgoing-edge location for one state.
type contentModerationKeywordNode struct {
	failure     int32 // shorter suffix recovery state
	bestKeyword int32 // earliest keyword index, minus-one absent
	// edgeStart stores a one-based linked-list head during construction, with zero
	// meaning absent. Packing replaces it with a zero-based offset interpreted
	// together with edgeCount by next.
	edgeStart uint32 // build link or packed offset
	edgeCount uint16 // packed outgoing edge count
}

// contentModerationKeywordEdge is one byte-labeled transition in a packed,
// sorted node range.
type contentModerationKeywordEdge struct {
	target int32 // destination automaton state index
	label  byte  // lowercased encoded byte label
}

// contentModerationKeywordBuildEdge links temporary outgoing transitions while
// the keyword trie is still growing.
type contentModerationKeywordBuildEdge struct {
	target      int32 // destination trie state index
	nextSibling int32 // next edge, minus-one terminates
	label       byte  // lowercased encoded byte label
}

// newContentModerationKeywordMatcher builds an immutable index that returns the
// earliest configured keyword found anywhere in text. Empty lists or lists
// containing only empty strings return nil. It copies original keyword
// spellings and constructs transitions over their strings.ToLower byte
// representation.
//
// Breadth-first failure links propagate earlier suffix matches, so an
// overlapping shorter keyword can still outrank a later configured longer
// keyword. Temporary sibling links are then converted to sorted outgoing ranges
// and a root transition table. That encoding changes edgeStart interpretation
// before the matcher is published to snapshot readers.
func newContentModerationKeywordMatcher(keywords []string) *contentModerationKeywordMatcher {
	if len(keywords) == 0 {
		return nil
	}

	buildNodes := []contentModerationKeywordNode{newContentModerationKeywordNode()}
	buildEdges := make([]contentModerationKeywordBuildEdge, 0)
	originalKeywords := append([]string(nil), keywords...)

	for keywordIndex, keyword := range keywords {
		if keyword == "" {
			continue
		}
		state := int32(0)
		for _, label := range []byte(strings.ToLower(keyword)) {
			next := contentModerationKeywordBuildTransition(buildNodes, buildEdges, state, label)
			if next < 0 {
				next = int32(len(buildNodes))
				buildNodes = append(buildNodes, newContentModerationKeywordNode())
				buildEdges = append(buildEdges, contentModerationKeywordBuildEdge{
					target:      next,
					nextSibling: contentModerationKeywordBuildFirstEdge(buildNodes[state]),
					label:       label,
				})
				buildNodes[state].edgeStart = uint32(len(buildEdges))
			}
			state = next
		}
		if current := buildNodes[state].bestKeyword; current < 0 || int32(keywordIndex) < current {
			buildNodes[state].bestKeyword = int32(keywordIndex)
		}
	}

	if len(buildNodes) == 1 {
		return nil
	}

	// Establish shorter suffix recovery breadth first so each child inherits an
	// already resolved failure match and keeps the earliest configured keyword index.
	queue := make([]int32, 0, len(buildNodes)-1)
	var rootTransitions [256]int32
	for edgeIndex := contentModerationKeywordBuildFirstEdge(buildNodes[0]); edgeIndex >= 0; edgeIndex = buildEdges[edgeIndex].nextSibling {
		edge := buildEdges[edgeIndex]
		rootTransitions[edge.label] = edge.target
		queue = append(queue, edge.target)
	}

	for queueIndex := 0; queueIndex < len(queue); queueIndex++ {
		state := queue[queueIndex]
		for edgeIndex := contentModerationKeywordBuildFirstEdge(buildNodes[state]); edgeIndex >= 0; edgeIndex = buildEdges[edgeIndex].nextSibling {
			edge := buildEdges[edgeIndex]
			failure := buildNodes[state].failure
			fallback := contentModerationKeywordBuildTransition(buildNodes, buildEdges, failure, edge.label)
			for fallback < 0 && failure != 0 {
				failure = buildNodes[failure].failure
				fallback = contentModerationKeywordBuildTransition(buildNodes, buildEdges, failure, edge.label)
			}
			if fallback >= 0 {
				buildNodes[edge.target].failure = fallback
			}
			buildNodes[edge.target].bestKeyword = minKeywordIndex(
				buildNodes[edge.target].bestKeyword,
				buildNodes[buildNodes[edge.target].failure].bestKeyword,
			)
			queue = append(queue, edge.target)
		}
	}

	// Replace build-list heads with packed offsets after failure links are complete;
	// Match can then search sorted byte ranges without consulting temporary sibling links.
	edges := make([]contentModerationKeywordEdge, 0, len(buildEdges))
	var outgoing [256]contentModerationKeywordEdge
	for nodeIndex := range buildNodes {
		count := 0
		for edgeIndex := contentModerationKeywordBuildFirstEdge(buildNodes[nodeIndex]); edgeIndex >= 0; edgeIndex = buildEdges[edgeIndex].nextSibling {
			edge := buildEdges[edgeIndex]
			outgoing[count] = contentModerationKeywordEdge{target: edge.target, label: edge.label}
			count++
		}
		for index := 1; index < count; index++ {
			current := outgoing[index]
			insertAt := index
			for insertAt > 0 && current.label < outgoing[insertAt-1].label {
				outgoing[insertAt] = outgoing[insertAt-1]
				insertAt--
			}
			outgoing[insertAt] = current
		}
		buildNodes[nodeIndex].edgeStart = uint32(len(edges))
		buildNodes[nodeIndex].edgeCount = uint16(count)
		edges = append(edges, outgoing[:count]...)
	}

	return &contentModerationKeywordMatcher{
		nodes:           buildNodes,
		edges:           edges,
		rootTransitions: rootTransitions,
		keywords:        originalKeywords,
	}
}

// newContentModerationKeywordNode creates an unmatched state with the root
// failure target and an absent keyword sentinel. Its zero edgeStart represents
// an empty build list until outgoing construction edges are assigned.
func newContentModerationKeywordNode() contentModerationKeywordNode {
	return contentModerationKeywordNode{bestKeyword: -1}
}

// contentModerationKeywordBuildFirstEdge decodes the temporary one-based list
// head before packing. Zero returns minus one for no outgoing edge; this helper
// requires construction nodes because packed offsets use a different
// interpretation.
func contentModerationKeywordBuildFirstEdge(node contentModerationKeywordNode) int32 {
	if node.edgeStart == 0 {
		return -1
	}
	return int32(node.edgeStart - 1)
}

// contentModerationKeywordBuildTransition searches a construction state's
// linked outgoing edges for one byte. Invalid state indexes or absent
// transitions return minus one; edges are still sibling-linked and this helper
// is used before packed range publication.
func contentModerationKeywordBuildTransition(
	nodes []contentModerationKeywordNode,
	edges []contentModerationKeywordBuildEdge,
	state int32,
	label byte,
) int32 {
	if state < 0 || int(state) >= len(nodes) {
		return -1
	}
	for edgeIndex := contentModerationKeywordBuildFirstEdge(nodes[state]); edgeIndex >= 0; edgeIndex = edges[edgeIndex].nextSibling {
		if edges[edgeIndex].label == label {
			return edges[edgeIndex].target
		}
	}
	return -1
}

// minKeywordIndex returns the earlier nonnegative keyword index, treating
// negative values as absent matches. Two absent inputs can return the supplied
// negative sentinel, and configured ordering stays independent of where a
// substring occurs.
func minKeywordIndex(left, right int32) int32 {
	if left < 0 {
		return right
	}
	if right < 0 || left < right {
		return left
	}
	return right
}

// Match returns the earliest configured keyword occurring in
// strings.ToLower(text), together with its hit flag. Nil or empty matchers and
// empty text return no hit. It scans lowered bytes and follows failure links
// when the current state has no transition, preserving overlapping suffix
// matches.
//
// Match combines state results by configured index instead of first text
// position. Finding index zero can return immediately because no later text
// match can outrank that keyword; other matches remain candidates until the
// scan ends. The returned spelling comes from the copied original keyword list.
func (m *contentModerationKeywordMatcher) Match(text string) (string, bool) {
	if m == nil || text == "" || len(m.nodes) == 0 || len(m.keywords) == 0 {
		return "", false
	}
	lower := strings.ToLower(text)
	state := int32(0)
	bestKeyword := int32(-1)
	for index := range len(lower) {
		label := lower[index]
		// Retry the byte along shorter suffix states so overlapping matches remain
		// candidates when the current prefix has no outgoing transition for this byte.
		for {
			next := m.next(state, label)
			if next != 0 {
				state = next
				break
			}
			if state == 0 {
				break
			}
			state = m.nodes[state].failure
		}
		bestKeyword = minKeywordIndex(bestKeyword, m.nodes[state].bestKeyword)
		if bestKeyword == 0 {
			return m.keywords[0], true
		}
	}
	if bestKeyword < 0 || int(bestKeyword) >= len(m.keywords) {
		return "", false
	}
	return m.keywords[bestKeyword], true
}

// next returns a packed transition target or root zero when that byte is
// absent. Root states use the direct table; other valid states binary-search
// their sorted edge range, while invalid nonroot states return zero.
func (m *contentModerationKeywordMatcher) next(state int32, label byte) int32 {
	if state == 0 {
		return m.rootTransitions[label]
	}
	if state < 0 || int(state) >= len(m.nodes) {
		return 0
	}
	node := m.nodes[state]
	left := int(node.edgeStart)
	right := left + int(node.edgeCount)
	for left < right {
		middle := left + (right-left)/2
		edge := m.edges[middle]
		if edge.label < label {
			left = middle + 1
			continue
		}
		right = middle
	}
	end := int(node.edgeStart) + int(node.edgeCount)
	if left < end && m.edges[left].label == label {
		return m.edges[left].target
	}
	return 0
}
