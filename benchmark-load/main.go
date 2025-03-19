package main

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Number of entries per map
const numEntries = 81920

var mapNames = []string{"map1", "map2", "map3", "map4", "map5"}

// Complex metadata for a record.
type Meta struct {
	CreatedAt int64    // record creation timestamp
	UpdatedAt int64    // record last update timestamp
	Tags      []string // list of tags
}

// ComplexRecord is a more complex structure with multiple fields.
type ComplexRecord struct {
	ID          string // record identifier
	Name        string // record name
	Description string // record description
	Data        string // fixed-size data payload
	Meta        Meta   // nested metadata
}

// flushPageCache flushes the filesystem page cache.
// NOTE: This requires root privileges.
func flushPageCache() {
	// Execute sync command to flush file system buffers
	if err := exec.Command("sync").Run(); err != nil {
		log.Fatal("flushPageCache: sync: ", err)
	}
	// Write "3" to /proc/sys/vm/drop_caches to drop page cache
	// Using tee to handle shell redirection under bash.
	err := exec.Command("bash", "-c", "echo 3 > /proc/sys/vm/drop_caches").Run()
	if err != nil {
		log.Fatal("flushPageCache: drop_caches: ", err)
	}
	time.Sleep(time.Second)
}

// readFileToMemory reads the entire file into memory as a byte slice.
// It opens the file, obtains its size, preallocates a buffer and reads the data.
// This approach minimizes memory reallocations for large files.
func readFileToMemory(filename string) (int, error) {
	// Open the file for reading
	file, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	// Get the file information
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}

	// Allocate a buffer with the exact file size
	size := info.Size()
	buffer := make([]byte, size)

	// Read the entire file into the buffer
	_, err = io.ReadFull(file, buffer)
	if err != nil {
		return 0, err
	}

	return len(buffer), nil
}

// dirSize calculates the total size of all files in the given directory.
func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func memstr(alloc uint64) string {
	switch {
	case alloc <= 1024:
		return fmt.Sprintf("%d bytes", alloc)
	case alloc <= 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(alloc)/1024)
	case alloc <= 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(alloc)/1024/1024)
	default:
		return fmt.Sprintf("%.1f GB", float64(alloc)/1024/1024/1024)
	}
}

func measure(name string, f func()) {
	flushPageCache()
	start := time.Now()
	var ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms1)

	f()
	fmt.Printf("%s read time: %.2fs\n", name, time.Since(start).Seconds())

	var ms2 runtime.MemStats
	runtime.ReadMemStats(&ms2)

	// Calculate memory difference as a signed integer
	diff := int64(ms2.HeapAlloc) - int64(ms1.HeapAlloc)
	if diff < 0 {
		diff = 0 // or handle negative difference appropriately
	}
	fmt.Printf("%s mem usage: %s\n", name, memstr(uint64(diff)))
}

// Weighted alphabet for word generation (without spaces)
// Letters are repeated to mimic natural frequency.
const weightedWordLetters = "eeeeeeeeeeee" +
	"tttttttttt" +
	"aaaaaaa" +
	"ooooooo" +
	"iiiiiii" +
	"nnnnnnn" +
	"ssssss" +
	"rrrrrr" +
	"ddd" +
	"lll" +
	"uu" +
	"cc" +
	"mm" +
	"ff" +
	"gg" +
	"yy" +
	"ww" +
	"pp" +
	"bb" +
	"kk" +
	"xjvqz" +
	"abcdefghijklmnopqrstuvwxyz" +
	"0123456789"

// generateRandomWord returns a random word of length n using the weighted alphabet.
func generateRandomWord(n int, r *rand.Rand) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(weightedWordLetters[r.Intn(len(weightedWordLetters))])
	}
	return b.String()
}

// generateHighEntropyLoremIpsum returns a pseudorandom text of approximate length n using the provided seed.
// It builds the text word by word, adding random punctuation occasionally.
func generateHighEntropyLoremIpsum(n int, seed int64) string {
	r := rand.New(rand.NewSource(seed))
	var b strings.Builder
	for b.Len() < n {
		// Random word length between 3 and 10.
		wordLength := r.Intn(8) + 3
		word := generateRandomWord(wordLength, r)
		// With 20% probability, append random punctuation (either comma or period).
		if r.Float64() < 0.2 {
			if r.Float64() < 0.5 {
				word += ","
			} else {
				word += "."
			}
		}
		// Append a space if this is not the first word.
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(word)
	}
	result := b.String()
	// Trim the result to the exact length if it overshoots.
	if len(result) > n {
		result = result[:n]
	}
	return result
}

// seedForField generates a reproducible seed based on mapName, index and field identifier.
func seedForField(mapName string, i int, field string) int64 {
	h := fnv.New64a() // using FNV-1a hash algorithm
	h.Write([]byte(mapName))
	h.Write([]byte(field))
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(i))
	h.Write(buf)
	return int64(h.Sum64())
}

// createRecord generates a new ComplexRecord for a given map name and index
func createRecord(mapName string, i int) ComplexRecord {
	now := time.Now().Unix()
	// Compute reproducible seeds for description and data.
	descriptionSeed := seedForField(mapName, i, "description")
	dataSeed := seedForField(mapName, i, "data")
	return ComplexRecord{
		ID:          fmt.Sprintf("%s-%d", mapName, i),
		Name:        fmt.Sprintf("Record %d", i),
		Description: generateHighEntropyLoremIpsum(42, descriptionSeed),
		Data:        generateHighEntropyLoremIpsum(1024, dataSeed),
		Meta: Meta{
			CreatedAt: now,
			UpdatedAt: now,
			Tags:      []string{"tag1", "tag2", "tag3"},
		},
	}
}

// preGenerateRecords pre-generates ComplexRecord entries for each map.
// Returns a map where the key is the map name and the value is a slice of pre-generated records.
func preGenerateRecords(names []string) map[string][]ComplexRecord {
	records := make(map[string][]ComplexRecord)
	for _, name := range names {
		slice := make([]ComplexRecord, numEntries)
		// Pre-generate records for each map
		for i := 0; i < numEntries; i++ {
			slice[i] = createRecord(name, i)
		}
		records[name] = slice
	}
	return records
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "persist":
			runPersist()
		case "bolt":
			runBoltDB()
		case "buntdb":
			runBuntDB()
		case "pebble":
			runPebble()
		case "badger":
			runBadger()
		case "void":
			runVoidDB()
		//
		case "seq":
			fmt.Println("\nSequential open-read test (1000 iterations) without flushing disk cache:")
			sequentialOpenBolt()
			sequentialOpenPebble()
			sequentialOpenBadger()
			sequentialOpenVoid()
		case "files":
			precomputePayloads(numFiles)
			println("File store test:", numFiles, "files\n")
			runBoltFiles()
			println()
			runPebbleFiles()
			println()
			runBadgerFiles()
			println()
			runVoidDBFiles()
		default:
			log.Fatal("Unknown db")
		}
	} else {
		runPersist()
		println()
		runBoltDB()
		println()
		runBuntDB()
		println()
		runPebble()
		println()
		runBadger()
		println()
		runVoidDB()
	}
}
