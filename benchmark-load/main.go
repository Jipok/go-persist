package main

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

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

// printSize prints the logical size and the actual disk usage (physical size) for the given file or directory
func printSize(path string) {
	var logicalSize, physicalSize int64

	fi, err := os.Stat(path)
	if err != nil {
		log.Printf("Error getting file info for '%s': %v", path, err)
		return
	}

	if !fi.IsDir() {
		// For a file: logical size is info.Size()
		logicalSize = fi.Size()
		// Attempt to get the underlying syscall.Stat_t for physical block count
		if statT, ok := fi.Sys().(*syscall.Stat_t); ok {
			// Multiply block count by 512 (bytes per block on Linux)
			physicalSize = int64(statT.Blocks) * 512
		} else {
			// Fallback in case syscall.Stat_t is not available
			physicalSize = fi.Size()
		}
	} else {
		// For a directory: traverse all files recursively
		err = filepath.Walk(path, func(item string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				// Accumulate logical size
				logicalSize += info.Size()
				// Accumulate physical size using block count if available
				if statT, ok := info.Sys().(*syscall.Stat_t); ok {
					physicalSize += int64(statT.Blocks) * 512
				} else {
					physicalSize += info.Size()
				}
			}
			return nil
		})
		if err != nil {
			log.Printf("Error walking through directory '%s': %v", path, err)
			return
		}
	}

	fmt.Printf("Physical size on disk: %.1f MB  (Logical %.1f MB)\n",
		float64(physicalSize)/(1024*1024), float64(logicalSize)/(1024*1024))
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
			fmt.Println("Sequential open-read test (1000 iterations) without flushing disk cache:")
			sequentialOpenBolt()
			sequentialOpenPebble()
			sequentialOpenBadger()
			sequentialOpenVoid()
		case "files":
			precomputePayloads(numFiles)
			println("File store test:", numFiles, "files\n")
			runBoltFiles()
			runPebbleFiles()
			runBadgerFiles()
			runVoidDBFiles()
			runExt4Files()
		default:
			log.Fatal("Unknown db/test")
		}
	} else {
		println("Load test, entries per map: ", numEntries, "\n")
		runPersist()
		runBoltDB()
		runBuntDB()
		runPebble()
		runBadger()
		runVoidDB()
	}
}
