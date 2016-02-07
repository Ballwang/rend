package metrics

import (
	"bytes"
	crand "crypto/rand"
	"encoding/binary"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
)

const (
	numHists   = 100
	buflen     = 0x3FFF // 16384 entries
	sampleRate = 0.25   // sample 1 out of every 4 observations
)

var (
	hnames    = make([]string, numHists)
	hists     = make([]*hist, numHists)
	curHistID = new(uint32)
)

func init() {
	// start at "-1" so the first ID is 0
	atomic.StoreUint32(curHistID, 0xFFFFFFFF)
}

// The hist struct holds a primary and secondary data structure so the reader of
// the histograms will get to read the data out while new observations are made.
// As well, pulling and resetting the histogram does not require a malloc in the
// path of pulling the data, and the large circular buffers can be reused.
type hist struct {
	lock sync.RWMutex
	rand *rand.Rand
	prim *hdat
	sec  *hdat
}
type hdat struct {
	count *uint64
	min   *uint64
	max   *uint64
	buf   []uint64
}

func newHist() *hist {
	return &hist{
		rand: rand.New(rand.NewSource(seed())),
		prim: newHdat(),
		sec:  newHdat(),
	}
}
func newHdat() *hdat {
	ret := &hdat{
		count: new(uint64),
		min:   new(uint64),
		max:   new(uint64),
		buf:   make([]uint64, buflen),
	}
	atomic.StoreUint64(ret.min, math.MaxUint64)
	return ret
}

func seed() int64 {
	b := make([]byte, 8)
	if _, err := crand.Read(b); err != nil {
		panic(err.Error())
	}
	var ret int64
	binary.Read(bytes.NewBuffer(b), binary.LittleEndian, &ret)
	return ret
}

func AddHistogram(name string) uint32 {
	idx := atomic.AddUint32(curHistID, 1)

	if idx >= numHists {
		panic("Too many histograms")
	}

	hnames[idx] = name
	hists[idx] = newHist()

	return idx
}

func ObserveHist(id uint32, value uint64) {
	h := hists[id]

	// We lock here to ensure that the min and max values are true to this time
	// period, meaning extractAndReset won't pull the data out from under us
	// while the current observation is being compared. Otherwise, min and max
	// could come from the previous period on the next read.
	h.lock.RLock()
	defer h.lock.RUnlock()

	// Set max and min (if needed) in an atomic fashion
	for {
		max := atomic.LoadUint64(h.prim.max)
		if value < max || atomic.CompareAndSwapUint64(h.prim.max, max, value) {
			break
		}
	}
	for {
		min := atomic.LoadUint64(h.prim.min)
		if value > min || atomic.CompareAndSwapUint64(h.prim.min, min, value) {
			break
		}
	}

	// Sample at a fixed rate
	if h.rand.Float64() > sampleRate {
		return
	}

	// Get the current index as the count % buflen
	idx := atomic.AddUint64(h.prim.count, 1)
	idx &= buflen

	// Add observation
	h.prim.buf[idx] = value
}

func getAllHistograms() ([]string, []*hdat) {
	n := int(atomic.LoadUint32(curHistID))

	retnames := hnames[:n]
	retdat := make([]*hdat, n)

	for i := 0; i < n; i++ {
		retdat[i] = extractAndReset(i)
	}

	return retnames, retdat
}

func extractAndReset(id int) *hdat {
	h := hists[id]

	h.lock.Lock()

	// flip and reset the count
	temp := h.prim
	h.prim = h.sec
	h.sec = temp

	atomic.StoreUint64(h.prim.count, 0)
	atomic.StoreUint64(h.prim.max, 0)
	atomic.StoreUint64(h.prim.min, math.MaxUint64)

	h.lock.Unlock()

	return h.sec
}
