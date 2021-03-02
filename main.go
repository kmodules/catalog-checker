package main

import (
	"io/fs"
	"log"
	"net/http"
	"os"

	"kmodules.xyz/catalog-checker/catalog"
)

func main() {
	useOS := len(os.Args) > 1 && os.Args[1] == "live"
	http.Handle("/", http.FileServer(getFileSystem(useOS)))
	http.ListenAndServe(":8888", nil)
}

func getFileSystem(useOS bool) http.FileSystem {
	if useOS {
		log.Print("using live mode")
		return http.FS(os.DirFS("static"))
	}

	log.Print("using embed mode")
	fsys, err := fs.Sub(catalog.FS, "raw")
	if err != nil {
		panic(err)
	}

	return http.FS(fsys)
}
