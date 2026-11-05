package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"log"
	"os"
	"sync"
	"time"
)

// The AI answer cache lives on disk and holds up to 20 million answers without using memory:
//   ai-cache.jsonl  one JSON line per answer {"k":key,"t":unix time,"o":answer}, appended
//   ai-cache.idx    a hash table of fixed 16-byte slots (64-bit hash of the key, offset of its line),
//                   33.5 million slots, linear probing, read and written in place
// A lookup is one or two small reads; nothing is loaded at start except a count.

var (
	aiFile    = fsData + "/ai-cache.jsonl"
	aiIdxFile = fsData + "/ai-cache.idx"
)

const (
	aiSlots    = 1 << 25 // 33,554,432
	aiMaxItems = 20_000_000
	slotSize   = 16
)

var (
	acMu    sync.RWMutex
	acData  *os.File
	acIdx   *os.File
	acDataN int64
	acItems int
)

func aiHash(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	if v := h.Sum64(); v != 0 {
		return v
	}
	return 1
}

func init() {
	var err error
	acData, err = os.OpenFile(aiFile, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		log.Printf("ai cache off: %v", err)
		return
	}
	st, _ := acData.Stat()
	acDataN = st.Size()
	_, statErr := os.Stat(aiIdxFile)
	acIdx, err = os.OpenFile(aiIdxFile, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		log.Printf("ai cache off: %v", err)
		acData = nil
		return
	}
	acIdx.Truncate(int64(aiSlots) * slotSize) // sparse: takes disk space only where slots are used
	if os.IsNotExist(statErr) && acDataN > 0 {
		rebuildAICache()
	}
	// count the used slots
	buf := make([]byte, 1<<20)
	for off := int64(0); off < int64(aiSlots)*slotSize; off += int64(len(buf)) {
		n, _ := acIdx.ReadAt(buf, off)
		for i := 0; i+slotSize <= n; i += slotSize {
			if binary.LittleEndian.Uint64(buf[i:]) != 0 {
				acItems++
			}
		}
	}
	log.Printf("ai cache: %d answers", acItems)
}

// rebuildAICache makes the table again from the lines (only used if the table file is missing).
func rebuildAICache() {
	f, err := os.Open(aiFile)
	if err != nil {
		return
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	var off int64
	for {
		b, err := r.ReadBytes('\n')
		if len(b) > 0 {
			var l struct {
				K string `json:"k"`
			}
			if json.Unmarshal(b, &l) == nil && l.K != "" {
				acPut(aiHash(l.K), off)
			}
			off += int64(len(b))
		}
		if err != nil {
			break
		}
	}
}

// acFind returns the slot of this hash (found) or the first empty slot on its probe path.
func acFind(h uint64) (slot int64, off uint64, found bool) {
	var buf [slotSize]byte
	for i := int64(h % aiSlots); ; i = (i + 1) % aiSlots {
		if _, err := acIdx.ReadAt(buf[:], i*slotSize); err != nil {
			return i, 0, false
		}
		sh := binary.LittleEndian.Uint64(buf[:8])
		if sh == 0 {
			return i, 0, false
		}
		if sh == h {
			return i, binary.LittleEndian.Uint64(buf[8:]), true
		}
	}
}

func acPut(h uint64, off int64) {
	slot, _, found := acFind(h)
	var buf [slotSize]byte
	binary.LittleEndian.PutUint64(buf[:8], h)
	binary.LittleEndian.PutUint64(buf[8:], uint64(off))
	acIdx.WriteAt(buf[:], slot*slotSize)
	if !found {
		acItems++
	}
}

type aiLine struct {
	K string          `json:"k"`
	T int64           `json:"t"`
	O json.RawMessage `json:"o"`
}

// aiGet returns a saved answer. Answers never expire and are never deleted.
func aiGet(key string) ([]byte, bool) {
	if acIdx == nil {
		return nil, false
	}
	acMu.RLock()
	defer acMu.RUnlock()
	_, off, found := acFind(aiHash(key))
	if !found {
		return nil, false
	}
	buf := make([]byte, 64<<10)
	n, _ := acData.ReadAt(buf, int64(off))
	i := bytes.IndexByte(buf[:n], '\n')
	if i < 0 {
		return nil, false
	}
	var l aiLine
	if json.Unmarshal(buf[:i], &l) != nil || l.K != key {
		return nil, false
	}
	return []byte(l.O), true
}

// aiPut saves an answer (not when the cache already holds 20 million, unless it replaces one).
func aiPut(key string, out []byte) {
	if acIdx == nil {
		return
	}
	h := aiHash(key)
	acMu.Lock()
	defer acMu.Unlock()
	if _, _, found := acFind(h); !found && acItems >= aiMaxItems {
		return
	}
	line, _ := json.Marshal(aiLine{key, time.Now().Unix(), out})
	if _, err := acData.WriteAt(append(line, '\n'), acDataN); err != nil {
		return
	}
	acPut(h, acDataN)
	acDataN += int64(len(line)) + 1
}
