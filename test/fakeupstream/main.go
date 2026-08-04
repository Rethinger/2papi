package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() { go serve(9001); serve(9002) }
func serve(port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "fail429") {
			http.Error(w, "rate", 429)
			return
		}
		if strings.Contains(string(b), "fail500") {
			http.Error(w, "boom", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, s := range []string{"hello", " world"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", s)
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	addr := fmt.Sprintf(":%d", port)
	log.Println("fake upstream", addr, os.Getpid())
	log.Fatal(http.ListenAndServe(addr, mux))
}
