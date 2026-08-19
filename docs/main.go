// Сервер документации tutu-smile: один бинарник, stdlib, всё встроено.
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
)

//go:embed web docs
var content embed.FS

func main() {
	addr := flag.String("addr", ":8090", "адрес прослушивания")
	flag.Parse()

	index, err := content.ReadFile("web/index.html")
	if err != nil {
		log.Fatal(err)
	}
	docs, err := fs.Sub(content, "docs")
	if err != nil {
		log.Fatal(err)
	}
	fonts, err := fs.Sub(content, "web/fonts")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
	mux.Handle("GET /docs/", http.StripPrefix("/docs/", http.FileServerFS(docs)))
	mux.Handle("GET /fonts/", http.StripPrefix("/fonts/", http.FileServerFS(fonts)))

	log.Printf("docs: http://localhost%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
