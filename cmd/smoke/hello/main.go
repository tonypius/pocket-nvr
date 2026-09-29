// Command hello is Phase 0 smoke test A: prove the runtime model (ENV-7)
// — a static Go binary runs as root on the phone, binds a port, and
// survives until killed. Deleted or ignored after the test.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	start := time.Now()
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		uid := os.Getuid()
		ver, _ := os.ReadFile("/proc/version")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"uid":      uid,
			"root":     uid == 0,
			"kernel":   string(ver),
			"uptime_s": int64(time.Since(start).Seconds()),
		})
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:18099", nil))
}
