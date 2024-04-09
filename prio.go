package main

// Priority queue: execution requests joined the queue earlier
// will be served earlier.

import "time"

type Waiting struct {
	PID      int
	Inserted RecordTime

	index int
}

type Queue []*Waiting

func (q Queue) Len() int    { return len(q) }
func (q Queue) Empty() bool { return len(q) == 0 }
func (q Queue) Less(i, j int) bool {
	return time.Time(q[i].Inserted).Before(time.Time(q[j].Inserted))
}
func (q Queue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]

	// Index is maintained for heap.Fix().
	q[i].index = i
	q[j].index = j
}

func (q *Queue) Push(j interface{}) {
	n := len(*q)
	item := j.(*Waiting)
	item.index = n
	*q = append(*q, item)
}

func (q *Queue) Pop() interface{} {
	old := *q
	n := len(old)
	x := old[n-1]
	*q = old[0 : n-1]
	return x
}
