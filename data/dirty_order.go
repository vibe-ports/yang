// SPDX-License-Identifier: BSD-3-Clause

package data

// dirtyOrder is the parser's ordered view of one deferred system-ordered operation run. The
// physical sibling and operation-index slices remain in link order until publication. Equal
// values are ordered by seq, matching rb_insert_node's insert-to-the-right stability.
type dirtyOrder struct {
	root  *dirtyOrderNode
	nodes map[*Node]*dirtyOrderNode
	next  uint64
	work  *workCounter
}

type dirtyOrderNode struct {
	node                *Node
	left, right, parent *dirtyOrderNode
	seq                 uint64
	height, size        int
	active              bool
}

func newDirtyOrder(work *workCounter, nodes []*Node) *dirtyOrder {
	d := &dirtyOrder{nodes: make(map[*Node]*dirtyOrderNode, len(nodes)), work: work}
	for _, n := range nodes {
		d.insert(n)
	}
	return d
}

func dirtyHeight(n *dirtyOrderNode) int {
	if n == nil {
		return 0
	}
	return n.height
}

func dirtySize(n *dirtyOrderNode) int {
	if n == nil {
		return 0
	}
	return n.size
}

func dirtyFix(n *dirtyOrderNode) {
	if n == nil {
		return
	}
	n.height = 1 + max(dirtyHeight(n.left), dirtyHeight(n.right))
	n.size = dirtySize(n.left) + dirtySize(n.right)
	if n.active {
		n.size++
	}
}

func (d *dirtyOrder) rotateLeft(n *dirtyOrderNode) *dirtyOrderNode {
	r := n.right
	n.right = r.left
	if r.left != nil {
		r.left.parent = n
	}
	r.left, r.parent = n, n.parent
	n.parent = r
	dirtyFix(n)
	dirtyFix(r)
	return r
}

func (d *dirtyOrder) rotateRight(n *dirtyOrderNode) *dirtyOrderNode {
	l := n.left
	n.left = l.right
	if l.right != nil {
		l.right.parent = n
	}
	l.right, l.parent = n, n.parent
	n.parent = l
	dirtyFix(n)
	dirtyFix(l)
	return l
}

func (d *dirtyOrder) balance(n *dirtyOrderNode) *dirtyOrderNode {
	dirtyFix(n)
	switch b := dirtyHeight(n.left) - dirtyHeight(n.right); {
	case b > 1:
		if dirtyHeight(n.left.left) < dirtyHeight(n.left.right) {
			n.left = d.rotateLeft(n.left)
			n.left.parent = n
		}
		return d.rotateRight(n)
	case b < -1:
		if dirtyHeight(n.right.right) < dirtyHeight(n.right.left) {
			n.right = d.rotateRight(n.right)
			n.right.parent = n
		}
		return d.rotateLeft(n)
	default:
		return n
	}
}

func (d *dirtyOrder) insert(n *Node) {
	d.next++
	x := &dirtyOrderNode{node: n, seq: d.next, height: 1, size: 1, active: true}
	d.nodes[n] = x
	var add func(**dirtyOrderNode, *dirtyOrderNode)
	add = func(slot **dirtyOrderNode, parent *dirtyOrderNode) {
		if *slot == nil {
			x.parent = parent
			*slot = x
			return
		}
		d.work.Add(1) // one unit for each visited ordered-index node
		cur := *slot
		if c := compareSorted(n, cur.node); c < 0 || c == 0 && x.seq < cur.seq {
			add(&cur.left, cur)
		} else {
			add(&cur.right, cur)
		}
		oldParent := cur.parent
		*slot = d.balance(cur)
		(*slot).parent = oldParent
	}
	add(&d.root, nil)
}

// remove makes an unlinked parser node invisible without restructuring the AVL tree. Its key is
// immutable for the lifetime of this parse, and inactive nodes still keep the tree balanced.
func (d *dirtyOrder) remove(n *Node) {
	x := d.nodes[n]
	if x == nil || !x.active {
		return
	}
	delete(d.nodes, n)
	x.active = false
	for ; x != nil; x = x.parent {
		d.work.Add(1)
		dirtyFix(x)
	}
}

func (d *dirtyOrder) rawNext(n *dirtyOrderNode) *dirtyOrderNode {
	if n.right != nil {
		n = n.right
		d.work.Add(1)
		for n.left != nil {
			n = n.left
			d.work.Add(1)
		}
		return n
	}
	for n.parent != nil && n == n.parent.right {
		n = n.parent
		d.work.Add(1)
	}
	if n.parent != nil {
		d.work.Add(1)
	}
	return n.parent
}

func (d *dirtyOrder) firstNode() *dirtyOrderNode {
	n := d.root
	for n != nil {
		d.work.Add(1)
		if dirtySize(n.left) > 0 {
			n = n.left
			continue
		}
		if n.active {
			return n
		}
		n = d.rawNext(n)
	}
	return nil
}

func (d *dirtyOrder) first() *Node {
	if n := d.firstNode(); n != nil {
		return n.node
	}
	return nil
}

func (d *dirtyOrder) successor(n *Node) *Node {
	start := d.nodes[n]
	if start == nil {
		return nil
	}
	for x := d.rawNext(start); x != nil; x = d.rawNext(x) {
		if x.active {
			return x.node
		}
	}
	return nil
}

// rank is the node's one-based position in sorted order.
func (d *dirtyOrder) rank(n *Node) (int, bool) {
	x := d.nodes[n]
	if x == nil || !x.active {
		return 0, false
	}
	d.work.Add(1)
	rank := dirtySize(x.left) + 1
	for x.parent != nil {
		d.work.Add(1)
		if x == x.parent.right {
			rank += dirtySize(x.parent.left)
			if x.parent.active {
				rank++
			}
		}
		x = x.parent
	}
	return rank, true
}

func (d *dirtyOrder) ordered() []*Node {
	out := make([]*Node, 0, dirtySize(d.root))
	for n := d.firstNode(); n != nil; n = d.rawNext(n) {
		if n.active {
			d.work.Add(1) // publication movement
			out = append(out, n.node)
		}
	}
	return out
}
